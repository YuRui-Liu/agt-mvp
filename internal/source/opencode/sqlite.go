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

var errSQLiteSessionMalformed = errors.New("opencode: malformed database session")
var errSQLiteBudgetExceeded = errors.New("opencode: database scan budget exceeded")

type sqliteScanLimits struct {
	maxSessions, maxRows               int
	maxPayloadBytes, maxCanonicalBytes int64
}

var defaultSQLiteScanLimits = sqliteScanLimits{
	maxSessions: maxDatabaseSessions, maxRows: 200_000,
	maxPayloadBytes: 256 << 20, maxCanonicalBytes: 64 << 20,
}

type sqliteScanBudget struct {
	limits                       sqliteScanLimits
	sessions, rows               int
	payloadBytes, canonicalBytes int64
}

func (b *sqliteScanBudget) consumeSessionRow() error {
	b.sessions++
	if b.sessions > b.limits.maxSessions {
		return errSQLiteBudgetExceeded
	}
	return b.consumeRow()
}

func (b *sqliteScanBudget) consumeRow() error {
	b.rows++
	if b.rows > b.limits.maxRows {
		return errSQLiteBudgetExceeded
	}
	return nil
}

func (b *sqliteScanBudget) consumePayload(size int) error {
	b.payloadBytes += int64(size)
	if b.payloadBytes > b.limits.maxPayloadBytes {
		return errSQLiteBudgetExceeded
	}
	return nil
}

func (b *sqliteScanBudget) consumeCanonical(size int) error {
	b.canonicalBytes += int64(size)
	if b.canonicalBytes > b.limits.maxCanonicalBytes {
		return errSQLiteBudgetExceeded
	}
	return nil
}

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) (*sql.Row, error)
}

type sqliteQueryKind string

const (
	sqliteQuerySchema sqliteQueryKind = "schema"
	sqliteQueryData   sqliteQueryKind = "data"
)

type sqliteQuerySpec struct {
	kind      sqliteQueryKind
	table     string
	columns   []string
	statement string
}

var observeSQLiteQuery func(sqliteQuerySpec)

func querySQLite(ctx context.Context, tx sqliteQueryer, spec sqliteQuerySpec, args ...any) (*sql.Rows, error) {
	if observeSQLiteQuery != nil {
		copySpec := spec
		copySpec.columns = append([]string(nil), spec.columns...)
		observeSQLiteQuery(copySpec)
	}
	return tx.QueryContext(ctx, spec.statement, args...)
}

func querySQLiteRow(ctx context.Context, tx sqliteQueryer, spec sqliteQuerySpec, args ...any) (*sql.Row, error) {
	if observeSQLiteQuery != nil {
		copySpec := spec
		copySpec.columns = append([]string(nil), spec.columns...)
		observeSQLiteQuery(copySpec)
	}
	return tx.QueryRowContext(ctx, spec.statement, args...)
}

var sqliteMessageQuery = sqliteQuerySpec{
	kind: sqliteQueryData, table: "message", columns: []string{"id", "session_id", "time_created", "data"},
	statement: `SELECT id,session_id,time_created,data FROM message WHERE session_id=? ORDER BY time_created,id LIMIT ?`,
}

var sqlitePartQuery = sqliteQuerySpec{
	kind: sqliteQueryData, table: "part", columns: []string{"id", "message_id", "session_id", "time_created", "data"},
	statement: `SELECT id,message_id,session_id,time_created,data FROM part WHERE session_id=? ORDER BY time_created,id LIMIT ?`,
}

type sqliteReadFunc func(context.Context, string, string, int64, func(sqliteQueryer) error) error

func defaultSQLiteRead(ctx context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
	return sqliteread.WithReadOnlyTx(ctx, root, path, maxBytes, func(tx *sqliteread.ReadTx) error { return fn(tx) })
}

type sqliteMeta struct {
	id, projectID, parentID, directory, worktree string
	created, updated                             int64
	createdPresent, updatedPresent               bool
	usage                                        map[string]int64
}

