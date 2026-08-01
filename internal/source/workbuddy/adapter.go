package workbuddy

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
	id, digest string
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
		return &Adapter{configErr: errors.New("workbuddy: multiple roots are not supported"), known: map[string]authorization{}}
	}
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	if root == "" {
		if env := os.Getenv("WORKBUDDY_PROJECTS_DIR"); filepath.IsAbs(env) {
			root = filepath.Clean(env)
		}
	}
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".workbuddy", "projects")
		}
	}
	return &Adapter{root: root, known: map[string]authorization{}}
}
func (*Adapter) Product() string                   { return "workbuddy" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type record struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Content   any    `json:"content"`
	CWD       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
	Name      string `json:"name"`
	CallID    string `json:"callId"`
	Arguments any    `json:"arguments"`
	Output    any    `json:"output"`
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
	agents, err := os.ReadDir(a.root)
	if os.IsNotExist(err) {
		a.replaceKnown(nil)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, agent := range agents {
		if !agent.IsDir() || agent.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(a.root, agent.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".jsonl") {
				paths = append(paths, filepath.Join(dir, f.Name()))
			}
			if !f.IsDir() || f.Type()&os.ModeSymlink != 0 {
				continue
			}
			subdir := filepath.Join(dir, f.Name(), "subagents")
			info, statErr := os.Lstat(subdir)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			subs, readErr := os.ReadDir(subdir)
			if readErr != nil {
				continue
			}
			for _, sub := range subs {
				if sub.Type().IsRegular() && strings.HasSuffix(sub.Name(), ".jsonl") {
					paths = append(paths, filepath.Join(subdir, sub.Name()))
				}
			}
		}
	}
	sort.Strings(paths)
	var out []source.Session
	next := map[string]authorization{}
	ids := map[string]bool{}
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
		marker := ":subagent:"
		at := strings.Index(out[i].ID, marker)
		if at < 0 {
			continue
		}
		parent := out[i].ID[:at]
		if ids[parent] {
			out[i].ParentID = parent
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
	rel, relErr := filepath.Rel(a.root, path)
	if relErr != nil {
		return source.Session{}, authorization{}, nil, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) < 2 {
		return source.Session{}, authorization{}, nil, false
	}
	project := parts[0]
	header, events, messages, malformed, start, end, ok := parse(data)
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
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
	identity := project
	id := "workbuddy:" + project + ":" + stem
	if len(parts) == 4 && parts[2] == "subagents" {
		parent := parts[1]
		identity = project
		id = "workbuddy:" + project + ":" + parent + ":subagent:" + stem
	}
	identitySum := sha256.Sum256([]byte(identity))
	scope := source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("workbuddy:%x", identitySum[:12]), Label: "WorkBuddy sessions"}
	if filepath.IsAbs(header.CWD) && filepath.Clean(header.CWD) == header.CWD {
		scope = source.ScopeRef{Type: source.ScopeProject, Root: header.CWD, Label: filepath.Base(header.CWD)}
	}
	if start.IsZero() {
		start = info.ModTime()
	}
	if end.IsZero() {
		end = info.ModTime()
	}
	s := source.Session{ID: id, Product: "workbuddy", FormatVersion: "jsonl-v1", AdapterVersion: "1", Capabilities: a.Capabilities(), Scope: scope, StartedAt: start, EndedAt: end, MessageCount: messages, MalformedCount: malformed, OpaqueRef: path}
	contentSum := sha256.Sum256(data)
	s.SnapshotID = fmt.Sprintf("%x", contentSum[:])
	auth := authorization{id: s.ID, digest: s.SnapshotID}
	return s, auth, append([]byte(nil), output.Bytes()...), true
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
			if r.Type != "message" || (r.Role != "user" && r.Role != "assistant") {
				return record{}, nil, 0, malformed, time.Time{}, time.Time{}, false
			}
			header = r
		}
		ts := r.Timestamp
		trackTime(ts, &start, &end)
		switch r.Type {
		case "message":
			if r.Role != "user" && r.Role != "assistant" {
				malformed++
				continue
			}
			evs, ok := contentEvents(r.Role, r.Content, ts)
			if !ok {
				malformed++
				continue
			}
			out = append(out, evs...)
			messages++
		case "function_call":
			if r.CallID == "" || r.Name == "" {
				malformed++
				continue
			}
			out = append(out, event{Type: "tool_use", Timestamp: ts, CallID: r.CallID, Name: r.Name, Input: r.Arguments})
			messages++
		case "function_call_result":
			if r.CallID == "" || r.Output == nil {
				malformed++
				continue
			}
			out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: r.CallID, Result: r.Output})
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
		return nil, errors.New("workbuddy: invalid session reference")
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID || auth.digest != s.SnapshotID {
		return nil, errors.New("workbuddy: unknown session reference")
	}
	fresh, freshAuth, output, valid := a.snapshot(s.OpaqueRef)
	if !valid || fresh.ID != s.ID || freshAuth.digest != auth.digest {
		return nil, errors.New("workbuddy: source changed since discovery")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
