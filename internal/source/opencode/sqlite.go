package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sqliteread"
)

const (
	maxDatabaseBytes    int64 = 256 << 20
	maxDatabaseSessions       = 10_000
	maxDatabaseMessages       = 10_000
	maxDatabaseParts          = 50_000
	databaseFormat            = "db-v2"
	databaseRefPrefix         = "db:"
)

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) (*sql.Row, error)
}

type sqliteReadFunc func(context.Context, string, string, int64, func(sqliteQueryer) error) error

func defaultSQLiteRead(ctx context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
	return sqliteread.WithReadOnlyTx(ctx, root, path, maxBytes, func(tx *sqliteread.ReadTx) error { return fn(tx) })
}

var requiredSQLiteColumns = map[string][]string{
	"project": {"id", "worktree"},
	"session": {"id", "project_id", "parent_id", "directory", "time_created", "time_updated"},
	"message": {"id", "session_id", "time_created", "data"},
	"part":    {"id", "message_id", "session_id", "time_created", "data"},
}

type sqliteMeta struct {
	id, projectID, parentID, directory, worktree string
	created, updated                             int64
	usage                                        map[string]int64
}

type sqliteSchema struct{ columns map[string]map[string]bool }

func validateSQLiteSchema(ctx context.Context, tx sqliteQueryer) (sqliteSchema, error) {
	schema := sqliteSchema{columns: map[string]map[string]bool{}}
	for _, table := range []string{"project", "session", "message", "part"} {
		rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return sqliteSchema{}, errors.New("opencode: unsupported database schema")
		}
		columns := map[string]bool{}
		for rows.Next() {
			var cid, notnull, primaryKey int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return sqliteSchema{}, errors.New("opencode: unsupported database schema")
			}
			columns[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return sqliteSchema{}, errors.New("opencode: unsupported database schema")
		}
		for _, name := range requiredSQLiteColumns[table] {
			if !columns[name] {
				return sqliteSchema{}, errors.New("opencode: unsupported database schema")
			}
		}
		schema.columns[table] = columns
	}
	return schema, nil
}

var sqliteUsageColumns = []struct{ column, key string }{
	{"tokens_input", "input_tokens"},
	{"tokens_output", "output_tokens"},
	{"tokens_reasoning", "reasoning_tokens"},
	{"tokens_cache_read", "cache_read_tokens"},
	{"tokens_cache_write", "cache_write_tokens"},
}

func sqliteMetaProjection(schema sqliteSchema) string {
	projection := `s.id,s.project_id,COALESCE(s.parent_id,''),s.directory,p.worktree,s.time_created,s.time_updated`
	for _, usage := range sqliteUsageColumns {
		if schema.columns["session"][usage.column] {
			projection += ",s." + usage.column
		} else {
			projection += ",0"
		}
	}
	return projection
}

func scanSQLiteMeta(scanner interface{ Scan(...any) error }, schema sqliteSchema) (sqliteMeta, error) {
	var m sqliteMeta
	var values [5]int64
	err := scanner.Scan(&m.id, &m.projectID, &m.parentID, &m.directory, &m.worktree, &m.created, &m.updated,
		&values[0], &values[1], &values[2], &values[3], &values[4])
	if err == nil {
		m.usage = map[string]int64{}
		for index, usage := range sqliteUsageColumns {
			if schema.columns["session"][usage.column] {
				m.usage[usage.key] = values[index]
			}
		}
	}
	return m, err
}