type sqliteColumn struct {
	kind                string
	notNull, primaryKey int
	hidden              int
}

type sqliteSchema struct {
	columns map[string]map[string]sqliteColumn
}

var sqliteTableListQuery = sqliteQuerySpec{kind: sqliteQuerySchema, statement: `PRAGMA table_list`}

var sqliteXInfoQueries = map[string]sqliteQuerySpec{
	"project": {kind: sqliteQuerySchema, table: "project", statement: `PRAGMA table_xinfo(project)`},
	"session": {kind: sqliteQuerySchema, table: "session", statement: `PRAGMA table_xinfo(session)`},
	"message": {kind: sqliteQuerySchema, table: "message", statement: `PRAGMA table_xinfo(message)`},
	"part":    {kind: sqliteQuerySchema, table: "part", statement: `PRAGMA table_xinfo(part)`},
}

var nativeSQLiteColumns = map[string]map[string]sqliteColumn{
	"project": {
		"id": {kind: "TEXT", primaryKey: 1}, "worktree": {kind: "TEXT", notNull: 1},
	},
	"session": {
		"id": {kind: "TEXT", primaryKey: 1}, "project_id": {kind: "TEXT", notNull: 1},
		"parent_id": {kind: "TEXT"}, "directory": {kind: "TEXT", notNull: 1},
		"time_created": {kind: "INTEGER", notNull: 1}, "time_updated": {kind: "INTEGER", notNull: 1},
	},
	"message": {
		"id": {kind: "TEXT", primaryKey: 1}, "session_id": {kind: "TEXT", notNull: 1},
		"time_created": {kind: "INTEGER", notNull: 1}, "data": {kind: "TEXT", notNull: 1},
	},
	"part": {
		"id": {kind: "TEXT", primaryKey: 1}, "message_id": {kind: "TEXT", notNull: 1},
		"session_id": {kind: "TEXT", notNull: 1}, "time_created": {kind: "INTEGER", notNull: 1},
		"data": {kind: "TEXT", notNull: 1},
	},
}

func validateSQLiteSchema(ctx context.Context, tx sqliteQueryer) (sqliteSchema, error) {
	rows, err := querySQLite(ctx, tx, sqliteTableListQuery)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return sqliteSchema{}, err
		}
		return sqliteSchema{}, errors.New("opencode: unsupported database schema")
	}
	objects := map[string]string{}
	for rows.Next() {
		var schemaName, name, objectType string
		var columns, withoutRowID, strict int
		if err := rows.Scan(&schemaName, &name, &objectType, &columns, &withoutRowID, &strict); err != nil {
			rows.Close()
			return sqliteSchema{}, errors.New("opencode: unsupported database schema")
		}
		if schemaName == "main" {
			objects[name] = objectType
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		if errors.Is(err, context.Canceled) {
			return sqliteSchema{}, err
		}
		return sqliteSchema{}, errors.New("opencode: unsupported database schema")
	}
	rows.Close()
	schema := sqliteSchema{columns: map[string]map[string]sqliteColumn{}}
	for _, table := range []string{"project", "session", "message", "part"} {
		if objects[table] != "table" {
			return sqliteSchema{}, errors.New("opencode: unsupported database schema")
		}
		rows, err := querySQLite(ctx, tx, sqliteXInfoQueries[table])
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return sqliteSchema{}, err
			}
			return sqliteSchema{}, errors.New("opencode: unsupported database schema")
		}
		columns := map[string]sqliteColumn{}
		for rows.Next() {
			var cid, notNull, primaryKey, hidden int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
				rows.Close()
				return sqliteSchema{}, errors.New("opencode: unsupported database schema")
			}
			columns[name] = sqliteColumn{kind: strings.ToUpper(kind), notNull: notNull, primaryKey: primaryKey, hidden: hidden}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return sqliteSchema{}, err
			}
			return sqliteSchema{}, errors.New("opencode: unsupported database schema")
		}
		for name, required := range nativeSQLiteColumns[table] {
			actual, ok := columns[name]
			if !ok || actual != required {
				return sqliteSchema{}, errors.New("opencode: unsupported database schema")
			}
		}
		if table == "session" {
			for _, usage := range sqliteUsageColumns {
				if actual, ok := columns[usage.column]; ok && (actual.kind != "INTEGER" || actual.hidden != 0 || actual.primaryKey != 0) {
					return sqliteSchema{}, errors.New("opencode: unsupported database schema")
				}
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
		if _, ok := schema.columns["session"][usage.column]; ok {
			projection += ",s." + usage.column
		} else {
			projection += ",0"
		}
	}
	return projection
}

