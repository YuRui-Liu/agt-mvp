package mocksvc

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSnapshotDoesNotContainPhone(t *testing.T) {
	store := NewMemoryStore()
	auth := NewAuthenticator(store, testMockSecret("snapshot phone"), func() time.Time {
		return time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)
	})
	const phone = "+8613800138000"

	if err := auth.RequestCode(phone); err != nil {
		t.Fatalf("request code: %v", err)
	}
	if _, err := auth.Verify(phone, "246810"); err != nil {
		t.Fatalf("verify: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}

	if strings.Contains(string(encoded), phone) || strings.Contains(string(encoded), "13800138000") {
		t.Fatalf("snapshot contains phone: %s", encoded)
	}
}

func TestMemorySnapshotIsDeepCopy(t *testing.T) {
	store := NewMemoryStore()
	identity := Identity{SubjectID: "sub-one", KuAIID: "KUAI-AAAAAAAAAA", CreatedAt: time.Now()}
	if err := store.Put(identity); err != nil {
		t.Fatalf("put: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	delete(snapshot.Identities, identity.SubjectID)

	second, err := store.Snapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if _, ok := second.Identities[identity.SubjectID]; !ok {
		t.Fatal("mutating snapshot changed store")
	}
}

func TestStoresGetOrPutPreserveFirstIdentity(t *testing.T) {
	fileStore, err := OpenFileStore(filepath.Join(t.TempDir(), "private", "state.json"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	stores := []IdentityStore{NewMemoryStore(), fileStore}
	for _, store := range stores {
		first := Identity{
			SubjectID: "sub-one",
			KuAIID:    "KUAI-AAAAAAAAAA",
			CreatedAt: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC),
		}
		second := first
		second.CreatedAt = first.CreatedAt.Add(time.Hour)
		got, err := store.GetOrPut(first)
		if err != nil {
			t.Fatalf("%T first GetOrPut: %v", store, err)
		}
		if got != first {
			t.Fatalf("%T first GetOrPut = %#v, want %#v", store, got, first)
		}
		got, err = store.GetOrPut(second)
		if err != nil {
			t.Fatalf("%T second GetOrPut: %v", store, err)
		}
		if got != first {
			t.Fatalf("%T replaced first identity: got %#v, want %#v", store, got, first)
		}
	}
}

func TestStoresRejectInvalidIdentityBeforeWriting(t *testing.T) {
	fileStore, err := OpenFileStore(filepath.Join(t.TempDir(), "private", "state.json"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	stores := []IdentityStore{NewMemoryStore(), fileStore}
	invalid := []Identity{
		{KuAIID: "KUAI-AAAAAAAAAA", CreatedAt: time.Now()},
		{SubjectID: "sub-one", CreatedAt: time.Now()},
		{SubjectID: "sub-one", KuAIID: "KUAI-AAAAAAAAAA"},
	}
	for _, store := range stores {
		for _, identity := range invalid {
			if err := store.Put(identity); !errors.Is(err, ErrInvalidIdentity) {
				t.Errorf("%T Put(%#v) error = %v, want ErrInvalidIdentity", store, identity, err)
			}
			if _, err := store.GetOrPut(identity); !errors.Is(err, ErrInvalidIdentity) {
				t.Errorf("%T GetOrPut(%#v) error = %v, want ErrInvalidIdentity", store, identity, err)
			}
		}
		snapshot, err := store.Snapshot()
		if err != nil {
			t.Fatalf("%T snapshot: %v", store, err)
		}
		if len(snapshot.Identities) != 0 {
			t.Fatalf("%T persisted invalid identities: %#v", store, snapshot.Identities)
		}
	}
}

func TestFileStorePersistsPrivatelyAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	identity := Identity{
		SubjectID: "sub-one",
		KuAIID:    "KUAI-AAAAAAAAAA",
		CreatedAt: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC),
	}
	if err := store.Put(identity); err != nil {
		t.Fatalf("put: %v", err)
	}

	if runtime.GOOS != "windows" {
		assertPermission(t, filepath.Dir(path), 0o700)
		assertPermission(t, path, 0o600)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen file store: %v", err)
	}
	got, ok, err := reopened.Get(identity.SubjectID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok || got != identity {
		t.Fatalf("Get = (%#v, %v), want (%#v, true)", got, ok, identity)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if strings.Contains(string(encoded), "phone") {
		t.Fatalf("state has phone field: %s", encoded)
	}
}

func TestFileStoreIdentitySecretSurvivesReopenWithoutPublicExposure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	first, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	firstSecret, err := first.IdentitySecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(firstSecret) != 32 {
		t.Fatalf("secret length=%d want 32", len(firstSecret))
	}
	firstSecret[0] ^= 0xff
	again, err := first.IdentitySecret()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstSecret, again) {
		t.Fatal("IdentitySecret returned mutable backing storage")
	}

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSecret, err := reopened.IdentitySecret()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, reopenedSecret) {
		t.Fatal("identity secret changed after reopen")
	}
	snapshot, err := reopened.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	publicJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(publicJSON, reopenedSecret) || strings.Contains(string(publicJSON), "identity_secret") {
		t.Fatalf("public snapshot exposed secret: %s", publicJSON)
	}
}

func TestFileStoreMigratesVersionOneStateWithStableIdentity(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"identities":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := store.IdentitySecret()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSecret, err := reopened.IdentitySecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 32 || !bytes.Equal(secret, reopenedSecret) {
		t.Fatal("migrated identity secret was not persisted")
	}
}

func TestFileStoreReopenReturnsSameIdentityForSamePhone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	verify := func(store *FileStore) Identity {
		t.Helper()
		secret, err := store.IdentitySecret()
		if err != nil {
			t.Fatal(err)
		}
		auth := NewAuthenticator(store, secret, fixedTaskClock())
		if err := auth.RequestCode("+8613800138000"); err != nil {
			t.Fatal(err)
		}
		session, err := auth.Verify("+8613800138000", "246810")
		if err != nil {
			t.Fatal(err)
		}
		return session.Identity
	}
	firstStore, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := verify(firstStore)
	secondStore, err := OpenFileStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second := verify(secondStore)
	if first.SubjectID != second.SubjectID || first.KuAIID != second.KuAIID {
		t.Fatalf("identity changed after reopen: %#v %#v", first, second)
	}
	state, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(state, []byte("13800138000")) {
		t.Fatalf("state contains phone: %s", state)
	}
}

func TestOpenFileStoreRejectsCorruptStateWithoutOverwriting(t *testing.T) {
	for name, content := range map[string][]byte{
		"invalid-json":   []byte(`{"version":`),
		"invalid-schema": []byte(`{"version":99,"identities":{}}`),
		"phone-field":    []byte(`{"version":1,"identities":{},"phone":"+8613800138000"}`),
	} {
		t.Run(name, func(t *testing.T) {
			parent := filepath.Join(t.TempDir(), "private")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatalf("mkdir private directory: %v", err)
			}
			path := filepath.Join(parent, "state.json")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatalf("write corrupt state: %v", err)
			}
			if _, err := OpenFileStore(path); err == nil {
				t.Fatal("OpenFileStore succeeded for corrupt state")
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read state: %v", err)
			}
			if !bytes.Equal(got, content) {
				t.Fatalf("corrupt state overwritten: got %q, want %q", got, content)
			}
		})
	}
}

