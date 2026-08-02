package codebuddycli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
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
	maxRoots            = 64
	maxIdentifierBytes  = 512
)

type pathIdentity struct{ directories []safeopen.Identity }

type authorization struct {
	id, digest, metadata, root, relative, project, uuid string
	rootIdentity                                        safeopen.Identity
	pathIdentity                                        pathIdentity
	fileInfo                                            os.FileInfo
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
		if home, err := os.UserHomeDir(); err == nil {
			roots = []string{filepath.Join(home, ".codebuddy", "projects")}
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{roots: clean, configErr: err, known: map[string]authorization{}, instance: instanceCounter.Add(1)}
}

func (*Adapter) Product() string { return "codebuddy-cli" }

func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}
}

func validatedRoots(roots []string) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, errors.New("codebuddy-cli: root scan limit")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, errors.New("codebuddy-cli: invalid root")
		}
		clean, err := canonicalizeRoot(root)
		if err != nil {
			return nil, err
		}
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	return out, nil
}

func canonicalizeRoot(root string) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", errors.New("codebuddy-cli: invalid root")
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
			return "", errors.New("codebuddy-cli: invalid root")
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
			return "", errors.New("codebuddy-cli: invalid root")
		}
		current = next
	}
	return filepath.Clean(current), nil
}

type candidate struct {
	root, relative, project, uuid string
	rootIdentity                  safeopen.Identity
	bound                         *safeopen.BoundRoot
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
	var out []source.Session
	next := map[string]authorization{}
	seenIDs := map[string]bool{}
	seenRoots := map[safeopen.Identity]bool{}
	globalEntries := 0
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validated, err := canonicalizeRoot(root)
		if err != nil || validated != root {
			return nil, errors.New("codebuddy-cli: root changed")
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("codebuddy-cli: root read failed")
		}
		if seenRoots[bound.Identity()] {
			bound.Close()
			continue
		}
		seenRoots[bound.Identity()] = true
		if a.afterBind != nil {
			a.afterBind(root)
		}
		projects, err := bound.ReadDirLimit(".", maxDirectoryEntries)
		if err != nil {
			bound.Close()
			if errors.Is(err, safeopen.ErrDirectoryLimit) {
				return nil, errors.New("codebuddy-cli: directory limit")
			}
			return nil, errors.New("codebuddy-cli: directory read failed")
		}
		globalEntries += len(projects)
		if globalEntries > maxGlobalEntries {
			bound.Close()
			return nil, errors.New("codebuddy-cli: directory limit")
		}
		sort.Slice(projects, func(i, j int) bool { return projects[i].Name() < projects[j].Name() })
		for _, project := range projects {
			if err := ctx.Err(); err != nil {
				bound.Close()
				return nil, err
			}
			if !validProjectDir(project) {
				continue
			}
			files, err := bound.ReadDirLimit(project.Name(), maxDirectoryEntries)
			if err != nil {
				if errors.Is(err, safeopen.ErrDirectoryLimit) {
					bound.Close()
					return nil, errors.New("codebuddy-cli: directory limit")
				}
				continue
			}
			globalEntries += len(files)
			if globalEntries > maxGlobalEntries {
				bound.Close()
				return nil, errors.New("codebuddy-cli: directory limit")
			}
			sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
			for _, file := range files {
				if err := ctx.Err(); err != nil {
					bound.Close()
					return nil, err
				}
				if !file.Type().IsRegular() || !strings.HasSuffix(strings.ToLower(file.Name()), ".jsonl") {
					continue
				}
				uuid := strings.ToLower(strings.TrimSuffix(file.Name(), filepath.Ext(file.Name())))
				if !validUUID(uuid) {
					continue
				}
				relative := filepath.Join(project.Name(), file.Name())
				item := candidate{root: root, relative: relative, project: project.Name(), uuid: uuid, rootIdentity: bound.Identity(), bound: bound}
				session, auth, _, valid := a.snapshot(ctx, item)
				if err := ctx.Err(); err != nil {
					bound.Close()
					return nil, err
				}
				if !valid || seenIDs[session.ID] {
					continue
				}
				seenIDs[session.ID] = true
				out = append(out, session)
				next[session.OpaqueRef] = auth
			}
		}
		bound.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	a.replaceKnown(next)
	return out, nil
}

