package gemini

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
	maxMappingBytes     = 1 << 20
	maxSessionRecords   = 4096
	maxSessionEvents    = 8192
	maxDirectoryEntries = 256
	maxGlobalEntries    = 4096
	maxJSONDepth        = 64
	maxUpstreamIDBytes  = 512
	maxRoots            = 64
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
		if configured := os.Getenv("GEMINI_DIR"); configured != "" {
			roots = []string{configured}
		} else if home, err := os.UserHomeDir(); err == nil {
			roots = []string{filepath.Join(home, ".gemini")}
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{roots: clean, configErr: err, known: map[string]authorization{}, instance: instanceCounter.Add(1)}
}

func (*Adapter) Product() string { return "gemini-cli" }

func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}
}

func validatedRoots(roots []string) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, errors.New("gemini-cli: root scan limit")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, errors.New("gemini-cli: invalid root")
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

// canonicalizeRoot permits only the operating system's first-component alias;
// every caller-controlled descendant must be a real directory, never a link.
func canonicalizeRoot(root string) (string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) {
		return "", errors.New("gemini-cli: invalid root")
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
			return "", errors.New("gemini-cli: invalid root")
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
			return "", errors.New("gemini-cli: invalid root")
		}
		current = next
	}
	return filepath.Clean(current), nil
}

type candidate struct {
	root, path, relative, project string
	bound                         *safeopen.BoundRoot
	rootIdentity                  safeopen.Identity
	projectLabel                  string
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

	var candidates []candidate
	globalVisited := 0
	boundIdentities := map[safeopen.Identity]bool{}
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validated, err := canonicalizeRoot(root)
		if err != nil || validated != root {
			return nil, errors.New("gemini-cli: root changed")
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("gemini-cli: root read failed")
		}
		if boundIdentities[bound.Identity()] {
			bound.Close()
			continue
		}
		rootIdentity := bound.Identity()
		boundIdentities[rootIdentity] = true
		if a.afterBind != nil {
			a.afterBind(root)
		}
		projects, err := bound.ReadDirLimit("tmp", maxDirectoryEntries)
		if os.IsNotExist(err) {
			bound.Close()
			continue
		}
		if err != nil {
			bound.Close()
			return nil, directoryError(err)
		}
		labels, mapErr := readProjectMap(ctx, bound)
		if mapErr != nil {
			bound.Close()
			return nil, mapErr
		}
		globalVisited += len(projects)
		if globalVisited > maxGlobalEntries {
			bound.Close()
			return nil, errors.New("gemini-cli: directory limit")
		}
		for _, project := range projects {
			if err := ctx.Err(); err != nil {
				bound.Close()
				return nil, err
			}
			if !validProjectDirectory(project) {
				continue
			}
			relDir := filepath.Join("tmp", project.Name(), "chats")
			files, err := bound.ReadDirLimit(relDir, maxDirectoryEntries)
			if err != nil {
				if errors.Is(err, safeopen.ErrDirectoryLimit) {
					bound.Close()
					return nil, errors.New("gemini-cli: directory limit")
				}
				continue
			}
			globalVisited += len(files)
			if globalVisited > maxGlobalEntries {
				bound.Close()
				return nil, errors.New("gemini-cli: directory limit")
			}
			for _, file := range files {
				if !validSessionFile(file) {
					continue
				}
				rel := filepath.Join(relDir, file.Name())
				candidates = append(candidates, candidate{
					root: root, path: filepath.Join(root, rel), relative: rel,
					project: project.Name(), projectLabel: labels[project.Name()], rootIdentity: rootIdentity,
				})
			}
		}
		bound.Close()
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
		bound, err := safeopen.Bind(item.root)
		if err != nil {
			continue
		}
		if bound.Identity() != item.rootIdentity {
			bound.Close()
			continue
		}
		item.bound = bound
		session, auth, _, ok := a.snapshot(ctx, item)
		bound.Close()
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.replaceKnown(next)
	return out, nil
}

func directoryError(err error) error {
	if errors.Is(err, safeopen.ErrDirectoryLimit) {
		return errors.New("gemini-cli: directory limit")
	}
	return errors.New("gemini-cli: directory read failed")
}

func rootIndex(roots []string, root string) int {
	for i, value := range roots {
		if value == root {
			return i
		}
	}
	return len(roots)
}

func validProjectDirectory(entry os.DirEntry) bool {
	if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		return false
	}
	name := strings.ToLower(entry.Name())
	return name != "" && !strings.Contains(name, "antigravity")
}

