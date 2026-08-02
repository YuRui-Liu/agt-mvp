package lingma

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
	cliProtocolVersion        = "0.11.3-quest"
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
	roots      []string
	configErr  error
	scanLimits cliScanLimits
	instance   uint64
	scanMu     sync.Mutex
	mu         sync.RWMutex
	known      map[string]cliAuthorization
}

type cliScanLimits struct{ maxTotalBytes int64 }

type cliByteBudget struct {
	maximum int64
	used    int64
}

var errCLIScanByteBudget = errors.New("lingma: CLI scan byte budget exceeded")

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

func NewCLI(roots ...string) *CLIAdapter {
	if len(roots) == 0 {
		roots = defaultRoots()
	}
	clean, err := validateRoots(roots)
	return &CLIAdapter{roots: clean, configErr: err, scanLimits: cliScanLimits{maxTotalBytes: maxCLIScanBytes}, instance: instanceCounter.Add(1), known: map[string]cliAuthorization{}}
}

func (*CLIAdapter) Product() string { return "tongyi-lingma-cli" }
func (*CLIAdapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages}
}

type cliRecord struct {
	UUID        *string     `json:"uuid"`
	ParentUUID  *string     `json:"parentUuid"`
	CWD         *string     `json:"cwd"`
	SessionID   *string     `json:"sessionId"`
	Version     *string     `json:"version"`
	AgentID     *string     `json:"agentId"`
	Type        *string     `json:"type"`
	Timestamp   *string     `json:"timestamp"`
	RequestSet  *string     `json:"requestSetId"`
	UserType    *string     `json:"userType"`
	IsMeta      *bool       `json:"isMeta"`
	IsSidechain *bool       `json:"isSidechain"`
	Message     *cliMessage `json:"message"`
}

type cliMessage struct {
	ID      *string       `json:"id"`
	Role    *string       `json:"role"`
	Content *[]cliContent `json:"content"`
}

type cliContent struct {
	Type *string `json:"type"`
	Text *string `json:"text"`
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
	sessionID, cwd, agentID string
	events                  []canonicalEvent
	seenEvents              map[string]cliEventBinding
	seenMessages            map[string]string
	malformed               int
	started, ended          time.Time
}

type cliEventBinding struct {
	messageID string
	event     canonicalEvent
}