func validProjectDir(entry os.DirEntry) bool {
	if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !safeBasename(entry.Name()) {
		return false
	}
	switch strings.ToLower(entry.Name()) {
	case "history", "settings", "mcp", "auth", "logs", "log", "cache", "plugins", "blobs", "subagents", "tool-results":
		return false
	default:
		return true
	}
}

func safeBasename(value string) bool {
	return value != "" && len(value) <= maxIdentifierBytes && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, "/\\:\x00\r\n")
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, char := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if char != '-' {
				return false
			}
			continue
		}
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
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
	IsError   bool   `json:"is_error,omitempty"`
}

type parsedSession struct {
	events                         []event
	messages, malformed            int
	start, end                     time.Time
	cwd                            string
	cwdSeen, cwdTrusted            bool
	tools, reasoning, messageEvent bool
}

type toolState struct{ name string }

func parse(ctx context.Context, data []byte, project string) (parsedSession, bool) {
	parsed := parsedSession{cwdTrusted: true}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
	records := make([]map[string]any, 0)
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
		var record map[string]any
		if !jsonDepthOK(ctx, line, maxJSONDepth) || json.Unmarshal(line, &record) != nil {
			parsed.malformed++
			continue
		}
		typeName, _ := record["type"].(string)
		if !recognizedType(typeName) {
			continue
		}
		id, _ := record["id"].(string)
		if id != "" {
			if position, exists := positions[id]; exists {
				records[position] = record
				continue
			}
			positions[id] = len(records)
		}
		records = append(records, record)
	}
	if scanner.Err() != nil || ctx.Err() != nil {
		return parsedSession{}, false
	}
	pending := map[string]toolState{}
	completed := map[string]bool{}
	for _, record := range records {
		if ctx.Err() != nil {
			return parsedSession{}, false
		}
		typeName, _ := record["type"].(string)
		timestamp, timestampOK := optionalTimestamp(record["timestamp"])
		if !timestampOK {
			parsed.malformed++
			continue
		}
		var events []event
		valid := false
		switch typeName {
		case "message":
			role, _ := record["role"].(string)
			if role != "user" && role != "assistant" {
				parsed.malformed++
				continue
			}
			var ok bool
			events, ok = messageEvents(ctx, role, record["content"], timestamp)
			if !ok {
				parsed.malformed++
				continue
			}
			valid = true
		case "reasoning":
			var ok bool
			events, ok = reasoningEvents(ctx, record["rawContent"], timestamp)
			if !ok {
				parsed.malformed++
				continue
			}
			valid = true
		case "function_call":
			callID, _ := record["callId"].(string)
			name, _ := record["name"].(string)
			arguments, exists := record["arguments"]
			if !safeToken(callID, maxIdentifierBytes) || !safeToken(name, 256) || !exists {
				parsed.malformed++
				continue
			}
			clean, ok := sanitizePayload(ctx, arguments)
			if !ok {
				return parsedSession{}, false
			}
			events = []event{{Type: "tool_use", CallID: callID, Name: name, Input: clean, Timestamp: timestamp}}
			valid = true
		case "function_call_result":
			callID, _ := record["callId"].(string)
			name, _ := record["name"].(string)
			status, _ := record["status"].(string)
			output, exists := record["output"]
			isError, providerOK := resultError(record["providerData"], status)
			if !safeToken(callID, maxIdentifierBytes) || !safeToken(name, 256) || !validResultStatus(status) || !exists || !providerOK {
				parsed.malformed++
				continue
			}
			clean, ok := sanitizePayload(ctx, output)
			if !ok {
				return parsedSession{}, false
			}
			events = []event{{Type: "tool_result", CallID: callID, Name: name, Result: clean, IsError: isError, Timestamp: timestamp}}
			valid = true
		}
		if !valid {
			continue
		}
		if !validateToolTransaction(events, pending, completed) {
			parsed.malformed++
			continue
		}
		if len(parsed.events)+len(events) > maxSessionEvents {
			return parsedSession{}, false
		}
		observeCWD(&parsed, record["cwd"], project)
		if timestamp != "" {
			trackTime(timestamp, &parsed.start, &parsed.end)
		}
		for _, value := range events {
			switch value.Type {
			case "message":
				parsed.messages++
				parsed.messageEvent = true
				if isThinkingEvent(value) {
					parsed.reasoning = true
				}
			case "tool_use", "tool_result":
				parsed.tools = true
			}
			parsed.events = append(parsed.events, value)
		}
	}
	if ctx.Err() != nil {
		return parsedSession{}, false
	}
	if !parsed.cwdSeen {
		parsed.cwdTrusted = false
	}
	return parsed, len(parsed.events) > 0
}

