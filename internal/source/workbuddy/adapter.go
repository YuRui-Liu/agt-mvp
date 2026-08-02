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
	id, digest, root, metadata string
}

type Adapter struct {
	roots     []string
	configErr error
	scanMu    sync.Mutex
	mu        sync.RWMutex
	known     map[string]authorization
}

func New(roots ...string) *Adapter {
	if len(roots) == 0 {
		if env := os.Getenv("WORKBUDDY_PROJECTS_DIR"); env != "" {
			roots = []string{env}
		} else if home, err := os.UserHomeDir(); err == nil {
			roots = []string{
				filepath.Join(home, ".workbuddy-ai", "projects"),
				filepath.Join(home, ".workbuddy", "projects"),
			}
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{roots: clean, configErr: err, known: map[string]authorization{}}
}

func validatedRoots(roots []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, errors.New("workbuddy: roots must be absolute")
		}
		root = filepath.Clean(root)
		if seen[root] {
			continue
		}
		if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("workbuddy: symlink root rejected")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		seen[root] = true
		out = append(out, root)
	}
	return out, nil
}

func (*Adapter) Product() string                   { return "workbuddy" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type record struct {
	Type       string         `json:"type"`
	SessionID  string         `json:"sessionId"`
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	RawContent any            `json:"rawContent"`
	CWD        string         `json:"cwd"`
	Timestamp  string         `json:"timestamp"`
	Name       string         `json:"name"`
	CallID     string         `json:"callId"`
	Arguments  any            `json:"arguments"`
	Output     any            `json:"output"`
	Message    map[string]any `json:"message"`
	Provider   map[string]any `json:"providerData"`
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

type parsed struct {
	sessionID, cwd string
	cwdTrusted     bool
	events         []event
	messages       int
	malformed      int
	usage          map[string]int64
	start, end     time.Time
	reasoning      bool
	tools          bool
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

	type candidate struct{ root, path string }
	var paths []candidate
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		projects, err := os.ReadDir(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, project := range projects {
			if !validProjectDir(project) {
				continue
			}
			dir := filepath.Join(root, project.Name())
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if regularJSONL(entry) {
					paths = append(paths, candidate{root, filepath.Join(dir, entry.Name())})
					continue
				}
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				subdir := filepath.Join(dir, entry.Name(), "subagents")
				info, err := os.Lstat(subdir)
				if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					continue
				}
				subs, err := os.ReadDir(subdir)
				if err != nil {
					continue
				}
				for _, sub := range subs {
					if regularJSONL(sub) {
						paths = append(paths, candidate{root, filepath.Join(subdir, sub.Name())})
					}
				}
			}
		}
	}
	// Stable traversal within the caller's root priority.
	sort.SliceStable(paths, func(i, j int) bool {
		li, lj := rootIndex(a.roots, paths[i].root), rootIndex(a.roots, paths[j].root)
		if li != lj {
			return li < lj
		}
		return paths[i].path < paths[j].path
	})

	var out []source.Session
	next := map[string]authorization{}
	byID := map[string]bool{}
	byPath := map[string]string{}
	for _, candidate := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s, auth, ok := inspect(candidate.root, candidate.path)
		if !ok {
			continue
		}
		byPath[candidate.path] = s.ID
		if byID[s.ID] {
			continue
		}
		byID[s.ID] = true
		out = append(out, s)
		next[candidate.path] = auth
	}
	for i := range out {
		parentPath, ok := parentSessionPath(out[i].OpaqueRef)
		if !ok {
			continue
		}
		if parentID := byPath[parentPath]; parentID != "" {
			out[i].ParentID = parentID
			auth := next[out[i].OpaqueRef]
			auth.metadata = sessionMetadata(out[i])
			next[out[i].OpaqueRef] = auth
		}
	}
	a.replaceKnown(next)
	return out, nil
}

func rootIndex(roots []string, root string) int {
	for i := range roots {
		if roots[i] == root {
			return i
		}
	}
	return len(roots)
}

