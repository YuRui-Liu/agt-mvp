package qwen

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
	id, digest, metadata string
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
		return &Adapter{configErr: errors.New("qwen-code: multiple roots are not supported"), known: map[string]authorization{}}
	}
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	if root == "" {
		if env := os.Getenv("QWEN_PROJECTS_DIR"); filepath.IsAbs(env) {
			root = filepath.Clean(env)
		}
	}
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".qwen", "projects")
		}
	}
	return &Adapter{root: root, known: map[string]authorization{}}
}
func (*Adapter) Product() string                   { return "qwen-code" }
func (*Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type record struct {
	Type, SessionID, CWD, Timestamp, Model string
	Message                                struct {
		Role    string `json:"role"`
		Parts   any    `json:"parts"`
		Content any    `json:"content"`
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
	for _, agent := range agents {
		if !agent.IsDir() || agent.Type()&os.ModeSymlink != 0 {
			continue
		}
		dir := filepath.Join(a.root, agent.Name(), "chats")
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.Type().IsRegular() && strings.HasSuffix(f.Name(), ".jsonl") {
				paths = append(paths, filepath.Join(dir, f.Name()))
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
	header, events, messages, malformed, start, end, ok := parse(data)
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if !ok || stem == "" || header.SessionID != stem || strings.ContainsAny(stem, `/\\:`) {
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
	scope := source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("qwen-code:%x", identitySum[:12]), Label: "Qwen Code sessions"}
	if filepath.IsAbs(header.CWD) && filepath.Clean(header.CWD) == header.CWD {
		scope = source.ScopeRef{Type: source.ScopeProject, Root: header.CWD, Label: filepath.Base(header.CWD)}
	}
	if start.IsZero() {
		start = info.ModTime()
	}
	if end.IsZero() {
		end = info.ModTime()
	}
	capabilities := a.Capabilities()
	if qwenHasReasoning(events) {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	s := source.Session{ID: "qwen-code:" + project + ":" + stem, Product: "qwen-code", FormatVersion: "chat-jsonl-v1", AdapterVersion: "2", Capabilities: capabilities, Scope: scope, StartedAt: start, EndedAt: end, MessageCount: messages, MalformedCount: malformed, OpaqueRef: path}
	contentSum := sha256.Sum256(data)
	s.SnapshotID = fmt.Sprintf("%x", contentSum[:])
	auth := authorization{id: s.ID, digest: s.SnapshotID, metadata: sessionMetadata(s)}
	return s, auth, append([]byte(nil), output.Bytes()...), true
}
func parse(data []byte) (record, []event, int, int, time.Time, time.Time, bool) {
	var header record
	var out []event
	messages, malformed := 0, 0
	var start, end time.Time
	cwd, cwdTrusted := "", true
	pending := map[string]string{}
	completed := map[string]bool{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 4096), maxLineBytes+1)
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
		if metadataType(r.Type) {
			continue
		}
		want, role := "user", "user"
		if r.Type == "assistant" {
			want, role = "model", "assistant"
		}
		if r.Message.Role != want {
			malformed++
			continue
		}
		events, textBearing, recognized, invalid := qwenMessageEvents(role, r.Message.Parts, r.Message.Content, r.Timestamp)
		if invalid {
			malformed++
			continue
		}
		if !recognized {
			continue
		}
		nextPending, nextCompleted, validPairs := validateToolPairs(events, pending, completed)
		if !validPairs {
			malformed++
			continue
		}
		if header.SessionID == "" {
			if r.Type != "user" || r.SessionID == "" {
				malformed++
				continue
			}
			header = r
		} else if r.SessionID != header.SessionID {
			malformed++
			continue
		}
		if r.CWD != "" {
			if !filepath.IsAbs(r.CWD) || filepath.Clean(r.CWD) != r.CWD || (cwd != "" && cwd != r.CWD) {
				cwdTrusted = false
			} else if cwd == "" {
				cwd = r.CWD
			}
		}
		trackTime(r.Timestamp, &start, &end)
		pending, completed = nextPending, nextCompleted
		out = append(out, events...)
		if r.Type == "assistant" || textBearing {
			messages++
		}
	}
	if sc.Err() != nil {
		return record{}, nil, 0, malformed, time.Time{}, time.Time{}, false
	}
	if !cwdTrusted || cwd == "" {
		header.CWD = ""
	} else {
		header.CWD = cwd
	}
	return header, out, messages, malformed, start, end, header.SessionID != "" && len(out) > 0
}

func validateToolPairs(events []event, pending map[string]string, completed map[string]bool) (map[string]string, map[string]bool, bool) {
	nextPending := make(map[string]string, len(pending))
	for id, name := range pending {
		nextPending[id] = name
	}
	nextCompleted := make(map[string]bool, len(completed))
	for id, done := range completed {
		nextCompleted[id] = done
	}
	for _, e := range events {
		switch e.Type {
		case "tool_use":
			if nextCompleted[e.CallID] || nextPending[e.CallID] != "" {
				return nil, nil, false
			}
			nextPending[e.CallID] = e.Name
		case "tool_result":
			name, exists := nextPending[e.CallID]
			if !exists || nextCompleted[e.CallID] || (e.Name != "" && e.Name != name) {
				return nil, nil, false
			}
			delete(nextPending, e.CallID)
			nextCompleted[e.CallID] = true
		}
	}
	return nextPending, nextCompleted, true
}

func metadataType(typ string) bool {
	switch typ {
	case "system", "systemPayload", "snapshot", "uiEvent", "telemetry":
		return true
	}
	return typ != "user" && typ != "assistant"
}

func qwenMessageEvents(role string, partsRaw, contentRaw any, ts string) ([]event, bool, bool, bool) {
	var out []event
	textBearing, recognized := false, false
	for _, rawCollection := range []any{partsRaw, contentRaw} {
		if rawCollection == nil {
			continue
		}
		blocks, ok := rawCollection.([]any)
		if !ok {
			return nil, false, true, true
		}
		if len(blocks) == 0 {
			return nil, false, true, true
		}
		for _, raw := range blocks {
			block, ok := raw.(map[string]any)
			if !ok {
				return nil, false, true, true
			}
			events, text, known, invalid := qwenBlock(role, block, ts)
			if invalid {
				return nil, false, true, true
			}
			if known {
				recognized = true
				textBearing = textBearing || text
				out = append(out, events...)
			}
		}
	}
	return out, textBearing, recognized, false
}

func qwenBlock(role string, block map[string]any, ts string) ([]event, bool, bool, bool) {
	if rawThought, exists := block["thought"]; exists {
		if role != "assistant" {
			return nil, false, true, true
		}
		var text string
		switch value := rawThought.(type) {
		case string:
			text = value
		case bool:
			text, _ = block["text"].(string)
			if !value {
				if strings.TrimSpace(text) == "" {
					return nil, false, true, true
				}
				return []event{{Type: "message", Role: role, Content: text, Timestamp: ts}}, true, true, false
			}
		default:
			return nil, false, true, true
		}
		if strings.TrimSpace(text) == "" {
			return nil, false, true, true
		}
		return []event{qwenThinking(role, text, ts)}, false, true, false
	}
	if rawThinking, exists := block["thinking"]; exists {
		text, ok := rawThinking.(string)
		if role != "assistant" || !ok || strings.TrimSpace(text) == "" {
			return nil, false, true, true
		}
		return []event{qwenThinking(role, text, ts)}, false, true, false
	}
	if typ, _ := block["type"].(string); typ == "thinking" || typ == "thought" {
		text, _ := block["thinking"].(string)
		if text == "" {
			text, _ = block["text"].(string)
		}
		if role != "assistant" || strings.TrimSpace(text) == "" {
			return nil, false, true, true
		}
		return []event{qwenThinking(role, text, ts)}, false, true, false
	}
	if typ, _ := block["type"].(string); typ == "text" {
		text, ok := block["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false, true, true
		}
		return []event{{Type: "message", Role: role, Content: text, Timestamp: ts}}, true, true, false
	}
	if rawCall, exists := block["functionCall"]; exists {
		call, ok := rawCall.(map[string]any)
		if !ok || role != "assistant" {
			return nil, false, true, true
		}
		id, _ := call["id"].(string)
		name, _ := call["name"].(string)
		if id == "" || name == "" {
			return nil, false, true, true
		}
		return []event{{Type: "tool_use", Timestamp: ts, CallID: id, Name: name, Input: call["args"]}}, false, true, false
	}
	if rawResponse, exists := block["functionResponse"]; exists {
		response, ok := rawResponse.(map[string]any)
		if !ok || role != "user" {
			return nil, false, true, true
		}
		id, _ := response["id"].(string)
		name, _ := response["name"].(string)
		result, hasResult := response["response"]
		if id == "" || !hasResult {
			return nil, false, true, true
		}
		return []event{{Type: "tool_result", Timestamp: ts, CallID: id, Name: name, Result: result}}, false, true, false
	}
	if textRaw, exists := block["text"]; exists {
		text, ok := textRaw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false, true, true
		}
		return []event{{Type: "message", Role: role, Content: text, Timestamp: ts}}, true, true, false
	}
	return nil, false, false, false
}

func qwenThinking(role, text, ts string) event {
	return event{Type: "message", Role: role, Content: []any{map[string]any{"type": "thinking", "thinking": text}}, Timestamp: ts}
}

func qwenHasReasoning(events []event) bool {
	for _, e := range events {
		if _, ok := e.Content.([]any); ok {
			return true
		}
	}
	return false
}

func sessionMetadata(s source.Session) string {
	data, _ := json.Marshal(s)
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum[:])
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
		return nil, errors.New("qwen-code: invalid session reference")
	}
	a.mu.RLock()
	auth, ok := a.known[s.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != s.ID || auth.digest != s.SnapshotID || auth.metadata != sessionMetadata(s) {
		return nil, errors.New("qwen-code: unknown session reference")
	}
	fresh, freshAuth, output, valid := a.snapshot(s.OpaqueRef)
	if !valid || fresh.ID != s.ID || freshAuth.digest != auth.digest {
		return nil, errors.New("qwen-code: source changed since discovery")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
