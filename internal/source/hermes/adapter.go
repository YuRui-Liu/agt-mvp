package hermes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const maxLineBytes = 1 << 20
const maxSessionBytes = 4 << 20

type authorization struct {
	id, digest, sourcePath string
}
type Adapter struct {
	root      string
	configErr error
	scanMu    sync.Mutex
	mu        sync.RWMutex
	known     map[string]authorization
}

func New(roots ...string) *Adapter {
	if len(roots) > 1 {
		return &Adapter{configErr: errors.New("hermes-agent: multiple roots are not supported"), known: map[string]authorization{}}
	}
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	if root == "" {
		if env := os.Getenv("HERMES_SESSIONS_DIR"); filepath.IsAbs(env) {
			root = filepath.Clean(env)
		}
	}
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".hermes", "sessions")
		}
	}
	return &Adapter{root: root, known: map[string]authorization{}}
}
func (*Adapter) Product() string                   { return "hermes-agent" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type record struct {
	Role            string `json:"role"`
	Content         string `json:"content"`
	Timestamp       string `json:"timestamp"`
	ToolCallID      string `json:"tool_call_id"`
	ParentSessionID string `json:"parent_session_id"`
	ToolCalls       []struct {
		ID       string                           `json:"id"`
		Function struct{ Name, Arguments string } `json:"function"`
	} `json:"tool_calls"`
}
type event struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   any    `json:"content,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	Result    any    `json:"result,omitempty"`
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.configErr != nil {
		return nil, a.configErr
	}
	stateSessions, stateAuth, _, stateErr := a.discoverState(ctx)
	if stateErr != nil {
		return nil, stateErr
	}
	files, err := os.ReadDir(a.root)
	if os.IsNotExist(err) {
		a.replaceKnown(stateAuth)
		return stateSessions, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, f := range files {
		if f.Type().IsRegular() && (strings.HasSuffix(f.Name(), ".jsonl") || strings.HasPrefix(f.Name(), "session_") && strings.HasSuffix(f.Name(), ".json")) {
			paths = append(paths, filepath.Join(a.root, f.Name()))
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		ij, jj := strings.HasSuffix(paths[i], ".jsonl"), strings.HasSuffix(paths[j], ".jsonl")
		if ij != jj {
			return ij
		}
		return paths[i] < paths[j]
	})
	out := append([]source.Session(nil), stateSessions...)
	next := map[string]authorization{}
	for k, v := range stateAuth {
		next[k] = v
	}
	ids := map[string]bool{}
	for _, s := range stateSessions {
		ids[s.ID] = true
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s, auth, _, ok := a.snapshot(path)
		if !ok || ids[s.ID] {
			continue
		}
		ids[s.ID] = true
		out = append(out, s)
		next[path] = auth
	}
	for i := range out {
		if out[i].ParentID == "" {
			continue
		}
		canonical := "hermes-agent:" + out[i].ParentID
		if ids[canonical] {
			out[i].ParentID = canonical
		} else {
			out[i].ParentID = ""
		}
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
func (a *Adapter) snapshot(path string) (source.Session, authorization, []byte, bool) {
	f, err := safeopen.Open(a.root, path, maxSessionBytes)
	if err != nil {
		return source.Session{}, authorization{}, nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	info, statErr := f.Stat()
	f.Close()
	if err != nil || statErr != nil || len(data) > maxSessionBytes {
		return source.Session{}, authorization{}, nil, false
	}
	if !linesWithinLimit(data) {
		return source.Session{}, authorization{}, nil, false
	}
	var events []event
	var messages, malformed int
	var start, end time.Time
	var ok bool
	format := "jsonl-v1"
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if strings.HasSuffix(path, ".json") {
		format = "json-v1"
		stem = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "session_"), ".json")
		events, messages, malformed, start, end, ok = parseJSON(data)
	} else {
		_, events, messages, malformed, start, end, ok = parse(data)
	}
	parentID := hermesParent(data, strings.HasSuffix(path, ".json"))
	if !ok || stem == "" || strings.ContainsAny(stem, `/\\:`) {
		return source.Session{}, authorization{}, nil, false
	}
	var output bytes.Buffer
	enc := json.NewEncoder(&output)
	for _, e := range events {
		if enc.Encode(e) != nil {
			return source.Session{}, authorization{}, nil, false
		}
	}
	identitySum := sha256.Sum256([]byte(filepath.Dir(path)))
	scope := source.ScopeRef{Type: source.ScopeConversationGroup, Root: fmt.Sprintf("hermes-agent:%x", identitySum[:12]), Label: "Hermes Agent conversations"}
	if start.IsZero() {
		start = info.ModTime()
	}
	if end.IsZero() {
		end = info.ModTime()
	}
	s := source.Session{ID: "hermes-agent:" + stem, Product: "hermes-agent", FormatVersion: format, AdapterVersion: "1", Capabilities: a.Capabilities(), Scope: scope, StartedAt: start, EndedAt: end, MessageCount: messages, MalformedCount: malformed, ParentID: parentID, OpaqueRef: path}
	contentSum := sha256.Sum256(data)
	s.SnapshotID = fmt.Sprintf("%x", contentSum[:])
	auth := authorization{id: s.ID, digest: s.SnapshotID, sourcePath: path}
	return s, auth, append([]byte(nil), output.Bytes()...), true
}
func hermesParent(data []byte, jsonFormat bool) string {
	if jsonFormat {
		var x struct {
			Parent string `json:"parent_session_id"`
		}
		_ = json.Unmarshal(data, &x)
		return x.Parent
	}
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	var r record
	_ = json.Unmarshal(line, &r)
	return r.ParentSessionID
}
func linesWithinLimit(data []byte) bool {
	n := 0
	for _, b := range data {
		if b == '\n' {
			n = 0
			continue
		}
		n++
		if n > maxLineBytes {
			return false
		}
	}
	return true
}
func parseJSON(data []byte) ([]event, int, int, time.Time, time.Time, bool) {
	var root struct {
		SessionStart string   `json:"session_start"`
		LastUpdated  string   `json:"last_updated"`
		Messages     []record `json:"messages"`
	}
	if json.Unmarshal(data, &root) != nil || root.Messages == nil {
		return nil, 0, 0, time.Time{}, time.Time{}, false
	}
	start, _ := time.Parse(time.RFC3339Nano, root.SessionStart)
	end, _ := time.Parse(time.RFC3339Nano, root.LastUpdated)
	var out []event
	messages, bad := 0, 0
	for _, r := range root.Messages {
		trackTime(r.Timestamp, &start, &end)
		switch r.Role {
		case "user":
			if strings.TrimSpace(r.Content) == "" {
				bad++
				continue
			}
			out = append(out, event{Type: "message", Role: "user", Content: r.Content, Timestamp: r.Timestamp})
			messages++
		case "assistant":
			valid := false
			if strings.TrimSpace(r.Content) != "" {
				out = append(out, event{Type: "message", Role: "assistant", Content: r.Content, Timestamp: r.Timestamp})
				valid = true
			}
			for _, tc := range r.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					bad++
					continue
				}
				var input any = map[string]any{}
				if tc.Function.Arguments != "" && json.Unmarshal([]byte(tc.Function.Arguments), &input) != nil {
					bad++
					continue
				}
				out = append(out, event{Type: "tool_use", Timestamp: r.Timestamp, CallID: tc.ID, Name: tc.Function.Name, Input: input})
				valid = true
			}
			if valid {
				messages++
			} else {
				bad++
			}
		case "tool":
			if r.ToolCallID == "" {
				bad++
				continue
			}
			out = append(out, event{Type: "tool_result", Timestamp: r.Timestamp, CallID: r.ToolCallID, Result: r.Content})
			messages++
		default:
			bad++
		}
	}
	return out, messages, bad, start, end, len(out) > 0
}
func parse(data []byte) (record, []event, int, int, time.Time, time.Time, bool) {
	var header record
	var out []event
	messages, malformed := 0, 0
	var start, end time.Time
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), maxLineBytes+1)
	first := true
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r record
		if json.Unmarshal(line, &r) != nil {
			malformed++
			continue
		}
		if first {
			first = false
			if r.Role != "session_meta" {
				return record{}, nil, 0, malformed, time.Time{}, time.Time{}, false
			}
			header = r
			trackTime(r.Timestamp, &start, &end)
			continue
		}
		ts := r.Timestamp
		trackTime(ts, &start, &end)
		switch r.Role {
		case "user":
			if strings.TrimSpace(r.Content) == "" {
				malformed++
				continue
			}
			out = append(out, event{Type: "message", Role: "user", Content: r.Content, Timestamp: ts})
			messages++
		case "assistant":
			if strings.TrimSpace(r.Content) != "" {
				out = append(out, event{Type: "message", Role: "assistant", Content: r.Content, Timestamp: ts})
			}
			valid := strings.TrimSpace(r.Content) != ""
			for _, tc := range r.ToolCalls {
				if tc.ID == "" || tc.Function.Name == "" {
					malformed++
					continue
				}
				var input any = map[string]any{}
				if tc.Function.Arguments != "" && json.Unmarshal([]byte(tc.Function.Arguments), &input) != nil {
					malformed++
					continue
				}
				out = append(out, event{Type: "tool_use", Timestamp: ts, CallID: tc.ID, Name: tc.Function.Name, Input: input})
				valid = true
			}
			if !valid {
				malformed++
				continue
			}
			messages++
		case "tool":
			if r.ToolCallID == "" {
				malformed++
				continue
			}
			out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: r.ToolCallID, Result: r.Content})
			messages++
		default:
			malformed++
		}
	}
	if sc.Err() != nil {
		return record{}, nil, 0, malformed, time.Time{}, time.Time{}, false
	}
	return header, out, messages, malformed, start, end, len(out) > 0
}
func contentEvents(role string, content any, ts string) ([]event, bool) {
	switch v := content.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil, false
		}
		return []event{{Type: "message", Role: role, Content: v, Timestamp: ts}}, true
	case []any:
		var out []event
		for _, raw := range v {
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, false
			}
			typ, _ := m["type"].(string)
			switch typ {
			case "text":
				text, _ := m["text"].(string)
				if strings.TrimSpace(text) == "" {
					return nil, false
				}
				out = append(out, event{Type: "message", Role: role, Content: text, Timestamp: ts})
			case "toolCall", "tool_use":
				if role != "assistant" {
					return nil, false
				}
				id, _ := m["id"].(string)
				name, _ := m["name"].(string)
				if id == "" || name == "" {
					return nil, false
				}
				input := m["arguments"]
				if input == nil {
					input = m["input"]
				}
				out = append(out, event{Type: "tool_use", Timestamp: ts, CallID: id, Name: name, Input: input})
			case "thinking":
				text, _ := m["thinking"].(string)
				if role != "assistant" || strings.TrimSpace(text) == "" {
					return nil, false
				}
				out = append(out, event{Type: "message", Role: role, Content: []any{map[string]any{"type": "thinking", "thinking": text}}, Timestamp: ts})
			default:
				return nil, false
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}
func trackTime(raw string, start, end *time.Time) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return
	}
	if start.IsZero() || t.Before(*start) {
		*start = t
	}
	if end.IsZero() || t.After(*end) {
		*end = t
	}
}
func (a *Adapter) Open(ctx context.Context, s source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Product != a.Product() {
		return nil, errors.New("hermes-agent: invalid session reference")
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID || auth.digest != s.SnapshotID {
		return nil, errors.New("hermes-agent: unknown session reference")
	}
	path := auth.sourcePath
	if path == "" {
		path = s.OpaqueRef
	}
	if strings.HasPrefix(s.OpaqueRef, "state:") {
		_, fresh, outputs, err := a.discoverState(ctx)
		current, exists := fresh[s.OpaqueRef]
		if err != nil || !exists || current.id != s.ID || current.digest != auth.digest {
			return nil, errors.New("hermes-agent: source changed since discovery")
		}
		return io.NopCloser(bytes.NewReader(outputs[s.OpaqueRef])), nil
	}
	fresh, freshAuth, output, valid := a.snapshot(path)
	if !valid || fresh.ID != s.ID || freshAuth.digest != auth.digest {
		return nil, errors.New("hermes-agent: source changed since discovery")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