func sqliteMetaQuerySpec(schema sqliteSchema, single bool) sqliteQuerySpec {
	columns := []string{"id", "project_id", "parent_id", "directory", "time_created", "time_updated"}
	for _, usage := range sqliteUsageColumns {
		if _, ok := schema.columns["session"][usage.column]; ok {
			columns = append(columns, usage.column)
		}
	}
	statement := `SELECT ` + sqliteMetaProjection(schema) + ` FROM session AS s LEFT JOIN project AS p ON p.id=s.project_id`
	if single {
		statement += ` WHERE s.id=?`
	} else {
		statement += ` ORDER BY s.time_created,s.id LIMIT ?`
	}
	return sqliteQuerySpec{kind: sqliteQueryData, table: "session", columns: columns, statement: statement}
}

func observeProjectJoin() {
	if observeSQLiteQuery != nil {
		observeSQLiteQuery(sqliteQuerySpec{kind: sqliteQueryData, table: "project", columns: []string{"id", "worktree"}})
	}
}

func scanSQLiteMeta(scanner interface{ Scan(...any) error }, schema sqliteSchema) (sqliteMeta, error) {
	var id, projectID, parentID, directory, worktree sql.NullString
	var created, updated sql.NullInt64
	var values [5]sql.NullInt64
	err := scanner.Scan(&id, &projectID, &parentID, &directory, &worktree, &created, &updated,
		&values[0], &values[1], &values[2], &values[3], &values[4])
	if err != nil || !id.Valid || id.String == "" || !projectID.Valid || projectID.String == "" || !worktree.Valid || worktree.String == "" {
		return sqliteMeta{}, errors.New("opencode: invalid database session metadata")
	}
	m := sqliteMeta{
		id: id.String, projectID: projectID.String, parentID: parentID.String,
		directory: directory.String, worktree: worktree.String,
		created: created.Int64, updated: updated.Int64,
		createdPresent: created.Valid, updatedPresent: updated.Valid,
		usage: map[string]int64{},
	}
	for index, usage := range sqliteUsageColumns {
		if _, ok := schema.columns["session"][usage.column]; ok && values[index].Valid {
			m.usage[usage.key] = values[index].Int64
		}
	}
	return m, nil
}

func listSQLiteMeta(ctx context.Context, tx sqliteQueryer, schema sqliteSchema, budget *sqliteScanBudget) ([]sqliteMeta, error) {
	observeProjectJoin()
	rows, err := querySQLite(ctx, tx, sqliteMetaQuerySpec(schema, false), maxDatabaseSessions+1)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, errors.New("opencode: unsupported database schema")
	}
	defer rows.Close()
	var out []sqliteMeta
	seen := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seen++
		if err := budget.consumeSessionRow(); err != nil {
			return nil, err
		}
		m, err := scanSQLiteMeta(rows, schema)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if seen > maxDatabaseSessions {
		return nil, errors.New("opencode: unsupported database schema")
	}
	return out, nil
}

func getSQLiteMeta(ctx context.Context, tx sqliteQueryer, schema sqliteSchema, id string) (sqliteMeta, error) {
	observeProjectJoin()
	row, err := querySQLiteRow(ctx, tx, sqliteMetaQuerySpec(schema, true), id)
	if err != nil {
		return sqliteMeta{}, err
	}
	return scanSQLiteMeta(row, schema)
}

