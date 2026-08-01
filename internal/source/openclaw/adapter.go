package openclaw

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
		return &Adapter{configErr: errors.New("openclaw: multiple roots are not supported"), known: map[string]authorization{}}
	}
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	if root == "" {
		if env := os.Getenv("OPENCLAW_DIR"); filepath.IsAbs(env) {
			root = filepath.Clean(env)
		}
	}
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".openclaw", "agents")
		}
	}
	return &Adapter{root: root, known: map[string]authorization{}}
}
func (*Adapter) Product() string                   { return "openclaw" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type record struct {
	Type, ID, CWD, Timestamp string
	Message                  struct {
		Role       string `json:"role"`
		Content    any    `json:"content"`
		ToolCallID string `json:"toolCallId"`
	} `json:"message"`
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
	chosen := map[string]string{}
	for _, agent := range agents {
		if !agent.IsDir() || agent.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(a.root, agent.Name(), "sessions")
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if !f.Type().IsRegular() {
				continue
			}
			name := f.Name()
			marker := strings.Index(name, ".jsonl")
			if marker <= 0 {
				continue
			}
			suffix := name[marker+len(".jsonl"):]
			if suffix != "" && !strings.HasPrefix(suffix, ".deleted.") && suffix != ".full.bak" && !strings.HasPrefix(suffix, ".reset.") {
				continue
			}
			key := agent.Name() + "\x00" + name[:marker]
			candidate := filepath.Join(dir, name)
			previous := chosen[key]
			if previous == "" || betterArchive(previous, candidate) {
				chosen[key] = candidate
			}
		}
	}
	var paths []string
	for _, p := range chosen {
		paths = append(paths, p)
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
	a.replaceKnown(next)
	return out, nil
}
func betterArchive(previous, candidate string) bool {
	if strings.HasSuffix(previous, ".jsonl") {
		return false
	}
	if strings.HasSuffix(candidate, ".jsonl") {
		return true
	}
	pt, ct := archiveTime(filepath.Base(previous)), archiveTime(filepath.Base(candidate))
	if !pt.IsZero() && !ct.IsZero() {
		return ct.After(pt)
	}
	if !ct.IsZero() {
		return true
	}
	if !pt.IsZero() {
		return false
	}
	pi, pe := os.Stat(previous)
	ci, ce := os.Stat(candidate)
	return pe == nil && ce == nil && ci.ModTime().After(pi.ModTime())
}
func archiveTime(name string) time.Time {
	i := strings.Index(name, ".jsonl.")
	if i < 0 {
		return time.Time{}
	}
	_, raw, ok := strings.Cut(name[i+len(".jsonl."):], ".")
	if !ok {
		return time.Time{}
	}
	if ti := strings.IndexByte(raw, 'T'); ti >= 0 {
		tail := raw[ti+1:]
		tail = strings.Replace(tail, "-", ":", 1)
		tail = strings.Replace(tail, "-", ":", 1)
		raw = raw[:ti+1] + tail
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z"} {
		if t, e := time.Parse(layout, raw); e == nil {
			return t
		}
	}
	return time.Time{}
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
	agent := filepath.Base(filepath.Dir(filepath.Dir(path)))
	header, events, messages, malformed, start, end, ok := parse(data)
	if !ok || header.ID == "" || strings.ContainsAny(header.ID, `/\\:`) {
		return source.Session{}, authorization{}, nil, false
	}
	var output bytes.Buffer
	enc := json.NewEncoder(&output)
	for _, e := range events {
		if enc.Encode(e) != nil {
			return source.Session{}, authorization{}, nil, false
		}
	}
	identitySum := sha256.Sum256([]byte(agent))
	scope := source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("openclaw:%x", identitySum[:12]), Label: "OpenClaw sessions"}
	if filepath.IsAbs(header.CWD) && filepath.Clean(header.CWD) == header.CWD {
		scope = source.ScopeRef{Type: source.ScopeProject, Root: header.CWD, Label: filepath.Base(header.CWD)}
	}
	if start.IsZero() {
		start = info.ModTime()
	}
	if end.IsZero() {
		end = info.ModTime()
	}
	s := source.Session{ID: "openclaw:" + agent + ":" + header.ID, Product: "openclaw", FormatVersion: "jsonl-v1", AdapterVersion: "1", Capabilities: a.Capabilities(), Scope: scope, StartedAt: start, EndedAt: end, MessageCount: messages, MalformedCount: malformed, OpaqueRef: path}
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
			if r.Type != "session" || r.ID == "" {
				return record{}, nil, 0, malformed, time.Time{}, time.Time{}, false
			}
			header = r
			trackTime(r.Timestamp, &start, &end)
			continue
		}
		if r.Type != "message" {
			continue
		}
		ts := r.Timestamp
		trackTime(ts, &start, &end)
		switch r.Message.Role {
		case "user", "assistant":
			evs, ok := contentEvents(r.Message.Role, r.Message.Content, ts)
			if !ok {
				malformed++
				continue
			}
			out = append(out, evs...)
			messages++
		case "toolResult":
			if r.Message.ToolCallID == "" || r.Message.Content == nil {
				malformed++
				continue
			}
			out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: r.Message.ToolCallID, Result: r.Message.Content})
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
		return nil, errors.New("openclaw: invalid session reference")
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID || auth.digest != s.SnapshotID {
		return nil, errors.New("openclaw: unknown session reference")
	}
	fresh, freshAuth, output, valid := a.snapshot(s.OpaqueRef)
	if !valid || fresh.ID != s.ID || freshAuth.digest != auth.digest {
		return nil, errors.New("openclaw: source changed since discovery")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
