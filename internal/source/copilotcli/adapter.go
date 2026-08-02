package copilotcli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const (
	maxLineBytes        = 1 << 20
	maxSessionBytes     = 4 << 20
	maxSessionRecords   = 4096
	maxSessionEvents    = 8192
	maxDirectoryEntries = 256
	maxGlobalEntries    = 4096
	maxJSONDepth        = 64
	maxUpstreamIDBytes  = 512
)

type pathIdentity struct{ directories []safeopen.Identity }

type authorization struct {
	id, digest, metadata, root, path, relative string
	rootIdentity                               safeopen.Identity
	pathIdentity                               pathIdentity
	fileInfo                                   os.FileInfo
}

type Adapter struct {
	roots     []string
	configErr error
	scanMu    sync.Mutex
	mu        sync.RWMutex
	known     map[string]authorization
	afterBind func(string)
	instance  uint64
}

var instanceCounter atomic.Uint64

func New(roots ...string) *Adapter {
	if len(roots) == 0 {
		if configured := os.Getenv("COPILOT_DIR"); configured != "" {
			roots = []string{configured}
		} else if home, err := os.UserHomeDir(); err == nil {
			roots = []string{filepath.Join(home, ".copilot")}
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{roots: clean, configErr: err, known: map[string]authorization{}, instance: instanceCounter.Add(1)}
}

func (*Adapter) Product() string { return "copilot-cli" }

func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}
}

func validatedRoots(roots []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, errors.New("copilot-cli: invalid root")
		}
		canonical, err := canonicalizeRoot(root)
		if err != nil {
			return nil, err
		}
		if !seen[canonical] {
			seen[canonical] = true
			out = append(out, canonical)
		}
	}
	return out, nil
}

func canonicalizeRoot(root string) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", errors.New("copilot-cli: invalid root")
	}
	volume := filepath.VolumeName(root)
	remainder := strings.TrimPrefix(root, volume+string(filepath.Separator))
	parts := strings.Split(remainder, string(filepath.Separator))
	current := volume + string(filepath.Separator)
	start := 0
	if len(parts) > 0 && parts[0] != "" {
		first := filepath.Join(current, parts[0])
		resolved, err := filepath.EvalSymlinks(first)
		if err == nil {
			current, start = resolved, 1
		} else if !os.IsNotExist(err) {
			return "", errors.New("copilot-cli: invalid root")
		}
	}
	for i := start; i < len(parts); i++ {
		if parts[i] == "" {
			continue
		}
		next := filepath.Join(current, parts[i])
		info, err := os.Lstat(next)
		if os.IsNotExist(err) {
			for ; i < len(parts); i++ {
				current = filepath.Join(current, parts[i])
			}
			return filepath.Clean(current), nil
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("copilot-cli: invalid root")
		}
		current = next
	}
	return filepath.Clean(current), nil
}

