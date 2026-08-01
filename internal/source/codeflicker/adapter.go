package codeflicker

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/myflicker"
)

type Adapter struct {
	json   *myflicker.Adapter
	dbPath string
	scanMu sync.Mutex
	mu     sync.RWMutex
	known  map[string]authorization
}

type authorization struct {
	session source.Session
	digest  [32]byte
}

func New(roots ...string) *Adapter {
	projectsRoot, dbPath := "", ""
	if len(roots) > 0 {
		projectsRoot = roots[0]
	}
	if len(roots) > 1 {
		dbPath = roots[1]
	}
	if home, err := os.UserHomeDir(); err == nil && len(roots) == 0 {
		if projectsRoot == "" {
			projectsRoot = filepath.Join(home, ".codeflicker", "projects")
		}
		dbPath = filepath.Join(home, ".codeflicker", "data", "codeflicker", "composer_data.sqlite")
	}
	return &Adapter{
		json:   myflicker.NewProduct("codeflicker", projectsRoot),
		dbPath: dbPath,
		known:  make(map[string]authorization),
	}
}

func (*Adapter) Product() string { return "codeflicker" }
func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{"messages", "tools"}
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	jsonSessions, err := a.json.Discover(ctx)
	if err != nil {
		return nil, err
	}
	dbSessions, dbDigests, err := a.discoverDatabase(ctx)
	if err != nil {
		return nil, err
	}
	// composer_data.sqlite is CodeFlicker's current authoritative store;
	// project JSONL files are retained as a legacy fallback.
	byID := make(map[string]source.Session, len(jsonSessions)+len(dbSessions))
	for _, session := range jsonSessions {
		byID[session.ID] = session
	}
	for _, session := range dbSessions {
		byID[session.ID] = session
	}
	out := make([]source.Session, 0, len(byID))
	next := make(map[string]authorization, len(byID))
	for _, session := range byID {
		out = append(out, session)
		next[session.OpaqueRef] = authorization{session: session, digest: dbDigests[session.OpaqueRef]}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
	return out, nil
}

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() || session.OpaqueRef == "" {
		return nil, errors.New("codeflicker: invalid session reference")
	}
	a.mu.RLock()
	authorized, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || !reflect.DeepEqual(authorized.session, session) {
		return nil, errors.New("codeflicker: unknown session reference")
	}
	if session.FormatVersion == databaseFormat {
		return a.openDatabase(ctx, session, authorized.digest)
	}
	return a.json.Open(ctx, session)
}