func recognizedType(value string) bool {
	switch value {
	case "message", "reasoning", "function_call", "function_call_result":
		return true
	default:
		return false
	}
}

func messageEvents(ctx context.Context, role string, raw any, timestamp string) ([]event, bool) {
	parts, ok := raw.([]any)
	if !ok || len(parts) == 0 {
		return nil, false
	}
	want := "input_text"
	if role == "assistant" {
		want = "output_text"
	}
	out := make([]event, 0, len(parts))
	for _, rawPart := range parts {
		if ctx.Err() != nil {
			return nil, false
		}
		part, ok := rawPart.(map[string]any)
		if !ok || part["type"] != want {
			return nil, false
		}
		text, ok := part["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
		clean, ok := sanitizePayload(ctx, text)
		if !ok {
			return nil, false
		}
		out = append(out, event{Type: "message", Role: role, Content: clean, Timestamp: timestamp})
	}
	return out, true
}

func reasoningEvents(ctx context.Context, raw any, timestamp string) ([]event, bool) {
	parts, ok := raw.([]any)
	if !ok || len(parts) == 0 {
		return nil, false
	}
	out := make([]event, 0, len(parts))
	for _, rawPart := range parts {
		if ctx.Err() != nil {
			return nil, false
		}
		part, ok := rawPart.(map[string]any)
		if !ok || part["type"] != "reasoning_text" {
			return nil, false
		}
		text, ok := part["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
		clean, ok := sanitizePayload(ctx, text)
		if !ok {
			return nil, false
		}
		out = append(out, event{Type: "message", Role: "assistant", Content: []any{map[string]any{"type": "thinking", "thinking": clean}}, Timestamp: timestamp})
	}
	return out, true
}

func validateToolTransaction(events []event, pending map[string]toolState, completed map[string]bool) bool {
	adds := map[string]toolState{}
	resolves := map[string]bool{}
	for _, value := range events {
		switch value.Type {
		case "tool_use":
			if pending[value.CallID].name != "" || completed[value.CallID] || adds[value.CallID].name != "" {
				return false
			}
			adds[value.CallID] = toolState{name: value.Name}
		case "tool_result":
			call, exists := pending[value.CallID]
			if !exists {
				call, exists = adds[value.CallID]
			}
			if !exists || completed[value.CallID] || resolves[value.CallID] || call.name != value.Name {
				return false
			}
			resolves[value.CallID] = true
		}
	}
	for id, call := range adds {
		pending[id] = call
	}
	for id := range resolves {
		delete(pending, id)
		completed[id] = true
	}
	return true
}

func resultError(rawProvider any, status string) (bool, bool) {
	isError := status == "incomplete" || status == "failed" || status == "error"
	if rawProvider == nil {
		return isError, true
	}
	provider, ok := rawProvider.(map[string]any)
	if !ok {
		return false, false
	}
	rawTool, exists := provider["toolResult"]
	if !exists {
		return isError, true
	}
	tool, ok := rawTool.(map[string]any)
	if !ok {
		return false, false
	}
	rawError, exists := tool["error"]
	if !exists || rawError == nil {
		return isError, true
	}
	switch value := rawError.(type) {
	case bool:
		return isError || value, true
	case string:
		return isError || strings.TrimSpace(value) != "", true
	default:
		return true, true
	}
}

func validResultStatus(value string) bool {
	switch value {
	case "completed", "complete", "success", "incomplete", "failed", "error":
		return true
	default:
		return false
	}
}

func observeCWD(parsed *parsedSession, raw any, project string) {
	if raw == nil {
		return
	}
	value, ok := raw.(string)
	if !ok {
		parsed.cwdTrusted = false
		return
	}
	clean, ok := cleanCWD(value)
	if !ok || CompressCWD(value) != project {
		parsed.cwdTrusted = false
		return
	}
	if parsed.cwdSeen && parsed.cwd != clean {
		parsed.cwdTrusted = false
		return
	}
	parsed.cwdSeen = true
	parsed.cwd = clean
}

// CompressCWD mirrors CodeBuddy's lossy project-directory encoding.
func CompressCWD(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		if char == '/' || char == '\\' || char == ':' || char == '-' {
			if builder.Len() > 0 && !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
			continue
		}
		builder.WriteRune(char)
		lastDash = false
	}
	return strings.Trim(builder.String(), "-")
}

func cleanCWD(value string) (string, bool) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	if filepath.IsAbs(value) {
		clean := filepath.Clean(value)
		return clean, clean == value
	}
	if windowsDrivePath(value) {
		replaced := strings.ReplaceAll(value, `\`, "/")
		if path.Clean(replaced[2:]) != replaced[2:] {
			return "", false
		}
		return strings.ToUpper(replaced[:1]) + replaced[1:], true
	}
	if strings.HasPrefix(value, `\\`) {
		replaced := strings.ReplaceAll(value, `\`, "/")
		rest := strings.TrimPrefix(replaced, "//")
		if rest == "" || strings.Contains(rest, "//") || path.Clean("/"+rest) != "/"+rest || len(strings.Split(rest, "/")) < 2 {
			return "", false
		}
		return "//" + rest, true
	}
	return "", false
}

func optionalTimestamp(raw any) (string, bool) {
	if raw == nil {
		return "", true
	}
	value, ok := raw.(string)
	if !ok {
		return "", false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", false
	}
	return parsed.UTC().Format(time.RFC3339Nano), true
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

func isThinkingEvent(value event) bool {
	parts, ok := value.Content.([]any)
	if !ok || len(parts) != 1 {
		return false
	}
	part, ok := parts[0].(map[string]any)
	return ok && part["type"] == "thinking"
}

func (a *Adapter) snapshot(ctx context.Context, item candidate) (source.Session, authorization, []byte, bool) {
	file, identities, err := item.bound.OpenWithPathIdentity(item.relative, maxSessionBytes)
	if err != nil {
		return source.Session{}, authorization{}, nil, false
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxSessionBytes+1))
	info, statErr := file.Stat()
	file.Close()
	if readErr != nil || statErr != nil || len(data) > maxSessionBytes || ctx.Err() != nil {
		return source.Session{}, authorization{}, nil, false
	}
	parsed, ok := parse(ctx, data, item.project)
	if !ok || ctx.Err() != nil {
		return source.Session{}, authorization{}, nil, false
	}
	if parsed.start.IsZero() {
		parsed.start = info.ModTime().UTC()
	}
	if parsed.end.IsZero() {
		parsed.end = info.ModTime().UTC()
	}
	rootIdentity := fmt.Sprintf("%d:%d", item.rootIdentity.Volume, item.rootIdentity.File)
	identitySeed := rootIdentity + "\x00" + item.project + "\x00" + item.uuid
	id := "codebuddy-cli:" + digestPrefix(identitySeed, 32)
	scope := source.ScopeRef{
		Type:  source.ScopeSessionCollection,
		Root:  "codebuddy-cli:collection:" + digestPrefix(identitySeed, 24),
		Label: "CodeBuddy CLI sessions",
	}
	if parsed.cwdTrusted {
		scope = source.ScopeRef{
			Type:  source.ScopeProject,
			Root:  "codebuddy-cli:project:" + digestPrefix(parsed.cwd, 24),
			Label: safeCWDLabel(parsed.cwd),
		}
	}
	capabilities := make([]source.Capability, 0, 3)
	if parsed.messageEvent {
		capabilities = append(capabilities, source.CapabilityMessages)
	}
	if parsed.tools {
		capabilities = append(capabilities, source.CapabilityTools)
	}
	if parsed.reasoning {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	session := source.Session{
		ID: id, Product: a.Product(), FormatVersion: "jsonl-v1", AdapterVersion: "1",
		Capabilities: capabilities, Scope: scope, StartedAt: parsed.start, EndedAt: parsed.end,
		MessageCount: parsed.messages, MalformedCount: parsed.malformed,
		OpaqueRef: a.opaqueRef(item.root, item.relative),
	}
	sum := sha256.Sum256(data)
	session.SnapshotID = hex.EncodeToString(sum[:])
	auth := authorization{
		id: session.ID, digest: session.SnapshotID, metadata: sessionMetadata(session),
		root: item.root, relative: item.relative, project: item.project, uuid: item.uuid,
		rootIdentity: item.rootIdentity, pathIdentity: pathIdentity{directories: identities}, fileInfo: info,
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

func (a *Adapter) opaqueRef(root, relative string) string {
	return "codebuddy-cli:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+relative, 24)
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

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("codebuddy-cli: invalid session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.digest != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("codebuddy-cli: unauthorized session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("codebuddy-cli: source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("codebuddy-cli: source changed")
	}
	item := candidate{root: auth.root, relative: auth.relative, project: auth.project, uuid: auth.uuid, rootIdentity: bound.Identity(), bound: bound}
	fresh, freshAuth, output, valid := a.snapshot(ctx, item)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !valid || fresh.ID != session.ID || fresh.SnapshotID != session.SnapshotID || freshAuth.metadata != auth.metadata ||
		freshAuth.digest != auth.digest || !os.SameFile(auth.fileInfo, freshAuth.fileInfo) || !samePathIdentity(auth.pathIdentity, freshAuth.pathIdentity) {
		return nil, errors.New("codebuddy-cli: source changed")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}

type traversalGuard struct {
	ctx       context.Context
	remaining int
}

func (g *traversalGuard) allowed() bool {
	g.remaining--
	if g.remaining > 0 {
		return true
	}
	g.remaining = 64
	return g.ctx.Err() == nil
}

func sanitizePayload(ctx context.Context, value any) (any, bool) {
	return sanitizePayloadGuarded(&traversalGuard{ctx: ctx, remaining: 1}, value)
}

func sanitizePayloadGuarded(guard *traversalGuard, value any) (any, bool) {
	if !guard.allowed() {
		return nil, false
	}
	switch typed := value.(type) {
	case string:
		if isAbsoluteString(typed) {
			return "[redacted-path]", true
		}
		return typed, true
	case []any:
		out := make([]any, len(typed))
		for i := range typed {
			clean, ok := sanitizePayloadGuarded(guard, typed[i])
			if !ok {
				return nil, false
			}
			out[i] = clean
		}
		return out, true
	case map[string]any:
		out := make(map[string]any, len(typed))
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, original := range keys {
			key := original
			if isAbsoluteString(key) {
				key = "[redacted-path-key:" + digestPrefix(original, 16) + "]"
			}
			if _, exists := out[key]; exists {
				return nil, false
			}
			clean, ok := sanitizePayloadGuarded(guard, typed[original])
			if !ok {
				return nil, false
			}
			out[key] = clean
		}
		return out, true
	default:
		return value, true
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
	rawPath := parsed.Path
	if rawPath == "" {
		rawPath = parsed.Opaque
	}
	decoded, err := url.PathUnescape(rawPath)
	return err != nil || filepath.IsAbs(decoded) || strings.HasPrefix(decoded, `\`) || windowsDrivePath(decoded)
}

func windowsDrivePath(value string) bool {
	return len(value) > 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func safeToken(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n") && !isAbsoluteString(value)
}

func safeCWDLabel(value string) string {
	normalized := strings.TrimRight(strings.ReplaceAll(value, `\`, "/"), "/")
	label := path.Base(normalized)
	if label == "" || label == "." || label == "/" || len(label) > 128 || strings.ContainsAny(label, "\x00\r\n") {
		return "CodeBuddy CLI project"
	}
	return label
}

func jsonDepthOK(ctx context.Context, data []byte, limit int) bool {
	depth := 0
	inString, escaped := false, false
	for index, value := range data {
		if index&4095 == 0 && ctx.Err() != nil {
			return false
		}
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
	return depth == 0 && !inString && ctx.Err() == nil
}
