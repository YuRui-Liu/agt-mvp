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
const maxWorkspaceDirs = 4096
const maxSessionFiles = 4096
const maxSessionRecords = 4096
const maxSessionEvents = 8192
const maxMutationDepth = 64

var errInvalidRoot = errors.New("copilot: invalid root")
var errScanLimit = errors.New("copilot: scan limit exceeded")
var errInvalidReference = errors.New("copilot: invalid reference")
var errUnknownReference = errors.New("copilot: unknown reference")
var errChangedSource = errors.New("copilot: source changed")
var errStaleReference = errors.New("copilot: stale reference")

type pathIdentity struct{ directories []safeopen.Identity }

type Adapter struct {
	roots        []string
	configErr    error
	scanMu       sync.Mutex
	mu           sync.RWMutex
	known        map[string]authorization
	afterBind    func(string)
	beforeOpen   func(string)
	beforeCommit func()
}
type authorization struct {
	id, digest, metadata, root string
	pathIdentity               pathIdentity
	rootIdentity               safeopen.Identity
	relative                   string
}

func New(roots ...string) *Adapter {
	if len(roots) == 0 {
		if h, e := os.UserHomeDir(); e == nil {
			roots = defaultRoots(h)
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{roots: clean, configErr: err, known: map[string]authorization{}}
}
func defaultRoots(h string) []string {
	return []string{filepath.Join(h, "Library", "Application Support", "Code", "User"), filepath.Join(h, "Library", "Application Support", "Code - Insiders", "User"), filepath.Join(h, "Library", "Application Support", "VSCodium", "User"), filepath.Join(h, ".config", "Code", "User"), filepath.Join(h, ".config", "Code - Insiders", "User"), filepath.Join(h, ".config", "VSCodium", "User"), filepath.Join(h, "AppData", "Roaming", "Code", "User"), filepath.Join(h, "AppData", "Roaming", "Code - Insiders", "User"), filepath.Join(h, "AppData", "Roaming", "VSCodium", "User")}
}
func validatedRoots(roots []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		canonical, err := canonicalizeRoot(root)
		if err != nil {
			return nil, errInvalidRoot
		}
		if !seen[canonical] {
			seen[canonical] = true
			out = append(out, canonical)
		}
	}
	return out, nil
}
func canonicalizeRoot(root string) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", errInvalidRoot
	}
	original := root
	volume := filepath.VolumeName(root)
	remainder := strings.TrimPrefix(root, volume+string(filepath.Separator))
	parts := strings.Split(remainder, string(filepath.Separator))
	current := volume + string(filepath.Separator)
	start := 0
	if len(parts) > 0 && parts[0] != "" {
		first := filepath.Join(current, parts[0])
		resolved, err := filepath.EvalSymlinks(first)
		if err == nil {
			current = resolved
			start = 1
		} else if !os.IsNotExist(err) {
			return "", errInvalidRoot
		}
	}
	for i := start; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		next := filepath.Join(current, parts[i])
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			for ; i < len(parts); i++ {
				current = filepath.Join(current, parts[i])
			}
			return original, nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errInvalidRoot
		}
		current = next
	}
	return original, nil
}
func samePathIdentity(left, right pathIdentity) bool {
	if len(left.directories) != len(right.directories) {
		return false
	}
	for i := range left.directories {
		if left.directories[i] != right.directories[i] {
			return false
		}
	}
	return true
}
func (*Adapter) Product() string                   { return "vscode-copilot" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type sess struct {
	Version         int       `json:"version"`
	SessionID       string    `json:"sessionId"`
	Responder       string    `json:"responderUsername"`
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
	Agent     chatAgent         `json:"agent"`
}
type chatAgent struct {
	ID                   string              `json:"id"`
	ExtensionID          extensionIdentifier `json:"extensionId"`
	ExtensionPublisherID string              `json:"extensionPublisherId"`
}
type extensionIdentifier struct {
	Value string `json:"value"`
	Lower string `json:"_lower"`
}

func hasCopilotProvenance(session sess) bool {
	if (session.Responder != "" && session.Responder != "GitHub Copilot") || len(session.Requests) == 0 || len(session.Requests) > maxSessionRecords {
		return false
	}
	for _, request := range session.Requests {
		if !copilotParticipant(request.Agent.ID) ||
			request.Agent.ExtensionID.Value != "GitHub.copilot-chat" ||
			(request.Agent.ExtensionID.Lower != "" && request.Agent.ExtensionID.Lower != "github.copilot-chat") ||
			request.Agent.ExtensionPublisherID != "GitHub" {
			return false
		}
	}
	return true
}

func copilotParticipant(id string) bool {
	switch id {
	case "github.copilot.default",
		"github.copilot.editingSession",
		"github.copilot.editingSessionEditor",
		"github.copilot.editsAgent",
		"github.copilot.notebook",
		"github.copilot.notebookEditorAgent",
		"github.copilot.vscode",
		"github.copilot.terminal",
		"github.copilot.terminalPanel":
		return true
	default:
		return false
	}
}

type item struct {
	Kind, Value, ToolID, ToolCallID                       string
	InvocationMessage, PastTenseMessage, ToolSpecificData json.RawMessage
	IsComplete                                            bool `json:"isComplete"`
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.replaceKnown(nil)
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if a.configErr != nil {
		return nil, a.configErr
	}
	type candidate struct {
		s        source.Session
		auth     authorization
		rank     int
		conflict bool
	}
	by := map[string]candidate{}
	entriesScanned := 0
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validated, err := canonicalizeRoot(root)
		if err != nil || validated != root {
			return nil, errInvalidRoot
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errInvalidRoot
		}
		if a.afterBind != nil {
			a.afterBind(root)
		}
		workspaces, err := bound.ReadDirLimit("workspaceStorage", maxWorkspaceDirs)
		if err != nil {
			bound.Close()
			if errors.Is(err, safeopen.ErrDirectoryLimit) {
				return nil, errScanLimit
			}
			continue
		}
		for _, workspace := range workspaces {
			if err := ctx.Err(); err != nil {
				bound.Close()
				return nil, err
			}
			if !workspace.IsDir() || workspace.Type()&os.ModeSymlink != 0 {
				continue
			}
			for _, sessionDir := range []string{"chatSessions", "chatEditingSessions"} {
				dirRel := filepath.Join("workspaceStorage", workspace.Name(), sessionDir)
				remaining := maxSessionFiles - entriesScanned
				entries, err := bound.ReadDirLimit(dirRel, remaining)
				if err != nil {
					if errors.Is(err, safeopen.ErrDirectoryLimit) {
						bound.Close()
						return nil, errScanLimit
					}
					continue
				}
				entriesScanned += len(entries)
				for _, entry := range entries {
					if err := ctx.Err(); err != nil {
						bound.Close()
						return nil, err
					}
					if !entry.Type().IsRegular() || (!strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".jsonl")) {
						continue
					}
					relative := filepath.Join(dirRel, entry.Name())
					path := filepath.Join(root, relative)
					if a.beforeOpen != nil {
						a.beforeOpen(path)
					}
					s, auth, ok := a.inspect(ctx, bound, root, path, relative)
					if !ok {
						continue
					}
					rank := duplicateRank(path)
					old, yes := by[s.ID]
					if !yes || rank < old.rank {
						by[s.ID] = candidate{s: s, auth: auth, rank: rank}
					} else if rank == old.rank && old.s.OpaqueRef != path {
						old.conflict = true
						by[s.ID] = old
					}
				}
			}
		}
		bound.Close()
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
		next[s.OpaqueRef] = c.auth
	}
	if a.beforeCommit != nil {
		a.beforeCommit()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.replaceKnown(next)
	return out, nil
}
func (a *Adapter) replaceKnown(next map[string]authorization) {
	if next == nil {
		next = map[string]authorization{}
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
}
func openBound(bound *safeopen.BoundRoot, relative string) (*os.File, pathIdentity, error) {
	f, identities, err := bound.OpenWithPathIdentity(relative, maxSessionBytes)
	if err != nil {
		return nil, pathIdentity{}, err
	}
	return f, pathIdentity{directories: identities}, nil
}
func readOpened(f *os.File) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	if err != nil || len(data) > int(maxSessionBytes) {
		return nil, errChangedSource
	}
	return data, nil
}
func readBound(bound *safeopen.BoundRoot, relative string) ([]byte, pathIdentity, error) {
	f, identities, err := openBound(bound, relative)
	if err != nil {
		return nil, pathIdentity{}, err
	}
	defer f.Close()
	data, err := readOpened(f)
	return data, identities, err
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
func (a *Adapter) inspect(ctx context.Context, bound *safeopen.BoundRoot, root, p, relative string) (source.Session, authorization, bool) {
	data, identityPath, e := readBound(bound, relative)
	if e != nil {
		return source.Session{}, authorization{}, false
	}
	s, mal, e := decode(ctx, p, data)
	if e != nil || s.Version != 3 || s.SessionID == "" || strings.ContainsAny(s.SessionID, `/\\#`) || !hasCopilotProvenance(s) {
		return source.Session{}, authorization{}, false
	}
	ev, recognizedInvalid, ok := eventsContext(ctx, s)
	if !ok {
		return source.Session{}, authorization{}, false
	}
	mal += recognizedInvalid
	count := 0
	hasTools := false
	hasReasoning := false
	for _, e := range ev {
		if e["type"] == "message" {
			count++
		}
		if e["type"] == "tool_use" || e["type"] == "tool_result" {
			hasTools = true
		}
		if blocks, ok := e["content"].([]any); ok {
			for _, block := range blocks {
				if object, ok := block.(map[string]any); ok && object["type"] == "thinking" {
					hasReasoning = true
				}
			}
		}
	}
	if count == 0 {
		return source.Session{}, authorization{}, false
	}
	hashRel := filepath.Dir(filepath.Dir(relative))
	scope := workspaceScope(bound, hashRel, s.SessionID)
	sum := sha256.Sum256(data)
	digest := fmt.Sprintf("%x", sum[:])
	capabilities := []source.Capability{source.CapabilityMessages}
	if hasTools {
		capabilities = append(capabilities, source.CapabilityTools)
	}
	if hasReasoning {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	session := source.Session{ID: "vscode-copilot:" + s.SessionID, Product: "vscode-copilot", FormatVersion: fmt.Sprintf("v%d", s.Version), AdapterVersion: "2", Capabilities: capabilities, Scope: scope, StartedAt: time.UnixMilli(s.CreationDate), EndedAt: time.UnixMilli(s.LastMessageDate), MessageCount: count, MalformedCount: mal, SnapshotID: digest, OpaqueRef: p}
	auth := authorization{id: session.ID, digest: digest, metadata: sessionMetadata(session), root: root, pathIdentity: identityPath, rootIdentity: bound.Identity(), relative: relative}
	return session, auth, true
}
func workspaceScope(bound *safeopen.BoundRoot, dir, id string) source.ScopeRef {
	var w struct{ Folder, Workspace string }
	p := filepath.Join(dir, "workspace.json")
	if _, e := readJSON(bound, p, &w); e == nil {
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
func readJSON(bound *safeopen.BoundRoot, p string, v any) (int64, error) {
	b, _, e := readBound(bound, p)
	if e != nil {
		return 0, e
	}
	return int64(len(b)), json.Unmarshal(b, v)
}
func sessionMetadata(s source.Session) string {
	data, _ := json.Marshal(s)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}
func decode(ctx context.Context, p string, data []byte) (sess, int, error) {
	if err := ctx.Err(); err != nil {
		return sess{}, 0, err
	}
	if strings.HasSuffix(p, ".json") {
		var s sess
		e := json.Unmarshal(data, &s)
		return s, 0, e
	}
	return replay(ctx, data)
}
func replay(ctx context.Context, data []byte) (sess, int, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), maxLineBytes+1)
	var doc any
	bad := 0
	records := 0
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return sess{}, bad, err
		}
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		records++
		if records > maxSessionRecords {
			return sess{}, bad, errScanLimit
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
	if len(keys) == 0 || len(keys) > maxMutationDepth {
		return false
	}
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
	out, bad, _ := eventsContext(context.Background(), s)
	return out, bad
}
func eventsContext(ctx context.Context, s sess) ([]map[string]any, int, bool) {
	var out []map[string]any
	bad := 0
	records := 0
	for _, r := range s.Requests {
		if ctx.Err() != nil {
			return nil, bad, false
		}
		records++
		if records > maxSessionRecords {
			return nil, bad, false
		}
		if r.RequestID == "" {
			bad++
			continue
		}
		ts := time.UnixMilli(r.Timestamp).UTC().Format(time.RFC3339Nano)
		if strings.TrimSpace(r.Message.Text) != "" {
			out = append(out, map[string]any{"type": "message", "role": "user", "content": r.Message.Text, "timestamp": ts})
		}
		for _, raw := range r.Response {
			records++
			if records > maxSessionRecords {
				return nil, bad, false
			}
			if ctx.Err() != nil || len(out) >= maxSessionEvents {
				return nil, bad, false
			}
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
			case "markdownContent":
				if strings.TrimSpace(x.Value) != "" {
					out = append(out, map[string]any{"type": "message", "role": "assistant", "content": x.Value, "timestamp": ts, "model": r.ModelID})
				} else {
					bad++
				}
			case "thinking":
				if strings.TrimSpace(x.Value) != "" {
					content := []any{map[string]any{"type": "thinking", "thinking": x.Value}}
					out = append(out, map[string]any{"type": "message", "role": "assistant", "content": content, "timestamp": ts, "model": r.ModelID})
				} else {
					bad++
				}
			}
		}
	}
	if len(out) > maxSessionEvents {
		return nil, bad, false
	}
	return out, bad, true
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
		return nil, errInvalidReference
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID || auth.metadata != sessionMetadata(s) {
		return nil, errUnknownReference
	}
	validated, err := canonicalizeRoot(auth.root)
	if err != nil || validated != auth.root {
		return nil, errInvalidReference
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errChangedSource
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errChangedSource
	}
	f, identityPath, err := openBound(bound, auth.relative)
	if err != nil {
		return nil, errChangedSource
	}
	defer f.Close()
	if !samePathIdentity(identityPath, auth.pathIdentity) {
		return nil, errChangedSource
	}
	b, err := readOpened(f)
	if err != nil {
		return nil, errChangedSource
	}
	sum := sha256.Sum256(b)
	if fmt.Sprintf("%x", sum[:]) != auth.digest {
		return nil, errChangedSource
	}
	ss, _, err := decode(ctx, s.OpaqueRef, b)
	if err != nil || ss.Version != 3 || !hasCopilotProvenance(ss) || "vscode-copilot:"+ss.SessionID != s.ID {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errStaleReference
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	ev, _, valid := eventsContext(ctx, ss)
	if !valid {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errStaleReference
	}
	for _, x := range ev {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		_ = enc.Encode(x)
	}
	return io.NopCloser(bytes.NewReader(out.Bytes())), nil
}
