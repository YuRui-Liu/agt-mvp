package kimicode

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
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
	maxStateBytes       = 1 << 20
	maxSessionBytes     = 4 << 20
	maxIndexBytes       = 4 << 20
	maxIndexRecords     = 8192
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
	id, metadata, root, indexDigest, stateDigest, wireDigest string
	indexRelative, stateRelative, wireRelative, sessionID    string
	indexWorkDir                                             string
	rootIdentity                                             safeopen.Identity
	indexPath, statePath, wirePath                           pathIdentity
	indexFile, stateFile, wireFile                           os.FileInfo
}

type Adapter struct {
	roots     []string
	legacy    bool
	configErr error
	scanMu    sync.Mutex
	mu        sync.RWMutex
	known     map[string]authorization
	afterBind func(string)
	instance  uint64
}

var instanceCounter atomic.Uint64

func New(roots ...string) *Adapter { return newAdapter(false, roots...) }

// NewLegacy explicitly enables the single historical state.custom.cwd field.
// Normal discovery never consumes arbitrary custom state.
func NewLegacy(roots ...string) *Adapter { return newAdapter(true, roots...) }

func newAdapter(legacy bool, roots ...string) *Adapter {
	if len(roots) == 0 {
		if home, err := os.UserHomeDir(); err == nil {
			roots = []string{filepath.Join(home, ".kimi-code")}
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{
		roots: clean, legacy: legacy, configErr: err,
		known: map[string]authorization{}, instance: instanceCounter.Add(1),
	}
}

func (*Adapter) Product() string { return "kimi-code" }

func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}
}

func validatedRoots(roots []string) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, errors.New("kimi-code: root scan limit")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, errors.New("kimi-code: invalid root")
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
		return "", errors.New("kimi-code: invalid root")
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
			return "", errors.New("kimi-code: invalid root")
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
			return "", errors.New("kimi-code: invalid root")
		}
		current = next
	}
	return filepath.Clean(current), nil
}

type indexEntry struct {
	sessionID, sessionRelative, indexWorkDir string
}

type fileSnapshot struct {
	data   []byte
	digest string
	path   pathIdentity
	info   os.FileInfo
}

type candidate struct {
	root         string
	rootIdentity safeopen.Identity
	bound        *safeopen.BoundRoot
	index        fileSnapshot
	entry        indexEntry
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
			return nil, errors.New("kimi-code: root changed")
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("kimi-code: root read failed")
		}
		if seenRoots[bound.Identity()] {
			bound.Close()
			continue
		}
		seenRoots[bound.Identity()] = true
		if a.afterBind != nil {
			a.afterBind(root)
		}
		index, ok := readBoundFile(ctx, bound, "session_index.jsonl", maxIndexBytes)
		if !ok {
			bound.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		entries, ok := parseIndex(ctx, root, index.data)
		if !ok {
			bound.Close()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			continue
		}
		dirsSeen := map[string]bool{}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				bound.Close()
				return nil, err
			}
			if err := validateCandidateDirectories(bound, entry, dirsSeen, &globalEntries); err != nil {
				if errors.Is(err, safeopen.ErrDirectoryLimit) || errors.Is(err, errGlobalDirectoryLimit) {
					bound.Close()
					return nil, errors.New("kimi-code: directory limit")
				}
				continue
			}
			item := candidate{root: root, rootIdentity: bound.Identity(), bound: bound, index: index, entry: entry}
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
		bound.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	a.replaceKnown(next)
	return out, nil
}

var errGlobalDirectoryLimit = errors.New("kimi-code: global directory limit")