type candidate struct {
	root, path, relative, rawID, format string
	bound                               *safeopen.BoundRoot
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

	chosen := map[string]candidate{}
	globalVisited := 0
	boundIdentities := map[safeopen.Identity]bool{}
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validated, err := canonicalizeRoot(root)
		if err != nil || validated != root {
			return nil, errors.New("copilot-cli: root changed")
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("copilot-cli: root read failed")
		}
		if boundIdentities[bound.Identity()] {
			bound.Close()
			continue
		}
		boundIdentities[bound.Identity()] = true
		defer bound.Close()
		if a.afterBind != nil {
			a.afterBind(root)
		}
		entries, err := bound.ReadDirLimit("session-state", maxDirectoryEntries)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, directoryError(err)
		}
		globalVisited += len(entries)
		if globalVisited > maxGlobalEntries {
			return nil, errors.New("copilot-cli: directory limit")
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".jsonl") {
				rawID := strings.TrimSuffix(entry.Name(), ".jsonl")
				if !validSessionName(rawID) {
					continue
				}
				rel := filepath.Join("session-state", entry.Name())
				key := root + "\x00" + rawID
				if _, exists := chosen[key]; !exists {
					chosen[key] = candidate{root: root, path: filepath.Join(root, rel), relative: rel, rawID: rawID, format: "flat-v1", bound: bound}
				}
				continue
			}
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validSessionName(entry.Name()) {
				continue
			}
			dirRel := filepath.Join("session-state", entry.Name())
			children, err := bound.ReadDirLimit(dirRel, maxDirectoryEntries)
			if err != nil {
				if errors.Is(err, safeopen.ErrDirectoryLimit) {
					return nil, errors.New("copilot-cli: directory limit")
				}
				continue
			}
			globalVisited += len(children)
			if globalVisited > maxGlobalEntries {
				return nil, errors.New("copilot-cli: directory limit")
			}
			for _, child := range children {
				if child.Name() != "events.jsonl" || !child.Type().IsRegular() {
					continue
				}
				rel := filepath.Join(dirRel, "events.jsonl")
				key := root + "\x00" + entry.Name()
				chosen[key] = candidate{root: root, path: filepath.Join(root, rel), relative: rel, rawID: entry.Name(), format: "directory-v2", bound: bound}
			}
		}
	}
	candidates := make([]candidate, 0, len(chosen))
	for _, item := range chosen {
		candidates = append(candidates, item)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := rootIndex(a.roots, candidates[i].root), rootIndex(a.roots, candidates[j].root)
		if left != right {
			return left < right
		}
		return candidates[i].relative < candidates[j].relative
	})

	out := make([]source.Session, 0, len(candidates))
	next := map[string]authorization{}
	seen := map[string]bool{}
	for _, item := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		session, auth, _, ok := a.snapshot(ctx, item)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ok || seen[session.ID] {
			continue
		}
		seen[session.ID] = true
		out = append(out, session)
		next[session.OpaqueRef] = auth
	}
	a.replaceKnown(next)
	return out, nil
}

func directoryError(err error) error {
	if errors.Is(err, safeopen.ErrDirectoryLimit) {
		return errors.New("copilot-cli: directory limit")
	}
	return errors.New("copilot-cli: directory read failed")
}

func rootIndex(roots []string, root string) int {
	for i, value := range roots {
		if value == root {
			return i
		}
	}
	return len(roots)
}