var errUnsupportedCLI = errors.New("lingma: unsupported execution format")
var errConflictingCLIEvent = errors.New("lingma: conflicting execution event")
var errMalformedCLI = errors.New("lingma: malformed execution candidate")
var errNoUsableIDEConversation = errors.New("lingma: no usable IDE conversation")

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
			return nil, errors.New("lingma: CLI root read failed")
		}
		if seenRoots[bound.Identity()] {
			bound.Close()
			continue
		}
		seenRoots[bound.Identity()] = true
		projects, err := bound.ReadDirLimit(filepath.Join("cli", "projects"), maxDirectoryEntries)
		if os.IsNotExist(err) {
			bound.Close()
			continue
		}
		if err != nil {
			bound.Close()
			return nil, errors.New("lingma: CLI projects read failed")
		}
		entriesRead += len(projects)
		if entriesRead > maxGlobalEntries {
			bound.Close()
			return nil, errors.New("lingma: CLI directory budget exceeded")
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
			projectRelative := filepath.Join("cli", "projects", project.Name())
			files, err := bound.ReadDirLimit(projectRelative, maxDirectoryEntries)
			if err != nil {
				bound.Close()
				return nil, errors.New("lingma: CLI project read failed")
			}
			entriesRead += len(files)
			if entriesRead > maxGlobalEntries {
				bound.Close()
				return nil, errors.New("lingma: CLI directory budget exceeded")
			}
			sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
			for _, entry := range files {
				if err := ctx.Err(); err != nil {
					bound.Close()
					return nil, err
				}
				const suffix = ".session.execution.jsonl"
				if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), suffix) || !validEntryName(entry.Name()) {
					continue
				}
				taskID := strings.TrimSuffix(entry.Name(), suffix)
				if !validTaskID(taskID) {
					continue
				}
				relative := filepath.Join(projectRelative, entry.Name())
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
			return nil, errors.New("lingma: no readable CLI candidates")
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
	file, identities, err := bound.OpenWithPathIdentity(relative, budget.maximum)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return source.Session{}, cliAuthorization{}, nil, ctxErr
		}
		if errors.Is(err, safeopen.ErrFileSizeLimit) {
			return source.Session{}, cliAuthorization{}, nil, errCLIScanByteBudget
		}
		if os.IsNotExist(err) {
			return source.Session{}, cliAuthorization{}, nil, errors.New("lingma: CLI source changed")
		}
		return source.Session{}, cliAuthorization{}, nil, errors.New("lingma: CLI session read failed")
	}
	info, statErr := file.Stat()
	if statErr != nil {
		file.Close()
		return source.Session{}, cliAuthorization{}, nil, errors.New("lingma: CLI session read failed")
	}
	if err := budget.consume(ctx, info.Size()); err != nil {
		file.Close()
		return source.Session{}, cliAuthorization{}, nil, err
	}
	if info.Size() > maxCLIFileBytes {
		file.Close()
		return source.Session{}, cliAuthorization{}, nil, errors.New("lingma: CLI session file exceeds limit")
	}
	hash := sha256.New()
	parsed := parsedCLI{}
	err = sharedclient.WalkJSONL(ctx, io.TeeReader(file, hash), sharedclient.Limits{MaxTotalBytes: maxCLIFileBytes, MaxLineBytes: maxCLILineBytes, MaxRecords: maxCLIRecords}, func(line sharedclient.JSONLLine) error {
		return parseCLILine(ctx, line.Bytes, taskID, &parsed)
	})
	file.Close()
	if err != nil {
		if errors.Is(err, sharedclient.ErrBudgetExceeded) || errors.Is(err, safeopen.ErrFileSizeLimit) {
			return source.Session{}, cliAuthorization{}, nil, errors.New("lingma: CLI session budget exceeded")
		}
		if errors.Is(err, errUnsupportedCLI) {
			return source.Session{}, cliAuthorization{}, nil, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return source.Session{}, cliAuthorization{}, nil, ctxErr
		}
		return source.Session{}, cliAuthorization{}, nil, errors.New("lingma: CLI session read failed")
	}
	projectKey := filepath.Base(filepath.Dir(relative))
	if parsed.sessionID == "" || len(parsed.events) == 0 || parsed.cwd == "" || encodedProjectKey(parsed.cwd) != projectKey {
		return source.Session{}, cliAuthorization{}, nil, errMalformedCLI
	}
	sortCanonical(parsed.events)
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, event := range parsed.events {
		if err := encoder.Encode(event); err != nil {
			return source.Session{}, cliAuthorization{}, nil, errors.New("lingma: canonical encoding failed")
		}
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	session := source.Session{
		ID: "tongyi-lingma-cli:execution-v1:" + digestPrefix(parsed.sessionID, 24), Product: a.Product(),
		FormatVersion: "execution-v1", AdapterVersion: "1", Capabilities: []source.Capability{source.CapabilityMessages},
		Scope: projectScope(parsed.cwd), StartedAt: parsed.started, EndedAt: parsed.ended,
		MessageCount: len(parsed.events), MalformedCount: parsed.malformed, SnapshotID: digest,
		OpaqueRef: "tongyi-lingma-cli:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+relative, 24),
	}
	auth := cliAuthorization{id: session.ID, snapshot: digest, metadata: sessionMetadata(session), root: root, relative: relative, rootIdentity: bound.Identity(), pathIdentity: pathIdentity{directories: identities}, fileInfo: info}
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
	if record.Version == nil || *record.Version == "" {
		parsed.malformed++
		return nil
	}
	if *record.Version != cliProtocolVersion {
		return errUnsupportedCLI
	}
	if record.Type == nil || *record.Type == "" {
		parsed.malformed++
		return nil
	}
	if *record.Type != "user" && *record.Type != "assistant" {
		return nil
	}
	if record.UUID == nil || record.ParentUUID == nil || record.CWD == nil || record.SessionID == nil || record.AgentID == nil || record.Timestamp == nil || record.RequestSet == nil || record.UserType == nil || record.IsMeta == nil || record.IsSidechain == nil || record.Message == nil ||
		*record.SessionID != taskID || !validIdentifier(*record.UUID) || (*record.ParentUUID != "" && !validOpaqueID(*record.ParentUUID)) || !validIdentifier(*record.AgentID) || !validOpaqueID(*record.RequestSet) || *record.UserType != "external" || *record.IsMeta || *record.IsSidechain ||
		record.Message.ID == nil || record.Message.Role == nil || record.Message.Content == nil || !validIdentifier(*record.Message.ID) || *record.Message.Role != *record.Type || !validAbsoluteProject(*record.CWD) {
		parsed.malformed++
		return nil
	}
	project, projectOK := canonicalProjectPath(*record.CWD)
	if !projectOK {
		parsed.malformed++
		return nil
	}
	if parsed.sessionID != "" && (parsed.sessionID != *record.SessionID || parsed.cwd != project || parsed.agentID != *record.AgentID) {
		parsed.malformed++
		return nil
	}
	var texts []string
	for _, part := range *record.Message.Content {
		if part.Type == nil || part.Text == nil || *part.Type != "text" {
			parsed.malformed++
			return nil
		}
		if *part.Text != "" {
			texts = append(texts, *part.Text)
		}
	}
	if len(texts) == 0 {
		parsed.malformed++
		return nil
	}
	timestamp, err := time.Parse(time.RFC3339Nano, *record.Timestamp)
	if err != nil {
		parsed.malformed++
		return nil
	}
	event := canonicalEvent{Type: "message", Role: *record.Type, Content: sanitizeCanonicalText(strings.Join(texts, "\n")), Timestamp: timestamp.UTC().Format(time.RFC3339Nano), StableID: *record.UUID, at: timestamp.UnixNano(), rank: 1}
	if parsed.seenEvents == nil {
		parsed.seenEvents = map[string]cliEventBinding{}
		parsed.seenMessages = map[string]string{}
	}
	if previous, exists := parsed.seenEvents[*record.UUID]; exists {
		if previous.event != event || previous.messageID != *record.Message.ID {
			return errConflictingCLIEvent
		}
		return nil
	}
	if previousUUID, exists := parsed.seenMessages[*record.Message.ID]; exists && previousUUID != *record.UUID {
		return errConflictingCLIEvent
	}
	parsed.seenEvents[*record.UUID] = cliEventBinding{messageID: *record.Message.ID, event: event}
	parsed.seenMessages[*record.Message.ID] = *record.UUID
	parsed.sessionID, parsed.cwd, parsed.agentID = *record.SessionID, project, *record.AgentID
	parsed.events = append(parsed.events, event)
	trackTime(timestamp, &parsed.started, &parsed.ended)
	return nil
}