func validateCandidateDirectories(bound *safeopen.BoundRoot, entry indexEntry, seen map[string]bool, global *int) error {
	parts := strings.Split(entry.sessionRelative, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != "sessions" {
		return errors.New("kimi-code: invalid session location")
	}
	dirs := []string{
		"sessions",
		filepath.Join("sessions", parts[1]),
		entry.sessionRelative,
		filepath.Join(entry.sessionRelative, "agents"),
		filepath.Join(entry.sessionRelative, "agents", "main"),
	}
	for _, relative := range dirs {
		if seen[relative] {
			continue
		}
		entries, err := bound.ReadDirLimit(relative, maxDirectoryEntries)
		if err != nil {
			return err
		}
		*global += len(entries)
		if *global > maxGlobalEntries {
			return errGlobalDirectoryLimit
		}
		seen[relative] = true
	}
	return nil
}

func (a *Adapter) replaceKnown(next map[string]authorization) {
	if next == nil {
		next = map[string]authorization{}
	}
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
}

func parseIndex(ctx context.Context, root string, data []byte) ([]indexEntry, bool) {
	live := map[string]indexEntry{}
	count := 0
	for _, rawLine := range bytes.Split(data, []byte{'\n'}) {
		if ctx.Err() != nil {
			return nil, false
		}
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 {
			continue
		}
		if len(line) > maxLineBytes {
			return nil, false
		}
		count++
		if count > maxIndexRecords {
			return nil, false
		}
		entry, deleted, ok := parseIndexLine(ctx, root, line)
		if !ok {
			continue
		}
		if deleted {
			delete(live, entry.sessionID)
			continue
		}
		live[entry.sessionID] = entry
	}
	if ctx.Err() != nil {
		return nil, false
	}
	out := make([]indexEntry, 0, len(live))
	for _, entry := range live {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].sessionRelative < out[j].sessionRelative })
	return out, true
}

func parseIndexLine(ctx context.Context, root string, line []byte) (indexEntry, bool, bool) {
	var record map[string]any
	if !jsonDepthOK(ctx, line, maxJSONDepth) || json.Unmarshal(line, &record) != nil {
		return indexEntry{}, false, false
	}
	id, _ := record["sessionId"].(string)
	if !safeBasename(id) {
		return indexEntry{}, false, false
	}
	if deleted, _ := record["deleted"].(bool); deleted {
		return indexEntry{sessionID: id}, true, true
	}
	sessionDir, dirOK := record["sessionDir"].(string)
	workDir, workOK := record["workDir"].(string)
	if !dirOK || !workOK {
		return indexEntry{}, false, false
	}
	entry, ok := validatedIndexEntry(root, id, sessionDir, workDir)
	return entry, false, ok
}

func validatedIndexEntry(root, id, sessionDir, workDir string) (indexEntry, bool) {
	if sessionDir == "" || !filepath.IsAbs(sessionDir) || filepath.Clean(sessionDir) != sessionDir || filepath.Base(sessionDir) != id {
		return indexEntry{}, false
	}
	canonicalSessionDir, err := canonicalizeRoot(sessionDir)
	if err != nil || filepath.Base(canonicalSessionDir) != id {
		return indexEntry{}, false
	}
	relative, err := filepath.Rel(root, canonicalSessionDir)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return indexEntry{}, false
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) != 3 || parts[0] != "sessions" || !safeBasename(parts[1]) || parts[2] != id {
		return indexEntry{}, false
	}
	if _, ok := cleanWorkDir(workDir); !ok {
		return indexEntry{}, false
	}
	return indexEntry{sessionID: id, sessionRelative: relative, indexWorkDir: workDir}, true
}

