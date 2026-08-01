package mocksvc

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

var ErrInvalidIdentity = errors.New("invalid identity")

type Identity struct {
	SubjectID string    `json:"subject_id"`
	KuAIID    string    `json:"kuai_id"`
	CreatedAt time.Time `json:"created_at"`
}

type StoreSnapshot struct {
	Version    int                 `json:"version"`
	Identities map[string]Identity `json:"identities"`
}

type IdentityStore interface {
	IdentitySecret() ([]byte, error)
	Get(subjectID string) (Identity, bool, error)
	Put(identity Identity) error
	GetOrPut(identity Identity) (Identity, error)
	Snapshot() (StoreSnapshot, error)
}

type MemoryStore struct {
	mu             sync.RWMutex
	identitySecret []byte
	initErr        error
	identities     map[string]Identity
}

func NewMemoryStore() *MemoryStore {
	secret, err := newIdentitySecret()
	return &MemoryStore{identitySecret: secret, initErr: err, identities: make(map[string]Identity)}
}

func (s *MemoryStore) IdentitySecret() ([]byte, error) {
	if s == nil {
		return nil, ErrInvalidSecret
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.initErr != nil || len(s.identitySecret) != 32 {
		return nil, ErrInvalidSecret
	}
	return append([]byte(nil), s.identitySecret...), nil
}

func (s *MemoryStore) Get(subjectID string) (Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.identities[subjectID]
	return identity, ok, nil
}

func (s *MemoryStore) Put(identity Identity) error {
	_, err := s.getOrPut(identity)
	return err
}

func (s *MemoryStore) GetOrPut(identity Identity) (Identity, error) {
	return s.getOrPut(identity)
}

func (s *MemoryStore) getOrPut(identity Identity) (Identity, error) {
	if err := validateIdentity(identity); err != nil {
		return Identity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.identities[identity.SubjectID]; exists {
		return existing, nil
	}
	s.identities[identity.SubjectID] = identity
	return identity, nil
}

func (s *MemoryStore) Snapshot() (StoreSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identities := make(map[string]Identity, len(s.identities))
	for key, identity := range s.identities {
		identities[key] = identity
	}
	return StoreSnapshot{Version: 1, Identities: identities}, nil
}

type FileStore struct {
	mu             sync.RWMutex
	path           string
	identitySecret []byte
	identities     map[string]Identity
	createTemp     func(string, string) (*os.File, error)
	rename         func(string, string) error
	openDir        func(string) (*os.File, error)
	syncDir        func(*os.File) error
	closeDir       func(*os.File) error
}

func OpenFileStore(path string) (*FileStore, error) {
	if path == "" {
		return nil, errors.New("identity store path is empty")
	}
	parent := filepath.Dir(path)
	if err := secureStoreDirectory(parent); err != nil {
		return nil, err
	}

	store := &FileStore{
		path:       path,
		identities: make(map[string]Identity),
		createTemp: os.CreateTemp,
		rename:     replaceFile,
		openDir:    os.Open,
		syncDir: func(directory *os.File) error {
			if runtime.GOOS == "windows" {
				return nil
			}
			return directory.Sync()
		},
		closeDir: func(directory *os.File) error { return directory.Close() },
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		snapshot, decodeErr := decodePersistedStore(data)
		if decodeErr != nil {
			return nil, decodeErr
		}
		store.identities = snapshot.Identities
		store.identitySecret = append([]byte(nil), snapshot.IdentitySecret...)
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("secure identity store file: %w", err)
		}
		if snapshot.Version == 1 {
			store.identitySecret, err = newIdentitySecret()
			if err != nil {
				return nil, err
			}
			if _, err := store.persistLocked(store.identities); err != nil {
				return nil, err
			}
		}
	case errors.Is(err, os.ErrNotExist):
		store.identitySecret, err = newIdentitySecret()
		if err != nil {
			return nil, err
		}
		if _, err := store.persistLocked(store.identities); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("read identity store: %w", err)
	}
	return store, nil
}

func (s *FileStore) IdentitySecret() ([]byte, error) {
	if s == nil {
		return nil, ErrInvalidSecret
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.identitySecret) != 32 {
		return nil, ErrInvalidSecret
	}
	return append([]byte(nil), s.identitySecret...), nil
}

func (s *FileStore) Get(subjectID string) (Identity, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	identity, ok := s.identities[subjectID]
	return identity, ok, nil
}

func (s *FileStore) Put(identity Identity) error {
	_, err := s.getOrPut(identity)
	return err
}

func (s *FileStore) GetOrPut(identity Identity) (Identity, error) {
	return s.getOrPut(identity)
}

func (s *FileStore) getOrPut(identity Identity) (Identity, error) {
	if err := validateIdentity(identity); err != nil {
		return Identity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.identities[identity.SubjectID]; ok {
		return existing, nil
	}
	next := cloneIdentities(s.identities)
	next[identity.SubjectID] = identity
	renamed, err := s.persistLocked(next)
	if err != nil {
		if renamed {
			s.identities = next
			return identity, err
		}
		return Identity{}, err
	}
	s.identities = next
	return identity, nil
}

func (s *FileStore) Snapshot() (StoreSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return StoreSnapshot{Version: 1, Identities: cloneIdentities(s.identities)}, nil
}

func (s *FileStore) persistLocked(identities map[string]Identity) (bool, error) {
	parent := filepath.Dir(s.path)
	temporary, err := s.createTemp(parent, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temporary identity store: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return false, fmt.Errorf("secure temporary identity store: %w", err)
	}
	snapshot := persistedStore{
		Version:        2,
		IdentitySecret: append([]byte(nil), s.identitySecret...),
		Identities:     identities,
	}
	if err := json.NewEncoder(temporary).Encode(snapshot); err != nil {
		return false, fmt.Errorf("encode identity store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return false, fmt.Errorf("sync temporary identity store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		closed = true
		return false, fmt.Errorf("close temporary identity store: %w", err)
	}
	closed = true
	if err := s.rename(temporaryPath, s.path); err != nil {
		return false, fmt.Errorf("replace identity store: %w", err)
	}
	directory, err := s.openDir(parent)
	if err != nil {
		return true, fmt.Errorf("identity store replaced but directory durability is uncertain: %w", err)
	}
	syncErr := s.syncDir(directory)
	closeErr := s.closeDir(directory)
	if syncErr != nil || closeErr != nil {
		var persistenceErrors []error
		if syncErr != nil {
			persistenceErrors = append(persistenceErrors, fmt.Errorf("sync identity store directory: %w", syncErr))
		}
		if closeErr != nil {
			persistenceErrors = append(persistenceErrors, fmt.Errorf("close identity store directory: %w", closeErr))
		}
		return true, fmt.Errorf("identity store replaced but directory durability is uncertain: %w", errors.Join(persistenceErrors...))
	}
	return true, nil
}

type persistedStore struct {
	Version        int                 `json:"version"`
	IdentitySecret []byte              `json:"identity_secret,omitempty"`
	Identities     map[string]Identity `json:"identities"`
}

func decodePersistedStore(data []byte) (persistedStore, error) {
	var snapshot persistedStore
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return persistedStore{}, errors.New("invalid identity store state")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return persistedStore{}, errors.New("invalid identity store state")
	}
	if (snapshot.Version != 1 && snapshot.Version != 2) || snapshot.Identities == nil ||
		(snapshot.Version == 1 && len(snapshot.IdentitySecret) != 0) ||
		(snapshot.Version == 2 && len(snapshot.IdentitySecret) != 32) {
		return persistedStore{}, errors.New("invalid identity store state")
	}
	for key, identity := range snapshot.Identities {
		if identity.SubjectID != key || validateIdentity(identity) != nil {
			return persistedStore{}, errors.New("invalid identity store state")
		}
	}
	return persistedStore{
		Version:        snapshot.Version,
		IdentitySecret: append([]byte(nil), snapshot.IdentitySecret...),
		Identities:     cloneIdentities(snapshot.Identities),
	}, nil
}

func newIdentitySecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate identity secret: %w", err)
	}
	return secret, nil
}

func validateIdentity(identity Identity) error {
	if identity.SubjectID == "" || identity.KuAIID == "" || identity.CreatedAt.IsZero() {
		return ErrInvalidIdentity
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("trailing JSON data")
}

func cloneIdentities(source map[string]Identity) map[string]Identity {
	cloned := make(map[string]Identity, len(source))
	for key, identity := range source {
		cloned[key] = identity
	}
	return cloned
}
