package myflicker

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
	"reflect"
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
	product string
	root    string
	scanMu  sync.Mutex
	mu      sync.RWMutex
	known   map[string]authorization
}

type authorization struct {
	session source.Session
	digest  [sha256.Size]byte
}

func New(roots ...string) *Adapter {
	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}
	if root == "" {
		if home, err := os.UserHomeDir(); err == nil {
			root = filepath.Join(home, ".myflicker", "projects")
		}
	}
	return newProduct("myflicker", root)
}

// NewProduct is restricted to the two independently named Flicker products.
// It exists so codeflicker can share the byte-level JSONL parser without
// conflating product IDs or authorization state.
func NewProduct(product, root string) *Adapter {
	if product != "myflicker" && product != "codeflicker" {
		panic("myflicker: unsupported product")
	}
	return newProduct(product, root)
}

func newProduct(product, root string) *Adapter {
	return &Adapter{product: product, root: root, known: make(map[string]authorization)}
}

func (a *Adapter) Product() string { return a.product }
func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{"messages", "tools"}
}

type record struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Timestamp string `json:"timestamp"`
	Model     string `json:"model"`
	CWD       string `json:"cwd"`
	Config    struct {
		CWD string `json:"cwd"`
	} `json:"config"`
	Content json.RawMessage `json:"content"`
}

