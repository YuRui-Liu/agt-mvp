package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const maxFileBytes int64 = 1 << 20
const maxSessionBytes int64 = 4 << 20

type Adapter struct {
	root              string
	scanMu            sync.Mutex
	mu                sync.RWMutex
	known             map[string]authorization
	readFile          func(string, string) ([]byte, error)
	sqliteRead        sqliteReadFunc
	afterSnapshotLoad func()
}
type authorization struct {
	id, digest string
	format     string
	ref        string
}

func New(roots ...string) *Adapter {
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = defaultRoot(home, runtime.GOOS, os.Getenv)
		}
	}
	return &Adapter{root: root, known: map[string]authorization{}, readFile: readBytes, sqliteRead: defaultSQLiteRead}
}
func defaultRoot(home, goos string, getenv func(string) string) string {
	if goos == "windows" {
		if local := getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "opencode")
		}
	}
	if data := getenv("XDG_DATA_HOME"); data != "" {
		return filepath.Join(data, "opencode")
	}
	return filepath.Join(home, ".local", "share", "opencode")
}
func (*Adapter) Product() string                   { return "opencode" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type sessionFile struct {
	ID, Directory, ParentID string
	Time                    struct{ Created, Updated int64 } `json:"time"`
}
type messageFile struct {
	ID, SessionID, Role, ModelID, ProviderID string
	Model                                    struct{ ModelID, ProviderID string }
	Time                                     struct{ Created, Start, End, Updated int64 } `json:"time"`
}
type partFile struct {
	ID, SessionID, MessageID, Type, Text, Tool, CallID string
	State                                              struct {
		Status string
		Input  json.RawMessage
		Output any
		Error  any
	} `json:"state"`
	Time struct{ Created, Start, End, Updated int64 } `json:"time"`
}
type event struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Model     string `json:"model,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	Result    any    `json:"result,omitempty"`
}
type snapshot struct {
	sessionPath string
	session     []byte
	files       map[string][]byte
	digest      string
	total       int64
}

func readBytes(root, path string) ([]byte, error) {
	f, e := safeopen.Open(root, path, maxFileBytes)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxFileBytes+1))
}
func (a *Adapter) loadSnapshot(ctx context.Context, path string) (snapshot, error) {
	s := snapshot{sessionPath: path, files: map[string][]byte{}}
	b, e := a.readFile(a.root, path)
	if e != nil {
		return s, e
	}
	s.session = b
	s.total = int64(len(b))
	var sf sessionFile
	if json.Unmarshal(b, &sf) != nil || sf.ID == "" {
		return s, errors.New("opencode: invalid session")
	}
	md := filepath.Join(a.root, "storage", "message", sf.ID)
	mes, e := os.ReadDir(md)
	if e != nil {
		return s, e
	}
	for _, x := range mes {
		if e := ctx.Err(); e != nil {
			return s, e
		}
		if x.IsDir() || !x.Type().IsRegular() || !strings.HasSuffix(x.Name(), ".json") {
			continue
		}
		p := filepath.Join(md, x.Name())
		raw, e := a.readFile(a.root, p)
		if e != nil {
			return s, e
		}
		s.total += int64(len(raw))
		if s.total > maxSessionBytes {
			return snapshot{}, errors.New("opencode: session exceeds limit")
		}
		s.files[p] = raw
		var m messageFile
		if json.Unmarshal(raw, &m) != nil || m.ID == "" {
			continue
		}
		pd := filepath.Join(a.root, "storage", "part", m.ID)
		ps, e := os.ReadDir(pd)
		if e != nil && !os.IsNotExist(e) {
			return s, e
		}
		for _, y := range ps {
			if y.IsDir() || !y.Type().IsRegular() || !strings.HasSuffix(y.Name(), ".json") {
				continue
			}
			pp := filepath.Join(pd, y.Name())
			raw, e := a.readFile(a.root, pp)
			if e != nil {
				return s, e
			}
			s.total += int64(len(raw))
			if s.total > maxSessionBytes {
				return snapshot{}, errors.New("opencode: session exceeds limit")
			}
			s.files[pp] = raw
		}
	}
	if s.total > maxSessionBytes {
		return s, errors.New("opencode: session exceeds limit")
	}
	keys := make([]string, 0, len(s.files)+1)
	keys = append(keys, path)
	for p := range s.files {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, p := range keys {
		io.WriteString(h, p)
		if p == path {
			h.Write(s.session)
		} else {
			h.Write(s.files[p])
		}
	}
	s.digest = fmt.Sprintf("%x", h.Sum(nil))
	if a.afterSnapshotLoad != nil {
		a.afterSnapshotLoad()
	}
	return s, nil
}

type parseStats struct{ FileVisits int }

func parseSnapshot(s snapshot) (sessionFile, []event, int, int, error) {
	return parseSnapshotWithStats(s, nil)
}
func parseSnapshotWithStats(s snapshot, stats *parseStats) (sessionFile, []event, int, int, error) {
	var sf sessionFile
	if json.Unmarshal(s.session, &sf) != nil {
		return sf, nil, 0, 0, errors.New("opencode: invalid session")
	}
	type msg struct {
		m    messageFile
		path string
	}
	var msgs []msg
	validMessages := make(map[string]bool)
	partsByMessage := make(map[string][]partFile)
	bad := 0
	for p, b := range s.files {
		if stats != nil {
			stats.FileVisits++
		}
		switch filepath.Base(filepath.Dir(filepath.Dir(p))) {
		case "message":
			var m messageFile
			if json.Unmarshal(b, &m) != nil || m.ID == "" || m.SessionID != sf.ID || (m.Role != "user" && m.Role != "assistant") {
				bad++
				continue
			}
			msgs = append(msgs, msg{m, p})
			validMessages[m.ID] = true
		case "part":
			var part partFile
			if json.Unmarshal(b, &part) != nil || part.ID == "" || part.MessageID == "" || part.SessionID != sf.ID {
				bad++
				continue
			}
			if physicalParent := filepath.Base(filepath.Dir(p)); physicalParent != part.MessageID {
				bad++
				continue
			}
			partsByMessage[part.MessageID] = append(partsByMessage[part.MessageID], part)
		}
	}
	for messageID, parts := range partsByMessage {
		if !validMessages[messageID] {
			bad += len(parts)
			delete(partsByMessage, messageID)
		}
	}
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].m.Time.Created == msgs[j].m.Time.Created {
			return msgs[i].m.ID < msgs[j].m.ID
		}
		return msgs[i].m.Time.Created < msgs[j].m.Time.Created
	})
	var out []event
	for _, mm := range msgs {
		ps := partsByMessage[mm.m.ID]
		sort.Slice(ps, func(i, j int) bool {
			if partTime(ps[i]) == partTime(ps[j]) {
				return ps[i].ID < ps[j].ID
			}
			return partTime(ps[i]) < partTime(ps[j])
		})
		ts := time.UnixMilli(mm.m.Time.Created).UTC().Format(time.RFC3339Nano)
		model := mm.m.ModelID
		if model == "" {
			model = mm.m.Model.ModelID
		}
		for _, p := range ps {
			switch p.Type {
			case "text":
				if strings.TrimSpace(p.Text) == "" {
					bad++
					continue
				}
				out = append(out, event{Type: "message", Role: mm.m.Role, Content: []any{map[string]any{"type": "text", "text": p.Text}}, Timestamp: ts, Model: model})
			case "reasoning":
				if mm.m.Role != "assistant" || strings.TrimSpace(p.Text) == "" {
					bad++
					continue
				}
				out = append(out, event{Type: "message", Role: mm.m.Role, Content: []any{map[string]any{"type": "thinking", "thinking": p.Text}}, Timestamp: ts, Model: model})
			case "tool":
				if mm.m.Role != "assistant" || p.Tool == "" || p.CallID == "" {
					bad++
					continue
				}
				var input any = map[string]any{}
				if len(p.State.Input) > 0 && json.Unmarshal(p.State.Input, &input) != nil {
					bad++
					continue
				}
				out = append(out, event{Type: "tool_use", Timestamp: ts, Model: model, CallID: p.CallID, Name: p.Tool, Input: input})
				if p.State.Status == "completed" && p.State.Output != nil {
					out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: p.CallID, Result: p.State.Output})
				} else if p.State.Status == "error" && p.State.Error != nil {
					out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: p.CallID, Result: p.State.Error})
				} else if p.State.Status == "completed" || p.State.Status == "error" {
					bad++
				}
			}
		}
	}
	return sf, out, len(msgs), bad, nil
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	databaseSessions, next, err := a.discoverSQLite(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]source.Session{}
	for _, session := range databaseSessions {
		byID[session.ID] = session
	}
	base := filepath.Join(a.root, "storage", "session")
	var paths []string
	_ = filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if err == nil && !d.IsDir() && d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".json") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	digests := map[string]string{}
	conflicts := map[string]bool{}
	for _, path := range paths {
		s, digest, _, ok := a.inspect(ctx, path)
		if !ok {
			continue
		}
		if existing, exists := byID[s.ID]; exists && existing.FormatVersion == databaseFormat {
			continue
		}
		if conflicts[s.ID] {
			continue
		}
		if _, conflict := byID[s.ID]; conflict {
			delete(byID, s.ID)
			conflicts[s.ID] = true
			continue
		}
		byID[s.ID] = s
		digests[s.ID] = digest
	}
	out := make([]source.Session, 0, len(byID))
	for _, s := range byID {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	for _, s := range out {
		if s.FormatVersion != databaseFormat {
			next[s.OpaqueRef] = authorization{id: s.ID, digest: digests[s.ID], format: "storage-v1", ref: s.OpaqueRef}
		}
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
	return out, ctx.Err()
}

func readJSON(root, path string, dst any) (int64, error) {
	f, err := safeopen.Open(root, path, maxFileBytes)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	dec := json.NewDecoder(io.LimitReader(f, maxFileBytes+1))
	if err := dec.Decode(dst); err != nil {
		return 0, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return 0, errors.New("opencode: trailing JSON")
	}
	return info.Size(), nil
}
func (a *Adapter) inspect(ctx context.Context, path string) (source.Session, string, []byte, bool) {
	snap, err := a.loadSnapshot(ctx, path)
	if err != nil {
		return source.Session{}, "", nil, false
	}
	sf, events, msgs, malformed, err := parseSnapshot(snap)
	if err != nil || sf.ID == "" || strings.ContainsAny(sf.ID, `/\\#`) || msgs == 0 || len(events) == 0 {
		return source.Session{}, "", nil, false
	}
	scope := source.ScopeRef{Type: source.ScopeProject, Root: sf.Directory, Label: filepath.Base(sf.Directory)}
	if !filepath.IsAbs(sf.Directory) || filepath.Clean(sf.Directory) != sf.Directory {
		sum := sha256.Sum256([]byte("opencode\x00" + sf.ID))
		scope = source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("%x", sum[:12]), Label: "OpenCode sessions"}
	}
	var output bytes.Buffer
	enc := json.NewEncoder(&output)
	for _, e := range events {
		if enc.Encode(e) != nil {
			return source.Session{}, "", nil, false
		}
	}
	return source.Session{ID: "opencode:" + sf.ID, Product: "opencode", FormatVersion: "storage-v1", AdapterVersion: "1", Capabilities: a.Capabilities(), Scope: scope, StartedAt: time.UnixMilli(sf.Time.Created), EndedAt: time.UnixMilli(sf.Time.Updated), MessageCount: msgs, MalformedCount: malformed, OpaqueRef: path}, snap.digest, append([]byte(nil), output.Bytes()...), true
}
func (a *Adapter) messageCount(sid string) (int, error) {
	es, e := os.ReadDir(filepath.Join(a.root, "storage", "message", sid))
	if e != nil {
		return 0, e
	}
	n := 0
	for _, x := range es {
		if !x.IsDir() && x.Type().IsRegular() && strings.HasSuffix(x.Name(), ".json") {
			var m messageFile
			if _, err := readJSON(a.root, filepath.Join(a.root, "storage", "message", sid, x.Name()), &m); err == nil && m.ID != "" && m.SessionID == sid && (m.Role == "user" || m.Role == "assistant") {
				n++
			}
		}
	}
	return n, nil
}
func (a *Adapter) fingerprint(sessionPath, sid string) (string, error) {
	paths := []string{sessionPath}
	md := filepath.Join(a.root, "storage", "message", sid)
	es, e := os.ReadDir(md)
	if e != nil {
		return "", e
	}
	for _, x := range es {
		if x.IsDir() || !x.Type().IsRegular() || !strings.HasSuffix(x.Name(), ".json") {
			continue
		}
		p := filepath.Join(md, x.Name())
		paths = append(paths, p)
		var m messageFile
		if _, e := readJSON(a.root, p, &m); e == nil && m.ID != "" {
			pd := filepath.Join(a.root, "storage", "part", m.ID)
			ps, _ := os.ReadDir(pd)
			for _, y := range ps {
				if !y.IsDir() && y.Type().IsRegular() && strings.HasSuffix(y.Name(), ".json") {
					paths = append(paths, filepath.Join(pd, y.Name()))
				}
			}
		}
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		f, e := safeopen.Open(a.root, p, maxFileBytes)
		if e != nil {
			return "", e
		}
		io.WriteString(h, p)
		_, e = io.Copy(h, f)
		f.Close()
		if e != nil {
			return "", e
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
func (a *Adapter) events(ctx context.Context, sid string) ([]event, int, int64, error) {
	dir := filepath.Join(a.root, "storage", "message", sid)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, 0, err
	}
	type msg struct {
		m messageFile
		p string
		t int64
	}
	var msgs []msg
	var used int64
	malformed := 0
	for _, d := range ents {
		if ctx.Err() != nil {
			return nil, 0, used, ctx.Err()
		}
		if d.IsDir() || !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		var m messageFile
		p := filepath.Join(dir, d.Name())
		n, e := readJSON(a.root, p, &m)
		used += n
		if e != nil || m.ID == "" || m.SessionID != sid || (m.Role != "user" && m.Role != "assistant") {
			malformed++
			continue
		}
		msgs = append(msgs, msg{m, p, m.Time.Created})
	}
	sort.Slice(msgs, func(i, j int) bool {
		if msgs[i].t == msgs[j].t {
			return msgs[i].m.ID < msgs[j].m.ID
		}
		return msgs[i].t < msgs[j].t
	})
	var out []event
	for _, mm := range msgs {
		pdir := filepath.Join(a.root, "storage", "part", mm.m.ID)
		parts, e := os.ReadDir(pdir)
		if e != nil && !os.IsNotExist(e) {
			return nil, 0, used, e
		}
		var ps []partFile
		for _, d := range parts {
			if d.IsDir() || !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), ".json") {
				continue
			}
			var p partFile
			n, e := readJSON(a.root, filepath.Join(pdir, d.Name()), &p)
			used += n
			if e != nil || p.ID == "" || p.MessageID != mm.m.ID || p.SessionID != sid {
				malformed++
				continue
			}
			ps = append(ps, p)
		}
		sort.Slice(ps, func(i, j int) bool {
			if partTime(ps[i]) == partTime(ps[j]) {
				return ps[i].ID < ps[j].ID
			}
			return partTime(ps[i]) < partTime(ps[j])
		})
		ts := time.UnixMilli(mm.t).UTC().Format(time.RFC3339Nano)
		model := mm.m.ModelID
		if model == "" {
			model = mm.m.Model.ModelID
		}
		for _, p := range ps {
			switch p.Type {
			case "text":
				if strings.TrimSpace(p.Text) != "" {
					out = append(out, event{Type: "message", Role: mm.m.Role, Content: []any{map[string]any{"type": "text", "text": p.Text}}, Timestamp: ts, Model: model})
				} else {
					malformed++
				}
			case "reasoning":
				if mm.m.Role == "assistant" && strings.TrimSpace(p.Text) != "" {
					out = append(out, event{Type: "message", Role: mm.m.Role, Content: []any{map[string]any{"type": "thinking", "thinking": p.Text}}, Timestamp: ts, Model: model})
				} else {
					malformed++
				}
			case "tool":
				if mm.m.Role != "assistant" || p.Tool == "" || p.CallID == "" {
					malformed++
					continue
				}
				var input any = map[string]any{}
				if len(p.State.Input) > 0 && json.Unmarshal(p.State.Input, &input) != nil {
					malformed++
					continue
				}
				out = append(out, event{Type: "tool_use", Timestamp: ts, Model: model, CallID: p.CallID, Name: p.Tool, Input: input})
				if p.State.Status == "completed" && p.State.Output != nil {
					out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: p.CallID, Result: p.State.Output})
				} else if p.State.Status == "error" && p.State.Error != nil {
					out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: p.CallID, Result: p.State.Error})
				} else if p.State.Status == "completed" || p.State.Status == "error" {
					malformed++
				}
			}
		}
	}
	return out, malformed, used, nil
}
func partTime(p partFile) int64 {
	if p.Time.Start != 0 {
		return p.Time.Start
	}
	if p.Time.Created != 0 {
		return p.Time.Created
	}
	return p.Time.Updated
}
func (a *Adapter) Open(ctx context.Context, s source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Product != a.Product() {
		return nil, errors.New("opencode: invalid session reference")
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID || auth.format != s.FormatVersion {
		return nil, errors.New("opencode: unknown session reference")
	}
	if auth.format == databaseFormat {
		return a.openSQLite(ctx, auth, s)
	}
	snap, err := a.loadSnapshot(ctx, s.OpaqueRef)
	if err != nil {
		return nil, errors.New("opencode: stale session reference")
	}
	sf, events, _, _, parseErr := parseSnapshot(snap)
	if parseErr != nil || "opencode:"+sf.ID != s.ID || snap.digest != auth.digest {
		return nil, errors.New("opencode: source changed since discovery")
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, event := range events {
		if encoder.Encode(event) != nil {
			return nil, errors.New("opencode: stale session reference")
		}
	}
	return io.NopCloser(bytes.NewReader(output.Bytes())), nil
}
