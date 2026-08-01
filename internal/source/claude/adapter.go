package claude

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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const maxLineBytes = 1 << 20
const maxSessionBytes = 4 << 20

type Adapter struct {
	root     string
	scanMu   sync.Mutex
	mu       sync.RWMutex
	bindings map[string]sessionBinding
}

type sessionBinding struct {
	id, parent string
}

func New(root ...string) *Adapter {
	path := ""
	if len(root) != 0 {
		path = root[0]
	}
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".claude", "projects")
		}
	}
	return &Adapter{root: path, bindings: make(map[string]sessionBinding)}
}

func (a *Adapter) Product() string { return "claude-code" }
func (a *Adapter) Capabilities() []source.Capability {
	return []source.Capability{"messages", "tools"}
}

type record struct {
	Type      string          `json:"type"`
	CWD       string          `json:"cwd"`
	SessionID string          `json:"sessionId"`
	Timestamp json.RawMessage `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(a.root)
	if os.IsNotExist(err) {
		a.mu.Lock()
		a.bindings = make(map[string]sessionBinding)
		a.mu.Unlock()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var sessions []source.Session
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !project.IsDir() {
			continue
		}
		projectPath := filepath.Join(a.root, project.Name())
		entries, err := os.ReadDir(projectPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") && !strings.HasPrefix(entry.Name(), "agent-") {
				if session, ok := inspect(ctx, filepath.Join(projectPath, entry.Name()), project.Name(), "", a.root); ok {
					sessions = append(sessions, session)
				}
			}
			if entry.IsDir() {
				parent := entry.Name()
				base := filepath.Join(projectPath, parent, "subagents")
				walkErr := filepath.WalkDir(base, func(path string, item os.DirEntry, walkErr error) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					if walkErr != nil || item.IsDir() {
						return nil
					}
					if item.Type().IsRegular() && strings.HasPrefix(item.Name(), "agent-") && strings.HasSuffix(item.Name(), ".jsonl") {
						if session, ok := inspect(ctx, path, project.Name(), parent, a.root); ok {
							sessions = append(sessions, session)
						}
					}
					return nil
				})
				if walkErr != nil && ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
		}
	}
	validParents := make(map[string]string)
	for _, session := range sessions {
		relative, err := filepath.Rel(a.root, session.OpaqueRef)
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) == 2 {
			stem := strings.TrimSuffix(parts[1], ".jsonl")
			validParents[parts[0]+"/"+stem] = strings.TrimPrefix(session.ID, "claude-code:")
		}
	}
	counts := make(map[string]int)
	for _, session := range sessions {
		counts[session.ID]++
	}
	for index := range sessions {
		if counts[sessions[index].ID] > 1 {
			relative, _ := filepath.Rel(a.root, sessions[index].OpaqueRef)
			sum := sha256.Sum256([]byte(filepath.ToSlash(relative)))
			sessions[index].ID += "-" + fmt.Sprintf("%x", sum[:6])
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].ID != sessions[j].ID {
			return sessions[i].ID < sessions[j].ID
		}
		return sessions[i].OpaqueRef < sessions[j].OpaqueRef
	})
	nextBindings := make(map[string]sessionBinding, len(sessions))
	for _, session := range sessions {
		parentID := ""
		relative, err := filepath.Rel(a.root, session.OpaqueRef)
		if err == nil {
			parts := strings.Split(filepath.ToSlash(relative), "/")
			if len(parts) >= 4 && parts[2] == "subagents" {
				parentID = validParents[parts[0]+"/"+parts[1]]
			}
		}
		nextBindings[session.OpaqueRef] = sessionBinding{id: session.ID, parent: parentID}
	}
	a.mu.Lock()
	a.bindings = nextBindings
	a.mu.Unlock()
	return sessions, nil
}

func inspect(ctx context.Context, path, project, parent, root string) (source.Session, bool) {
	file, err := safeSourceFile(path, root)
	if err != nil {
		return source.Session{}, false
	}
	defer file.Close()
	scanner, limited := newSessionScanner(file)
	var count int
	var malformed int
	var cwd string
	var sessionID string
	cwdInvalid := false
	idConflict := false
	var start, end time.Time
	for scanner.Scan() {
		if ctx.Err() != nil {
			return source.Session{}, false
		}
		var rec record
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		if json.Unmarshal(scanner.Bytes(), &rec) != nil {
			malformed++
			continue
		}
		if rec.Type != "user" && rec.Type != "assistant" {
			continue
		}
		if !validMessage(rec) {
			malformed++
			continue
		}
		count++
		if rec.SessionID != "" {
			if sessionID == "" {
				sessionID = rec.SessionID
			} else if sessionID != rec.SessionID {
				idConflict = true
			}
		}
		if rec.Type == "user" && rec.CWD == "" {
			cwdInvalid = true
		} else if rec.CWD != "" {
			if !filepath.IsAbs(rec.CWD) || filepath.Clean(rec.CWD) != rec.CWD {
				cwdInvalid = true
			} else if cwd == "" {
				cwd = rec.CWD
			} else if cwd != rec.CWD {
				cwdInvalid = true
			}
		}
		if ts := parseTime(rec.Timestamp); !ts.IsZero() {
			if start.IsZero() || ts.Before(start) {
				start = ts
			}
			if ts.After(end) {
				end = ts
			}
		}
	}
	if scanner.Err() != nil || limited.N == 0 || count == 0 {
		return source.Session{}, false
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if sessionID == "" || idConflict {
		sessionID = stem
	}
	scope := source.ScopeRef{Type: source.ScopeProject, Root: cwd, Label: project}
	if cwd == "" || cwdInvalid {
		scope = fallbackScope("claude-code", sessionID, project)
	}
	_ = parent
	return source.Session{ID: "claude-code:" + sessionID, Product: "claude-code", FormatVersion: "jsonl", AdapterVersion: "1", Capabilities: []source.Capability{"messages", "tools"}, Scope: scope, StartedAt: start, EndedAt: end, MessageCount: count, MalformedCount: malformed, OpaqueRef: path}, true
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
	return nil, fmt.Errorf("claude: unsafe source: %w", lastErr)
}

func fallbackScope(product, id, label string) source.ScopeRef {
	sum := sha256.Sum256([]byte(product + "\x00" + id))
	return source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("%x", sum[:12]), Label: label}
}

func validMessage(rec record) bool {
	if rec.Type != "user" && rec.Type != "assistant" {
		return false
	}
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(rec.Message, &message) != nil || (message.Role != "user" && message.Role != "assistant") {
		return false
	}
	return validContent(message.Content)
}

func validContent(raw json.RawMessage) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch content := value.(type) {
	case string:
		return strings.TrimSpace(content) != ""
	case []any:
		for _, item := range content {
			if block, ok := item.(map[string]any); ok && validBlock(block) {
				return true
			}
		}
	case map[string]any:
		return validBlock(content)
	}
	return false
}

func validBlock(block map[string]any) bool {
	kind, _ := block["type"].(string)
	switch kind {
	case "text", "thinking":
		text, _ := block["text"].(string)
		return strings.TrimSpace(text) != ""
	case "tool_use":
		id, _ := block["id"].(string)
		name, _ := block["name"].(string)
		return id != "" && name != ""
	case "tool_result":
		id, _ := block["tool_use_id"].(string)
		return id != ""
	}
	return false
}

func cleanClaudeContent(raw json.RawMessage) any {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	if text, ok := value.(string); ok {
		return text
	}
	var blocks []any
	switch content := value.(type) {
	case []any:
		blocks = content
	case map[string]any:
		blocks = []any{content}
	default:
		return nil
	}
	cleaned := make([]any, 0, len(blocks))
	for _, item := range blocks {
		block, ok := item.(map[string]any)
		if !ok || !validBlock(block) {
			continue
		}
		out := map[string]any{"type": block["type"]}
		switch block["type"] {
		case "text", "thinking":
			out["text"] = block["text"]
		case "tool_use":
			out["id"], out["name"], out["input"] = block["id"], block["name"], block["input"]
		case "tool_result":
			out["tool_use_id"], out["content"] = block["tool_use_id"], block["content"]
		}
		cleaned = append(cleaned, out)
	}
	return cleaned
}

func encodeEvent(rec record) []byte {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(rec.Message, &message)
	event := map[string]any{"type": rec.Type, "role": message.Role, "content": cleanClaudeContent(message.Content)}
	if len(rec.Timestamp) != 0 {
		var timestamp any
		if json.Unmarshal(rec.Timestamp, &timestamp) == nil {
			event["timestamp"] = timestamp
		}
	}
	encoded, _ := json.Marshal(event)
	return encoded
}

func parseTime(raw json.RawMessage) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if ts, err := time.Parse(time.RFC3339Nano, text); err == nil {
			return ts
		}
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return time.UnixMilli(n)
		}
	}
	var number int64
	if json.Unmarshal(raw, &number) == nil {
		return time.UnixMilli(number)
	}
	return time.Time{}
}

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() || session.OpaqueRef == "" {
		return nil, errors.New("claude: invalid session reference")
	}
	a.mu.RLock()
	binding, known := a.bindings[session.OpaqueRef]
	a.mu.RUnlock()
	if !known || binding.id != session.ID {
		return nil, errors.New("claude: unknown session reference")
	}
	file, err := safeSourceFile(session.OpaqueRef, a.root)
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
		var rec record
		if json.Unmarshal(scanner.Bytes(), &rec) == nil && validMessage(rec) {
			event := encodeEvent(rec)
			if binding.parent != "" {
				var decoded map[string]any
				_ = json.Unmarshal(event, &decoded)
				decoded["parent_id"] = binding.parent
				event, _ = json.Marshal(decoded)
			}
			out.Write(event)
			out.WriteByte('\n')
		}
	}
	err = scanner.Err()
	file.Close()
	if err != nil || limited.N == 0 {
		if err == nil {
			err = errors.New("claude: session exceeds size limit")
		}
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(out.Bytes())), nil
}
