package kimi

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
		return &Adapter{configErr: errors.New("kimi-cli: multiple roots are not supported"), known: map[string]authorization{}}
	}
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	if root == "" {
		if env := os.Getenv("KIMI_DIR"); filepath.IsAbs(env) {
			root = filepath.Clean(env)
		}
	}
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".kimi", "sessions")
		}
	}
	return &Adapter{root: root, known: map[string]authorization{}}
}
func (*Adapter) Product() string                   { return "kimi-cli" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type record struct {
	Type      string  `json:"type"`
	Timestamp float64 `json:"timestamp"`
	Message   struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
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
	var paths []string
	for _, project := range agents {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		projectDir := filepath.Join(a.root, project.Name())
		files, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, session := range files {
			if !session.IsDir() || session.Type()&os.ModeSymlink != 0 {
				continue
			}
			p := filepath.Join(projectDir, session.Name(), "wire.jsonl")
			if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
				paths = append(paths, p)
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
	project := filepath.Base(filepath.Dir(filepath.Dir(path)))
	sessionID := filepath.Base(filepath.Dir(path))
	_, events, messages, malformed, start, end, ok := parse(data)
	if !ok || strings.ContainsAny(project+sessionID, `/\\:`) {
		return source.Session{}, authorization{}, nil, false
	}
	var output bytes.Buffer
	enc := json.NewEncoder(&output)
	for _, e := range events {
		if enc.Encode(e) != nil {
			return source.Session{}, authorization{}, nil, false
		}
	}
	identitySum := sha256.Sum256([]byte(project))
	scope := source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("kimi-cli:%x", identitySum[:12]), Label: "Kimi CLI sessions"}
	if start.IsZero() {
		start = info.ModTime()
	}
	if end.IsZero() {
		end = info.ModTime()
	}
	s := source.Session{ID: "kimi-cli:" + project + ":" + sessionID, Product: "kimi-cli", FormatVersion: "wire-v1", AdapterVersion: "1", Capabilities: a.Capabilities(), Scope: scope, StartedAt: start, EndedAt: end, MessageCount: messages, MalformedCount: malformed, OpaqueRef: path}
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
	assistantOpen := false
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
			if r.Type != "metadata" {
				return record{}, nil, 0, malformed, time.Time{}, time.Time{}, false
			}
			header = r
			continue
		}
		t := time.Unix(int64(r.Timestamp), int64((r.Timestamp-float64(int64(r.Timestamp)))*1e9)).UTC()
		if r.Timestamp > 0 {
			if start.IsZero() || t.Before(start) {
				start = t
			}
			if end.IsZero() || t.After(end) {
				end = t
			}
		}
		ts := ""
		if r.Timestamp > 0 {
			ts = t.Format(time.RFC3339Nano)
		}
		p := r.Message.Payload
		switch r.Message.Type {
		case "TurnBegin":
			parts, _ := p["user_input"].([]any)
			var texts []string
			for _, x := range parts {
				m, _ := x.(map[string]any)
				if m["type"] == "text" {
					if s, _ := m["text"].(string); strings.TrimSpace(s) != "" {
						texts = append(texts, s)
					}
				}
			}
			if len(texts) == 0 {
				malformed++
				continue
			}
			out = append(out, event{Type: "message", Role: "user", Content: strings.Join(texts, "\n"), Timestamp: ts})
			messages++
			assistantOpen = false
		case "ContentPart":
			typ, _ := p["type"].(string)
			text, _ := p["text"].(string)
			if typ != "text" || strings.TrimSpace(text) == "" {
				malformed++
				continue
			}
			out = append(out, event{Type: "message", Role: "assistant", Content: text, Timestamp: ts})
			assistantOpen = true
		case "ToolCall":
			id, _ := p["id"].(string)
			name, _ := p["name"].(string)
			if id == "" || name == "" {
				malformed++
				continue
			}
			out = append(out, event{Type: "tool_use", Timestamp: ts, CallID: id, Name: name, Input: p["arguments"]})
			assistantOpen = true
		case "ToolResult":
			id, _ := p["tool_call_id"].(string)
			if id == "" || p["content"] == nil {
				malformed++
				continue
			}
			out = append(out, event{Type: "tool_result", Timestamp: ts, CallID: id, Result: p["content"]})
		case "TurnEnd":
			if assistantOpen {
				messages++
				assistantOpen = false
			}
		case "StepBegin", "StepEnd", "StatusUpdate":
		default:
			malformed++
		}
	}
	if assistantOpen {
		messages++
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
		return nil, errors.New("kimi-cli: invalid session reference")
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID || auth.digest != s.SnapshotID {
		return nil, errors.New("kimi-cli: unknown session reference")
	}
	fresh, freshAuth, output, valid := a.snapshot(s.OpaqueRef)
	if !valid || fresh.ID != s.ID || freshAuth.digest != auth.digest {
		return nil, errors.New("kimi-cli: source changed since discovery")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
