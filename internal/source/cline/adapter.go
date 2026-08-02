package cline

import (
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
	maxRoots            = 64
	maxDirectoryEntries = 256
	maxGlobalEntries    = 4096
	maxFileBytes        = 4 << 20
	maxJSONDepth        = 64
	maxMessages         = 4096
	maxEvents           = 8192
	maxIDBytes          = 512
)

type pathIdentity struct{ directories []safeopen.Identity }

type fileAuthorization struct {
	digest       string
	pathIdentity pathIdentity
	fileInfo     os.FileInfo
}

type authorization struct {
	id, metadata, root, relativeDir string
	rootIdentity                    safeopen.Identity
	manifest, messages              fileAuthorization
}

type Adapter struct {
	roots     []string
	configErr error
	scanMu    sync.Mutex
	mu        sync.RWMutex
	known     map[string]authorization
	instance  uint64
}

var instanceCounter atomic.Uint64

func New(roots ...string) *Adapter {
	if len(roots) == 0 {
		if home, err := os.UserHomeDir(); err == nil {
			roots = []string{filepath.Join(home, ".cline", "data", "sessions")}
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{roots: clean, configErr: err, known: map[string]authorization{}, instance: instanceCounter.Add(1)}
}

func (*Adapter) Product() string { return "cline" }

func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}
}

func validatedRoots(roots []string) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, errors.New("cline: root scan limit")
	}
	out := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, errors.New("cline: invalid root")
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
		return "", errors.New("cline: invalid root")
	}
	volume := filepath.VolumeName(root)
	prefix := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(root, prefix), string(filepath.Separator))
	current, start := prefix, 0
	if len(parts) > 0 && parts[0] != "" {
		first := filepath.Join(current, parts[0])
		resolved, err := filepath.EvalSymlinks(first)
		if err == nil {
			current, start = resolved, 1
		} else if !os.IsNotExist(err) {
			return "", errors.New("cline: invalid root")
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
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("cline: invalid root")
		}
		current = next
	}
	return filepath.Clean(current), nil
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
	seenRoots := map[safeopen.Identity]bool{}
	seenIDs := map[string]bool{}
	globalEntries := 0
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		validated, err := canonicalizeRoot(root)
		if err != nil || validated != root {
			return nil, errors.New("cline: root changed")
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("cline: root read failed")
		}
		if seenRoots[bound.Identity()] {
			bound.Close()
			continue
		}
		seenRoots[bound.Identity()] = true
		entries, err := bound.ReadDirLimit(".", maxDirectoryEntries)
		if err != nil {
			bound.Close()
			return nil, directoryError(err)
		}
		globalEntries += len(entries)
		if globalEntries > maxGlobalEntries {
			bound.Close()
			return nil, errors.New("cline: directory limit")
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				bound.Close()
				return nil, err
			}
			id := entry.Name()
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !safeID(id) || excludedDirectory(id) {
				continue
			}
			children, err := bound.ReadDirLimit(id, maxDirectoryEntries)
			if err != nil {
				if errors.Is(err, safeopen.ErrDirectoryLimit) {
					bound.Close()
					return nil, errors.New("cline: directory limit")
				}
				continue
			}
			globalEntries += len(children)
			if globalEntries > maxGlobalEntries {
				bound.Close()
				return nil, errors.New("cline: directory limit")
			}
			if !hasStrictPair(children, id) {
				continue
			}
			session, auth, _, ok := a.snapshot(ctx, bound, root, id)
			if err := ctx.Err(); err != nil {
				bound.Close()
				return nil, err
			}
			if !ok || seenIDs[session.ID] {
				continue
			}
			seenIDs[session.ID] = true
			out = append(out, session)
			next[session.OpaqueRef] = auth
		}
		bound.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.replaceKnown(next)
	return out, nil
}

func directoryError(err error) error {
	if errors.Is(err, safeopen.ErrDirectoryLimit) {
		return errors.New("cline: directory limit")
	}
	return errors.New("cline: directory read failed")
}