func validSessionName(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func (a *Adapter) replaceKnown(next map[string]authorization) {
	if next == nil {
		next = map[string]authorization{}
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
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

type parsedSession struct {
	sessionID, cwd string
	cwdTrusted     bool
	events         []event
	messages       int
	malformed      int
	start, end     time.Time
}

func (a *Adapter) snapshot(ctx context.Context, item candidate) (source.Session, authorization, []byte, bool) {
	file, identities, err := item.bound.OpenWithPathIdentity(item.relative, maxSessionBytes)
	if err != nil {
		return source.Session{}, authorization{}, nil, false
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSessionBytes+1))
	info, statErr := file.Stat()
	file.Close()
	if err != nil || statErr != nil || len(data) > maxSessionBytes || ctx.Err() != nil {
		return source.Session{}, authorization{}, nil, false
	}
	parsed, ok := parse(ctx, data)
	if parsed.sessionID == "" {
		parsed.sessionID = item.rawID
	}
	if !ok || len(parsed.events) == 0 || len(parsed.sessionID) > maxUpstreamIDBytes || strings.ContainsAny(parsed.sessionID, "\x00\r\n") {
		return source.Session{}, authorization{}, nil, false
	}
	if parsed.start.IsZero() {
		parsed.start = info.ModTime()
	}
	if parsed.end.IsZero() {
		parsed.end = info.ModTime()
	}
	identitySeed := item.root + "\x00" + item.rawID + "\x00" + parsed.sessionID
	id := "copilot-cli:" + item.format + ":" + digestPrefix(identitySeed, 24)
	scope := source.ScopeRef{Type: source.ScopeSessionCollection, Root: "copilot-cli:collection:" + digestPrefix(item.root, 24), Label: "Copilot CLI sessions"}
	if parsed.cwdTrusted {
		label := safeLabel(filepath.Base(parsed.cwd))
		if label != "" {
			scope = source.ScopeRef{Type: source.ScopeProject, Root: "copilot-cli:project:" + digestPrefix(parsed.cwd, 24), Label: label}
		}
	}
	capabilities := []source.Capability{source.CapabilityMessages}
	if hasEventType(parsed.events, "tool_use", "tool_result") {
		capabilities = append(capabilities, source.CapabilityTools)
	}
	if hasReasoning(parsed.events) {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	session := source.Session{
		ID: id, Product: a.Product(), FormatVersion: item.format, AdapterVersion: "1",
		Capabilities: capabilities, Scope: scope, StartedAt: parsed.start, EndedAt: parsed.end,
		MessageCount: parsed.messages, MalformedCount: parsed.malformed, OpaqueRef: a.opaqueRef(item.path),
	}
	sum := sha256.Sum256(data)
	session.SnapshotID = hex.EncodeToString(sum[:])
	auth := authorization{
		id: session.ID, digest: session.SnapshotID, metadata: sessionMetadata(session), root: item.root, path: item.path,
		relative: item.relative, rootIdentity: item.bound.Identity(), pathIdentity: pathIdentity{directories: identities}, fileInfo: info,
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, value := range parsed.events {
		if encoder.Encode(value) != nil {
			return source.Session{}, authorization{}, nil, false
		}
	}
	return session, auth, output.Bytes(), true
}

func (a *Adapter) opaqueRef(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "copilot-cli:ref:" + strconv.FormatUint(a.instance, 10) + ":" + hex.EncodeToString(sum[:12])
}

func parse(ctx context.Context, data []byte) (parsedSession, bool) {
	parsed := parsedSession{cwdTrusted: true}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
	var records []map[string]any
	positions := map[string]int{}
	count := 0
	for scanner.Scan() {
		if ctx.Err() != nil {
			return parsedSession{}, false
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		count++
		if count > maxSessionRecords {
			return parsedSession{}, false
		}
		var object map[string]any
		if !jsonDepthOK(line, maxJSONDepth) || json.Unmarshal(line, &object) != nil {
			parsed.malformed++
			continue
		}
		id, _ := object["id"].(string)
		if id != "" {
			if position, exists := positions[id]; exists {
				records[position] = object
				continue
			}
			positions[id] = len(records)
		}
		records = append(records, object)
	}
	if scanner.Err() != nil || ctx.Err() != nil {
		return parsedSession{}, false
	}
	pending := map[string]string{}
	completed := map[string]bool{}
	for _, record := range records {
		if ctx.Err() != nil {
			return parsedSession{}, false
		}
		typeName, _ := record["type"].(string)
		if !recognizedType(typeName) {
			continue
		}
		dataObject, ok := record["data"].(map[string]any)
		if !ok {
			parsed.malformed++
			continue
		}
		timestamp := stringValue(record["timestamp"])
		var events []event
		message := false
		switch typeName {
		case "session.start":
			id, _ := dataObject["sessionId"].(string)
			if id == "" || len(id) > maxUpstreamIDBytes {
				parsed.malformed++
				continue
			}
			if parsed.sessionID != "" && parsed.sessionID != id {
				parsed.malformed++
				continue
			}
			contextObject, hasContext := dataObject["context"].(map[string]any)
			if rawContext, exists := dataObject["context"]; exists && !hasContext {
				_ = rawContext
				parsed.malformed++
				continue
			}
			cwd := ""
			if hasContext {
				if rawCWD, exists := contextObject["cwd"]; exists {
					var valid bool
					cwd, valid = validCWD(rawCWD)
					if !valid {
						parsed.malformed++
						continue
					}
				}
			}
			parsed.sessionID = id
			if cwd != "" {
				if parsed.cwd != "" && parsed.cwd != cwd {
					parsed.cwdTrusted = false
				} else if parsed.cwd == "" {
					parsed.cwd = cwd
				}
			}
			trackTime(timestamp, &parsed.start, &parsed.end)
			continue
		case "user.message":
			content, ok := dataObject["content"].(string)
			if !ok || strings.TrimSpace(content) == "" {
				parsed.malformed++
				continue
			}
			events = []event{{Type: "message", Role: "user", Content: sanitizePayload(content), Timestamp: timestamp}}
			message = true
		case "assistant.message":
			var valid bool
			events, valid = assistantEvents(ctx, dataObject, timestamp)
			if !valid {
				parsed.malformed++
				continue
			}
			message = true
		case "assistant.reasoning":
			text, _ := dataObject["content"].(string)
			if text == "" {
				text, _ = dataObject["reasoningText"].(string)
			}
			if strings.TrimSpace(text) == "" {
				parsed.malformed++
				continue
			}
			events = []event{thinkingEvent(sanitizePayload(text).(string), timestamp)}
		case "tool.execution_complete":
			id, _ := dataObject["toolCallId"].(string)
			name, _ := dataObject["name"].(string)
			result, exists := dataObject["result"]
			if !safeToken(id, maxUpstreamIDBytes) || !exists || (name != "" && !safeToken(name, 256)) {
				parsed.malformed++
				continue
			}
			events = []event{{Type: "tool_result", Timestamp: timestamp, CallID: id, Name: name, Result: sanitizePayload(result)}}
		}
		if !validateToolPairs(events, pending, completed) {
			parsed.malformed++
			continue
		}
		if len(parsed.events)+len(events) > maxSessionEvents {
			return parsedSession{}, false
		}
		trackTime(timestamp, &parsed.start, &parsed.end)
		parsed.events = append(parsed.events, events...)
		if message {
			parsed.messages++
		}
	}
	if ctx.Err() != nil {
		return parsedSession{}, false
	}
	if parsed.cwd == "" {
		parsed.cwdTrusted = false
	}
	return parsed, len(parsed.events) > 0
}

func recognizedType(value string) bool {
	switch value {
	case "session.start", "user.message", "assistant.message", "assistant.reasoning", "tool.execution_complete":
		return true
	default:
		return false
	}
}

func assistantEvents(ctx context.Context, data map[string]any, timestamp string) ([]event, bool) {
	var out []event
	if raw, exists := data["reasoningText"]; exists {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
		out = append(out, thinkingEvent(sanitizePayload(text).(string), timestamp))
	}
	if raw, exists := data["content"]; exists {
		text, ok := raw.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
		out = append(out, event{Type: "message", Role: "assistant", Content: sanitizePayload(text), Timestamp: timestamp})
	}
	if raw, exists := data["toolRequests"]; exists {
		requests, ok := raw.([]any)
		if !ok || len(requests) == 0 {
			return nil, false
		}
		for _, rawRequest := range requests {
			if ctx.Err() != nil {
				return nil, false
			}
			request, ok := rawRequest.(map[string]any)
			if !ok {
				return nil, false
			}
			id, _ := request["toolCallId"].(string)
			name, _ := request["name"].(string)
			if !safeToken(id, maxUpstreamIDBytes) || !safeToken(name, 256) {
				return nil, false
			}
			input := request["arguments"]
			if input == nil {
				input = map[string]any{}
			}
			out = append(out, event{Type: "tool_use", Timestamp: timestamp, CallID: id, Name: name, Input: sanitizePayload(input)})
		}
	}
	return out, len(out) > 0
}

func validateToolPairs(events []event, pending map[string]string, completed map[string]bool) bool {
	adds := map[string]string{}
	resolves := map[string]bool{}
	for _, value := range events {
		switch value.Type {
		case "tool_use":
			if pending[value.CallID] != "" || completed[value.CallID] || adds[value.CallID] != "" {
				return false
			}
			adds[value.CallID] = value.Name
		case "tool_result":
			name, exists := pending[value.CallID]
			if !exists {
				name, exists = adds[value.CallID]
			}
			if !exists || completed[value.CallID] || resolves[value.CallID] || (value.Name != "" && value.Name != name) {
				return false
			}
			resolves[value.CallID] = true
		}
	}
	for id, name := range adds {
		pending[id] = name
	}
	for id := range resolves {
		delete(pending, id)
		completed[id] = true
	}
	return true
}

func thinkingEvent(text, timestamp string) event {
	return event{Type: "message", Role: "assistant", Content: []any{map[string]any{"type": "thinking", "thinking": text}}, Timestamp: timestamp}
}

func safeToken(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n") && !isAbsoluteString(value)
}

func sanitizePayload(value any) any {
	switch typed := value.(type) {
	case string:
		if isAbsoluteString(typed) {
			return "[redacted-path]"
		}
		return typed
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			out[i] = sanitizePayload(typed[i])
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if isAbsoluteString(key) {
				key = "[redacted-path-key]"
			}
			out[key] = sanitizePayload(child)
		}
		return out
	default:
		return value
	}
}

func isAbsoluteString(value string) bool {
	if filepath.IsAbs(value) || strings.HasPrefix(value, `\`) || windowsDrivePath(value) {
		return true
	}
	if len(value) < len("file:") || !strings.EqualFold(value[:len("file:")], "file:") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host != "" {
		return true
	}
	path := parsed.Path
	if path == "" {
		path = parsed.Opaque
	}
	decoded, err := url.PathUnescape(path)
	return err != nil || filepath.IsAbs(decoded) || windowsDrivePath(decoded)
}

func windowsDrivePath(value string) bool {
	return len(value) > 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':'
}

func validCWD(raw any) (string, bool) {
	value, ok := raw.(string)
	return value, ok && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func safeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || filepath.IsAbs(value) || strings.ContainsAny(value, `/\`) || value == "." || value == ".." {
		return ""
	}
	return value
}

func stringValue(raw any) string {
	value, _ := raw.(string)
	return value
}

func trackTime(raw string, start, end *time.Time) {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return
	}
	if start.IsZero() || value.Before(*start) {
		*start = value
	}
	if end.IsZero() || value.After(*end) {
		*end = value
	}
}

func hasEventType(events []event, types ...string) bool {
	for _, value := range events {
		for _, typeName := range types {
			if value.Type == typeName {
				return true
			}
		}
	}
	return false
}

func hasReasoning(events []event) bool {
	for _, value := range events {
		if _, ok := value.Content.([]any); ok {
			return true
		}
	}
	return false
}

func digestPrefix(value string, length int) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:length]
}

func sessionMetadata(session source.Session) string {
	data, _ := json.Marshal(session)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func samePathIdentity(left, right pathIdentity) bool {
	if len(left.directories) != len(right.directories) {
		return false
	}
	for i := range left.directories {
		if left.directories[i] != right.directories[i] {
			return false
		}
	}
	return true
}

func jsonDepthOK(data []byte, limit int) bool {
	depth := 0
	inString, escaped := false, false
	for _, value := range data {
		if inString {
			if escaped {
				escaped = false
			} else if value == '\\' {
				escaped = true
			} else if value == '"' {
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > limit {
				return false
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !inString
}

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("copilot-cli: invalid session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.digest != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("copilot-cli: unauthorized session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("copilot-cli: source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("copilot-cli: source changed")
	}
	format := "flat-v1"
	rawID := strings.TrimSuffix(filepath.Base(auth.relative), ".jsonl")
	if filepath.Base(auth.relative) == "events.jsonl" {
		format = "directory-v2"
		rawID = filepath.Base(filepath.Dir(auth.relative))
	}
	item := candidate{root: auth.root, path: auth.path, relative: auth.relative, rawID: rawID, format: format, bound: bound}
	fresh, freshAuth, output, valid := a.snapshot(ctx, item)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !valid || fresh.ID != session.ID || freshAuth.digest != auth.digest || freshAuth.metadata != auth.metadata || !os.SameFile(auth.fileInfo, freshAuth.fileInfo) || !samePathIdentity(auth.pathIdentity, freshAuth.pathIdentity) {
		return nil, errors.New("copilot-cli: source changed")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
