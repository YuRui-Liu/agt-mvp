package cursor

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

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const maxLineBytes = 1 << 20
const maxSessionBytes = 4 << 20

type Adapter struct {
	root   string
	scanMu sync.Mutex
	mu     sync.RWMutex
	known  map[string]string
}

func New(root ...string) *Adapter {
	path := ""
	if len(root) > 0 {
		path = root[0]
	}
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".cursor", "projects")
		}
	}
	return &Adapter{root: path, known: make(map[string]string)}
}
func (a *Adapter) Product() string { return "cursor" }
func (a *Adapter) Capabilities() []source.Capability {
	return []source.Capability{"messages", "tools"}
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
		a.known = make(map[string]string)
		a.mu.Unlock()
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	chosen := map[string]source.Session{}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		base := filepath.Join(a.root, project.Name(), "agent-transcripts")
		baseInfo, statErr := os.Lstat(base)
		if statErr != nil || !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			var candidates []string
			if entry.Type().IsRegular() && validExt(entry.Name()) {
				candidates = append(candidates, filepath.Join(base, entry.Name()))
			}
			if entry.IsDir() {
				for _, ext := range []string{".jsonl", ".txt"} {
					candidates = append(candidates, filepath.Join(base, entry.Name(), entry.Name()+ext))
				}
			}
			for _, path := range candidates {
				session, ok := inspect(ctx, path, project.Name(), a.root)
				if !ok {
					continue
				}
				old, exists := chosen[session.ID]
				if !exists || strings.HasSuffix(path, ".jsonl") && strings.HasSuffix(old.OpaqueRef, ".txt") {
					chosen[session.ID] = session
				}
			}
		}
	}
	out := make([]source.Session, 0, len(chosen))
	for _, s := range chosen {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nextKnown := make(map[string]string, len(out))
	for _, session := range out {
		nextKnown[session.OpaqueRef] = session.ID
	}
	a.mu.Lock()
	a.known = nextKnown
	a.mu.Unlock()
	return out, nil
}
func validExt(name string) bool {
	return strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".txt")
}
func inspect(ctx context.Context, path, encoded, root string) (source.Session, bool) {
	file, err := safeSourceFile(path, root)
	if err != nil {
		return source.Session{}, false
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return source.Session{}, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionBytes+1))
	file.Close()
	if err != nil {
		return source.Session{}, false
	}
	if ctx.Err() != nil || len(data) > maxSessionBytes || !linesWithinLimit(data) {
		return source.Session{}, false
	}
	jsonl := hasJSONLSignature(data)
	if !jsonl && !hasPlaintextSignature(data) {
		return source.Session{}, false
	}
	events := parse(data, jsonl)
	if len(events) == 0 {
		return source.Session{}, false
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	sum := sha256.Sum256([]byte(encoded))
	scope := source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("cursor:%x", sum[:12]), Label: safeProjectLabel(encoded)}
	if cwd, ok := trustedCWD(data, jsonl); ok {
		scope = source.ScopeRef{Type: source.ScopeProject, Root: cwd, Label: filepath.Base(cwd)}
	}
	malformed := 0
	if jsonl {
		malformed = countMalformedJSONL(data)
	}
	return source.Session{ID: "cursor:" + stem, Product: "cursor", FormatVersion: strings.TrimPrefix(filepath.Ext(path), "."), AdapterVersion: "1", Capabilities: []source.Capability{"messages", "tools"}, Scope: scope, StartedAt: info.ModTime(), EndedAt: info.ModTime(), MessageCount: len(events), MalformedCount: malformed, OpaqueRef: path}, true
}

func countMalformedJSONL(data []byte) int {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
	count := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var value any
		if json.Unmarshal(line, &value) != nil {
			count++
			continue
		}
		raw, object := value.(map[string]any)
		if !object {
			continue
		}
		_, hasRole := raw["role"]
		_, hasMessage := raw["message"]
		if !hasRole && !hasMessage {
			continue
		}
		var event cursorEvent
		if json.Unmarshal(line, &event) != nil || !validCursorEvent(event) {
			count++
		}
	}
	return count
}

func hasPlaintextSignature(data []byte) bool {
	prefix := data
	if len(prefix) > 4096 {
		prefix = prefix[:4096]
	}
	for _, line := range strings.Split(string(prefix), "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if line == "" {
			continue
		}
		return strings.HasPrefix(line, "user:") || strings.HasPrefix(line, "assistant:")
	}
	return false
}

func linesWithinLimit(data []byte) bool {
	length := 0
	for _, char := range data {
		if char == '\n' {
			length = 0
			continue
		}
		length++
		if length > maxLineBytes {
			return false
		}
	}
	return true
}