func safeBasename(value string) bool {
	if value == "" || len(value) > maxIdentifierBytes || value == "." || value == ".." || filepath.Base(value) != value || strings.ContainsAny(value, "/\\:\x00\r\n") {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func readBoundFile(ctx context.Context, bound *safeopen.BoundRoot, relative string, maxBytes int64) (fileSnapshot, bool) {
	file, identities, err := bound.OpenWithPathIdentity(relative, maxBytes)
	if err != nil {
		return fileSnapshot{}, false
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxBytes+1))
	info, statErr := file.Stat()
	file.Close()
	if readErr != nil || statErr != nil || len(data) > int(maxBytes) || ctx.Err() != nil {
		return fileSnapshot{}, false
	}
	sum := sha256.Sum256(data)
	return fileSnapshot{data: data, digest: hex.EncodeToString(sum[:]), path: pathIdentity{directories: identities}, info: info}, true
}

type stateMetadata struct {
	createdAt, updatedAt time.Time
	workDir              string
}

func parseState(ctx context.Context, data []byte, legacy bool) (stateMetadata, bool) {
	if !jsonDepthOK(ctx, data, maxJSONDepth) {
		return stateMetadata{}, false
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || ctx.Err() != nil {
		return stateMetadata{}, false
	}
	createdRaw, createdOK := object["createdAt"].(string)
	updatedRaw, updatedOK := object["updatedAt"].(string)
	if !createdOK || !updatedOK {
		return stateMetadata{}, false
	}
	created, err := time.Parse(time.RFC3339Nano, createdRaw)
	if err != nil {
		return stateMetadata{}, false
	}
	updated, err := time.Parse(time.RFC3339Nano, updatedRaw)
	if err != nil || updated.Before(created) {
		return stateMetadata{}, false
	}
	workDir, _ := object["workDir"].(string)
	if workDir == "" && legacy {
		if custom, ok := object["custom"].(map[string]any); ok {
			workDir, _ = custom["cwd"].(string)
		}
	}
	cleaned, ok := cleanWorkDir(workDir)
	if !ok {
		return stateMetadata{}, false
	}
	return stateMetadata{createdAt: created.UTC(), updatedAt: updated.UTC(), workDir: cleaned}, true
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

type parsedWire struct {
	events              []event
	messages, malformed int
	usage               map[string]int64
	tools, reasoning    bool
}

type stepState struct {
	message bool
	closed  bool
}

type toolState struct {
	stepUUID, eventUUID, name string
}

func parseWire(ctx context.Context, data []byte) (parsedWire, bool) {
	var parsed parsedWire
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), maxLineBytes+1)
	first := true
	records := 0
	steps := map[string]*stepState{}
	pending := map[string]toolState{}
	completedTools := map[string]bool{}
	eventUUIDs := map[string]bool{}
	var primaryUsage []map[string]int64
	var fallbackUsage []map[string]int64
	for scanner.Scan() {
		if ctx.Err() != nil {
			return parsedWire{}, false
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		records++
		if records > maxSessionRecords {
			return parsedWire{}, false
		}
		var record map[string]any
		if !jsonDepthOK(ctx, line, maxJSONDepth) || json.Unmarshal(line, &record) != nil {
			if first {
				return parsedWire{}, false
			}
			parsed.malformed++
			continue
		}
		typeName, _ := record["type"].(string)
		if first {
			first = false
			version, _ := record["protocol_version"].(string)
			if typeName != "metadata" || version != "1.4" {
				return parsedWire{}, false
			}
			if _, ok := requiredMillisTimestamp(record["created_at"]); !ok {
				parsed.malformed++
			}
			continue
		}
		switch typeName {
		case "context.append_message":
			timestamp, ok := optionalMillisTimestamp(record, "time")
			if !ok {
				parsed.malformed++
				continue
			}
			message, ok := record["message"].(map[string]any)
			if !ok {
				parsed.malformed++
				continue
			}
			role, _ := message["role"].(string)
			if role != "user" {
				continue
			}
			text, known, valid := textParts(message["content"])
			if !known {
				continue
			}
			if !valid {
				parsed.malformed++
				continue
			}
			clean, ok := sanitizePayload(ctx, text)
			if !ok {
				return parsedWire{}, false
			}
			parsed.events = append(parsed.events, event{Type: "message", Role: "user", Content: clean, Timestamp: timestamp})
			parsed.messages++
		case "context.append_loop_event":
			timestamp, ok := optionalMillisTimestamp(record, "time")
			if !ok {
				parsed.malformed++
				continue
			}
			rawEvent, ok := record["event"].(map[string]any)
			if !ok {
				parsed.malformed++
				continue
			}
			known, valid, added, fallback := applyLoopEvent(ctx, rawEvent, timestamp, steps, pending, completedTools, eventUUIDs)
			if !known {
				continue
			}
			if !valid {
				parsed.malformed++
				continue
			}
			for _, value := range added {
				if len(parsed.events) >= maxSessionEvents {
					return parsedWire{}, false
				}
				parsed.events = append(parsed.events, value)
				if value.Type == "tool_use" || value.Type == "tool_result" {
					parsed.tools = true
				}
				if isThinkingEvent(value) {
					parsed.reasoning = true
				}
			}
			if len(added) > 0 {
				stepUUID, _ := rawEvent["stepUuid"].(string)
				if stepUUID == "" {
					stepUUID, _ = rawEvent["uuid"].(string)
				}
				if step := steps[stepUUID]; step != nil && !step.message {
					step.message = true
					parsed.messages++
				}
			}
			if fallback != nil {
				fallbackUsage = append(fallbackUsage, fallback)
			}
		case "usage.record":
			if _, ok := optionalMillisTimestamp(record, "time"); !ok {
				parsed.malformed++
				continue
			}
			usage, ok := parseUsage(record["usage"])
			if !ok {
				parsed.malformed++
				continue
			}
			primaryUsage = append(primaryUsage, usage)
		default:
			// Unknown records, including credentials/config/turn/task metadata, are ignored.
		}
	}
	if scanner.Err() != nil || first || ctx.Err() != nil {
		return parsedWire{}, false
	}
	chosen := fallbackUsage
	if len(primaryUsage) > 0 {
		chosen = primaryUsage
	}
	for _, usage := range chosen {
		if parsed.usage == nil {
			parsed.usage = map[string]int64{}
		}
		for key, value := range usage {
			if value > math.MaxInt64-parsed.usage[key] {
				return parsedWire{}, false
			}
			parsed.usage[key] += value
		}
	}
	return parsed, len(parsed.events) > 0
}

func applyLoopEvent(ctx context.Context, raw map[string]any, timestamp string, steps map[string]*stepState, pending map[string]toolState, completed map[string]bool, eventUUIDs map[string]bool) (bool, bool, []event, map[string]int64) {
	typeName, _ := raw["type"].(string)
	switch typeName {
	case "step.begin":
		uuid, _ := raw["uuid"].(string)
		if !safeToken(uuid, maxIdentifierBytes) || steps[uuid] != nil || eventUUIDs[uuid] {
			return true, false, nil, nil
		}
		eventUUIDs[uuid] = true
		steps[uuid] = &stepState{}
		return true, true, nil, nil
	case "content.part":
		uuid, _ := raw["uuid"].(string)
		stepUUID, _ := raw["stepUuid"].(string)
		step := steps[stepUUID]
		if !safeToken(uuid, maxIdentifierBytes) || eventUUIDs[uuid] || step == nil || step.closed {
			return true, false, nil, nil
		}
		part, ok := raw["part"].(map[string]any)
		if !ok {
			return true, false, nil, nil
		}
		partType, _ := part["type"].(string)
		var output event
		switch partType {
		case "text":
			text, ok := part["text"].(string)
			if !ok || strings.TrimSpace(text) == "" {
				return true, false, nil, nil
			}
			clean, ok := sanitizePayload(ctx, text)
			if !ok {
				return true, false, nil, nil
			}
			output = event{Type: "message", Role: "assistant", Content: clean, Timestamp: timestamp}
		case "think":
			text, ok := part["think"].(string)
			if !ok || strings.TrimSpace(text) == "" {
				return true, false, nil, nil
			}
			clean, ok := sanitizePayload(ctx, text)
			if !ok {
				return true, false, nil, nil
			}
			output = event{Type: "message", Role: "assistant", Content: []any{map[string]any{"type": "thinking", "thinking": clean}}, Timestamp: timestamp}
		default:
			return false, true, nil, nil
		}
		eventUUIDs[uuid] = true
		return true, true, []event{output}, nil
	case "tool.call":
		uuid, _ := raw["uuid"].(string)
		stepUUID, _ := raw["stepUuid"].(string)
		callID, _ := raw["toolCallId"].(string)
		name, _ := raw["name"].(string)
		step := steps[stepUUID]
		if !safeToken(uuid, maxIdentifierBytes) || eventUUIDs[uuid] || step == nil || step.closed || !safeToken(callID, maxIdentifierBytes) || !safeToken(name, 256) || pending[callID].eventUUID != "" || completed[callID] {
			return true, false, nil, nil
		}
		input := raw["args"]
		if input == nil {
			input = map[string]any{}
		}
		clean, ok := sanitizePayload(ctx, input)
		if !ok {
			return true, false, nil, nil
		}
		eventUUIDs[uuid] = true
		pending[callID] = toolState{stepUUID: stepUUID, eventUUID: uuid, name: name}
		return true, true, []event{{Type: "tool_use", CallID: callID, Name: name, Input: clean, Timestamp: timestamp}}, nil
	case "tool.result":
		uuid, _ := raw["uuid"].(string)
		parentUUID, _ := raw["parentUuid"].(string)
		callID, _ := raw["toolCallId"].(string)
		tool, exists := pending[callID]
		step := steps[tool.stepUUID]
		result, resultOK := raw["result"].(map[string]any)
		output, outputOK := result["output"]
		if !safeToken(uuid, maxIdentifierBytes) || eventUUIDs[uuid] || !exists || completed[callID] || parentUUID != tool.eventUUID || step == nil || step.closed || !resultOK || !outputOK {
			return true, false, nil, nil
		}
		clean, ok := sanitizePayload(ctx, output)
		if !ok {
			return true, false, nil, nil
		}
		isError, _ := result["isError"].(bool)
		eventUUIDs[uuid] = true
		delete(pending, callID)
		completed[callID] = true
		return true, true, []event{{Type: "tool_result", CallID: callID, Result: clean, IsError: isError, Timestamp: timestamp}}, nil
	case "step.end":
		uuid, _ := raw["uuid"].(string)
		step := steps[uuid]
		if !safeToken(uuid, maxIdentifierBytes) || step == nil || step.closed {
			return true, false, nil, nil
		}
		for _, tool := range pending {
			if tool.stepUUID == uuid {
				return true, false, nil, nil
			}
		}
		var usage map[string]int64
		if rawUsage, exists := raw["usage"]; exists {
			var ok bool
			usage, ok = parseUsage(rawUsage)
			if !ok {
				return true, false, nil, nil
			}
		}
		step.closed = true
		return true, true, nil, usage
	default:
		return false, true, nil, nil
	}
}

func textParts(raw any) (string, bool, bool) {
	parts, ok := raw.([]any)
	if !ok {
		return "", true, false
	}
	var texts []string
	known := false
	for _, rawPart := range parts {
		part, ok := rawPart.(map[string]any)
		if !ok {
			return "", true, false
		}
		typeName, _ := part["type"].(string)
		if typeName != "text" {
			continue
		}
		known = true
		text, ok := part["text"].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", true, false
		}
		texts = append(texts, text)
	}
	if !known {
		return "", false, true
	}
	return strings.Join(texts, "\n"), true, true
}

func parseUsage(raw any) (map[string]int64, bool) {
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	fields := map[string][]string{
		"input_tokens":       {"inputOther", "input_other"},
		"output_tokens":      {"output", "output_tokens"},
		"cache_read_tokens":  {"inputCacheRead", "input_cache_read"},
		"cache_write_tokens": {"inputCacheCreation", "input_cache_creation"},
	}
	out := map[string]int64{}
	found := false
	for target, names := range fields {
		for _, name := range names {
			rawValue, exists := object[name]
			if !exists {
				continue
			}
			value, ok := nonNegativeInt(rawValue)
			if !ok {
				return nil, false
			}
			out[target] = value
			found = true
			break
		}
	}
	return out, found
}

func nonNegativeInt(raw any) (int64, bool) {
	value, ok := raw.(float64)
	return int64(value), ok && value >= 0 && value <= math.MaxInt64 && math.Trunc(value) == value
}

func optionalMillisTimestamp(record map[string]any, field string) (string, bool) {
	raw, exists := record[field]
	if !exists {
		return "", true
	}
	return requiredMillisTimestamp(raw)
}

func requiredMillisTimestamp(raw any) (string, bool) {
	value, ok := raw.(float64)
	if !ok || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value >= math.Exp2(63) || math.Trunc(value) != value {
		return "", false
	}
	millis := int64(value)
	if millis < 0 {
		return "", false
	}
	return time.UnixMilli(millis).UTC().Format(time.RFC3339Nano), true
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
	stateRelative := filepath.Join(item.entry.sessionRelative, "state.json")
	wireRelative := filepath.Join(item.entry.sessionRelative, "agents", "main", "wire.jsonl")
	stateFile, ok := readBoundFile(ctx, item.bound, stateRelative, maxStateBytes)
	if !ok {
		return source.Session{}, authorization{}, nil, false
	}
	wireFile, ok := readBoundFile(ctx, item.bound, wireRelative, maxSessionBytes)
	if !ok {
		return source.Session{}, authorization{}, nil, false
	}
	state, ok := parseState(ctx, stateFile.data, a.legacy)
	if !ok {
		return source.Session{}, authorization{}, nil, false
	}
	indexWorkDir, ok := cleanWorkDir(item.entry.indexWorkDir)
	if !ok || indexWorkDir != state.workDir {
		return source.Session{}, authorization{}, nil, false
	}
	parsed, ok := parseWire(ctx, wireFile.data)
	if !ok || ctx.Err() != nil {
		return source.Session{}, authorization{}, nil, false
	}
	rootSeed := item.root + "\x00" + item.entry.sessionRelative
	id := "kimi-code:" + digestPrefix(rootSeed, 32)
	scope := source.ScopeRef{
		Type:  source.ScopeProject,
		Root:  "kimi-code:project:" + digestPrefix(state.workDir, 24),
		Label: safeWorkDirLabel(state.workDir),
	}
	capabilities := []source.Capability{source.CapabilityMessages}
	if parsed.tools {
		capabilities = append(capabilities, source.CapabilityTools)
	}
	if parsed.reasoning {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	session := source.Session{
		ID: id, Product: a.Product(), FormatVersion: "wire-1.4", AdapterVersion: "1",
		Capabilities: capabilities, Scope: scope, StartedAt: state.createdAt, EndedAt: state.updatedAt,
		MessageCount: parsed.messages, MalformedCount: parsed.malformed, Usage: parsed.usage,
		OpaqueRef: a.opaqueRef(item.root, item.entry.sessionRelative),
	}
	composite := sha256.New()
	for _, value := range []string{item.index.digest, stateFile.digest, wireFile.digest, item.entry.sessionID, item.entry.sessionRelative, item.entry.indexWorkDir} {
		_, _ = composite.Write([]byte(strconv.Itoa(len(value))))
		_, _ = composite.Write([]byte{':'})
		_, _ = composite.Write([]byte(value))
	}
	session.SnapshotID = hex.EncodeToString(composite.Sum(nil))
	auth := authorization{
		id: session.ID, metadata: sessionMetadata(session), root: item.root,
		indexDigest: item.index.digest, stateDigest: stateFile.digest, wireDigest: wireFile.digest,
		indexRelative: "session_index.jsonl", stateRelative: stateRelative, wireRelative: wireRelative,
		sessionID: item.entry.sessionID, indexWorkDir: item.entry.indexWorkDir,
		rootIdentity: item.rootIdentity, indexPath: item.index.path, statePath: stateFile.path, wirePath: wireFile.path,
		indexFile: item.index.info, stateFile: stateFile.info, wireFile: wireFile.info,
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
	return "kimi-code:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+relative, 24)
}

func sessionMetadata(session source.Session) string {
	data, _ := json.Marshal(session)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func digestPrefix(value string, length int) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:length]
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
		return nil, errors.New("kimi-code: invalid session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("kimi-code: unauthorized session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("kimi-code: source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("kimi-code: source changed")
	}
	indexFile, ok := readBoundFile(ctx, bound, auth.indexRelative, maxIndexBytes)
	if !ok {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("kimi-code: source changed")
	}
	entries, ok := parseIndex(ctx, auth.root, indexFile.data)
	if !ok {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("kimi-code: source changed")
	}
	var live indexEntry
	for _, entry := range entries {
		if entry.sessionID == auth.sessionID {
			live = entry
			break
		}
	}
	if live.sessionID == "" || live.indexWorkDir != auth.indexWorkDir || filepath.Join(live.sessionRelative, "state.json") != auth.stateRelative || filepath.Join(live.sessionRelative, "agents", "main", "wire.jsonl") != auth.wireRelative {
		return nil, errors.New("kimi-code: source changed")
	}
	item := candidate{root: auth.root, rootIdentity: bound.Identity(), bound: bound, index: indexFile, entry: live}
	fresh, freshAuth, output, valid := a.snapshot(ctx, item)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !valid || fresh.ID != session.ID || fresh.SnapshotID != session.SnapshotID || freshAuth.metadata != auth.metadata ||
		freshAuth.indexDigest != auth.indexDigest || freshAuth.stateDigest != auth.stateDigest || freshAuth.wireDigest != auth.wireDigest ||
		!os.SameFile(auth.indexFile, freshAuth.indexFile) || !os.SameFile(auth.stateFile, freshAuth.stateFile) || !os.SameFile(auth.wireFile, freshAuth.wireFile) ||
		!samePathIdentity(auth.indexPath, freshAuth.indexPath) || !samePathIdentity(auth.statePath, freshAuth.statePath) || !samePathIdentity(auth.wirePath, freshAuth.wirePath) {
		return nil, errors.New("kimi-code: source changed")
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

func cleanWorkDir(value string) (string, bool) {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	if windowsDrivePath(value) {
		replaced := strings.ReplaceAll(value, `\`, "/")
		if path.Clean(replaced[2:]) != replaced[2:] {
			return "", false
		}
		return strings.ToUpper(replaced[:1]) + replaced[1:], true
	}
	if strings.HasPrefix(value, `\\`) || strings.HasPrefix(value, "//") {
		replaced := strings.ReplaceAll(value, `\`, "/")
		rest := strings.TrimPrefix(replaced, "//")
		if rest == "" || strings.Contains(rest, "//") || path.Clean("/"+rest) != "/"+rest || len(strings.Split(rest, "/")) < 2 {
			return "", false
		}
		return "//" + rest, true
	}
	if filepath.IsAbs(value) {
		clean := filepath.Clean(value)
		return clean, clean == value
	}
	return "", false
}

func safeWorkDirLabel(value string) string {
	normalized := strings.TrimRight(strings.ReplaceAll(value, `\`, "/"), "/")
	label := path.Base(normalized)
	if label == "" || label == "." || label == "/" || len(label) > 128 || strings.ContainsAny(label, "\x00\r\n") {
		return "Kimi Code project"
	}
	return label
}

func safeToken(value string, limit int) bool {
	return value != "" && len(value) <= limit && !strings.ContainsAny(value, "\x00\r\n") && !isAbsoluteString(value)
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