func TestFileStoreRenameFailurePreservesOldStateAndCleansTemp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	first := Identity{
		SubjectID: "sub-one",
		KuAIID:    "KUAI-AAAAAAAAAA",
		CreatedAt: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC),
	}
	if err := store.Put(first); err != nil {
		t.Fatalf("put first: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read old state: %v", err)
	}
	store.rename = func(string, string) error { return errors.New("injected rename failure") }

	second := Identity{
		SubjectID: "sub-two",
		KuAIID:    "KUAI-BBBBBBBBBB",
		CreatedAt: first.CreatedAt.Add(time.Hour),
	}
	if err := store.Put(second); err == nil {
		t.Fatal("Put succeeded despite rename failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state after failure: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("old state changed after failure:\ngot  %s\nwant %s", after, before)
	}
	if _, ok, getErr := store.Get(second.SubjectID); getErr != nil || ok {
		t.Fatalf("failed Put changed memory: Get = (_, %v, %v)", ok, getErr)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".state.json.tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files not cleaned: %v", matches)
	}
}

func TestFileStoreCreateFailurePreservesOldState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	first := Identity{
		SubjectID: "sub-one",
		KuAIID:    "KUAI-AAAAAAAAAA",
		CreatedAt: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC),
	}
	if err := store.Put(first); err != nil {
		t.Fatalf("put first: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read old state: %v", err)
	}
	store.createTemp = func(string, string) (*os.File, error) {
		return nil, errors.New("injected create failure")
	}

	second := Identity{
		SubjectID: "sub-two",
		KuAIID:    "KUAI-BBBBBBBBBB",
		CreatedAt: first.CreatedAt.Add(time.Hour),
	}
	if err := store.Put(second); err == nil {
		t.Fatal("Put succeeded despite create failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state after failure: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("old state changed after failure:\ngot  %s\nwant %s", after, before)
	}
	if _, ok, getErr := store.Get(second.SubjectID); getErr != nil || ok {
		t.Fatalf("failed Put changed memory: Get = (_, %v, %v)", ok, getErr)
	}
}

func TestFileStoreDirectoryOpenErrorCommitsRenamedState(t *testing.T) {
	store, first := seededFileStore(t)
	injected := errors.New("injected directory open failure")
	store.openDir = func(string) (*os.File, error) {
		return nil, injected
	}

	assertDirectoryPersistenceFailure(t, store, first, injected)
}

