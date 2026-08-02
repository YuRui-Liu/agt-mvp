package aider

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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const (
	fileName            = ".aider.chat.history.md"
	markerPrefix        = "# aider chat started at "
	markerLayout        = "2006-01-02 15:04:05"
	maxRoots            = 64
	maxDirectoryEntries = 256
	maxGlobalEntries    = 4096
	maxFileBytes        = 4 << 20
	maxLineBytes        = 1 << 20
	maxSegments         = 1024
	maxEvents           = 8192
)

type pathIdentity struct{ directories []safeopen.Identity }

type authorization struct {
	id, digest, metadata, root string
	segmentIndex               int
	marker, opaqueRef          string
	rootIdentity               safeopen.Identity
	pathIdentity               pathIdentity
	fileInfo                   os.FileInfo
}

type Adapter struct {
	roots       []string
	defaultRoot bool
	configErr   error
	scanMu      sync.Mutex
	mu          sync.RWMutex
	known       map[string]authorization
	instance    uint64
}

var instanceCounter atomic.Uint64

func New(roots ...string) *Adapter {
	defaultRoot := len(roots) == 0
	if defaultRoot {
		if home, err := os.UserHomeDir(); err == nil {
			roots = []string{home}
		}
	}
	clean, err := validatedRoots(roots)
	return &Adapter{roots: clean, defaultRoot: defaultRoot, configErr: err, known: map[string]authorization{}, instance: instanceCounter.Add(1)}
}

func (*Adapter) Product() string { return "aider" }
func (*Adapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages}
}

func validatedRoots(roots []string) ([]string, error) {
	if len(roots) > maxRoots {
		return nil, errors.New("aider: root scan limit")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" || !filepath.IsAbs(root) {
			return nil, errors.New("aider: invalid root")
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
		return "", errors.New("aider: invalid root")
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
			return "", errors.New("aider: invalid root")
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
			return "", errors.New("aider: invalid root")
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
			return nil, errors.New("aider: root changed")
		}
		bound, err := safeopen.Bind(root)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, errors.New("aider: root read failed")
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
			return nil, errors.New("aider: directory limit")
		}
		found := false
		for _, entry := range entries {
			if entry.Name() == fileName && entry.Type().IsRegular() {
				found = true
				break
			}
		}
		if !found {
			bound.Close()
			continue
		}
		sessions, auths, ok := a.snapshot(ctx, bound, root)
		bound.Close()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		for index, session := range sessions {
			if seenIDs[session.ID] {
				continue
			}
			seenIDs[session.ID] = true
			out = append(out, session)
			next[session.OpaqueRef] = auths[index]
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a.replaceKnown(next)
	return out, nil
}

func directoryError(err error) error {
	if errors.Is(err, safeopen.ErrDirectoryLimit) {
		return errors.New("aider: directory limit")
	}
	return errors.New("aider: directory read failed")
}

type event struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}
type segment struct {
	index, ordinal int
	marker         string
	started        time.Time
	raw            []byte
	events         []event
}