func validSessionFile(entry os.DirEntry) bool {
	if !entry.Type().IsRegular() {
		return false
	}
	name := entry.Name()
	if strings.Contains(strings.ToLower(name), "antigravity") || !strings.HasPrefix(name, "session-") {
		return false
	}
	return strings.HasSuffix(name, ".json") || strings.HasSuffix(name, ".jsonl")
}

type projectsFile struct {
	Projects map[string]string `json:"projects"`
}

func readProjectMap(ctx context.Context, bound *safeopen.BoundRoot) (map[string]string, error) {
	file, _, err := bound.OpenWithPathIdentity("projects.json", maxMappingBytes)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMappingBytes+1))
	if err != nil || len(data) > maxMappingBytes {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	if !jsonDepthOK(ctx, data, maxJSONDepth) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, nil
	}
	var parsed projectsFile
	if json.Unmarshal(data, &parsed) != nil || len(parsed.Projects) > maxDirectoryEntries {
		return nil, nil
	}
	result := map[string]string{}
	keys := make([]string, 0, len(parsed.Projects))
	for path := range parsed.Projects {
		keys = append(keys, path)
	}
	sort.Strings(keys)
	for _, path := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			continue
		}
		label := safeLabel(parsed.Projects[path])
		if label == "" {
			label = safeLabel(filepath.Base(path))
		}
		if label == "" {
			continue
		}
		sum := sha256.Sum256([]byte(path))
		key := hex.EncodeToString(sum[:])
		if result[key] == "" {
			result[key] = label
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func safeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || filepath.IsAbs(value) || strings.ContainsAny(value, `/\`) {
		return ""
	}
	if value == "." || value == ".." || strings.Contains(strings.ToLower(value), "antigravity") {
		return ""
	}
	return value
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
	format         string
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
	if !ok || len(parsed.events) == 0 || len(parsed.sessionID) > maxUpstreamIDBytes {
		return source.Session{}, authorization{}, nil, false
	}
	if strings.ContainsAny(parsed.sessionID, "\x00\r\n") {
		return source.Session{}, authorization{}, nil, false
	}
	if parsed.start.IsZero() {
		parsed.start = info.ModTime()
	}
	if parsed.end.IsZero() {
		parsed.end = info.ModTime()
	}
	identitySeed := item.root + "\x00" + item.project + "\x00" + parsed.sessionID
	id := "gemini-cli:" + parsed.format + ":" + digestPrefix(identitySeed, 24)
	scope := collectionScope(item.root + "\x00" + item.project)
	if label := safeLabel(item.projectLabel); label != "" {
		scope = projectScope(label, "map:"+item.project)
	} else if parsed.cwdTrusted {
		label := safeLabel(filepath.Base(parsed.cwd))
		if label != "" {
			scope = projectScope(label, "cwd:"+parsed.cwd)
		}
	} else if !isHexHash(item.project) {
		if label := safeLabel(item.project); label != "" {
			scope = projectScope(label, "dir:"+label)
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
		ID: id, Product: a.Product(), FormatVersion: parsed.format, AdapterVersion: "1",
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
	return "gemini-cli:ref:" + strconv.FormatUint(a.instance, 10) + ":" + hex.EncodeToString(sum[:12])
}

func isHexHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func projectScope(label, seed string) source.ScopeRef {
	return source.ScopeRef{Type: source.ScopeProject, Root: "gemini-cli:project:" + digestPrefix(seed, 24), Label: label}
}

func collectionScope(seed string) source.ScopeRef {
	return source.ScopeRef{Type: source.ScopeSessionCollection, Root: "gemini-cli:collection:" + digestPrefix(seed, 24), Label: "Gemini CLI sessions"}
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

func parse(ctx context.Context, data []byte) (parsedSession, bool) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return parsedSession{}, false
	}
	var object map[string]any
	if jsonDepthOK(ctx, trimmed, maxJSONDepth) && json.Unmarshal(trimmed, &object) == nil {
		if _, exists := object["messages"]; exists {
			return parseObject(ctx, object)
		}
	}
	return parseStream(ctx, data)
}

func parseObject(ctx context.Context, root map[string]any) (parsedSession, bool) {
	var parsed parsedSession
	parsed.format = "object-v1"
	parsed.cwdTrusted = true
	parsed.sessionID, _ = root["sessionId"].(string)
	messages, ok := root["messages"].([]any)
	if parsed.sessionID == "" || !ok || len(messages) > maxSessionRecords {
		return parsedSession{}, false
	}
	if raw, exists := root["cwd"]; exists {
		parsed.cwd, parsed.cwdTrusted = validCWD(raw)
	}
	trackTime(stringValue(root["startTime"]), &parsed.start, &parsed.end)
	trackTime(stringValue(root["lastUpdated"]), &parsed.start, &parsed.end)
	records := dedupeMessages(messages)
	return parseMessages(ctx, parsed, records)
}

func dedupeMessages(messages []any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	positions := map[string]int{}
	for _, raw := range messages {
		object, ok := raw.(map[string]any)
		if !ok {
			out = append(out, nil)
			continue
		}
		id, _ := object["id"].(string)
		if id != "" {
			if index, exists := positions[id]; exists {
				out[index] = object
				continue
			}
			positions[id] = len(out)
		}
		out = append(out, object)
	}
	return out
}

func parseStream(ctx context.Context, data []byte) (parsedSession, bool) {
	parsed := parsedSession{format: "stream-v1", cwdTrusted: true}
	var records []map[string]any
	positions := map[string]int{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
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
		if !jsonDepthOK(ctx, line, maxJSONDepth) || json.Unmarshal(line, &object) != nil {
			parsed.malformed++
			continue
		}
		typeName, _ := object["type"].(string)
		if typeName == "metadata" {
			id, _ := object["sessionId"].(string)
			if id == "" {
				parsed.malformed++
				continue
			}
			if parsed.sessionID == "" {
				parsed.sessionID = id
			} else if parsed.sessionID != id {
				parsed.malformed++
			}
			trackTime(stringValue(object["startTime"]), &parsed.start, &parsed.end)
			trackTime(stringValue(object["lastUpdated"]), &parsed.start, &parsed.end)
			continue
		}
		if typeName != "user" && typeName != "gemini" {
			continue
		}
		id, _ := object["id"].(string)
		if id != "" {
			if index, exists := positions[id]; exists {
				records[index] = object
				continue
			}
			positions[id] = len(records)
		}
		records = append(records, object)
	}
	if scanner.Err() != nil || ctx.Err() != nil {
		return parsedSession{}, false
	}
	return parseMessages(ctx, parsed, records)
}

func parseMessages(ctx context.Context, parsed parsedSession, records []map[string]any) (parsedSession, bool) {
	pending := map[string]string{}
	completed := map[string]bool{}
	for _, record := range records {
		if ctx.Err() != nil {
			return parsedSession{}, false
		}
		if record == nil {
			parsed.malformed++
			continue
		}
		typeName, _ := record["type"].(string)
		if typeName != "user" && typeName != "gemini" {
			continue
		}
		events, valid := geminiMessageEvents(ctx, typeName, record)
		if !valid {
			parsed.malformed++
			continue
		}
		if rawID, exists := record["sessionId"]; exists {
			id, ok := rawID.(string)
			if !ok || id == "" || (parsed.sessionID != "" && parsed.sessionID != id) {
				parsed.malformed++
				continue
			}
			if parsed.sessionID == "" {
				parsed.sessionID = id
			}
		}
		if !validateToolPairs(events, pending, completed) {
			parsed.malformed++
			continue
		}
		if len(parsed.events)+len(events) > maxSessionEvents {
			return parsedSession{}, false
		}
		if rawCWD, exists := record["cwd"]; exists {
			cwd, valid := validCWD(rawCWD)
			if !valid || (parsed.cwd != "" && parsed.cwd != cwd) {
				parsed.cwdTrusted = false
			} else if parsed.cwd == "" {
				parsed.cwd = cwd
			}
		}
		trackTime(stringValue(record["timestamp"]), &parsed.start, &parsed.end)
		parsed.events = append(parsed.events, events...)
		parsed.messages++
	}
	if parsed.cwd == "" {
		parsed.cwdTrusted = false
	}
	if ctx.Err() != nil {
		return parsedSession{}, false
	}
	return parsed, parsed.sessionID != "" && len(parsed.events) > 0
}

func geminiMessageEvents(ctx context.Context, typeName string, message map[string]any) ([]event, bool) {
	role := "user"
	if typeName == "gemini" {
		role = "assistant"
	}
	timestamp := stringValue(message["timestamp"])
	var out []event
	if raw, exists := message["thoughts"]; exists {
		if role != "assistant" {
			return nil, false
		}
		thoughts, ok := raw.([]any)
		if !ok {
			return nil, false
		}
		for _, rawThought := range thoughts {
			if ctx.Err() != nil {
				return nil, false
			}
			thought, ok := rawThought.(map[string]any)
			if !ok {
				return nil, false
			}
			description, _ := thought["description"].(string)
			if strings.TrimSpace(description) == "" {
				return nil, false
			}
			clean, ok := sanitizePayload(ctx, description)
			if !ok {
				return nil, false
			}
			out = append(out, thinkingEvent(clean.(string), timestamp))
		}
	}
	if raw, exists := message["content"]; exists {
		textEvents, ok := geminiContent(ctx, role, raw, timestamp)
		if !ok {
			return nil, false
		}
		out = append(out, textEvents...)
	}
	if raw, exists := message["toolCalls"]; exists {
		if role != "assistant" {
			return nil, false
		}
		calls, ok := raw.([]any)
		if !ok || len(calls) == 0 {
			return nil, false
		}
		for _, rawCall := range calls {
			if ctx.Err() != nil {
				return nil, false
			}
			callEvents, ok := geminiToolCall(ctx, rawCall, timestamp)
			if !ok {
				return nil, false
			}
			out = append(out, callEvents...)
		}
	}
	return out, len(out) > 0
}

func geminiContent(ctx context.Context, role string, raw any, timestamp string) ([]event, bool) {
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, false
		}
		clean, ok := sanitizePayload(ctx, value)
		if !ok {
			return nil, false
		}
		return []event{{Type: "message", Role: role, Content: clean, Timestamp: timestamp}}, true
	case []any:
		if len(value) == 0 {
			return nil, false
		}
		var out []event
		for _, rawPart := range value {
			if ctx.Err() != nil {
				return nil, false
			}
			part, ok := rawPart.(map[string]any)
			if !ok {
				return nil, false
			}
			text, exists := part["text"]
			if !exists {
				continue
			}
			value, ok := text.(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, false
			}
			clean, ok := sanitizePayload(ctx, value)
			if !ok {
				return nil, false
			}
			out = append(out, event{Type: "message", Role: role, Content: clean, Timestamp: timestamp})
		}
		return out, true
	default:
		return nil, false
	}
}

func geminiToolCall(ctx context.Context, raw any, timestamp string) ([]event, bool) {
	call, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	id, _ := call["id"].(string)
	name, _ := call["name"].(string)
	if !safeToken(id, maxUpstreamIDBytes) || !safeToken(name, 256) {
		return nil, false
	}
	input := call["args"]
	if input == nil {
		input = map[string]any{}
	}
	cleanInput, ok := sanitizePayload(ctx, input)
	if !ok {
		return nil, false
	}
	out := []event{{Type: "tool_use", Timestamp: timestamp, CallID: id, Name: name, Input: cleanInput}}
	rawResults, exists := call["result"]
	if !exists {
		return out, true
	}
	results, ok := rawResults.([]any)
	if !ok {
		return nil, false
	}
	for _, rawResult := range results {
		if ctx.Err() != nil {
			return nil, false
		}
		wrapper, ok := rawResult.(map[string]any)
		if !ok {
			return nil, false
		}
		response, ok := wrapper["functionResponse"].(map[string]any)
		if !ok {
			return nil, false
		}
		resultID, _ := response["id"].(string)
		resultName, _ := response["name"].(string)
		body, hasBody := response["response"]
		if resultID == "" {
			resultID = id
		}
		if resultID != id || (resultName != "" && resultName != name) || !hasBody {
			return nil, false
		}
		if object, ok := body.(map[string]any); ok {
			if output, exists := object["output"]; exists {
				body = output
			}
		}
		cleanResult, ok := sanitizePayload(ctx, body)
		if !ok {
			return nil, false
		}
		out = append(out, event{Type: "tool_result", Timestamp: timestamp, CallID: id, Name: name, Result: cleanResult})
	}
	return out, true
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
	guard := &traversalGuard{ctx: ctx, remaining: 1}
	return sanitizePayloadGuarded(guard, value)
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

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("gemini-cli: invalid session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.digest != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("gemini-cli: unauthorized session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("gemini-cli: source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("gemini-cli: source changed")
	}
	project := filepath.Base(filepath.Dir(filepath.Dir(auth.relative)))
	item := candidate{root: auth.root, path: auth.path, relative: auth.relative, project: project, bound: bound}
	mapping, mapErr := readProjectMap(ctx, bound)
	if mapErr != nil {
		return nil, mapErr
	}
	if mapping != nil {
		item.projectLabel = mapping[project]
	}
	fresh, freshAuth, output, valid := a.snapshot(ctx, item)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !valid || fresh.ID != session.ID || freshAuth.digest != auth.digest || freshAuth.metadata != auth.metadata || !os.SameFile(auth.fileInfo, freshAuth.fileInfo) || !samePathIdentity(auth.pathIdentity, freshAuth.pathIdentity) {
		return nil, errors.New("gemini-cli: source changed")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