func excludedDirectory(name string) bool {
	switch strings.ToLower(name) {
	case "db", "settings", "providers", "connectors", "logs", "cache", "locks", "cron", "teams", "compaction", "subagents", "subagent":
		return true
	default:
		return false
	}
}

func safeID(id string) bool {
	if id == "" || len(id) > maxIDBytes || id != filepath.Base(id) || id == "." || id == ".." || strings.ContainsAny(id, "/\\\\\x00\r\n") {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func hasStrictPair(entries []os.DirEntry, id string) bool {
	manifest, messages := false, false
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		switch entry.Name() {
		case id + ".json":
			manifest = true
		case id + ".messages.json":
			messages = true
		}
	}
	return manifest && messages
}

type manifestV1 struct {
	Version       *int    `json:"version"`
	SessionID     *string `json:"session_id"`
	Source        *string `json:"source"`
	PID           *int    `json:"pid"`
	StartedAt     *string `json:"started_at"`
	EndedAt       *string `json:"ended_at"`
	Status        *string `json:"status"`
	Interactive   *bool   `json:"interactive"`
	Provider      *string `json:"provider"`
	Model         *string `json:"model"`
	CWD           *string `json:"cwd"`
	WorkspaceRoot *string `json:"workspace_root"`
	EnableTools   *bool   `json:"enable_tools"`
	EnableSpawn   *bool   `json:"enable_spawn"`
	EnableTeams   *bool   `json:"enable_teams"`
}

type messagesV1 struct {
	Version   *int            `json:"version"`
	UpdatedAt *string         `json:"updated_at"`
	Agent     *string         `json:"agent"`
	SessionID *string         `json:"sessionId"`
	Messages  []messageRecord `json:"messages"`
}

type messageRecord struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type event struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Input   any    `json:"input,omitempty"`
	Result  any    `json:"result,omitempty"`
	IsError *bool  `json:"is_error,omitempty"`
}

type parsed struct {
	manifest  manifestV1
	events    []event
	start     time.Time
	end       time.Time
	messages  int
	malformed int
}