func (a *Adapter) snapshot(ctx context.Context, bound *safeopen.BoundRoot, root string) ([]source.Session, []authorization, bool) {
	file, identities, err := bound.OpenWithPathIdentity(fileName, maxFileBytes)
	if err != nil {
		return nil, nil, false
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	info, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || len(data) > maxFileBytes || ctx.Err() != nil || !linesWithinLimit(data) {
		return nil, nil, false
	}
	segments, ok := parseSegments(ctx, data)
	if !ok {
		return nil, nil, false
	}
	rootIdentity := bound.Identity()
	var sessions []source.Session
	var auths []authorization
	for _, part := range segments {
		if ctx.Err() != nil {
			return nil, nil, false
		}
		if len(part.events) == 0 {
			continue
		}
		seed := "aider\x00" + identityString(rootIdentity) + "\x00" + fileName + "\x00" + part.marker + "\x00" + strconv.Itoa(part.ordinal)
		session := source.Session{
			ID: "aider:v1:" + digestPrefix(seed, 24), Product: a.Product(), FormatVersion: "markdown-v1", AdapterVersion: "1",
			Capabilities: a.Capabilities(), Scope: a.scope(root, rootIdentity), StartedAt: part.started, EndedAt: part.started,
			MessageCount: len(part.events), SnapshotID: digest(part.raw),
		}
		session.OpaqueRef = a.opaqueRef(root, session.ID)
		auth := authorization{
			id: session.ID, digest: session.SnapshotID, metadata: sessionMetadata(session), root: root,
			segmentIndex: part.index, marker: part.marker, opaqueRef: session.OpaqueRef, rootIdentity: rootIdentity,
			pathIdentity: pathIdentity{directories: identities}, fileInfo: info,
		}
		sessions = append(sessions, session)
		auths = append(auths, auth)
	}
	return sessions, auths, true
}

func parseSegments(ctx context.Context, data []byte) ([]segment, bool) {
	type markerAt struct {
		start   int
		text    string
		started time.Time
		ordinal int
	}
	var markers []markerAt
	ordinals := map[string]int{}
	for offset, lineNo := 0, 0; offset < len(data); lineNo++ {
		if lineNo&127 == 0 && ctx.Err() != nil {
			return nil, false
		}
		next := bytes.IndexByte(data[offset:], '\n')
		end := len(data)
		if next >= 0 {
			end = offset + next + 1
		}
		lineEnd := end
		if lineEnd > offset && data[lineEnd-1] == '\n' {
			lineEnd--
		}
		if lineEnd > offset && data[lineEnd-1] == '\r' {
			lineEnd--
		}
		line := string(data[offset:lineEnd])
		if started, ok := parseMarker(line); ok {
			ordinal := ordinals[line]
			ordinals[line] = ordinal + 1
			markers = append(markers, markerAt{start: offset, text: line, started: started, ordinal: ordinal})
			if len(markers) > maxSegments {
				return nil, false
			}
		}
		if next < 0 {
			break
		}
		offset = end
	}
	segments := make([]segment, 0, len(markers))
	for index, marker := range markers {
		if ctx.Err() != nil {
			return nil, false
		}
		end := len(data)
		if index+1 < len(markers) {
			end = markers[index+1].start
		}
		raw := data[marker.start:end]
		events, ok := parseSegmentEvents(ctx, raw)
		if !ok {
			return nil, false
		}
		segments = append(segments, segment{index: index, ordinal: marker.ordinal, marker: marker.text, started: marker.started, raw: raw, events: events})
	}
	return segments, ctx.Err() == nil
}

func parseMarker(line string) (time.Time, bool) {
	if !strings.HasPrefix(line, markerPrefix) || len(line) != len(markerPrefix)+len(markerLayout) {
		return time.Time{}, false
	}
	value := strings.TrimPrefix(line, markerPrefix)
	parsed, err := time.ParseInLocation(markerLayout, value, time.Local)
	if err != nil || parsed.Format(markerLayout) != value {
		return time.Time{}, false
	}
	return parsed, true
}

func parseSegmentEvents(ctx context.Context, raw []byte) ([]event, bool) {
	var out []event
	var userLines, assistantLines []string
	flushUser := func() {
		if len(userLines) == 0 {
			return
		}
		content := strings.Join(userLines, "\n")
		if strings.TrimSpace(content) != "" {
			out = append(out, event{Type: "message", Role: "user", Content: content})
		}
		userLines = nil
	}
	flushAssistant := func() {
		if len(assistantLines) == 0 {
			return
		}
		content := strings.Join(assistantLines, "\n")
		if strings.TrimSpace(content) != "" {
			out = append(out, event{Type: "message", Role: "assistant", Content: content})
		}
		assistantLines = nil
	}
	lineNo := 0
	for offset := 0; offset < len(raw); lineNo++ {
		if lineNo&127 == 0 && ctx.Err() != nil {
			return nil, false
		}
		next := bytes.IndexByte(raw[offset:], '\n')
		end := len(raw)
		if next >= 0 {
			end = offset + next + 1
		}
		lineEnd := end
		if lineEnd > offset && raw[lineEnd-1] == '\n' {
			lineEnd--
		}
		if lineEnd > offset && raw[lineEnd-1] == '\r' {
			lineEnd--
		}
		line := string(raw[offset:lineEnd])
		if lineNo == 0 { // Exact marker, already validated by the segmenter.
			offset = end
			if next < 0 {
				break
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "#### "):
			flushAssistant()
			value := strings.TrimPrefix(line, "#### ")
			value = strings.TrimSuffix(value, "  ")
			userLines = append(userLines, value)
		case strings.HasPrefix(line, "> "):
			flushUser()
			flushAssistant()
		case line == "":
			flushUser()
			flushAssistant()
		default:
			flushUser()
			assistantLines = append(assistantLines, line)
		}
		if len(out) > maxEvents {
			return nil, false
		}
		if next < 0 {
			break
		}
		offset = end
	}
	flushUser()
	flushAssistant()
	return out, len(out) <= maxEvents && ctx.Err() == nil
}

func linesWithinLimit(data []byte) bool {
	length := 0
	for _, value := range data {
		if value == '\n' {
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

func (a *Adapter) scope(root string, identity safeopen.Identity) source.ScopeRef {
	seed := identityString(identity)
	if a.defaultRoot {
		return source.ScopeRef{Type: source.ScopeSessionCollection, Root: "aider:collection:" + digestPrefix(seed, 24), Label: "Aider sessions"}
	}
	label := safeLabel(filepath.Base(root))
	if label == "" {
		label = "Aider project"
	}
	return source.ScopeRef{Type: source.ScopeProject, Root: "aider:project:" + digestPrefix(seed, 24), Label: label}
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

func (a *Adapter) opaqueRef(root, id string) string {
	return "aider:ref:" + strconv.FormatUint(a.instance, 10) + ":" + digestPrefix(root+"\x00"+id, 24)
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
		return nil, errors.New("aider: invalid session")
	}
	a.mu.RLock()
	auth, ok := a.known[session.OpaqueRef]
	a.mu.RUnlock()
	if !ok || auth.id != session.ID || auth.digest != session.SnapshotID || auth.metadata != sessionMetadata(session) {
		return nil, errors.New("aider: unauthorized session")
	}
	bound, err := safeopen.Bind(auth.root)
	if err != nil {
		return nil, errors.New("aider: source changed")
	}
	defer bound.Close()
	if bound.Identity() != auth.rootIdentity {
		return nil, errors.New("aider: source changed")
	}
	file, identities, err := bound.OpenWithPathIdentity(fileName, maxFileBytes)
	if err != nil {
		return nil, errors.New("aider: source changed")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	info, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil || statErr != nil || closeErr != nil || len(data) > maxFileBytes || !linesWithinLimit(data) {
		return nil, errors.New("aider: source changed")
	}
	segments, valid := parseSegments(ctx, data)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !valid || auth.segmentIndex < 0 || auth.segmentIndex >= len(segments) || !os.SameFile(auth.fileInfo, info) || !samePathIdentity(auth.pathIdentity, pathIdentity{directories: identities}) {
		return nil, errors.New("aider: source changed")
	}
	part := segments[auth.segmentIndex]
	if part.marker != auth.marker || digest(part.raw) != auth.digest {
		return nil, errors.New("aider: source changed")
	}
	seed := "aider\x00" + identityString(bound.Identity()) + "\x00" + fileName + "\x00" + part.marker + "\x00" + strconv.Itoa(part.ordinal)
	if "aider:v1:"+digestPrefix(seed, 24) != session.ID {
		return nil, errors.New("aider: source changed")
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, value := range part.events {
		if encoder.Encode(value) != nil {
			return nil, errors.New("aider: source changed")
		}
	}
	return io.NopCloser(bytes.NewReader(output.Bytes())), nil
}