func validProjectDir(entry os.DirEntry) bool {
	if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		return false
	}
	switch entry.Name() {
	case "connectors", "plugins", "mcp", "cache", "sessions", "index", "workbuddy.db":
		return false
	}
	return entry.Name() != "" && entry.Name() != "." && entry.Name() != ".."
}

func regularJSONL(entry os.DirEntry) bool {
	return entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl")
}

func parentSessionPath(path string) (string, bool) {
	dir := filepath.Dir(path)
	if filepath.Base(dir) != "subagents" {
		return "", false
	}
	container := filepath.Dir(dir)
	return filepath.Join(filepath.Dir(container), filepath.Base(container)+".jsonl"), true
}

func (a *Adapter) replaceKnown(next map[string]authorization) {
	if next == nil {
		next = map[string]authorization{}
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
}

func inspect(root, path string) (source.Session, authorization, bool) {
	data, info, ok := readSession(root, path)
	if !ok {
		return source.Session{}, authorization{}, false
	}
	p, ok := parse(data)
	if !ok {
		return source.Session{}, authorization{}, false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return source.Session{}, authorization{}, false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 && !(len(parts) == 4 && parts[2] == "subagents") {
		return source.Session{}, authorization{}, false
	}
	project := parts[0]
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if stem == "" || strings.ContainsAny(stem, `/\:`) {
		return source.Session{}, authorization{}, false
	}
	id := "workbuddy:" + p.sessionID
	if p.sessionID == "" {
		id = "workbuddy:" + project + ":" + stem
	}
	if len(parts) == 4 && p.sessionID == "" {
		id = "workbuddy:" + project + ":" + parts[1] + ":subagent:" + stem
	}
	identity := project
	identitySum := sha256.Sum256([]byte(identity))
	scope := source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("workbuddy:%x", identitySum[:12]), Label: "WorkBuddy sessions"}
	if p.cwdTrusted {
		scope = source.ScopeRef{Type: source.ScopeProject, Root: p.cwd, Label: filepath.Base(p.cwd)}
	}
	if p.start.IsZero() {
		p.start = info.ModTime()
	}
	if p.end.IsZero() {
		p.end = info.ModTime()
	}
	capabilities := []source.Capability{source.CapabilityMessages}
	if p.tools {
		capabilities = append(capabilities, source.CapabilityTools)
	}
	if p.reasoning {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	s := source.Session{ID: id, Product: "workbuddy", FormatVersion: "jsonl-v1", AdapterVersion: "2", Capabilities: capabilities, Scope: scope, StartedAt: p.start, EndedAt: p.end, MessageCount: p.messages, MalformedCount: p.malformed, Usage: p.usage, OpaqueRef: path}
	sum := sha256.Sum256(data)
	s.SnapshotID = fmt.Sprintf("%x", sum[:])
	return s, authorization{id: s.ID, digest: s.SnapshotID, root: root, metadata: sessionMetadata(s)}, true
}

func sessionMetadata(s source.Session) string {
	data, _ := json.Marshal(s)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
}

func readSession(root, path string) ([]byte, os.FileInfo, bool) {
	f, err := safeopen.Open(root, path, maxSessionBytes)
	if err != nil {
		return nil, nil, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	info, statErr := f.Stat()
	if err != nil || statErr != nil || len(data) > maxSessionBytes {
		return nil, nil, false
	}
	return data, info, true
}

func parse(data []byte) (parsed, bool) {
	var p parsed
	p.cwdTrusted = true
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), maxLineBytes+1)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r record
		if json.Unmarshal(line, &r) != nil {
			p.malformed++
			continue
		}
		if !workbuddyRecordType(r.Type) {
			// Metadata must not influence identity, scope, timestamps, or counts.
			continue
		}
		var events []event
		valid := false
		switch r.Type {
		case "message":
			if r.Role != "user" && r.Role != "assistant" {
				p.malformed++
				continue
			}
			var ok bool
			events, ok = messageContentEvents(r.Role, r.Content, r.Timestamp)
			if !ok {
				p.malformed++
				continue
			}
			valid = true
		case "reasoning":
			var ok bool
			events, ok = reasoningEvents(r.Content, r.RawContent, r.Timestamp)
			if !ok {
				continue
			}
			valid = true
		case "function_call":
			if r.CallID == "" || r.Name == "" {
				p.malformed++
				continue
			}
			events = []event{{Type: "tool_use", Timestamp: r.Timestamp, CallID: r.CallID, Name: r.Name, Input: r.Arguments}}
			valid = true
		case "function_call_result":
			if r.CallID == "" || r.Output == nil {
				p.malformed++
				continue
			}
			events = []event{{Type: "tool_result", Timestamp: r.Timestamp, CallID: r.CallID, Result: r.Output}}
			valid = true
		case "usage":
			if p.sessionID != "" && (r.SessionID == "" || r.SessionID == p.sessionID) {
				mergeUsage(p.usageMap(), r.Provider["usage"])
			}
			continue
		}
		if !valid {
			continue
		}
		if r.SessionID != "" {
			if p.sessionID == "" {
				p.sessionID = r.SessionID
			} else if p.sessionID != r.SessionID {
				p.malformed++
				continue
			}
		}
		if r.CWD != "" {
			clean := filepath.Clean(r.CWD)
			if !filepath.IsAbs(r.CWD) || clean != r.CWD || (p.cwd != "" && p.cwd != r.CWD) {
				p.cwdTrusted = false
			} else if p.cwd == "" {
				p.cwd = r.CWD
			}
		}
		trackTime(r.Timestamp, &p.start, &p.end)
		p.events = append(p.events, events...)
		p.messages += len(events)
		for _, e := range events {
			switch e.Type {
			case "tool_use", "tool_result":
				p.tools = true
			}
		}
		if hasThinking(events) {
			p.reasoning = true
		}
		if r.Type == "message" {
			mergeUsage(p.usageMap(), r.Provider["usage"])
		}
	}
	if sc.Err() != nil {
		return parsed{}, false
	}
	if p.cwd == "" {
		p.cwdTrusted = false
	}
	return p, len(p.events) > 0
}

