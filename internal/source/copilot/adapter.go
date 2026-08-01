package copilot

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const maxLineBytes = 1 << 20
const maxSessionBytes int64 = 4 << 20

type Adapter struct {
	roots  []string
	scanMu sync.Mutex
	mu     sync.RWMutex
	known  map[string]authorization
}
type authorization struct{ id, digest string }

func New(roots ...string) *Adapter {
	if len(roots) == 0 {
		if h, e := os.UserHomeDir(); e == nil {
			roots = defaultRoots(h)
		}
	}
	return &Adapter{roots: roots, known: map[string]authorization{}}
}
func defaultRoots(h string) []string {
	return []string{filepath.Join(h, "Library", "Application Support", "Code", "User"), filepath.Join(h, "Library", "Application Support", "Code - Insiders", "User"), filepath.Join(h, "Library", "Application Support", "VSCodium", "User"), filepath.Join(h, ".config", "Code", "User"), filepath.Join(h, ".config", "Code - Insiders", "User"), filepath.Join(h, ".config", "VSCodium", "User"), filepath.Join(h, "AppData", "Roaming", "Code", "User"), filepath.Join(h, "AppData", "Roaming", "Code - Insiders", "User"), filepath.Join(h, "AppData", "Roaming", "VSCodium", "User")}
}
func (*Adapter) Product() string                   { return "vscode-copilot" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type sess struct {
	Version         int       `json:"version"`
	SessionID       string    `json:"sessionId"`
	CreationDate    int64     `json:"creationDate"`
	LastMessageDate int64     `json:"lastMessageDate"`
	Requests        []request `json:"requests"`
}
type request struct {
	RequestID string `json:"requestId"`
	Message   struct {
		Text string `json:"text"`
	} `json:"message"`
	Response  []json.RawMessage `json:"response"`
	ModelID   string            `json:"modelId"`
	Timestamp int64             `json:"timestamp"`
}
type item struct {
	Kind, Value, ToolID, ToolCallID                       string
	InvocationMessage, PastTenseMessage, ToolSpecificData json.RawMessage
	IsComplete                                            bool `json:"isComplete"`
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	type candidate struct {
		s        source.Session
		digest   string
		rank     int
		conflict bool
	}
	by := map[string]candidate{}
	for _, root := range a.roots {
		_ = filepath.WalkDir(filepath.Join(root, "workspaceStorage"), func(p string, d os.DirEntry, e error) error {
			if x := ctx.Err(); x != nil {
				return x
			}
			if e != nil || d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			parent := filepath.Base(filepath.Dir(p))
			if (parent != "chatSessions" && parent != "chatEditingSessions") || (!strings.HasSuffix(p, ".json") && !strings.HasSuffix(p, ".jsonl")) {
				return nil
			}
			s, digest, ok := a.inspect(root, p)
			if ok {
				rank := duplicateRank(p)
				old, yes := by[s.ID]
				if !yes || rank < old.rank {
					by[s.ID] = candidate{s: s, digest: digest, rank: rank}
				} else if rank == old.rank && old.s.OpaqueRef != p {
					old.conflict = true
					by[s.ID] = old
				}
			}
			return nil
		})
	}
	out := []source.Session{}
	for _, c := range by {
		if !c.conflict {
			out = append(out, c.s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	next := map[string]authorization{}
	for _, s := range out {
		c := by[s.ID]
		next[s.OpaqueRef] = authorization{s.ID, c.digest}
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
	return out, ctx.Err()
}
func read(root, p string) ([]byte, error) {
	f, e := safeopen.Open(root, p, maxSessionBytes)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
}
func duplicateRank(p string) int {
	r := 0
	if strings.HasSuffix(p, ".jsonl") {
		r++
	}
	if filepath.Base(filepath.Dir(p)) == "chatEditingSessions" {
		r += 2
	}
	return r
}
func (a *Adapter) inspect(root, p string) (source.Session, string, bool) {
	data, e := read(root, p)
	if e != nil {
		return source.Session{}, "", false
	}
	s, mal, e := decode(p, data)
	if e != nil || s.Version != 3 || s.SessionID == "" || strings.ContainsAny(s.SessionID, `/\\#`) || len(s.Requests) == 0 {
		return source.Session{}, "", false
	}
	ev, recognizedInvalid := events(s)
	mal += recognizedInvalid
	count := 0
	for _, e := range ev {
		if e["type"] == "message" {
			count++
		}
	}
	if count == 0 {
		return source.Session{}, "", false
	}
	hashDir := filepath.Dir(filepath.Dir(p))
	scope := workspaceScope(root, hashDir, s.SessionID)
	sum := sha256.Sum256(data)
	return source.Session{ID: "vscode-copilot:" + s.SessionID, Product: "vscode-copilot", FormatVersion: fmt.Sprintf("v%d", s.Version), AdapterVersion: "1", Capabilities: a.Capabilities(), Scope: scope, StartedAt: time.UnixMilli(s.CreationDate), EndedAt: time.UnixMilli(s.LastMessageDate), MessageCount: count, MalformedCount: mal, OpaqueRef: p}, fmt.Sprintf("%x", sum[:]), true
}
func workspaceScope(root, dir, id string) source.ScopeRef {
	var w struct{ Folder, Workspace string }
	p := filepath.Join(dir, "workspace.json")
	if _, e := readJSON(root, p, &w); e == nil {
		v := w.Folder
		if v == "" {
			v = w.Workspace
		}
		if u, e := url.Parse(v); e == nil && u.Scheme == "file" {
			path, _ := url.PathUnescape(u.Path)
			if len(path) > 2 && path[0] == '/' && path[2] == ':' {
				path = path[1:]
			}
			path = filepath.FromSlash(path)
			if filepath.IsAbs(path) {
				return source.ScopeRef{Type: source.ScopeWorkspace, Root: path, Label: filepath.Base(path)}
			}
		}
	}
	sum := sha256.Sum256([]byte("vscode-copilot\x00" + id))
	return source.ScopeRef{Type: source.ScopeConversationGroup, Root: fmt.Sprintf("%x", sum[:12]), Label: "VS Code Copilot sessions"}
}
func readJSON(root, p string, v any) (int64, error) {
	b, e := read(root, p)
	if e != nil {
		return 0, e
	}
	return int64(len(b)), json.Unmarshal(b, v)
}
func decode(p string, data []byte) (sess, int, error) {
	if strings.HasSuffix(p, ".json") {
		var s sess
		e := json.Unmarshal(data, &s)
		return s, 0, e
	}
	return replay(data)
}
func replay(data []byte) (sess, int, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), maxLineBytes+1)
	var doc any
	bad := 0
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		var op struct {
			Kind int   `json:"kind"`
			K    []any `json:"k"`
			V    any   `json:"v"`
			I    *int  `json:"i"`
		}
		if json.Unmarshal(sc.Bytes(), &op) != nil {
			bad++
			continue
		}
		if op.Kind == 0 {
			if len(op.K) != 0 || op.I != nil || op.V == nil {
				bad++
				continue
			}
			doc = op.V
			continue
		}
		if doc == nil || len(op.K) == 0 || !validKeys(op.K) || (op.Kind != 1 && op.Kind != 2 && op.Kind != 3) {
			bad++
			continue
		}
		_ = mutate(&doc, op.Kind, op.K, op.V, op.I) // Unknown paths are valid forward-compatible mutations.
	}
	if sc.Err() != nil {
		return sess{}, bad, sc.Err()
	}
	b, e := json.Marshal(doc)
	if e != nil {
		return sess{}, bad, e
	}
	var s sess
	e = json.Unmarshal(b, &s)
	return s, bad, e
}
func mutate(doc *any, kind int, path []any, val any, index *int) bool {
	next, ok := mutateNode(*doc, kind, path, val, index)
	if ok {
		*doc = next
	}
	return ok
}
func validKeys(keys []any) bool {
	for _, k := range keys {
		switch v := k.(type) {
		case string:
			if v == "" {
				return false
			}
		case float64:
			if v < 0 || v != float64(int(v)) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func mutateNode(node any, kind int, path []any, val any, index *int) (any, bool) {
	key := keyString(path[0])
	if len(path) > 1 {
		childNode, ok := child(node, key)
		if !ok {
			return node, false
		}
		next, ok := mutateNode(childNode, kind, path[1:], val, index)
		if !ok {
			return node, false
		}
		return replaceChild(node, key, next)
	}
	switch p := node.(type) {
	case map[string]any:
		if kind == 1 {
			p[key] = val
			return p, true
		}
		if kind == 3 {
			delete(p, key)
			return p, true
		}
		arr, ok := p[key].([]any)
		if !ok {
			return node, false
		}
		next, ok := pushArray(arr, val, index)
		if ok {
			p[key] = next
		}
		return p, ok
	case []any:
		i, e := strconv.Atoi(key)
		if e != nil || i < 0 || i >= len(p) {
			return node, false
		}
		if kind == 1 {
			p[i] = val
			return p, true
		}
		if kind == 3 {
			return append(p[:i:i], p[i+1:]...), true
		}
		arr, ok := p[i].([]any)
		if !ok {
			return node, false
		}
		next, ok := pushArray(arr, val, index)
		if ok {
			p[i] = next
		}
		return p, ok
	}
	return node, false
}
func replaceChild(node any, key string, val any) (any, bool) {
	switch p := node.(type) {
	case map[string]any:
		p[key] = val
		return p, true
	case []any:
		i, e := strconv.Atoi(key)
		if e != nil || i < 0 || i >= len(p) {
			return node, false
		}
		p[i] = val
		return p, true
	}
	return node, false
}
func pushArray(a []any, val any, index *int) ([]any, bool) {
	vals, ok := val.([]any)
	if !ok {
		return a, false
	}
	i := len(a)
	if index != nil {
		i = *index
	}
	if i < 0 || i > len(a) {
		return a, false
	}
	return append(append(a[:i:i], vals...), a[i:]...), true
}
func keyString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if n, ok := v.(float64); ok {
		return strconv.Itoa(int(n))
	}
	return fmt.Sprint(v)
}
func child(node any, key string) (any, bool) {
	switch n := node.(type) {
	case map[string]any:
		v, ok := n[key]
		return v, ok
	case []any:
		i, e := strconv.Atoi(key)
		if e != nil || i < 0 || i >= len(n) {
			return nil, false
		}
		return n[i], true
	}
	return nil, false
}
func events(s sess) ([]map[string]any, int) {
	var out []map[string]any
	bad := 0
	for _, r := range s.Requests {
		if r.RequestID == "" {
			bad++
			continue
		}
		ts := time.UnixMilli(r.Timestamp).UTC().Format(time.RFC3339Nano)
		if strings.TrimSpace(r.Message.Text) != "" {
			out = append(out, map[string]any{"type": "message", "role": "user", "content": r.Message.Text, "timestamp": ts})
		}
		for _, raw := range r.Response {
			var x item
			if json.Unmarshal(raw, &x) != nil {
				bad++
				continue
			}
			switch x.Kind {
			case "toolInvocationSerialized":
				if x.ToolCallID == "" || x.ToolID == "" {
					bad++
					continue
				}
				input := map[string]any{}
				if len(x.InvocationMessage) > 0 {
					v, ok := rawValue(x.InvocationMessage)
					if !ok {
						bad++
						continue
					}
					input["invocation"] = v
				}
				if len(x.ToolSpecificData) > 0 {
					v, ok := rawValue(x.ToolSpecificData)
					if !ok {
						bad++
						continue
					}
					input["tool_specific_data"] = v
				}
				out = append(out, map[string]any{"type": "tool_use", "call_id": x.ToolCallID, "name": x.ToolID, "input": input, "timestamp": ts})
				if x.IsComplete && x.Value != "" {
					out = append(out, map[string]any{"type": "tool_result", "call_id": x.ToolCallID, "result": x.Value, "timestamp": ts})
				}
			case "markdownContent", "thinking":
				if strings.TrimSpace(x.Value) != "" {
					out = append(out, map[string]any{"type": "message", "role": "assistant", "content": x.Value, "timestamp": ts, "model": r.ModelID})
				} else {
					bad++
				}
			}
		}
	}
	return out, bad
}
func rawValue(r json.RawMessage) (any, bool) {
	var v any
	if json.Unmarshal(r, &v) != nil {
		return nil, false
	}
	return v, true
}
func (a *Adapter) Open(ctx context.Context, s source.Session) (io.ReadCloser, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if s.Product != a.Product() {
		return nil, errors.New("copilot: invalid reference")
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID {
		return nil, errors.New("copilot: unknown reference")
	}
	var root string
	for _, r := range a.roots {
		if rel, e := filepath.Rel(r, s.OpaqueRef); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			root = r
			break
		}
	}
	b, e := read(root, s.OpaqueRef)
	if e != nil {
		return nil, e
	}
	sum := sha256.Sum256(b)
	if fmt.Sprintf("%x", sum[:]) != auth.digest {
		return nil, errors.New("copilot: source changed since discovery")
	}
	ss, _, e := decode(s.OpaqueRef, b)
	if e != nil || "vscode-copilot:"+ss.SessionID != s.ID {
		return nil, errors.New("copilot: stale reference")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	ev, _ := events(ss)
	for _, x := range ev {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		_ = enc.Encode(x)
	}
	return io.NopCloser(bytes.NewReader(out.Bytes())), nil
}
