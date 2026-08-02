package qoder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sharedclient"
)

const (
	maxCLILineBytes           = 1 << 20
	maxCLIFileBytes     int64 = 4 << 20
	maxCLIScanBytes     int64 = 64 << 20
	maxCLIRecords             = 4096
	maxDirectoryEntries       = 512
	maxGlobalEntries          = 8192
	maxRoots                  = 32
	maxJSONDepth              = 64
	maxDatabaseBytes    int64 = 32 << 20
	maxDatabaseSessions       = 2048
	maxDatabaseRows           = 32768
	maxDatabasePayload        = 16 << 20
	maxCanonicalBytes         = 20 << 20
	databaseRelative          = "cache/db/local.db"
)

var instanceCounter atomic.Uint64

type pathIdentity struct{ directories []safeopen.Identity }

type cliAuthorization struct {
	id, snapshot, metadata, root, relative string
	rootIdentity                           safeopen.Identity
	pathIdentity                           pathIdentity
	fileInfo                               os.FileInfo
}

type CLIAdapter struct {
	roots              []string
	configErr          error
	scanLimits         cliScanLimits
	afterCandidateStat func()
	instance           uint64
	scanMu             sync.Mutex
	mu                 sync.RWMutex
	known              map[string]cliAuthorization
}

type cliScanLimits struct{ maxTotalBytes int64 }

type cliByteBudget struct {
	maximum int64
	used    int64
}

var errCLIScanByteBudget = errors.New("qoder: CLI scan byte budget exceeded")

func (budget *cliByteBudget) consume(ctx context.Context, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if budget == nil || budget.maximum <= 0 || size < 0 || budget.used < 0 || budget.used > budget.maximum || size > budget.maximum-budget.used {
		return errCLIScanByteBudget
	}
	budget.used += size
	return nil
}

type cliBudgetReader struct {
	ctx    context.Context
	reader io.Reader
	budget *cliByteBudget
}

func (reader *cliBudgetReader) Read(buffer []byte) (int, error) {
	if reader == nil || reader.ctx == nil || reader.reader == nil || reader.budget == nil {
		return 0, errors.New("qoder: invalid CLI budget reader")
	}
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if reader.budget.maximum <= 0 || reader.budget.used < 0 || reader.budget.used > reader.budget.maximum {
		return 0, errCLIScanByteBudget
	}
	remaining := reader.budget.maximum - reader.budget.used
	readBuffer := buffer
	if remaining < int64(len(buffer)) {
		readBuffer = buffer[:int(remaining)+1]
	}
	n, readErr := reader.reader.Read(readBuffer)
	if n < 0 || n > len(readBuffer) {
		return 0, errors.New("qoder: invalid CLI reader result")
	}
	if int64(n) > remaining {
		reader.budget.used = reader.budget.maximum
		return 0, errCLIScanByteBudget
	}
	if err := reader.budget.consume(context.Background(), int64(n)); err != nil {
		return 0, err
	}
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return n, readErr
}

func NewCLI(roots ...string) *CLIAdapter {
	if len(roots) == 0 {
		roots = defaultCLIRoots()
	}
	clean, err := validateRoots(roots)
	return &CLIAdapter{roots: clean, configErr: err, scanLimits: cliScanLimits{maxTotalBytes: maxCLIScanBytes}, instance: instanceCounter.Add(1), known: map[string]cliAuthorization{}}
}

func (*CLIAdapter) Product() string { return "qoder-cli" }
func (*CLIAdapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages, source.CapabilityReasoning}
}