func listSQLiteMeta(ctx context.Context, tx sqliteQueryer, schema sqliteSchema) ([]sqliteMeta, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+sqliteMetaProjection(schema)+` FROM session AS s JOIN project AS p ON p.id=s.project_id ORDER BY s.time_created,s.id LIMIT ?`, maxDatabaseSessions+1)
	if err != nil {
		return nil, errors.New("opencode: unsupported database schema")
	}
	defer rows.Close()
	var out []sqliteMeta
	for rows.Next() {
		m, err := scanSQLiteMeta(rows, schema)
		if err != nil {
			return nil, errors.New("opencode: unsupported database schema")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil || len(out) > maxDatabaseSessions {
		return nil, errors.New("opencode: unsupported database schema")
	}
	return out, nil
}

func getSQLiteMeta(ctx context.Context, tx sqliteQueryer, schema sqliteSchema, id string) (sqliteMeta, error) {
	row, err := tx.QueryRowContext(ctx, `SELECT `+sqliteMetaProjection(schema)+` FROM session AS s JOIN project AS p ON p.id=s.project_id WHERE s.id=?`, id)
	if err != nil {
		return sqliteMeta{}, err
	}
	return scanSQLiteMeta(row, schema)
}

func (a *Adapter) discoverSQLite(ctx context.Context) ([]source.Session, map[string]authorization, error) {
	path := filepath.Join(a.root, "opencode.db")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, map[string]authorization{}, nil
	} else if err != nil {
		return nil, nil, err
	}
	var sessions []source.Session
	auths := map[string]authorization{}
	err := a.sqliteRead(ctx, a.root, path, maxDatabaseBytes, func(tx sqliteQueryer) error {
		schema, err := validateSQLiteSchema(ctx, tx)
		if err != nil {
			return err
		}
		metas, err := listSQLiteMeta(ctx, tx, schema)
		if err != nil {
			return err
		}
		for _, meta := range metas {
			if err := ctx.Err(); err != nil {
				return err
			}
			session, _, digest, err := loadSQLiteSession(ctx, tx, meta)
			if err != nil {
				continue
			}
			sessions = append(sessions, session)
			auths[session.OpaqueRef] = authorization{id: session.ID, digest: digest, format: databaseFormat, ref: meta.id}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil, err
		}
		return nil, nil, source.NewDiscoveryError(source.SourceFormatUnsupported, errors.New("opencode: unsupported database schema"))
	}
	return sessions, auths, nil
}

func loadSQLiteSession(ctx context.Context, tx sqliteQueryer, meta sqliteMeta) (source.Session, []byte, string, error) {
	if meta.id == "" || strings.ContainsAny(meta.id, `/\\#`) {
		return source.Session{}, nil, "", errors.New("opencode: invalid database session")
	}
	bad := 0
	messageRows, err := tx.QueryContext(ctx, `SELECT id,session_id,time_created,data FROM message WHERE session_id=? ORDER BY time_created,id LIMIT ?`, meta.id, maxDatabaseMessages+1)
	if err != nil {
		return source.Session{}, nil, "", err
	}
	messageRowsSeen := 0
	messages := map[string]messageFile{}
	for messageRows.Next() {
		var id, sessionID, data string
		var created int64
		if err := messageRows.Scan(&id, &sessionID, &created, &data); err != nil {
			messageRows.Close()
			return source.Session{}, nil, "", err
		}
		messageRowsSeen++
		if len(data) > int(maxFileBytes) {
			bad++
			continue
		}
		var envelope messageFile
		if json.Unmarshal([]byte(data), &envelope) != nil || id == "" || sessionID != meta.id || (envelope.Role != "user" && envelope.Role != "assistant") {
			bad++
			continue
		}
		envelope.ID, envelope.SessionID, envelope.Time.Created = id, sessionID, created
		messages[id] = envelope
	}
	err = messageRows.Err()
	messageRows.Close()
	if err != nil || messageRowsSeen > maxDatabaseMessages {
		return source.Session{}, nil, "", errors.New("opencode: database session exceeds limit")
	}
	partRows, err := tx.QueryContext(ctx, `SELECT id,message_id,session_id,time_created,data FROM part WHERE session_id=? ORDER BY time_created,id LIMIT ?`, meta.id, maxDatabaseParts+1)
	if err != nil {
		return source.Session{}, nil, "", err
	}
	type orderedPart struct {
		created        int64
		messageCreated int64
		id             string
		message        messageFile
		part           partFile
	}
	partRowsSeen := 0
	var ordered []orderedPart
	for partRows.Next() {
		var id, messageID, sessionID, data string
		var created int64
		if err := partRows.Scan(&id, &messageID, &sessionID, &created, &data); err != nil {
			partRows.Close()
			return source.Session{}, nil, "", err
		}
		partRowsSeen++
		if len(data) > int(maxFileBytes) {
			bad++
			continue
		}
		var envelope partFile
		message, messageOK := messages[messageID]
		if json.Unmarshal([]byte(data), &envelope) != nil || id == "" || messageID == "" || sessionID != meta.id || !messageOK {
			bad++
			continue
		}
		envelope.ID, envelope.MessageID, envelope.SessionID, envelope.Time.Created = id, messageID, sessionID, created
		ordered = append(ordered, orderedPart{created: created, messageCreated: message.Time.Created, id: id, message: message, part: envelope})
	}
	err = partRows.Err()
	partRows.Close()
	if err != nil || partRowsSeen > maxDatabaseParts {
		return source.Session{}, nil, "", errors.New("opencode: database session exceeds limit")
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].created == ordered[j].created {
			if ordered[i].messageCreated == ordered[j].messageCreated {
				if ordered[i].message.ID == ordered[j].message.ID {
					return ordered[i].id < ordered[j].id
				}
				return ordered[i].message.ID < ordered[j].message.ID
			}
			return ordered[i].messageCreated < ordered[j].messageCreated
		}
		return ordered[i].created < ordered[j].created
	})
	var events []event
	for _, item := range ordered {
		mapped, recognized, valid := mapSQLitePart(item.message, item.part, item.created)
		if !recognized || !valid {
			bad++
			continue
		}
		events = append(events, mapped...)
	}
	if len(messages) == 0 || len(events) == 0 {
		return source.Session{}, nil, "", errors.New("opencode: no valid database messages")
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil || output.Len() > int(maxSessionBytes) {
			return source.Session{}, nil, "", errors.New("opencode: database session exceeds limit")
		}
	}
	scopeRoot := meta.directory
	if !filepath.IsAbs(scopeRoot) || filepath.Clean(scopeRoot) != scopeRoot {
		scopeRoot = meta.worktree
	}
	scope := source.ScopeRef{Type: source.ScopeProject, Root: scopeRoot, Label: filepath.Base(scopeRoot)}
	if !filepath.IsAbs(scopeRoot) || filepath.Clean(scopeRoot) != scopeRoot {
		sum := sha256.Sum256([]byte("opencode\x00" + meta.projectID))
		scope = source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("%x", sum[:12]), Label: "OpenCode sessions"}
	}
	parent := ""
	if meta.parentID != "" {
		parent = "opencode:" + meta.parentID
	}
	session := source.Session{
		ID: "opencode:" + meta.id, Product: "opencode", FormatVersion: databaseFormat, AdapterVersion: "1",
		Capabilities: []source.Capability{"messages", "tools"}, Scope: scope,
		StartedAt: time.UnixMilli(meta.created), EndedAt: time.UnixMilli(meta.updated), MessageCount: len(messages),
		MalformedCount: bad, ParentID: parent,
		Usage:     meta.usage,
		OpaqueRef: databaseRefPrefix + meta.id,
	}
	metadata, err := json.Marshal(struct {
		ID, Parent string
		Scope      source.ScopeRef
		Started    int64
		Ended      int64
		Messages   int
		Malformed  int
		Usage      map[string]int64
	}{session.ID, session.ParentID, session.Scope, meta.created, meta.updated, session.MessageCount, session.MalformedCount, session.Usage})
	if err != nil {
		return source.Session{}, nil, "", errors.New("opencode: database metadata encoding failed")
	}
	hash := sha256.New()
	hash.Write(metadata)
	hash.Write([]byte{0})
	hash.Write(output.Bytes())
	digest := hash.Sum(nil)
	session.SnapshotID = fmt.Sprintf("%x", digest[:])
	return session, output.Bytes(), session.SnapshotID, nil
}