func workbuddyRecordType(typ string) bool {
	switch typ {
	case "message", "reasoning", "function_call", "function_call_result", "usage":
		return true
	}
	return false
}

func (p *parsed) usageMap() map[string]int64 {
	if p.usage == nil {
		p.usage = map[string]int64{}
	}
	return p.usage
}

func messageContentEvents(role string, content any, ts string) ([]event, bool) {
	switch value := content.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, false
		}
		return []event{{Type: "message", Role: role, Content: value, Timestamp: ts}}, true
	case []any:
		var out []event
		for _, raw := range value {
			block, ok := raw.(map[string]any)
			if !ok {
				return nil, false
			}
			typ, _ := block["type"].(string)
			switch typ {
			case "text", "input_text", "output_text":
				text, _ := block["text"].(string)
				if strings.TrimSpace(text) == "" {
					return nil, false
				}
				out = append(out, event{Type: "message", Role: role, Content: text, Timestamp: ts})
			case "thinking", "reasoning":
				if role != "assistant" {
					return nil, false
				}
				text, _ := block["thinking"].(string)
				if text == "" {
					text, _ = block["text"].(string)
				}
				if strings.TrimSpace(text) == "" {
					return nil, false
				}
				out = append(out, thinkingEvent(role, text, ts))
			case "toolCall", "tool_use":
				if role != "assistant" {
					return nil, false
				}
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				if id == "" || name == "" {
					return nil, false
				}
				input := block["arguments"]
				if input == nil {
					input = block["input"]
				}
				out = append(out, event{Type: "tool_use", Timestamp: ts, CallID: id, Name: name, Input: input})
			default:
				return nil, false
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

func reasoningEvents(content, rawContent any, ts string) ([]event, bool) {
	if text, ok := rawContent.(string); ok && strings.TrimSpace(text) != "" {
		return []event{thinkingEvent("assistant", text, ts)}, true
	}
	if text, ok := content.(string); ok && strings.TrimSpace(text) != "" {
		return []event{thinkingEvent("assistant", text, ts)}, true
	}
	blocks, ok := rawContent.([]any)
	if !ok || len(blocks) == 0 {
		blocks, ok = content.([]any)
	}
	if !ok {
		return nil, false
	}
	var out []event
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			return nil, false
		}
		typ, _ := block["type"].(string)
		if typ != "reasoning" && typ != "thinking" && typ != "reasoning_text" {
			return nil, false
		}
		text, _ := block["text"].(string)
		if text == "" {
			text, _ = block["thinking"].(string)
		}
		if strings.TrimSpace(text) == "" {
			return nil, false
		}
		out = append(out, thinkingEvent("assistant", text, ts))
	}
	return out, len(out) > 0
}

