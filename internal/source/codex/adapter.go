package codex

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

type Adapter struct {
	live, archived string
	scanMu         sync.Mutex
	mu             sync.RWMutex
	known          map[string]string
}

func New(roots ...string) *Adapter {
	var live, archived string
	if len(roots) > 0 {
		live = roots[0]
	}
	if len(roots) > 1 {
		archived = roots[1]
	}
	if live == "" || archived == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if live == "" {
				live = filepath.Join(home, ".codex", "sessions")
			}
			if archived == "" {
				archived = filepath.Join(home, ".codex", "archived_sessions")
			}
		}
	}
	return &Adapter{live: live, archived: archived, known: make(map[string]string)}
}
func (a *Adapter) Product() string                   { return "codex" }
func (a *Adapter) Capabilities() []source.Capability { return []source.Capability{"messages", "tools"} }

type line struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		ID        string          `json:"id"`
		CWD       string          `json:"cwd"`
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Content   json.RawMessage `json:"content"`
		Arguments json.RawMessage `json:"arguments"`
		Output    json.RawMessage `json:"output"`
		Input     json.RawMessage `json:"input"`
	} `json:"payload"`
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	paths := make([]string, 0)
	walkErr := filepath.WalkDir(a.live, func(path string, d os.DirEntry, err error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(a.live, path)
		parts := strings.Split(rel, string(filepath.Separator))
		if relErr == nil && len(parts) == 4 && validDateParts(parts[:3]) && validRollout(parts[3]) && d.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if walkErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	archived, err := os.ReadDir(a.archived)
	if err == nil {
		for _, d := range archived {
			if d.Type().IsRegular() && validRollout(d.Name()) {
				paths = append(paths, filepath.Join(a.archived, d.Name()))
			}
		}
	}
	sort.Strings(paths)
	byID := map[string]source.Session{}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, ok := inspect(ctx, path, a.live, a.archived)
		if !ok {
			continue
		}
		old, exists := byID[session.ID]
		isLive := strings.HasPrefix(path, a.live+string(filepath.Separator))
		oldLive := exists && strings.HasPrefix(old.OpaqueRef, a.live+string(filepath.Separator))
		if !exists || (isLive && !oldLive) {
			byID[session.ID] = session
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]source.Session, 0, len(byID))
	for _, session := range byID {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	nextKnown := make(map[string]string, len(out))
	for _, session := range out {
		nextKnown[session.OpaqueRef] = session.ID
	}
	a.mu.Lock()
	a.known = nextKnown
	a.mu.Unlock()
	return out, nil
}
func validDateParts(parts []string) bool {
	if len(parts) != 3 || len(parts[0]) != 4 || len(parts[1]) != 2 || len(parts[2]) != 2 {
		return false
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
func validRollout(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

func inspect(ctx context.Context, path string, roots ...string) (source.Session, bool) {
	file, err := safeSourceFile(path, roots...)
	if err != nil {
		return source.Session{}, false
	}
	defer file.Close()
	scanner, limited := newSessionScanner(file)
	id, cwd, count := "", "", 0
	malformed := 0
	idConflict, cwdInvalid := false, false
	var start, end time.Time
	for scanner.Scan() {
		if ctx.Err() != nil {
			return source.Session{}, false
		}
		var rec line
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			malformed++
			continue
		}
		if rec.Type == "session_meta" {
			if rec.Payload.ID == "" {
				idConflict = true
			} else {
				if id == "" {
					id = rec.Payload.ID
				} else if id != rec.Payload.ID {
					idConflict = true
				}
			}
			if rec.Payload.CWD == "" {
				cwdInvalid = true
			} else {
				if !filepath.IsAbs(rec.Payload.CWD) || filepath.Clean(rec.Payload.CWD) != rec.Payload.CWD {
					cwdInvalid = true
				} else if cwd == "" {
					cwd = rec.Payload.CWD
				} else if cwd != rec.Payload.CWD {
					cwdInvalid = true
				}
			}
		}
		if rec.Type == "response_item" {
			if codexMessage(rec) {
				count++
			} else if rec.Payload.Type == "" || rec.Payload.Type == "message" {
				malformed++
			}
		}
		if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			if start.IsZero() || ts.Before(start) {
				start = ts
			}
			if ts.After(end) {
				end = ts
			}
		}
	}
	if scanner.Err() != nil || limited.N == 0 {
		return source.Session{}, false
	}
	if id == "" || idConflict {
		id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	if count == 0 {
		return source.Session{}, false
	}
	scope := source.ScopeRef{Type: source.ScopeProject, Root: cwd, Label: filepath.Base(cwd)}
	if cwd == "" || cwdInvalid {
		sum := sha256.Sum256([]byte("codex\x00" + id))
		scope = source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("%x", sum[:12]), Label: "Codex sessions"}
	}
	return source.Session{ID: "codex:" + id, Product: "codex", FormatVersion: "jsonl", AdapterVersion: "1", Capabilities: []source.Capability{"messages", "tools"}, Scope: scope, StartedAt: start, EndedAt: end, MessageCount: count, MalformedCount: malformed, OpaqueRef: path}, true
}

func newSessionScanner(reader io.Reader) (*bufio.Scanner, *io.LimitedReader) {
	limited := &io.LimitedReader{R: reader, N: maxSessionBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes+1)
	return scanner, limited
}

func safeSourceFile(path string, roots ...string) (*os.File, error) {
	var lastErr error
	for _, root := range roots {
		file, err := safeopen.Open(root, path, maxSessionBytes)
		if err == nil {
			return file, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("codex: unsafe source: %w", lastErr)
}

func codexMessage(rec line) bool {
	if rec.Payload.Type != "" && rec.Payload.Type != "message" {
		return false
	}
	if rec.Payload.Role != "user" && rec.Payload.Role != "assistant" {
		return false
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(rec.Payload.Content, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if (block.Type == "input_text" || block.Type == "output_text" || block.Type == "text") && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

func retainResponseItem(rec line) bool {
	switch rec.Payload.Type {
	case "", "message":
		return codexMessage(rec)
	case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
		return true
	default:
		return false
	}
}

func rawValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return nil
}

func cleanMessageBlocks(raw json.RawMessage) []any {
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	cleaned := make([]any, 0, len(blocks))
	for _, block := range blocks {
		kind, _ := block["type"].(string)
		text, _ := block["text"].(string)
		if (kind == "input_text" || kind == "output_text" || kind == "text") && strings.TrimSpace(text) != "" {
			cleaned = append(cleaned, map[string]any{"type": kind, "text": text})
		}
	}
	return cleaned
}

func encodeEvent(rec line) []byte {
	kind := rec.Payload.Type
	if kind == "" {
		kind = "message"
	}
	event := map[string]any{"type": kind}
	if rec.Timestamp != "" {
		event["timestamp"] = rec.Timestamp
	}
	if kind == "message" {
		event["role"] = rec.Payload.Role
		event["content"] = cleanMessageBlocks(rec.Payload.Content)
	} else {
		if rec.Payload.CallID != "" {
			event["call_id"] = rec.Payload.CallID
		}
		if rec.Payload.Name != "" {
			event["name"] = rec.Payload.Name
		}
		if value := rawValue(rec.Payload.Arguments); value != nil {
			event["input"] = value
		} else if value := rawValue(rec.Payload.Input); value != nil {
			event["input"] = value
		}
		if value := rawValue(rec.Payload.Output); value != nil {
			event["result"] = value
		}
	}
	encoded, _ := json.Marshal(event)
	return encoded
}

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() || session.OpaqueRef == "" {
		return nil, errors.New("codex: invalid session reference")
	}
	a.mu.RLock()
	id, known := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !known || id != session.ID {
		return nil, errors.New("codex: unknown session reference")
	}
	file, err := safeSourceFile(session.OpaqueRef, a.live, a.archived)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	scanner, limited := newSessionScanner(file)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			file.Close()
			return nil, err
		}
		var rec line
		if json.Unmarshal(scanner.Bytes(), &rec) == nil && rec.Type == "response_item" && retainResponseItem(rec) {
			out.Write(encodeEvent(rec))
			out.WriteByte('\n')
		}
	}
	err = scanner.Err()
	file.Close()
	if err != nil || limited.N == 0 {
		if err == nil {
			err = errors.New("codex: session exceeds size limit")
		}
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(out.Bytes())), nil
}