func mapSQLitePart(message messageFile, part partFile, created int64) ([]event, bool, bool) {
	timestamp := time.UnixMilli(created).UTC().Format(time.RFC3339Nano)
	model := message.ModelID
	if model == "" {
		model = message.Model.ModelID
	}
	switch part.Type {
	case "text":
		if strings.TrimSpace(part.Text) == "" {
			return nil, true, false
		}
		return []event{{Type: "message", Role: message.Role, Content: []any{map[string]any{"type": "text", "text": part.Text}}, Timestamp: timestamp, Model: model}}, true, true
	case "reasoning":
		if message.Role != "assistant" || strings.TrimSpace(part.Text) == "" {
			return nil, true, false
		}
		return []event{{Type: "message", Role: "assistant", Content: []any{map[string]any{"type": "thinking", "thinking": part.Text}}, Timestamp: timestamp, Model: model}}, true, true
	case "tool":
		if message.Role != "assistant" || part.Tool == "" || part.CallID == "" {
			return nil, true, false
		}
		var input any = map[string]any{}
		if len(part.State.Input) > 0 && json.Unmarshal(part.State.Input, &input) != nil {
			return nil, true, false
		}
		events := []event{{Type: "tool_use", Timestamp: timestamp, Model: model, CallID: part.CallID, Name: part.Tool, Input: input}}
		switch part.State.Status {
		case "completed":
			if part.State.Output == nil {
				return nil, true, false
			}
			events = append(events, event{Type: "tool_result", Timestamp: timestamp, CallID: part.CallID, Result: part.State.Output})
		case "error":
			if part.State.Error == nil {
				return nil, true, false
			}
			events = append(events, event{Type: "tool_result", Timestamp: timestamp, CallID: part.CallID, Result: part.State.Error})
		}
		return events, true, true
	default:
		return nil, false, false
	}
}

func (a *Adapter) openSQLite(ctx context.Context, auth authorization, expected source.Session) (io.ReadCloser, error) {
	path := filepath.Join(a.root, "opencode.db")
	var output []byte
	err := a.sqliteRead(ctx, a.root, path, maxDatabaseBytes, func(tx sqliteQueryer) error {
		schema, err := validateSQLiteSchema(ctx, tx)
		if err != nil {
			return err
		}
		meta, err := getSQLiteMeta(ctx, tx, schema, auth.ref)
		if err != nil {
			return err
		}
		session, current, digest, err := loadSQLiteSession(ctx, tx, meta)
		if err != nil || digest != auth.digest || digest != expected.SnapshotID || !sameSessionMetadata(session, expected) {
			return errors.New("opencode: source changed since discovery")
		}
		output = append([]byte(nil), current...)
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, errors.New("opencode: source changed since discovery")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}

func sameSessionMetadata(left, right source.Session) bool {
	return left.ID == right.ID &&
		left.Product == right.Product &&
		left.FormatVersion == right.FormatVersion &&
		left.AdapterVersion == right.AdapterVersion &&
		slices.Equal(left.Capabilities, right.Capabilities) &&
		left.Scope == right.Scope &&
		left.StartedAt.Equal(right.StartedAt) &&
		left.EndedAt.Equal(right.EndedAt) &&
		left.MessageCount == right.MessageCount &&
		left.ParentID == right.ParentID &&
		maps.Equal(left.Usage, right.Usage) &&
		left.MalformedCount == right.MalformedCount &&
		left.OpaqueRef == right.OpaqueRef
}