func hasJSONLSignature(data []byte) bool {
	prefix := data
	if len(prefix) > 4096 {
		prefix = prefix[:4096]
	}
	scanner := bufio.NewScanner(bytes.NewReader(prefix))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var event cursorEvent
		if json.Unmarshal(line, &event) != nil {
			return false
		}
		return validCursorEvent(event)
	}
	return false
}
func safeProjectLabel(encoded string) string {
	encoded = strings.Trim(encoded, "- ")
	if encoded == "" {
		return "Cursor sessions"
	}
	parts := strings.Split(encoded, "-")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return strings.Join(parts, "-")
}
func parse(data []byte, jsonl bool) [][]byte {
	if jsonl {
		var events [][]byte
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
		for scanner.Scan() {
			var event cursorEvent
			if json.Unmarshal(scanner.Bytes(), &event) != nil || !validCursorEvent(event) {
				continue
			}
			encoded, _ := json.Marshal(map[string]any{
				"type": "message", "role": event.Role,
				"content": cleanCursorContent(event.Role, event.Message.Content),
			})
			events = append(events, encoded)
		}
		if scanner.Err() != nil {
			return nil
		}
		return events
	}
	var events [][]byte
	var role string
	var text strings.Builder
	flush := func() {
		content := strings.TrimSpace(text.String())
		content = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(content, "<user_query>"), "</user_query>"))
		if role != "" && content != "" {
			b, _ := json.Marshal(map[string]any{"role": role, "message": map[string]any{"content": content}})
			events = append(events, b)
		}
		text.Reset()
	}
	for _, line := range strings.Split(string(data), "\n") {
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "user:"):
			flush()
			role = "user"
			text.WriteString(strings.TrimSpace(line[len("user:"):]))
		case strings.HasPrefix(lower, "assistant:"):
			flush()
			role = "assistant"
			text.WriteString(strings.TrimSpace(line[len("assistant:"):]))
		default:
			if role != "" {
				text.WriteByte('\n')
				text.WriteString(line)
			}
		}
	}
	flush()
	return events
}

type cursorEvent struct {
	Role    string `json:"role"`
	CWD     string `json:"cwd"`
	Message struct {
		Content any `json:"content"`
	} `json:"message"`
}

func trustedCWD(data []byte, jsonl bool) (string, bool) {
	if !jsonl {
		return "", false
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
	cwd := ""
	count := 0
	for scanner.Scan() {
		var event cursorEvent
		if json.Unmarshal(scanner.Bytes(), &event) != nil || !validCursorEvent(event) {
			continue
		}
		count++
		if event.CWD == "" || !filepath.IsAbs(event.CWD) || filepath.Clean(event.CWD) != event.CWD {
			return "", false
		}
		if cwd == "" {
			cwd = event.CWD
		} else if cwd != event.CWD {
			return "", false
		}
	}
	return cwd, scanner.Err() == nil && count > 0 && cwd != ""
}

func validCursorEvent(event cursorEvent) bool {
	if event.Role != "user" && event.Role != "assistant" {
		return false
	}
	return cleanCursorContent(event.Role, event.Message.Content) != nil
}

func cleanCursorContent(role string, content any) any {
	switch value := content.(type) {
	case string:
		value = strings.TrimSpace(value)
		if role == "user" {
			value = stripUserQuery(value)
		}
		if value != "" {
			return value
		}
	case []any:
		cleaned := make([]any, 0, len(value))
		for _, item := range value {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			kind, _ := block["type"].(string)
			out := map[string]any{"type": kind}
			switch kind {
			case "text":
				text, _ := block["text"].(string)
				if role == "user" {
					text = stripUserQuery(text)
				}
				if strings.TrimSpace(text) == "" {
					continue
				}
				out["text"] = text
			case "tool_use":
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				if id == "" || name == "" {
					continue
				}
				out["id"], out["name"], out["input"] = id, name, block["input"]
			case "tool_result":
				id, _ := block["tool_use_id"].(string)
				if id == "" {
					continue
				}
				out["tool_use_id"], out["content"] = id, block["content"]
			default:
				continue
			}
			cleaned = append(cleaned, out)
		}
		if len(cleaned) != 0 {
			return cleaned
		}
	}
	return nil
}

func stripUserQuery(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<user_query>") && strings.HasSuffix(text, "</user_query>") {
		text = strings.TrimPrefix(text, "<user_query>")
		text = strings.TrimSuffix(text, "</user_query>")
	}
	return strings.TrimSpace(text)
}
func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() || session.OpaqueRef == "" {
		return nil, errors.New("cursor: invalid session reference")
	}
	a.mu.RLock()
	id, known := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !known || id != session.ID {
		return nil, errors.New("cursor: unknown session reference")
	}
	file, err := safeSourceFile(session.OpaqueRef, a.root)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionBytes+1))
	file.Close()
	if err != nil {
		return nil, err
	}
	if len(data) > maxSessionBytes {
		return nil, errors.New("cursor: transcript too large")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !linesWithinLimit(data) {
		return nil, errors.New("cursor: transcript line too large")
	}
	events := parse(data, hasJSONLSignature(data))
	var out bytes.Buffer
	for _, event := range events {
		out.Write(event)
		out.WriteByte('\n')
	}
	return io.NopCloser(bytes.NewReader(out.Bytes())), nil
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
	return nil, fmt.Errorf("cursor: unsafe source: %w", lastErr)
}