func (a *CLIAdapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("lingma: invalid CLI session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.snapshot != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("lingma: unauthorized CLI session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("lingma: CLI source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("lingma: CLI source changed")
	}
	const suffix = ".session.execution.jsonl"
	taskID := strings.TrimSuffix(filepath.Base(auth.relative), suffix)
	fresh, freshAuth, output, err := a.snapshotCLI(ctx, bound, auth.root, auth.relative, taskID, &cliByteBudget{maximum: maxCLIScanBytes})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil || fresh.ID != session.ID || freshAuth.snapshot != auth.snapshot || freshAuth.metadata != auth.metadata || !sameFileInfo(auth.fileInfo, freshAuth.fileInfo) || !samePathIdentity(auth.pathIdentity, freshAuth.pathIdentity) {
		return nil, errors.New("lingma: CLI source changed")
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
		roots = defaultRoots()
	}
	clean, err := validateRoots(roots)
	return &IDEAdapter{roots: clean, configErr: err, instance: instanceCounter.Add(1), known: map[string]ideAuthorization{}}
}

func (*IDEAdapter) Product() string { return "tongyi-lingma-ide" }
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
			return nil, errors.New("lingma: IDE root read failed")
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
			return nil, errors.New("lingma: IDE database read failed")
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
			return nil, errors.New("lingma: IDE database read failed")
		}
		for _, item := range discovered {
			if previous, duplicate := seenIDs[item.session.ID]; duplicate {
				if previous.snapshot != item.auth.snapshot || previous.root != item.auth.root {
					return nil, errors.New("lingma: duplicate IDE session")
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
	err = sharedclient.WithChatSnapshot(ctx, root, databasePath, sharedclient.LingmaIDEV1, sharedclient.Limits{MaxDatabaseBytes: maxDatabaseBytes, MaxSessions: maxDatabaseSessions, MaxRows: maxDatabaseRows, MaxPayloadBytes: maxDatabasePayload, MaxCanonicalBytes: maxCanonicalBytes}, func(reader sharedclient.ChatReader) error {
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
	if err != nil || !sameDatabaseFileSet(before, after) {
		return nil, errors.New("lingma: IDE database changed")
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
	scope := source.ScopeRef{Type: source.ScopeProject, Root: "tongyi-lingma-ide:project:" + digestPrefix(row.ProjectID, 32), Label: "Lingma IDE project"}
	if validAbsoluteProject(row.ProjectID) {
		scope = projectScope(row.ProjectID)
	}
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
		ID: "tongyi-lingma-ide:sharedclient-db-v1:" + digestPrefix(row.ID, 24), Product: a.Product(),
		FormatVersion: "sharedclient-db-v1", AdapterVersion: "1", Capabilities: []source.Capability{source.CapabilityMessages},
		Scope: scope, StartedAt: started, EndedAt: ended, MessageCount: len(events),
		SnapshotID: hex.EncodeToString(snapshotSum[:]), OpaqueRef: "tongyi-lingma-ide:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+row.ID, 24),
	}
	auth := ideAuthorization{id: session.ID, snapshot: session.SnapshotID, metadata: sessionMetadata(session), root: root, files: files}
	return ideDiscovered{session: session, auth: auth, output: canonical}, true
}

func (a *IDEAdapter) Open(ctx context.Context, session source.Session) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if session.Product != a.Product() {
		return nil, errors.New("lingma: invalid IDE session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.snapshot != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("lingma: unauthorized IDE session")
	}
	items, err := a.snapshotIDE(ctx, auth.root)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, errors.New("lingma: IDE source changed")
	}
	for _, item := range items {
		if item.session.ID != session.ID {
			continue
		}
		if item.auth.snapshot != auth.snapshot || item.auth.metadata != auth.metadata || !sameDatabaseFileSet(auth.files, item.auth.files) {
			break
		}
		return io.NopCloser(bytes.NewReader(item.output)), nil
	}
	return nil, errors.New("lingma: IDE source changed")
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

func sameDatabaseFileSet(left, right databaseFileSet) bool {
	if left.root != right.root || len(left.files) != len(right.files) {
		return false
	}
	for index := range left.files {
		a, b := left.files[index], right.files[index]
		if a.suffix != b.suffix || !os.SameFile(a.info, b.info) || a.info.Size() != b.info.Size() || !a.info.ModTime().Equal(b.info.ModTime()) || !samePathIdentity(a.path, b.path) {
			return false
		}
	}
	return true
}

func defaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Lingma", "SharedClientCache")}
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return []string{filepath.Join(appData, "Lingma", "SharedClientCache")}
		}
		return nil
	default:
		if config := os.Getenv("XDG_CONFIG_HOME"); config != "" {
			return []string{filepath.Join(config, "Lingma", "SharedClientCache")}
		}
		return []string{filepath.Join(home, ".config", "Lingma", "SharedClientCache")}
	}
}

func validateRoots(roots []string) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, errors.New("lingma: root limit exceeded")
	}
	seen := map[string]bool{}
	clean := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return nil, errors.New("lingma: invalid root")
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
func validOpaqueID(value string) bool {
	return value != "" && len(value) <= 512 && !strings.ContainsAny(value, "\x00\r\n")
}
func validTaskID(value string) bool {
	return strings.HasPrefix(value, "task-") && validIdentifier(value)
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
		label = "Lingma project"
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