type cliRecord struct {
	UUID      *string         `json:"uuid"`
	CWD       *string         `json:"cwd"`
	SessionID *string         `json:"sessionId"`
	Type      *string         `json:"type"`
	Timestamp *string         `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
	Data      json.RawMessage `json:"data"`
}

type cliMessage struct {
	Content json.RawMessage `json:"content"`
}

type cliContent struct {
	Type     *string `json:"type"`
	Text     *string `json:"text"`
	Thinking *string `json:"thinking"`
}

type canonicalEvent struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	StableID  string `json:"-"`
	at        int64
	rank      int
}

type parsedCLI struct {
	sessionID, cwd string
	events         []canonicalEvent
	seenRecords    map[string][32]byte
	malformed      int
	hasReasoning   bool
	lastAt         time.Time
	started, ended time.Time
}

var errUnsupportedCLI = errors.New("qoder: unsupported execution format")
var errConflictingCLIEvent = errors.New("qoder: conflicting execution event")
var errMalformedCLI = errors.New("qoder: malformed execution candidate")
var errNoUsableIDEConversation = errors.New("qoder: no usable IDE conversation")

func (a *CLIAdapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.configErr != nil {
		return nil, a.configErr
	}
	var sessions []source.Session
	next := map[string]cliAuthorization{}
	seenIDs := map[string]cliAuthorization{}
	seenRoots := map[safeopen.Identity]bool{}
	entriesRead := 0
	budget := &cliByteBudget{maximum: a.scanLimits.maxTotalBytes}
	hadUnsupported := false
	hadCandidateFailure := false
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("qoder: CLI root read failed")
		}
		if seenRoots[bound.Identity()] {
			bound.Close()
			continue
		}
		seenRoots[bound.Identity()] = true
		projects, err := bound.ReadDirLimit("projects", maxDirectoryEntries)
		if os.IsNotExist(err) {
			bound.Close()
			continue
		}
		if err != nil {
			bound.Close()
			return nil, errors.New("qoder: CLI projects read failed")
		}
		entriesRead += len(projects)
		if entriesRead > maxGlobalEntries {
			bound.Close()
			return nil, errors.New("qoder: CLI directory budget exceeded")
		}
		sort.Slice(projects, func(i, j int) bool { return projects[i].Name() < projects[j].Name() })
		for _, project := range projects {
			if err := ctx.Err(); err != nil {
				bound.Close()
				return nil, err
			}
			if !validEntryName(project.Name()) || !project.Type().IsDir() {
				continue
			}
			projectRelative := filepath.Join("projects", project.Name())
			transcriptRelative := filepath.Join(projectRelative, "transcript")
			files, err := bound.ReadDirLimit(transcriptRelative, maxDirectoryEntries)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				bound.Close()
				return nil, errors.New("qoder: CLI project read failed")
			}
			entriesRead += len(files)
			if entriesRead > maxGlobalEntries {
				bound.Close()
				return nil, errors.New("qoder: CLI directory budget exceeded")
			}
			sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
			for _, entry := range files {
				if err := ctx.Err(); err != nil {
					bound.Close()
					return nil, err
				}
				const suffix = ".jsonl"
				if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), suffix) || !validEntryName(entry.Name()) {
					continue
				}
				taskID := strings.TrimSuffix(entry.Name(), suffix)
				if !validIdentifier(taskID) {
					continue
				}
				relative := filepath.Join(transcriptRelative, entry.Name())
				session, auth, _, err := a.snapshotCLI(ctx, bound, root, relative, taskID, budget)
				if errors.Is(err, errCLIScanByteBudget) {
					bound.Close()
					return nil, errCLIScanByteBudget
				}
				if errors.Is(err, errUnsupportedCLI) {
					hadUnsupported = true
					continue
				}
				if err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						bound.Close()
						return nil, ctxErr
					}
					hadCandidateFailure = true
					continue
				}
				if previous, duplicate := seenIDs[session.ID]; duplicate {
					if previous.snapshot != auth.snapshot || previous.root != auth.root || previous.relative != auth.relative {
						hadCandidateFailure = true
					}
					continue
				}
				seenIDs[session.ID] = auth
				sessions = append(sessions, session)
				next[session.OpaqueRef] = auth
			}
		}
		bound.Close()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		if hadCandidateFailure {
			return nil, errors.New("qoder: no readable CLI candidates")
		}
		if hadUnsupported {
			return nil, source.NewDiscoveryError(source.SourceFormatUnsupported, errUnsupportedCLI)
		}
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
	return sessions, nil
}

func (a *CLIAdapter) snapshotCLI(ctx context.Context, bound *safeopen.BoundRoot, root, relative, taskID string, budget *cliByteBudget) (source.Session, cliAuthorization, []byte, error) {
	if budget == nil || budget.maximum <= 0 {
		return source.Session{}, cliAuthorization{}, nil, errCLIScanByteBudget
	}
	file, identities, err := bound.OpenWithPathIdentity(relative, maxCLIFileBytes)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return source.Session{}, cliAuthorization{}, nil, ctxErr
		}
		if os.IsNotExist(err) {
			return source.Session{}, cliAuthorization{}, nil, errors.New("qoder: CLI source changed")
		}
		return source.Session{}, cliAuthorization{}, nil, errors.New("qoder: CLI session read failed")
	}
	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return source.Session{}, cliAuthorization{}, nil, errors.New("qoder: CLI session read failed")
	}
	if a.afterCandidateStat != nil {
		a.afterCandidateStat()
	}
	hash := sha256.New()
	parsed := parsedCLI{}
	accounted := &cliBudgetReader{ctx: ctx, reader: file, budget: budget}
	err = sharedclient.WalkJSONL(ctx, io.TeeReader(accounted, hash), sharedclient.Limits{MaxTotalBytes: maxCLIFileBytes, MaxLineBytes: maxCLILineBytes, MaxRecords: maxCLIRecords}, func(line sharedclient.JSONLLine) error {
		return parseCLILine(ctx, line.Bytes, taskID, &parsed)
	})
	finalInfo, finalStatErr := file.Stat()
	file.Close()
	if err != nil {
		if errors.Is(err, errCLIScanByteBudget) {
			return source.Session{}, cliAuthorization{}, nil, errCLIScanByteBudget
		}
		if errors.Is(err, sharedclient.ErrBudgetExceeded) || errors.Is(err, safeopen.ErrFileSizeLimit) {
			return source.Session{}, cliAuthorization{}, nil, errors.New("qoder: CLI session budget exceeded")
		}
		if errors.Is(err, errUnsupportedCLI) {
			return source.Session{}, cliAuthorization{}, nil, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return source.Session{}, cliAuthorization{}, nil, ctxErr
		}
		return source.Session{}, cliAuthorization{}, nil, errors.New("qoder: CLI session read failed")
	}
	if finalStatErr != nil || !sameFileInfo(info, finalInfo) {
		return source.Session{}, cliAuthorization{}, nil, errors.New("qoder: CLI session changed during read")
	}
	projectKey := filepath.Base(filepath.Dir(filepath.Dir(relative)))
	if parsed.sessionID == "" || len(parsed.events) == 0 || parsed.cwd == "" || encodedProjectKey(parsed.cwd) != projectKey {
		return source.Session{}, cliAuthorization{}, nil, errMalformedCLI
	}
	sortCanonical(parsed.events)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, event := range parsed.events {
		if err := encoder.Encode(event); err != nil {
			return source.Session{}, cliAuthorization{}, nil, errors.New("qoder: canonical encoding failed")
		}
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	session := source.Session{
		ID: "qoder-cli:transcript-v1:" + digestPrefix(parsed.sessionID+"\x00"+parsed.cwd, 24), Product: a.Product(),
		FormatVersion: "transcript-v1", AdapterVersion: "1", Capabilities: cliCapabilities(parsed),
		Scope: projectScope(parsed.cwd), StartedAt: parsed.started, EndedAt: parsed.ended,
		MessageCount: len(parsed.events), MalformedCount: parsed.malformed, SnapshotID: digest,
		OpaqueRef: "qoder-cli:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+relative, 24),
	}
	auth := cliAuthorization{id: session.ID, snapshot: digest, metadata: sessionMetadata(session), root: root, relative: relative, rootIdentity: bound.Identity(), pathIdentity: pathIdentity{directories: identities}, fileInfo: finalInfo}
	return session, auth, output.Bytes(), nil
}

func parseCLILine(ctx context.Context, data []byte, taskID string, parsed *parsedCLI) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !jsonDepthOK(ctx, data, maxJSONDepth) {
		if err := ctx.Err(); err != nil {
			return err
		}
		parsed.malformed++
		return nil
	}
	var record cliRecord
	if json.Unmarshal(data, &record) != nil {
		parsed.malformed++
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.Type == nil || record.UUID == nil || record.CWD == nil || record.SessionID == nil || record.Timestamp == nil ||
		!validIdentifier(*record.UUID) || *record.SessionID != taskID || !validIdentifier(*record.SessionID) || !validAbsoluteProject(*record.CWD) {
		parsed.malformed++
		return nil
	}
	project, projectOK := canonicalProjectPath(*record.CWD)
	if !projectOK || (parsed.sessionID != "" && (parsed.sessionID != *record.SessionID || parsed.cwd != project)) {
		parsed.malformed++
		return nil
	}
	timestamp, err := time.Parse(time.RFC3339Nano, *record.Timestamp)
	if err != nil {
		parsed.malformed++
		return nil
	}
	digest := sha256.Sum256(data)
	if parsed.seenRecords == nil {
		parsed.seenRecords = map[string][32]byte{}
	}
	if previous, exists := parsed.seenRecords[*record.UUID]; exists {
		if previous != digest {
			return errConflictingCLIEvent
		}
		return nil
	}
	if !parsed.lastAt.IsZero() && timestamp.Before(parsed.lastAt) {
		parsed.malformed++
		return nil
	}
	var events []canonicalEvent
	switch *record.Type {
	case "session_meta":
		if len(record.Message) != 0 || len(record.Data) != 0 {
			parsed.malformed++
			return nil
		}
	case "progress":
		var progress struct {
			Type      *string `json:"type"`
			HookName  *string `json:"hookName"`
			HookEvent *string `json:"hookEvent"`
			Command   *string `json:"command"`
		}
		if len(record.Message) != 0 || json.Unmarshal(record.Data, &progress) != nil || progress.Type == nil || *progress.Type != "hook_progress" || progress.HookName == nil || strings.TrimSpace(*progress.HookName) == "" || progress.HookEvent == nil || strings.TrimSpace(*progress.HookEvent) == "" || progress.Command == nil {
			parsed.malformed++
			return nil
		}
	case "user":
		var message cliMessage
		var content string
		if len(record.Data) != 0 || json.Unmarshal(record.Message, &message) != nil || json.Unmarshal(message.Content, &content) != nil || strings.TrimSpace(content) == "" {
			parsed.malformed++
			return nil
		}
		events = append(events, canonicalEvent{Type: "message", Role: "user", Content: sanitizeCanonicalText(content), Timestamp: timestamp.UTC().Format(time.RFC3339Nano), StableID: *record.UUID, at: timestamp.UnixNano(), rank: 1})
	case "assistant":
		var message cliMessage
		var parts []cliContent
		if len(record.Data) != 0 || json.Unmarshal(record.Message, &message) != nil || json.Unmarshal(message.Content, &parts) != nil || len(parts) == 0 {
			parsed.malformed++
			return nil
		}
		for index, part := range parts {
			stable := *record.UUID + ":" + strconv.Itoa(index)
			switch {
			case part.Type != nil && *part.Type == "text" && part.Text != nil && part.Thinking == nil && strings.TrimSpace(*part.Text) != "":
				events = append(events, canonicalEvent{Type: "message", Role: "assistant", Content: sanitizeCanonicalText(*part.Text), Timestamp: timestamp.UTC().Format(time.RFC3339Nano), StableID: stable, at: timestamp.UnixNano(), rank: 2})
			case part.Type != nil && *part.Type == "thinking" && part.Thinking != nil && part.Text == nil && strings.TrimSpace(*part.Thinking) != "":
				events = append(events, canonicalEvent{Type: "reasoning", Content: sanitizeCanonicalText(*part.Thinking), Timestamp: timestamp.UTC().Format(time.RFC3339Nano), StableID: stable, at: timestamp.UnixNano(), rank: 1})
				parsed.hasReasoning = true
			default:
				parsed.malformed++
				return nil
			}
		}
	default:
		return errUnsupportedCLI
	}
	parsed.seenRecords[*record.UUID] = digest
	parsed.sessionID, parsed.cwd = *record.SessionID, project
	parsed.events = append(parsed.events, events...)
	parsed.lastAt = timestamp
	trackTime(timestamp, &parsed.started, &parsed.ended)
	return nil
}

func cliCapabilities(parsed parsedCLI) []source.Capability {
	capabilities := []source.Capability{source.CapabilityMessages}
	if parsed.hasReasoning {
		capabilities = append(capabilities, source.CapabilityReasoning)
	}
	return capabilities
}

func (a *CLIAdapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("qoder: invalid CLI session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.snapshot != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("qoder: unauthorized CLI session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("qoder: CLI source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("qoder: CLI source changed")
	}
	const suffix = ".jsonl"
	taskID := strings.TrimSuffix(filepath.Base(auth.relative), suffix)
	fresh, freshAuth, output, err := a.snapshotCLI(ctx, bound, auth.root, auth.relative, taskID, &cliByteBudget{maximum: maxCLIScanBytes})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil || fresh.ID != session.ID || freshAuth.snapshot != auth.snapshot || freshAuth.metadata != auth.metadata || !sameFileInfo(auth.fileInfo, freshAuth.fileInfo) || !samePathIdentity(auth.pathIdentity, freshAuth.pathIdentity) {
		return nil, errors.New("qoder: CLI source changed")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}

type databaseFile struct {
	suffix string
	info   os.FileInfo
	path   pathIdentity
}
type databaseFileSet struct {
	root  safeopen.Identity
	files []databaseFile
}
type ideAuthorization struct {
	id, snapshot, metadata, root string
	files                        databaseFileSet
}

type IDEAdapter struct {
	roots     []string
	configErr error
	instance  uint64
	options   []sharedclient.Option
	scanMu    sync.Mutex
	mu        sync.RWMutex
	known     map[string]ideAuthorization
}

func NewIDE(roots ...string) *IDEAdapter {
	if len(roots) == 0 {
		roots = defaultIDERoots()
	}
	clean, err := validateRoots(roots)
	return &IDEAdapter{roots: clean, configErr: err, instance: instanceCounter.Add(1), known: map[string]ideAuthorization{}}
}

func (*IDEAdapter) Product() string { return "qoder-ide" }
func (*IDEAdapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages}
}

type ideDiscovered struct {
	session source.Session
	auth    ideAuthorization
	output  []byte
}

func (a *IDEAdapter) Discover(ctx context.Context) ([]source.Session, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.configErr != nil {
		return nil, a.configErr
	}
	var sessions []source.Session
	next := map[string]ideAuthorization{}
	seenIDs := map[string]ideAuthorization{}
	seenRoots := map[safeopen.Identity]bool{}
	for _, root := range a.roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("qoder: IDE root read failed")
		}
		if seenRoots[bound.Identity()] {
			bound.Close()
			continue
		}
		seenRoots[bound.Identity()] = true
		probe, _, probeErr := bound.OpenWithPathIdentity(databaseRelative, maxDatabaseBytes)
		if os.IsNotExist(probeErr) {
			bound.Close()
			continue
		}
		if probeErr != nil {
			bound.Close()
			return nil, errors.New("qoder: IDE database read failed")
		}
		probe.Close()
		bound.Close()
		discovered, err := a.snapshotIDE(ctx, root)
		if errors.Is(err, sharedclient.ErrUnsupportedSchema) {
			return nil, source.NewDiscoveryError(source.SourceFormatUnsupported, err)
		}
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, errors.New("qoder: IDE database read failed")
		}
		for _, item := range discovered {
			if previous, duplicate := seenIDs[item.session.ID]; duplicate {
				if previous.snapshot != item.auth.snapshot || previous.root != item.auth.root {
					return nil, errors.New("qoder: duplicate IDE session")
				}
				continue
			}
			seenIDs[item.session.ID] = item.auth
			sessions = append(sessions, item.session)
			next[item.session.OpaqueRef] = item.auth
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID < sessions[j].ID })
	a.mu.Lock()
	a.known = next
	a.mu.Unlock()
	return sessions, nil
}

func (a *IDEAdapter) snapshotIDE(ctx context.Context, root string) ([]ideDiscovered, error) {
	before, err := captureDatabaseFiles(root)
	if err != nil {
		return nil, err
	}
	databasePath := filepath.Join(root, databaseRelative)
	var discovered []ideDiscovered
	err = sharedclient.WithChatSnapshot(ctx, root, databasePath, sharedclient.QoderIDEV1, sharedclient.Limits{MaxDatabaseBytes: maxDatabaseBytes, MaxSessions: maxDatabaseSessions, MaxRows: maxDatabaseRows, MaxPayloadBytes: maxDatabasePayload, MaxCanonicalBytes: maxCanonicalBytes}, func(reader sharedclient.ChatReader) error {
		rows, err := reader.ListSessions(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			conversation, err := reader.ReadConversation(ctx, row.ID)
			if errors.Is(err, sharedclient.ErrMalformedConversation) {
				continue
			}
			if err != nil {
				return err
			}
			item, ok := a.convertIDE(conversation, root, before)
			if ok {
				discovered = append(discovered, item)
			}
		}
		if len(rows) > 0 && len(discovered) == 0 {
			return errNoUsableIDEConversation
		}
		return nil
	}, a.options...)
	if err != nil {
		return nil, err
	}
	after, err := captureDatabaseFiles(root)
	if err != nil || !sameDatabaseSnapshot(before, after) {
		return nil, errors.New("qoder: IDE database changed")
	}
	for index := range discovered {
		discovered[index].auth.files = after
	}
	return discovered, nil
}

func (a *IDEAdapter) convertIDE(conversation sharedclient.Conversation, root string, files databaseFileSet) (ideDiscovered, bool) {
	row := conversation.Session
	if row.ID == "" || row.ProjectID == "" {
		return ideDiscovered{}, false
	}
	// project_id is an opaque identifier in the evidenced Qoder schema. It is
	// deliberately not interpreted as project_uri or joined with CLI cwd.
	scope := source.ScopeRef{Type: source.ScopeProject, Root: "qoder-ide:project:" + digestPrefix(root+"\x00"+row.ProjectID, 32), Label: "Qoder IDE project"}
	var events []canonicalEvent
	// Machine evidence currently proves only plain user/assistant chat_message
	// content. Records, snapshots, reasoning_content, and tool_result remain
	// snapshot inputs but are deliberately not promoted to canonical events.
	for _, message := range conversation.Messages {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		if message.Content == "" {
			continue
		}
		at := milliseconds(message.CreatedAt)
		if at.IsZero() {
			continue
		}
		events = append(events, canonicalEvent{Type: "message", Role: message.Role, Content: sanitizeCanonicalText(message.Content), Timestamp: at.Format(time.RFC3339Nano), StableID: message.ID, at: at.UnixNano(), rank: 1})
	}
	if len(events) == 0 {
		return ideDiscovered{}, false
	}
	sortCanonical(events)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, event := range events {
		if encoder.Encode(event) != nil {
			return ideDiscovered{}, false
		}
	}
	canonical := output.Bytes()
	conversationBytes, err := json.Marshal(conversation)
	if err != nil {
		return ideDiscovered{}, false
	}
	snapshotSum := sha256.Sum256(conversationBytes)
	started, ended := milliseconds(row.CreatedAt), milliseconds(row.ModifiedAt)
	for _, event := range events {
		trackTime(time.Unix(0, event.at).UTC(), &started, &ended)
	}
	session := source.Session{
		ID: "qoder-ide:sharedclient-db-v1:" + digestPrefix(root+"\x00"+row.ID, 24), Product: a.Product(),
		FormatVersion: "sharedclient-db-v1", AdapterVersion: "1", Capabilities: []source.Capability{source.CapabilityMessages},
		Scope: scope, StartedAt: started, EndedAt: ended, MessageCount: len(events),
		SnapshotID: hex.EncodeToString(snapshotSum[:]), OpaqueRef: "qoder-ide:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+row.ID, 24),
	}
	auth := ideAuthorization{id: session.ID, snapshot: session.SnapshotID, metadata: sessionMetadata(session), root: root, files: files}
	return ideDiscovered{session: session, auth: auth, output: canonical}, true
}

func (a *IDEAdapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("qoder: invalid IDE session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.snapshot != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("qoder: unauthorized IDE session")
	}
	items, err := a.snapshotIDE(ctx, auth.root)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, errors.New("qoder: IDE source changed")
	}
	for _, item := range items {
		if item.session.ID != session.ID {
			continue
		}
		if item.auth.snapshot != auth.snapshot || item.auth.metadata != auth.metadata || !sameDatabaseSnapshot(auth.files, item.auth.files) {
			break
		}
		return io.NopCloser(bytes.NewReader(item.output)), nil
	}
	return nil, errors.New("qoder: IDE source changed")
}

func captureDatabaseFiles(root string) (databaseFileSet, error) {
	bound, err := safeopen.Bind(root)
	if err != nil {
		return databaseFileSet{}, err
	}
	defer bound.Close()
	set := databaseFileSet{root: bound.Identity()}
	var total int64
	for index, suffix := range []string{"", "-wal", "-shm"} {
		file, identities, err := bound.OpenWithPathIdentity(databaseRelative+suffix, maxDatabaseBytes)
		if err != nil {
			if index > 0 && os.IsNotExist(err) {
				continue
			}
			return databaseFileSet{}, err
		}
		info, statErr := file.Stat()
		file.Close()
		if statErr != nil {
			return databaseFileSet{}, statErr
		}
		total += info.Size()
		if total > maxDatabaseBytes {
			return databaseFileSet{}, sharedclient.ErrBudgetExceeded
		}
		set.files = append(set.files, databaseFile{suffix: suffix, info: info, path: pathIdentity{directories: identities}})
	}
	return set, nil
}

// sameDatabaseSnapshot compares content-bearing files while tolerating the
// expected bookkeeping performed by a read-only WAL connection. SHM contents
// and timestamps are coordination state, not transcript content. An empty WAL
// may be created/touched by the reader; a non-empty WAL remains exact-bound.
func sameDatabaseSnapshot(before, after databaseFileSet) bool {
	if before.root != after.root {
		return false
	}
	index := func(set databaseFileSet, suffix string) *databaseFile {
		for position := range set.files {
			if set.files[position].suffix == suffix {
				return &set.files[position]
			}
		}
		return nil
	}
	mainBefore, mainAfter := index(before, ""), index(after, "")
	if mainBefore == nil || mainAfter == nil || !sameFileInfo(mainBefore.info, mainAfter.info) || !samePathIdentity(mainBefore.path, mainAfter.path) {
		return false
	}
	walBefore, walAfter := index(before, "-wal"), index(after, "-wal")
	if walBefore != nil && walBefore.info.Size() > 0 {
		if walAfter == nil || !sameFileInfo(walBefore.info, walAfter.info) || !samePathIdentity(walBefore.path, walAfter.path) {
			return false
		}
	} else if walAfter != nil {
		if walAfter.info.Size() != 0 || walBefore != nil && (!os.SameFile(walBefore.info, walAfter.info) || !samePathIdentity(walBefore.path, walAfter.path)) {
			return false
		}
	}
	shmBefore, shmAfter := index(before, "-shm"), index(after, "-shm")
	if shmAfter != nil && (shmAfter.info.Size() <= 0 || shmAfter.info.Size() > maxDatabaseBytes) {
		return false
	}
	if shmBefore != nil && shmAfter != nil && (!os.SameFile(shmBefore.info, shmAfter.info) || !samePathIdentity(shmBefore.path, shmAfter.path)) {
		return false
	}
	return true
}

func defaultCLIRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{filepath.Join(home, ".qoder")}
}

func defaultIDERoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Qoder", "SharedClientCache")}
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return []string{filepath.Join(appData, "Qoder", "SharedClientCache")}
		}
		return nil
	default:
		if config := os.Getenv("XDG_CONFIG_HOME"); config != "" {
			return []string{filepath.Join(config, "Qoder", "SharedClientCache")}
		}
		return []string{filepath.Join(home, ".config", "Qoder", "SharedClientCache")}
	}
}

func validateRoots(roots []string) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, errors.New("qoder: root limit exceeded")
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return nil, errors.New("qoder: invalid root")
		}
		if !seen[root] {
			seen[root] = true
			clean = append(clean, root)
		}
	}
	return clean, nil
}

func validEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, "\x00\r\n")
}
func validIdentifier(value string) bool {
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\x00\r\n/\\")
}
func validAbsoluteProject(value string) bool {
	_, ok := canonicalProjectPath(value)
	return ok
}

func projectScope(project string) source.ScopeRef {
	clean, ok := canonicalProjectPath(project)
	if !ok {
		clean = project
	}
	label := safeProjectLabel(projectBase(clean))
	if label == "" {
		label = "Qoder project"
	}
	return source.ScopeRef{Type: source.ScopeProject, Root: sharedProjectRoot(clean), Label: label}
}

func safeProjectLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 128 || label == "." || label == ".." || strings.ContainsAny(label, "/\\\x00\r\n") {
		return ""
	}
	lower := strings.ToLower(label)
	if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || containsPhoneNumber(label) {
		return ""
	}
	for _, character := range label {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return ""
		}
	}
	return label
}

func containsPhoneNumber(value string) bool {
	for index := 0; index+11 <= len(value); index++ {
		candidate := value[index : index+11]
		if candidate[0] != '1' || candidate[1] < '3' || candidate[1] > '9' {
			continue
		}
		valid := true
		for _, digit := range candidate[2:] {
			if digit < '0' || digit > '9' {
				valid = false
				break
			}
		}
		if valid {
			return true
		}
	}
	return false
}

func sanitizeCanonicalText(value string) string {
	trimmed := strings.TrimSpace(value)
	if filepath.IsAbs(trimmed) || windowsDrivePath(trimmed) || strings.HasPrefix(strings.ToLower(trimmed), "file:") {
		return "[redacted-path:" + digestPrefix(trimmed, 16) + "]"
	}
	return value
}

func windowsDrivePath(value string) bool {
	return len(value) > 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func sharedProjectRoot(project string) string {
	clean, ok := canonicalProjectPath(project)
	if !ok {
		clean = project
	}
	return "sharedclient:project:" + digestPrefix(clean, 32)
}
func encodedProjectKey(project string) string {
	clean, ok := canonicalProjectPath(project)
	if !ok {
		clean = project
	}
	return strings.NewReplacer(":", "-", "/", "-", "\\", "-").Replace(clean)
}

func canonicalProjectPath(value string) (string, bool) {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\x00\r\n") {
		return "", false
	}
	if windowsCanonicalProject(value) {
		return value, true
	}
	if filepath.IsAbs(value) && filepath.Clean(value) == value {
		return value, true
	}
	return "", false
}

func windowsCanonicalProject(value string) bool {
	if len(value) < 3 || !((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) || value[1] != ':' || value[2] != '\\' || strings.Contains(value, "/") {
		return false
	}
	if len(value) == 3 {
		return true
	}
	for _, segment := range strings.Split(value[3:], `\`) {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func projectBase(project string) string {
	if windowsCanonicalProject(project) {
		if index := strings.LastIndexByte(project, '\\'); index >= 0 && index+1 < len(project) {
			return project[index+1:]
		}
		return ""
	}
	return filepath.Base(project)
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
	for index := range left.directories {
		if left.directories[index] != right.directories[index] {
			return false
		}
	}
	return true
}

func sameFileInfo(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) && left.Size() == right.Size() && left.Mode() == right.Mode() && left.ModTime().Equal(right.ModTime())
}

func milliseconds(value int64) time.Time {
	if value < 1_000_000_000_000 || value > 9_999_999_999_999 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func trackTime(value time.Time, start, end *time.Time) {
	if value.IsZero() {
		return
	}
	if start.IsZero() || value.Before(*start) {
		*start = value
	}
	if end.IsZero() || value.After(*end) {
		*end = value
	}
}

func sortCanonical(events []canonicalEvent) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].at != events[j].at {
			return events[i].at < events[j].at
		}
		if events[i].rank != events[j].rank {
			return events[i].rank < events[j].rank
		}
		if events[i].StableID != events[j].StableID {
			return events[i].StableID < events[j].StableID
		}
		if events[i].Type != events[j].Type {
			return events[i].Type < events[j].Type
		}
		if events[i].Role != events[j].Role {
			return events[i].Role < events[j].Role
		}
		return events[i].Content < events[j].Content
	})
}

func jsonDepthOK(ctx context.Context, data []byte, limit int) bool {
	depth := 0
	inString, escaped := false, false
	for index, character := range data {
		if index&4095 == 0 && ctx.Err() != nil {
			return false
		}
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
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