func thinkingEvent(role, text, ts string) event {
	return event{Type: "message", Role: role, Content: []any{map[string]any{"type": "thinking", "thinking": text}}, Timestamp: ts}
}

func hasThinking(events []event) bool {
	for _, e := range events {
		if _, ok := e.Content.([]any); ok {
			return true
		}
	}
	return false
}

func mergeUsage(dst map[string]int64, raw any) {
	m, ok := raw.(map[string]any)
	if !ok {
		return
	}
	usageKeys := map[string][]string{
		"input_tokens":       {"inputTokens", "prompt_tokens", "input_tokens"},
		"output_tokens":      {"outputTokens", "completion_tokens", "output_tokens"},
		"cache_read_tokens":  {"cacheReadTokens", "cached_tokens", "cache_read_input_tokens"},
		"cache_write_tokens": {"cacheWriteTokens", "prompt_cache_write_tokens", "cache_creation_input_tokens"},
		"reasoning_tokens":   {"reasoningTokens", "completion_thinking_tokens", "reasoning_tokens"},
	}
	for canonical, keys := range usageKeys {
		for _, key := range keys {
			if n, ok := int64Value(m[key]); ok {
				dst[canonical] = n
				break
			}
		}
	}
	if details, ok := m["outputTokensDetails"].([]any); ok {
		for _, rawDetail := range details {
			if detail, ok := rawDetail.(map[string]any); ok {
				if n, ok := int64Value(detail["reasoning_tokens"]); ok {
					dst["reasoning_tokens"] = n
				}
			}
		}
	}
	if details, ok := m["completion_tokens_details"].(map[string]any); ok {
		if n, ok := int64Value(details["reasoning_tokens"]); ok {
			dst["reasoning_tokens"] = n
		}
	}
}

func int64Value(value any) (int64, bool) {
	switch n := value.(type) {
	case float64:
		return int64(n), n >= 0 && n == float64(int64(n))
	case json.Number:
		v, err := n.Int64()
		return v, err == nil && v >= 0
	}
	return 0, false
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
	if !ok || auth.id != s.ID || auth.digest != s.SnapshotID || auth.metadata != sessionMetadata(s) {
		return nil, errors.New("workbuddy: unknown session reference")
	}
	data, _, valid := readSession(auth.root, s.OpaqueRef)
	if !valid {
		return nil, errors.New("workbuddy: source changed since discovery")
	}
	sum := sha256.Sum256(data)
	if fmt.Sprintf("%x", sum[:]) != auth.digest {
		return nil, errors.New("workbuddy: source changed since discovery")
	}
	p, valid := parse(data)
	if !valid {
		return nil, errors.New("workbuddy: source changed since discovery")
	}
	var output bytes.Buffer
	enc := json.NewEncoder(&output)
	for _, e := range p.events {
		if enc.Encode(e) != nil {
			return nil, errors.New("workbuddy: encode failed")
		}
	}
	return io.NopCloser(bytes.NewReader(output.Bytes())), nil
}