func (a *Adapter) snapshot(ctx context.Context, bound *safeopen.BoundRoot, root, id string) (source.Session, authorization, []byte, bool) {
	manifestRel := filepath.Join(id, id+".json")
	messagesRel := filepath.Join(id, id+".messages.json")
	manifestData, manifestInfo, manifestPath, ok := readBoundFile(ctx, bound, manifestRel)
	if !ok {
		return source.Session{}, authorization{}, nil, false
	}
	messagesData, messagesInfo, messagesPath, ok := readBoundFile(ctx, bound, messagesRel)
	if !ok {
		return source.Session{}, authorization{}, nil, false
	}
	parsed, ok := parseComposite(ctx, id, manifestData, messagesData)
	if !ok || len(parsed.events) == 0 {
		return source.Session{}, authorization{}, nil, false
	}
	rootIdentity := bound.Identity()
	rootSeed := identityString(rootIdentity)
	session := source.Session{
		ID: "cline:v1:" + digestPrefix(rootSeed+"\x00"+id, 24), Product: a.Product(), FormatVersion: "v1", AdapterVersion: "1",
		Capabilities: dynamicCapabilities(parsed.events), Scope: clineScope(parsed.manifest, rootSeed), StartedAt: parsed.start, EndedAt: parsed.end,
		MessageCount: parsed.messages, MalformedCount: parsed.malformed,
	}
	manifestDigest := digest(manifestData)
	messagesDigest := digest(messagesData)
	session.SnapshotID = digestPrefix(manifestDigest+"\x00"+messagesDigest, 64)
	session.OpaqueRef = a.opaqueRef(root, id)
	auth := authorization{
		id: session.ID, metadata: sessionMetadata(session), root: root, relativeDir: id, rootIdentity: rootIdentity,
		manifest: fileAuthorization{digest: manifestDigest, pathIdentity: pathIdentity{directories: manifestPath}, fileInfo: manifestInfo},
		messages: fileAuthorization{digest: messagesDigest, pathIdentity: pathIdentity{directories: messagesPath}, fileInfo: messagesInfo},
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

func readBoundFile(ctx context.Context, bound *safeopen.BoundRoot, relative string) ([]byte, os.FileInfo, []safeopen.Identity, bool) {
	file, identities, err := bound.OpenWithPathIdentity(relative, maxFileBytes)
	if err != nil {
		return nil, nil, nil, false
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	info, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || len(data) > maxFileBytes || ctx.Err() != nil || !jsonDepthOK(ctx, data, maxJSONDepth) {
		return nil, nil, nil, false
	}
	return data, info, identities, true
}

func parseComposite(ctx context.Context, id string, manifestData, messagesData []byte) (parsed, bool) {
	var manifest manifestV1
	if json.Unmarshal(manifestData, &manifest) != nil || !validManifest(id, manifest) {
		return parsed{}, false
	}
	var wrapper messagesV1
	if json.Unmarshal(messagesData, &wrapper) != nil || !validWrapper(id, wrapper) || len(wrapper.Messages) > maxMessages {
		return parsed{}, false
	}
	start, _ := time.Parse(time.RFC3339Nano, *manifest.StartedAt)
	end, _ := time.Parse(time.RFC3339Nano, *wrapper.UpdatedAt)
	if manifest.EndedAt != nil {
		end, _ = time.Parse(time.RFC3339Nano, *manifest.EndedAt)
	}
	result := parsed{manifest: manifest, start: start, end: end}
	type stagedRecord struct {
		events      []event
		transitions []toolTransition
	}
	var staged []stagedRecord
	stagedEvents := 0
	for _, record := range wrapper.Messages {
		if ctx.Err() != nil {
			return parsed{}, false
		}
		events, transitions, ok := parseMessage(ctx, record)
		if !ok {
			result.malformed++
			continue
		}
		if len(events) == 0 {
			continue
		}
		stagedEvents += len(events)
		if stagedEvents > maxEvents {
			return parsed{}, false
		}
		staged = append(staged, stagedRecord{events: events, transitions: transitions})
	}
	if ctx.Err() != nil {
		return parsed{}, false
	}
	type pairReference struct {
		stage int
		name  string
	}
	type pairSet struct {
		uses, results []pairReference
	}
	pairs := map[string]*pairSet{}
	for stageIndex, record := range staged {
		for _, transition := range record.transitions {
			pair := pairs[transition.id]
			if pair == nil {
				pair = &pairSet{}
				pairs[transition.id] = pair
			}
			reference := pairReference{stage: stageIndex, name: transition.name}
			if transition.use {
				pair.uses = append(pair.uses, reference)
			} else {
				pair.results = append(pair.results, reference)
			}
		}
	}
	invalid := make([]bool, len(staged))
	adjacent := make([][]int, len(staged))
	for _, pair := range pairs {
		exact := len(pair.uses) == 1 && len(pair.results) == 1 && pair.uses[0].name == pair.results[0].name && pair.uses[0].stage < pair.results[0].stage
		if !exact {
			for _, reference := range append(append([]pairReference(nil), pair.uses...), pair.results...) {
				invalid[reference.stage] = true
			}
			continue
		}
		useStage, resultStage := pair.uses[0].stage, pair.results[0].stage
		adjacent[useStage] = append(adjacent[useStage], resultStage)
		adjacent[resultStage] = append(adjacent[resultStage], useStage)
	}
	queue := make([]int, 0, len(staged))
	for index, bad := range invalid {
		if bad {
			queue = append(queue, index)
		}
	}
	for len(queue) > 0 {
		index := queue[0]
		queue = queue[1:]
		for _, neighbor := range adjacent[index] {
			if !invalid[neighbor] {
				invalid[neighbor] = true
				queue = append(queue, neighbor)
			}
		}
	}
	for index, record := range staged {
		if invalid[index] {
			result.malformed++
			continue
		}
		result.events = append(result.events, record.events...)
	}
	for _, value := range result.events {
		if value.Type == "message" {
			result.messages++
		}
	}
	return result, len(result.events) > 0
}

func validManifest(id string, value manifestV1) bool {
	if value.Version == nil || *value.Version != 1 || value.SessionID == nil || *value.SessionID != id || value.Source == nil || *value.Source != "cline" || value.PID == nil || value.StartedAt == nil || value.Status == nil || value.Interactive == nil || value.Provider == nil || strings.TrimSpace(*value.Provider) == "" || value.Model == nil || strings.TrimSpace(*value.Model) == "" || value.CWD == nil || strings.TrimSpace(*value.CWD) == "" || value.WorkspaceRoot == nil || strings.TrimSpace(*value.WorkspaceRoot) == "" || value.EnableTools == nil || value.EnableSpawn == nil || value.EnableTeams == nil {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, *value.StartedAt); err != nil {
		return false
	}
	if value.EndedAt != nil {
		if _, err := time.Parse(time.RFC3339Nano, *value.EndedAt); err != nil {
			return false
		}
	}
	switch *value.Status {
	case "idle", "running", "pending", "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func validWrapper(id string, value messagesV1) bool {
	if value.Version == nil || *value.Version != 1 || value.UpdatedAt == nil || value.Agent == nil || *value.Agent != "lead" || value.SessionID == nil || *value.SessionID != id || value.Messages == nil {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, *value.UpdatedAt)
	return err == nil
}

type toolTransition struct {
	use      bool
	id, name string
}

func parseMessage(ctx context.Context, record messageRecord) ([]event, []toolTransition, bool) {
	if record.Role != "user" && record.Role != "assistant" {
		return nil, nil, false
	}
	var text string
	if json.Unmarshal(record.Content, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return nil, nil, false
		}
		return []event{{Type: "message", Role: record.Role, Content: text}}, nil, true
	}
	var parts []map[string]any
	if json.Unmarshal(record.Content, &parts) != nil || len(parts) == 0 {
		return nil, nil, false
	}
	var out []event
	var transitions []toolTransition
	for _, part := range parts {
		if ctx.Err() != nil {
			return nil, nil, false
		}
		typeName, _ := part["type"].(string)
		switch typeName {
		case "text":
			value, ok := part["text"].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, nil, false
			}
			out = append(out, event{Type: "message", Role: record.Role, Content: value})
		case "thinking":
			if record.Role != "assistant" {
				return nil, nil, false
			}
			value, ok := part["thinking"].(string)
			if !ok || strings.TrimSpace(value) == "" {
				return nil, nil, false
			}
			out = append(out, event{Type: "reasoning", Content: value})
		case "redacted_thinking", "file", "image":
			continue
		case "tool_use":
			if record.Role != "assistant" {
				return nil, nil, false
			}
			id, hasID := part["id"].(string)
			callID, hasCallID := part["call_id"].(string)
			if hasID && hasCallID && id != callID {
				return nil, nil, false
			}
			if !hasID {
				id = callID
			}
			name, _ := part["name"].(string)
			input, hasInput := part["input"]
			if !safeToken(id, maxIDBytes) || !safeToken(name, 256) || !hasInput {
				return nil, nil, false
			}
			if input == nil {
				input = map[string]any{}
			}
			cleanInput, sanitized := sanitizePayload(ctx, input)
			if !sanitized {
				return nil, nil, false
			}
			input = cleanInput
			out = append(out, event{Type: "tool_use", CallID: id, Name: name, Input: input})
			transitions = append(transitions, toolTransition{use: true, id: id, name: name})
		case "tool_result":
			if record.Role != "user" {
				return nil, nil, false
			}
			id, _ := part["tool_use_id"].(string)
			name, _ := part["name"].(string)
			isError, ok := part["is_error"].(bool)
			result, exists := part["content"]
			if !safeToken(id, maxIDBytes) || !safeToken(name, 256) || !ok || !exists {
				return nil, nil, false
			}
			cleanResult, sanitized := sanitizePayload(ctx, result)
			if !sanitized {
				return nil, nil, false
			}
			result = cleanResult
			out = append(out, event{Type: "tool_result", CallID: id, Name: name, Result: result, IsError: &isError})
			transitions = append(transitions, toolTransition{id: id, name: name})
		default:
			return nil, nil, false
		}
	}
	return out, transitions, true
}

func clineScope(manifest manifestV1, fallback string) source.ScopeRef {
	for _, candidate := range []*string{manifest.WorkspaceRoot, manifest.CWD} {
		if candidate == nil || !validPayloadPath(*candidate) {
			continue
		}
		label := safeLabel(filepath.Base(*candidate))
		if label != "" {
			return source.ScopeRef{Type: source.ScopeProject, Root: "cline:project:" + digestPrefix(*candidate, 24), Label: label}
		}
	}
	return source.ScopeRef{Type: source.ScopeSessionCollection, Root: "cline:collection:" + digestPrefix(fallback, 24), Label: "Cline sessions"}
}

func validPayloadPath(value string) bool {
	return value != "" && len(value) <= 4096 && filepath.IsAbs(value) && filepath.Clean(value) == value
}

func safeLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 || filepath.IsAbs(value) || strings.ContainsAny(value, `/\\`) || value == "." || value == ".." {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func dynamicCapabilities(events []event) []source.Capability {
	var capabilities []source.Capability
	messages, tools, reasoning := false, false, false
	for _, value := range events {
		messages = messages || value.Type == "message"
		tools = tools || value.Type == "tool_use" || value.Type == "tool_result"
		reasoning = reasoning || value.Type == "reasoning"
	}
	if messages {
		capabilities = append(capabilities, source.CapabilityMessages)
	}
	if tools {
		capabilities = append(capabilities, source.CapabilityTools)
	}
	if reasoning {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	return capabilities
}

func safeToken(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n")
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
		for index := range typed {
			clean, ok := sanitizePayloadGuarded(guard, typed[index])
			if !ok {
				return nil, false
			}
			out[index] = clean
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
	if rootedPath(value) {
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
	return err != nil || rootedPath(decoded) || windowsDrivePath(strings.TrimPrefix(decoded, "/"))
}

func rootedPath(value string) bool {
	return strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) || windowsDrivePath(value)
}

func windowsDrivePath(value string) bool {
	return len(value) > 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func identityString(value safeopen.Identity) string {
	return strconv.FormatUint(value.Volume, 16) + ":" + strconv.FormatUint(value.File, 16)
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func digestPrefix(value string, length int) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:length]
}
func sessionMetadata(session source.Session) string {
	data, _ := json.Marshal(session)
	return digest(data)
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

func (a *Adapter) opaqueRef(root, id string) string {
	return "cline:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+id, 24)
}

func (a *Adapter) replaceKnown(next map[string]authorization) {
	if next == nil {
		next = map[string]authorization{}
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
}

func (a *Adapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("cline: invalid session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("cline: unauthorized session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("cline: source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("cline: source changed")
	}
	fresh, freshAuth, output, valid := a.snapshot(ctx, bound, auth.root, auth.relativeDir)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !valid || fresh.ID != session.ID || freshAuth.metadata != auth.metadata || freshAuth.manifest.digest != auth.manifest.digest || freshAuth.messages.digest != auth.messages.digest || !os.SameFile(auth.manifest.fileInfo, freshAuth.manifest.fileInfo) || !os.SameFile(auth.messages.fileInfo, freshAuth.messages.fileInfo) || !samePathIdentity(auth.manifest.pathIdentity, freshAuth.manifest.pathIdentity) || !samePathIdentity(auth.messages.pathIdentity, freshAuth.messages.pathIdentity) {
		return nil, errors.New("cline: source changed")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