type event struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	Model     string `json:"model,omitempty"`
	Content   any    `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	Result    any    `json:"result,omitempty"`
}

type parsed struct {
	session source.Session
	output  []byte
	digest  [sha256.Size]byte
}

func (a *Adapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	projects, err := os.ReadDir(a.root)
	if os.IsNotExist(err) {
		a.replaceKnown(nil)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		projectRoot := filepath.Join(a.root, project.Name())
		files, err := os.ReadDir(projectRoot)
		if err != nil {
			continue
		}
		for _, file := range files {
			if file.Type().IsRegular() && validFilename(file.Name()) {
				paths = append(paths, filepath.Join(projectRoot, file.Name()))
			}
		}
	}
	sort.Strings(paths)
	out := make([]source.Session, 0, len(paths))
	next := make(map[string]authorization, len(paths))
	byID := make(map[string]string, len(paths))
	conflicts := make(map[string]bool)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p, err := a.parse(ctx, path)
		if err != nil {
			continue
		}
		if previousPath, exists := byID[p.session.ID]; exists {
			conflicts[p.session.ID] = true
			delete(next, previousPath)
			continue
		}
		byID[p.session.ID] = path
		next[path] = authorization{session: p.session, digest: p.digest}
		out = append(out, p.session)
	}
	if len(conflicts) > 0 {
		filtered := out[:0]
		for _, session := range out {
			if !conflicts[session.ID] {
				filtered = append(filtered, session)
			}
		}
		out = filtered
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
	return out, nil
}

func (a *Adapter) replaceKnown(next map[string]authorization) {
	if next == nil {
		next = make(map[string]authorization)
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
}

func validFilename(name string) bool {
	if !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	id := strings.TrimSuffix(name, ".jsonl")
	if id == "" || len(id) > 200 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (a *Adapter) parse(ctx context.Context, path string) (parsed, error) {
	file, err := safeopen.Open(a.root, path, maxSessionBytes)
	if err != nil {
		return parsed{}, err
	}
	defer file.Close()
	limited := &io.LimitedReader{R: file, N: maxSessionBytes + 1}
	scanner := bufio.NewScanner(limited)
	scanner.Buffer(make([]byte, 64<<10), maxLineBytes)
	var raw bytes.Buffer
	var output bytes.Buffer
	var cwd string
	cwdInvalid := false
	var start, end time.Time
	messageCount, malformed := 0, 0
	gotConfig := false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return parsed{}, err
		}
		line := append([]byte(nil), scanner.Bytes()...)
		raw.Write(line)
		raw.WriteByte('\n')
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec record
		if json.Unmarshal(line, &rec) != nil {
			malformed++
			continue
		}
		switch rec.Type {
		case "config":
			if gotConfig {
				return parsed{}, errors.New("myflicker: invalid config signature")
			}
			gotConfig = true
			observeCWD(rec.Config.CWD, &cwd, &cwdInvalid)
		case "snapshot":
			continue
		case "message":
			if !gotConfig {
				return parsed{}, errors.New("myflicker: message before config")
			}
			observeCWD(rec.CWD, &cwd, &cwdInvalid)
			events, ok := mapMessage(rec)
			if !ok {
				malformed++
				continue
			}
			for _, item := range events {
				encoded, _ := json.Marshal(item)
				output.Write(encoded)
				output.WriteByte('\n')
			}
			messageCount++
			if ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
				if start.IsZero() || ts.Before(start) {
					start = ts
				}
				if ts.After(end) {
					end = ts
				}
			}
		}
	}
	if err := scanner.Err(); err != nil || limited.N == 0 {
		return parsed{}, errors.New("myflicker: session exceeds limit")
	}
	if !gotConfig || messageCount == 0 {
		return parsed{}, errors.New("myflicker: incomplete signature")
	}
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	digest := sha256.Sum256(raw.Bytes())
	scope := jsonlScope(a.product, id, cwd, cwdInvalid)
	s := source.Session{
		ID: a.product + ":" + id, Product: a.product,
		FormatVersion: "jsonl", AdapterVersion: "1",
		Capabilities: []source.Capability{"messages", "tools"},
		Scope:        scope,
		StartedAt:    start, EndedAt: end, MessageCount: messageCount,
		MalformedCount: malformed, OpaqueRef: path,
	}
	return parsed{session: s, output: output.Bytes(), digest: digest}, nil
}

func validCWD(cwd string) bool {
	return filepath.IsAbs(cwd) && filepath.Clean(cwd) == cwd
}

func observeCWD(value string, cwd *string, invalid *bool) {
	if value == "" {
		return
	}
	if !validCWD(value) {
		*invalid = true
		return
	}
	if *cwd == "" {
		*cwd = value
		return
	}
	if *cwd != value {
		*invalid = true
	}
}

func jsonlScope(product, id, cwd string, invalid bool) source.ScopeRef {
	if cwd != "" && !invalid {
		return source.ScopeRef{Type: source.ScopeProject, Root: cwd, Label: filepath.Base(cwd)}
	}
	sum := sha256.Sum256([]byte(product + "\x00" + id))
	label := "MyFlicker sessions"
	if product == "codeflicker" {
		label = "CodeFlicker sessions"
	}
	return source.ScopeRef{
		Type:  source.ScopeSessionCollection,
		Root:  fmt.Sprintf("%x", sum[:12]),
		Label: label,
	}
}

func mapMessage(rec record) ([]event, bool) {
	base := event{Timestamp: rec.Timestamp, Model: rec.Model}
	switch rec.Role {
	case "user":
		var text string
		if json.Unmarshal(rec.Content, &text) != nil || strings.TrimSpace(text) == "" {
			return nil, false
		}
		base.Type, base.Role = "message", "user"
		base.Content = []any{map[string]any{"type": "text", "text": text}}
		return []event{base}, true
	case "assistant":
		var text string
		if json.Unmarshal(rec.Content, &text) == nil {
			if strings.TrimSpace(text) == "" {
				return nil, false
			}
			base.Type, base.Role = "message", "assistant"
			base.Content = []any{map[string]any{"type": "text", "text": text}}
			return []event{base}, true
		}
		var blocks []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(rec.Content, &blocks) != nil {
			return nil, false
		}
		var out []event
		for _, block := range blocks {
			switch block.Type {
			case "text", "reasoning":
				if strings.TrimSpace(block.Text) == "" {
					return nil, false
				}
				kind := "text"
				if block.Type == "reasoning" {
					kind = "thinking"
				}
				item := base
				item.Type, item.Role = "message", "assistant"
				item.Content = []any{map[string]any{"type": kind, kind: block.Text}}
				out = append(out, item)
			case "tool_use":
				if block.ID == "" || block.Name == "" {
					return nil, false
				}
				var input any = map[string]any{}
				if len(block.Input) > 0 && json.Unmarshal(block.Input, &input) != nil {
					return nil, false
				}
				item := base
				item.Type, item.CallID, item.Name, item.Input = "tool_use", block.ID, block.Name, input
				out = append(out, item)
			default:
				return nil, false
			}
		}
		return out, len(out) > 0
	case "tool":
		var blocks []struct {
			Type       string `json:"type"`
			ToolCallID string `json:"toolCallId"`
			Result     any    `json:"result"`
		}
		if json.Unmarshal(rec.Content, &blocks) != nil || len(blocks) == 0 {
			return nil, false
		}
		out := make([]event, 0, len(blocks))
		for _, block := range blocks {
			if (block.Type != "tool-result" && block.Type != "tool_result") || block.ToolCallID == "" {
				return nil, false
			}
			item := base
			item.Type, item.CallID, item.Result = "tool_result", block.ToolCallID, block.Result
			out = append(out, item)
		}
		return out, true
	default:
		return nil, false
	}
}

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.product || session.OpaqueRef == "" {
		return nil, errors.New("myflicker: invalid session reference")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || !reflect.DeepEqual(auth.session, session) {
		return nil, errors.New("myflicker: unknown session reference")
	}
	p, err := a.parse(ctx, session.OpaqueRef)
	if err != nil {
		return nil, err
	}
	if p.digest != auth.digest || !reflect.DeepEqual(p.session, auth.session) {
		return nil, fmt.Errorf("myflicker: source changed")
	}
	return io.NopCloser(bytes.NewReader(p.output)), nil
}