func (a *Adapter) discoverSQLite(ctx context.Context) ([]source.Session, map[string]authorization, error) {
	return a.discoverSQLiteWithLimits(ctx, defaultSQLiteScanLimits)
}

func (a *Adapter) discoverSQLiteWithLimits(ctx context.Context, limits sqliteScanLimits) ([]source.Session, map[string]authorization, error) {
	path := filepath.Join(a.root, "opencode.db")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, map[string]authorization{}, nil
	} else if err != nil {
		return nil, nil, err
	}
	var sessions []source.Session
	auths := map[string]authorization{}
	budget := &sqliteScanBudget{limits: limits}
	err := a.sqliteRead(ctx, a.root, path, maxDatabaseBytes, func(tx sqliteQueryer) error {
		schema, err := validateSQLiteSchema(ctx, tx)
		if err != nil {
			return err
		}
		metas, err := listSQLiteMeta(ctx, tx, schema, budget)
		if err != nil {
			return err
		}
		for _, meta := range metas {
			if err := ctx.Err(); err != nil {
				return err
			}
			session, _, digest, err := loadSQLiteSession(ctx, tx, meta, budget)
			if err != nil {
				if errors.Is(err, errSQLiteSessionMalformed) {
					continue
				}
				return err
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

func loadSQLiteSession(ctx context.Context, tx sqliteQueryer, meta sqliteMeta, budget *sqliteScanBudget) (source.Session, []byte, string, error) {
	if meta.id == "" || strings.ContainsAny(meta.id, `/\\#`) {
		return source.Session{}, nil, "", errSQLiteSessionMalformed
	}
	bad := 0
	messageRows, err := querySQLite(ctx, tx, sqliteMessageQuery, meta.id, maxDatabaseMessages+1)
	if err != nil {
		return source.Session{}, nil, "", err
	}
	messageRowsSeen := 0
	messages := map[string]messageFile{}
	for messageRows.Next() {
		if err := ctx.Err(); err != nil {
			messageRows.Close()
			return source.Session{}, nil, "", err
		}
		messageRowsSeen++
		if err := budget.consumeRow(); err != nil {
			messageRows.Close()
			return source.Session{}, nil, "", err
		}
		var rawID, rawSessionID, rawCreated, rawData any
		if err := messageRows.Scan(&rawID, &rawSessionID, &rawCreated, &rawData); err != nil {
			messageRows.Close()
			return source.Session{}, nil, "", err
		}
		id, idOK := sqliteText(rawID)
		sessionID, sessionOK := sqliteText(rawSessionID)
		created, createdOK := sqliteInteger(rawCreated)
		data, dataOK := sqliteText(rawData)
		if dataOK {
			if err := budget.consumePayload(len(data)); err != nil {
				messageRows.Close()
				return source.Session{}, nil, "", err
			}
		}
		if !idOK || !sessionOK || !createdOK || !dataOK || id == "" || sessionID != meta.id {
			bad++
			continue
		}
		if len(data) > int(maxFileBytes) {
			bad++
			continue
		}
		var envelope messageFile
		if json.Unmarshal([]byte(data), &envelope) != nil || (envelope.Role != "user" && envelope.Role != "assistant") {
			bad++
			continue
		}
		envelope.ID, envelope.SessionID, envelope.Time.Created = id, sessionID, created
		messages[id] = envelope
	}
	err = messageRows.Err()
	messageRows.Close()
	if err != nil {
		return source.Session{}, nil, "", err
	}
	if messageRowsSeen > maxDatabaseMessages {
		return source.Session{}, nil, "", errSQLiteSessionMalformed
	}
	partRows, err := querySQLite(ctx, tx, sqlitePartQuery, meta.id, maxDatabaseParts+1)
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
		if err := ctx.Err(); err != nil {
			partRows.Close()
			return source.Session{}, nil, "", err
		}
		partRowsSeen++
		if err := budget.consumeRow(); err != nil {
			partRows.Close()
			return source.Session{}, nil, "", err
		}
		var rawID, rawMessageID, rawSessionID, rawCreated, rawData any
		if err := partRows.Scan(&rawID, &rawMessageID, &rawSessionID, &rawCreated, &rawData); err != nil {
			partRows.Close()
			return source.Session{}, nil, "", err
		}
		id, idOK := sqliteText(rawID)
		messageID, messageOK := sqliteText(rawMessageID)
		sessionID, sessionOK := sqliteText(rawSessionID)
		created, createdOK := sqliteInteger(rawCreated)
		data, dataOK := sqliteText(rawData)
		if dataOK {
			if err := budget.consumePayload(len(data)); err != nil {
				partRows.Close()
				return source.Session{}, nil, "", err
			}
		}
		if !idOK || !messageOK || !sessionOK || !createdOK || !dataOK || id == "" || messageID == "" || sessionID != meta.id {
			bad++
			continue
		}
		if len(data) > int(maxFileBytes) {
			bad++
			continue
		}
		var envelope partFile
		message, relationOK := messages[messageID]
		if json.Unmarshal([]byte(data), &envelope) != nil || !relationOK {
			bad++
			continue
		}
		envelope.ID, envelope.MessageID, envelope.SessionID, envelope.Time.Created = id, messageID, sessionID, created
		ordered = append(ordered, orderedPart{created: created, messageCreated: message.Time.Created, id: id, message: message, part: envelope})
	}
	err = partRows.Err()
	partRows.Close()
	if err != nil {
		return source.Session{}, nil, "", err
	}
	if partRowsSeen > maxDatabaseParts {
		return source.Session{}, nil, "", errSQLiteSessionMalformed
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
		if !recognized {
			continue
		}
		if !valid {
			bad++
			continue
		}
		events = append(events, mapped...)
	}
	if len(messages) == 0 || len(events) == 0 {
		return source.Session{}, nil, "", errSQLiteSessionMalformed
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, event := range events {
		before := output.Len()
		if err := encoder.Encode(event); err != nil || output.Len() > int(maxSessionBytes) {
			return source.Session{}, nil, "", errSQLiteSessionMalformed
		}
		if err := budget.consumeCanonical(output.Len() - before); err != nil {
			return source.Session{}, nil, "", err
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
	var startedAt, endedAt time.Time
	if meta.createdPresent {
		startedAt = time.UnixMilli(meta.created)
	}
	if meta.updatedPresent {
		endedAt = time.UnixMilli(meta.updated)
	}
	session := source.Session{
		ID: "opencode:" + meta.id, Product: "opencode", FormatVersion: databaseFormat, AdapterVersion: "1",
		Capabilities: []source.Capability{"messages", "tools"}, Scope: scope,
		StartedAt: startedAt, EndedAt: endedAt, MessageCount: len(messages),
		MalformedCount: bad, ParentID: parent,
		Usage:     meta.usage,
		OpaqueRef: databaseRefPrefix + meta.id,
	}
	metadata, err := json.Marshal(struct {
		ID, Parent string
		Scope      source.ScopeRef
		Started    int64
		Ended      int64
		StartedSet bool
		EndedSet   bool
		Messages   int
		Malformed  int
		Usage      map[string]int64
	}{session.ID, session.ParentID, session.Scope, meta.created, meta.updated, meta.createdPresent, meta.updatedPresent, session.MessageCount, session.MalformedCount, session.Usage})
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

func sqliteText(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case []byte:
		return string(typed), true
	default:
		return "", false
	}
}

func sqliteInteger(value any) (int64, bool) {
	integer, ok := value.(int64)
	return integer, ok
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
	budget := &sqliteScanBudget{limits: defaultSQLiteScanLimits}
	err := a.sqliteRead(ctx, a.root, path, maxDatabaseBytes, func(tx sqliteQueryer) error {
		schema, err := validateSQLiteSchema(ctx, tx)
		if err != nil {
			return err
		}
		meta, err := getSQLiteMeta(ctx, tx, schema, auth.ref)
		if err != nil {
			return err
		}
		session, current, digest, err := loadSQLiteSession(ctx, tx, meta, budget)
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
