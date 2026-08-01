package codeflicker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	_ "modernc.org/sqlite"
)

const maxDatabaseBlobBytes = 4 << 20
const databaseFormat = "sqlite"
const databaseOpaquePrefix = "sqlite:"

type databaseBlob struct {
	SessionID     string            `json:"sessionId"`
	WorkspaceURI  string            `json:"workspaceUri"`
	ChatModel     string            `json:"chatModel"`
	LocalMessages []json.RawMessage `json:"localMessages"`
}

type localMessage struct {
	TS         int64           `json:"ts"`
	Role       string          `json:"role"`
	Type       string          `json:"type"`
	Say        string          `json:"say"`
	Text       string          `json:"text"`
	JSONText   json.RawMessage `json:"jsonText"`
	Partial    bool            `json:"partial"`
	ToolCallID string          `json:"toolCallId"`
	ChatModel  string          `json:"chatModel"`
}

type databaseEvent struct {
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

func sqliteReadOnlyURI(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("codeflicker: invalid database path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("codeflicker: unsafe database")
	}
	return sqliteReadOnlyURIForOS(path, runtime.GOOS)
}

func sqliteReadOnlyURIForOS(databasePath, goos string) (string, error) {
	if hasUnsafePathByte(databasePath) {
		return "", errors.New("codeflicker: invalid database path")
	}
	var uri url.URL
	uri.Scheme = "file"
	if goos == "windows" {
		slashed := strings.ReplaceAll(databasePath, `\`, "/")
		if strings.HasPrefix(slashed, "//") {
			parts := strings.Split(strings.TrimPrefix(slashed, "//"), "/")
			if len(parts) < 2 || !validFileHost(parts[0]) || parts[1] == "" {
				return "", errors.New("codeflicker: invalid UNC database path")
			}
			uncPath := "/" + strings.Join(parts[1:], "/")
			if pathpkg.Clean(uncPath) != uncPath {
				return "", errors.New("codeflicker: unclean UNC database path")
			}
			uri.Host, uri.Path = parts[0], uncPath
		} else {
			if !validWindowsDrivePath(slashed) || pathpkg.Clean(slashed) != slashed {
				return "", errors.New("codeflicker: invalid Windows database path")
			}
			uri.Path = "/" + slashed
		}
	} else {
		if !pathpkg.IsAbs(databasePath) || pathpkg.Clean(databasePath) != databasePath {
			return "", errors.New("codeflicker: invalid Unix database path")
		}
		uri.Path = databasePath
	}
	uri.RawQuery = "mode=ro"
	return uri.String(), nil
}

func openReadOnlyDatabase(path string) (*sql.DB, error) {
	uri, err := sqliteReadOnlyURI(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}

func (a *Adapter) discoverDatabase(ctx context.Context) ([]source.Session, map[string][sha256.Size]byte, error) {
	if a.dbPath == "" {
		return nil, nil, nil
	}
	if _, err := os.Lstat(a.dbPath); os.IsNotExist(err) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	db, err := openReadOnlyDatabase(a.dbPath)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx,
		`SELECT key, updatedAt FROM KwaipilotKV WHERE key LIKE 'composerData:%'`)
	if err != nil {
		return nil, nil, fmt.Errorf("codeflicker: list sessions: %w", err)
	}
	defer rows.Close()
	type meta struct {
		key string
	}
	var metas []meta
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		var key string
		var updatedAt int64
		if err := rows.Scan(&key, &updatedAt); err != nil {
			return nil, nil, err
		}
		_ = updatedAt
		id := strings.TrimPrefix(key, "composerData:")
		if key != "composerData:"+id || !validDatabaseSessionID(id) {
			continue
		}
		metas = append(metas, meta{key: key})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].key < metas[j].key })
	out := make([]source.Session, 0, len(metas))
	digests := make(map[string][sha256.Size]byte, len(metas))
	seen := make(map[string]bool, len(metas))
	for _, item := range metas {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		blob, err := queryDatabaseBlob(ctx, db, item.key)
		if err != nil {
			continue
		}
		session, _, digest, err := parseDatabaseBlobContext(ctx, blob, item.key)
		if err != nil || seen[session.ID] {
			continue
		}
		seen[session.ID] = true
		out = append(out, session)
		digests[session.OpaqueRef] = digest
	}
	return out, digests, nil
}

func queryDatabaseBlob(ctx context.Context, db *sql.DB, key string) ([]byte, error) {
	var blob []byte
	if err := db.QueryRowContext(ctx,
		`SELECT value FROM KwaipilotKV WHERE key = ?`, key).Scan(&blob); err != nil {
		return nil, err
	}
	if len(blob) == 0 || len(blob) > maxDatabaseBlobBytes {
		return nil, errors.New("codeflicker: database session exceeds limit")
	}
	return blob, nil
}

func validDatabaseSessionID(id string) bool {
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

func parseDatabaseBlob(blob []byte, key string) (source.Session, []byte, [sha256.Size]byte, error) {
	return parseDatabaseBlobContext(context.Background(), blob, key)
}

func parseDatabaseBlobContext(ctx context.Context, blob []byte, key string) (source.Session, []byte, [sha256.Size]byte, error) {
	if err := ctx.Err(); err != nil {
		return source.Session{}, nil, [sha256.Size]byte{}, err
	}
	var data databaseBlob
	if json.Unmarshal(blob, &data) != nil {
		return source.Session{}, nil, [sha256.Size]byte{}, errors.New("codeflicker: malformed blob")
	}
	id := strings.TrimPrefix(key, "composerData:")
	if !validDatabaseSessionID(id) || data.SessionID != id {
		return source.Session{}, nil, [sha256.Size]byte{}, errors.New("codeflicker: session identity mismatch")
	}
	cwd, err := workspacePath(data.WorkspaceURI)
	if err != nil {
		return source.Session{}, nil, [sha256.Size]byte{}, err
	}
	var output bytes.Buffer
	messageCount, malformed := 0, 0
	var start, end time.Time
	for _, raw := range data.LocalMessages {
		if err := ctx.Err(); err != nil {
			return source.Session{}, nil, [sha256.Size]byte{}, err
		}
		var message localMessage
		if json.Unmarshal(raw, &message) != nil {
			malformed++
			continue
		}
		events, recognized, valid := mapDatabaseMessage(message, data.ChatModel)
		if !recognized {
			continue
		}
		if !valid {
			malformed++
			continue
		}
		messageCount++
		for _, event := range events {
			encoded, _ := json.Marshal(event)
			output.Write(encoded)
			output.WriteByte('\n')
		}
		ts := time.UnixMilli(message.TS).UTC()
		if message.TS > 0 {
			if start.IsZero() || ts.Before(start) {
				start = ts
			}
			if ts.After(end) {
				end = ts
			}
		}
	}
	if messageCount == 0 {
		return source.Session{}, nil, [sha256.Size]byte{}, errors.New("codeflicker: no valid messages")
	}
	digest := sha256.Sum256(blob)
	scope := databaseScope(cwd, id)
	session := source.Session{
		ID: "codeflicker:" + id, Product: "codeflicker",
		FormatVersion: databaseFormat, AdapterVersion: "1",
		Capabilities: []source.Capability{"messages", "tools"},
		Scope:        scope,
		StartedAt:    start, EndedAt: end, MessageCount: messageCount,
		MalformedCount: malformed, OpaqueRef: databaseOpaquePrefix + key,
	}
	return session, output.Bytes(), digest, nil
}

func databaseScope(cwd, id string) source.ScopeRef {
	slashed := filepath.ToSlash(cwd)
	if strings.Contains(slashed, "/workspaceStorage/") &&
		strings.Contains(strings.ToLower(filepath.Base(cwd)), "codeflicker") {
		sum := sha256.Sum256([]byte("codeflicker\x00" + id))
		return source.ScopeRef{
			Type:  source.ScopeSessionCollection,
			Root:  fmt.Sprintf("%x", sum[:12]),
			Label: "CodeFlicker sessions",
		}
	}
	return source.ScopeRef{Type: source.ScopeProject, Root: cwd, Label: filepath.Base(cwd)}
}

func workspacePath(raw string) (string, error) {
	return workspacePathForOS(raw, runtime.GOOS)
}

func workspacePathForOS(raw, goos string) (string, error) {
	uri, err := url.Parse(raw)
	if err != nil || uri.Scheme != "file" || uri.Opaque != "" || uri.User != nil ||
		uri.RawQuery != "" || uri.Fragment != "" {
		return "", errors.New("codeflicker: invalid workspace URI")
	}
	uriPath := uri.Path // net/url has already validated and decoded escapes once.
	if hasUnsafePathByte(uriPath) || strings.Contains(uriPath, `\`) {
		return "", errors.New("codeflicker: unsafe workspace path")
	}
	if goos == "windows" {
		if uri.Host != "" {
			if !validFileHost(uri.Host) || uriPath == "" || uriPath[0] != '/' ||
				pathpkg.Clean(uriPath) != uriPath {
				return "", errors.New("codeflicker: invalid workspace UNC")
			}
			parts := strings.Split(strings.TrimPrefix(uriPath, "/"), "/")
			if len(parts) == 0 || parts[0] == "" {
				return "", errors.New("codeflicker: missing workspace share")
			}
			return `\\` + uri.Host + `\` + strings.Join(parts, `\`), nil
		}
		if len(uriPath) < 4 || uriPath[0] != '/' ||
			!validWindowsDrivePath(uriPath[1:]) ||
			pathpkg.Clean(uriPath) != uriPath {
			return "", errors.New("codeflicker: invalid Windows workspace path")
		}
		return strings.ReplaceAll(uriPath[1:], "/", `\`), nil
	}
	if uri.Host != "" && uri.Host != "localhost" {
		return "", errors.New("codeflicker: remote Unix workspace URI")
	}
	if !pathpkg.IsAbs(uriPath) || pathpkg.Clean(uriPath) != uriPath {
		return "", errors.New("codeflicker: invalid Unix workspace path")
	}
	return uriPath, nil
}

func validWindowsDrivePath(value string) bool {
	return len(value) >= 3 &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) &&
		value[1] == ':' && value[2] == '/'
}

func validFileHost(host string) bool {
	if host == "" || strings.ContainsAny(host, `/\:@`) {
		return false
	}
	return !hasUnsafePathByte(host)
}

func hasUnsafePathByte(value string) bool {
	for _, char := range value {
		if char == 0 || char < 0x20 || char == 0x7f {
			return true
		}
	}
	return false
}

func mapDatabaseMessage(message localMessage, defaultModel string) ([]databaseEvent, bool, bool) {
	switch message.Say {
	case "api_req_started", "checkpoint_created", "summarize_messages",
		"context_sync", "read_lints_result", "completion_result":
		return nil, false, true
	}
	if message.Partial {
		return nil, false, true
	}
	if message.TS <= 0 {
		return nil, true, false
	}
	model := message.ChatModel
	if model == "" {
		model = defaultModel
	}
	timestamp := time.UnixMilli(message.TS).UTC().Format(time.RFC3339Nano)
	switch message.Say {
	case "text":
		role := message.Role
		if role == "" {
			role = "assistant"
		}
		if (role != "user" && role != "assistant") || strings.TrimSpace(message.Text) == "" {
			return nil, true, false
		}
		return []databaseEvent{{
			Type: "message", Role: role, Timestamp: timestamp, Model: model,
			Content: []any{map[string]any{"type": "text", "text": message.Text}},
		}}, true, true
	case "thinking":
		if (message.Role != "" && message.Role != "assistant") || strings.TrimSpace(message.Text) == "" {
			return nil, true, false
		}
		return []databaseEvent{{
			Type: "message", Role: "assistant", Timestamp: timestamp, Model: model,
			Content: []any{map[string]any{"type": "thinking", "thinking": message.Text}},
		}}, true, true
	case "tool", "":
		if message.ToolCallID == "" {
			return nil, true, false
		}
		var input map[string]any
		raw := message.JSONText
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			raw = []byte(message.Text)
		}
		if json.Unmarshal(raw, &input) != nil {
			return nil, true, false
		}
		name, _ := input["tool"].(string)
		if name == "" {
			return nil, true, false
		}
		delete(input, "tool")
		result, hasResult := input["output"]
		if !hasResult {
			result, hasResult = input["content"]
		}
		if failure, exists := input["error"]; exists {
			result, hasResult = map[string]any{"error": failure}, true
		}
		delete(input, "output")
		delete(input, "content")
		delete(input, "error")
		events := []databaseEvent{{
			Type: "tool_use", Timestamp: timestamp, Model: model,
			CallID: message.ToolCallID, Name: name, Input: input,
		}}
		if hasResult {
			events = append(events, databaseEvent{
				Type: "tool_result", Timestamp: timestamp,
				CallID: message.ToolCallID, Result: result,
			})
		}
		return events, true, true
	case "tool_error":
		if message.ToolCallID == "" || strings.TrimSpace(message.Text) == "" {
			return nil, true, false
		}
		return []databaseEvent{{
			Type: "tool_result", Timestamp: timestamp, CallID: message.ToolCallID,
			Result: map[string]any{"error": message.Text},
		}}, true, true
	default:
		return nil, true, false
	}
}

func (a *Adapter) openDatabase(ctx context.Context, session source.Session, expectedDigest [sha256.Size]byte) (io.ReadCloser, error) {
	key := strings.TrimPrefix(session.OpaqueRef, databaseOpaquePrefix)
	if session.OpaqueRef != databaseOpaquePrefix+key ||
		key != "composerData:"+strings.TrimPrefix(session.ID, "codeflicker:") {
		return nil, errors.New("codeflicker: invalid database reference")
	}
	db, err := openReadOnlyDatabase(a.dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	blob, err := queryDatabaseBlob(ctx, db, key)
	if err != nil {
		return nil, err
	}
	current, output, digest, err := parseDatabaseBlobContext(ctx, blob, key)
	if err != nil {
		return nil, err
	}
	if digest != expectedDigest || !reflect.DeepEqual(current, session) {
		return nil, errors.New("codeflicker: database session changed")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