func TestFileStoreDirectorySyncErrorCommitsRenamedState(t *testing.T) {
	store, first := seededFileStore(t)
	injected := errors.New("injected directory sync failure")
	store.syncDir = func(*os.File) error {
		return injected
	}

	assertDirectoryPersistenceFailure(t, store, first, injected)
}

func TestFileStoreDirectoryCloseErrorCommitsRenamedState(t *testing.T) {
	store, first := seededFileStore(t)
	injected := errors.New("injected directory close failure")
	store.closeDir = func(directory *os.File) error {
		_ = directory.Close()
		return injected
	}

	assertDirectoryPersistenceFailure(t, store, first, injected)
}

func seededFileStore(t *testing.T) (*FileStore, Identity) {
	t.Helper()
	store, err := OpenFileStore(filepath.Join(t.TempDir(), "private", "state.json"))
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	first := Identity{
		SubjectID: "sub-one",
		KuAIID:    "KUAI-AAAAAAAAAA",
		CreatedAt: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC),
	}
	if err := store.Put(first); err != nil {
		t.Fatalf("seed file store: %v", err)
	}
	return store, first
}

func assertDirectoryPersistenceFailure(t *testing.T, store *FileStore, first Identity, injected error) {
	t.Helper()
	second := Identity{
		SubjectID: "sub-two",
		KuAIID:    "KUAI-BBBBBBBBBB",
		CreatedAt: first.CreatedAt.Add(time.Hour),
	}
	err := store.Put(second)
	if !errors.Is(err, injected) {
		t.Fatalf("Put error = %v, want injected error", err)
	}
	snapshot, snapshotErr := store.Snapshot()
	if snapshotErr != nil {
		t.Fatalf("snapshot: %v", snapshotErr)
	}
	if got, ok := snapshot.Identities[first.SubjectID]; !ok || got != first {
		t.Fatalf("previous identity changed: got (%#v, %v), want (%#v, true)", got, ok, first)
	}
	if got, ok := snapshot.Identities[second.SubjectID]; !ok || got != second {
		t.Fatalf("renamed identity missing from memory: got (%#v, %v), want (%#v, true)", got, ok, second)
	}
	reopened, reopenErr := OpenFileStore(store.path)
	if reopenErr != nil {
		t.Fatalf("reopen after durability uncertainty: %v", reopenErr)
	}
	got, ok, getErr := reopened.Get(second.SubjectID)
	if getErr != nil || !ok || got != second {
		t.Fatalf("reopened Get = (%#v, %v, %v), want (%#v, true, nil)", got, ok, getErr, second)
	}
}

func TestFileStoreDoesNotPersistPhoneOrAuthSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	clock := &fixedClock{now: time.Date(2026, 7, 28, 9, 30, 0, 0, time.UTC)}
	auth := NewAuthenticator(store, testMockSecret("file auth"), clock.Now)
	const phone = "+8613800138000"
	if err := auth.RequestCode(phone); err != nil {
		t.Fatalf("request code: %v", err)
	}
	session, err := auth.Verify(phone, "246810")
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	for label, sensitive := range map[string]string{
		"phone":        phone,
		"phone digits": strings.TrimPrefix(phone, "+"),
		"token":        session.Token,
	} {
		if strings.Contains(string(data), sensitive) {
			t.Fatalf("state contains %s: %s", label, data)
		}
	}

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	restartedAuth := NewAuthenticator(reopened, testMockSecret("file auth"), clock.Now)
	if _, err := restartedAuth.Authenticate(session.Token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("restarted authenticator accepted old token: %v", err)
	}
}

func TestFileStoreSnapshotIsDeepCopy(t *testing.T) {
	store, err := OpenFileStore(filepath.Join(t.TempDir(), "private", "state.json"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	identity := Identity{SubjectID: "sub-one", KuAIID: "KUAI-AAAAAAAAAA", CreatedAt: time.Now()}
	if err := store.Put(identity); err != nil {
		t.Fatalf("put: %v", err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	delete(snapshot.Identities, identity.SubjectID)
	second, err := store.Snapshot()
	if err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if _, ok := second.Identities[identity.SubjectID]; !ok {
		t.Fatal("mutating snapshot changed file store")
	}
}

func TestFileStoreConcurrentPut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "state.json")
	store, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("open file store: %v", err)
	}
	const count = 24
	var wg sync.WaitGroup
	for index := range count {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			identity := Identity{
				SubjectID: "sub-" + string(rune('A'+index)),
				KuAIID:    "KUAI-AAAAAAAAAA",
				CreatedAt: time.Unix(int64(index), 0).UTC(),
			}
			if err := store.Put(identity); err != nil {
				t.Errorf("put %d: %v", index, err)
			}
		}(index)
	}
	wg.Wait()

	reopened, err := OpenFileStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	snapshot, err := reopened.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Identities) != count {
		t.Fatalf("identity count = %d, want %d", len(snapshot.Identities), count)
	}
}

func assertPermission(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s permissions = %#o, want %#o", path, got, want)
	}
}
