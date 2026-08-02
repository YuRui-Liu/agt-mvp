package sharedclient

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sqliteread"
)

var (
	ErrUnsupportedSchema     = errors.New("sharedclient: unsupported database schema")
	ErrMalformedConversation = errors.New("sharedclient: malformed conversation")
)

// SchemaID is a closed identifier for an evidenced SharedClient schema.
type SchemaID uint8

const (
	LingmaIDEV1 SchemaID = iota + 1
	QoderIDEV1
)

type QueryKind string

const (
	QuerySchema QueryKind = "schema"
	QueryData   QueryKind = "data"
)

// QueryEvent contains structural query metadata only. It never includes the
// database path, parameters, or values returned by SQLite.
type QueryEvent struct {
	Kind    QueryKind
	Table   string
	Columns []string
}

type readOptions struct{ observer func(QueryEvent) }

// Option configures one WithChatSnapshot call.
type Option func(*readOptions)

// WithQueryObserver installs a synchronous structural observer. The observer
// must not call methods on the ChatReader from the same WithChatSnapshot call.
func WithQueryObserver(observer func(QueryEvent)) Option {
	return func(options *readOptions) { options.observer = observer }
}

type SessionRow struct {
	ID, ProjectID                                 string
	CreatedAt, ModifiedAt, LastUserQueryAt        int64
	SessionType, Mode, Version, Status            string
	StopReason, ParentSessionID, ParentToolCallID string
}

type RecordRow struct {
	RequestID, SessionID               string
	Question, Answer, ReasoningContent string
	CreatedAt, ModifiedAt              int64
	FinishStatus                       int64
}

type MessageRow struct {
	ID, SessionID, RequestID  string
	Role, Content, ToolResult string
	CreatedAt                 int64
}

type SnapshotRow struct {
	ID, SessionID, RecordID, Status string
	CreatedAt, ModifiedAt           int64
}

type Conversation struct {
	Session   SessionRow
	Records   []RecordRow
	Messages  []MessageRow
	Snapshots []SnapshotRow
}

// ChatReader deliberately exposes no transaction, SQL, or generic query API.
// Its methods are safe for concurrent use and are serialized to preserve one
// aggregate budget across the snapshot.
type ChatReader interface {
	ListSessions(context.Context) ([]SessionRow, error)
	ReadConversation(context.Context, string) (Conversation, error)
}

type querySpec struct {
	kind      QueryKind
	table     string
	columns   []string
	statement string
}

var fixedSchemaQueries = []querySpec{
	{kind: QuerySchema, columns: []string{"schema", "name", "type", "ncol", "wr", "strict"}, statement: `PRAGMA table_list`},
	{kind: QuerySchema, table: "chat_session", columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}, statement: `PRAGMA table_xinfo(chat_session)`},
	{kind: QuerySchema, table: "chat_record", columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}, statement: `PRAGMA table_xinfo(chat_record)`},
	{kind: QuerySchema, table: "chat_message", columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}, statement: `PRAGMA table_xinfo(chat_message)`},
	{kind: QuerySchema, table: "chat_snapshot", columns: []string{"cid", "name", "type", "notnull", "dflt_value", "pk", "hidden"}, statement: `PRAGMA table_xinfo(chat_snapshot)`},
}

var (
	lingmaSessionColumns = []string{"session_id", "project_id", "gmt_create", "gmt_modified", "session_type", "mode", "version", "stop_reason", "parent_session_id", "parent_tool_call_id"}
	qoderSessionColumns  = []string{"session_id", "project_id", "gmt_create", "gmt_modified", "session_type", "mode", "version", "status", "last_user_query_at", "stop_reason", "parent_session_id", "parent_tool_call_id"}
	recordColumns        = []string{"request_id", "session_id", "question", "answer", "reasoning_content", "gmt_create", "gmt_modified", "finish_status"}
	messageColumns       = []string{"id", "session_id", "request_id", "role", "content", "tool_result", "gmt_create"}
	snapshotColumns      = []string{"snapshot_id", "session_id", "chat_record_id", "status", "gmt_create", "gmt_modified"}
)

var (
	lingmaSessionListQuery = querySpec{kind: QueryData, table: "chat_session", columns: lingmaSessionColumns,
		statement: `SELECT session_id,project_id,gmt_create,gmt_modified,session_type,mode,version,stop_reason,parent_session_id,parent_tool_call_id FROM chat_session ORDER BY gmt_create,session_id LIMIT ?`}
	lingmaSessionOneQuery = querySpec{kind: QueryData, table: "chat_session", columns: lingmaSessionColumns,
		statement: `SELECT session_id,project_id,gmt_create,gmt_modified,session_type,mode,version,stop_reason,parent_session_id,parent_tool_call_id FROM chat_session WHERE session_id=? LIMIT ?`}
	qoderSessionListQuery = querySpec{kind: QueryData, table: "chat_session", columns: qoderSessionColumns,
		statement: `SELECT session_id,project_id,gmt_create,gmt_modified,session_type,mode,version,status,last_user_query_at,stop_reason,parent_session_id,parent_tool_call_id FROM chat_session ORDER BY gmt_create,session_id LIMIT ?`}
	qoderSessionOneQuery = querySpec{kind: QueryData, table: "chat_session", columns: qoderSessionColumns,
		statement: `SELECT session_id,project_id,gmt_create,gmt_modified,session_type,mode,version,status,last_user_query_at,stop_reason,parent_session_id,parent_tool_call_id FROM chat_session WHERE session_id=? LIMIT ?`}
	recordQuery = querySpec{kind: QueryData, table: "chat_record", columns: recordColumns,
		statement: `SELECT request_id,session_id,question,answer,reasoning_content,gmt_create,gmt_modified,finish_status FROM chat_record WHERE session_id=? ORDER BY gmt_create,request_id LIMIT ?`}
	messageQuery = querySpec{kind: QueryData, table: "chat_message", columns: messageColumns,
		statement: `SELECT id,session_id,request_id,role,content,tool_result,gmt_create FROM chat_message WHERE session_id=? ORDER BY gmt_create,id LIMIT ?`}
	snapshotQuery = querySpec{kind: QueryData, table: "chat_snapshot", columns: snapshotColumns,
		statement: `SELECT snapshot_id,session_id,chat_record_id,status,gmt_create,gmt_modified FROM chat_snapshot WHERE session_id=? ORDER BY gmt_create,snapshot_id LIMIT ?`}
)

var fixedDataQueries = []querySpec{
	lingmaSessionListQuery, lingmaSessionOneQuery, qoderSessionListQuery, qoderSessionOneQuery,
	recordQuery, messageQuery, snapshotQuery,
}

type columnDefinition struct {
	name, declaredType          string
	defaultSQL                  string
	notNull, primaryKey, hidden int
	hasDefault                  bool
}

type tableDefinition struct {
	name                 string
	columns              []columnDefinition
	withoutRowID, strict int
}

type schemaDefinition struct {
	id     SchemaID
	tables []tableDefinition
}

func column(name, declaredType string, notNull, primaryKey int) columnDefinition {
	return columnDefinition{name: name, declaredType: declaredType, notNull: notNull, primaryKey: primaryKey}
}

func columnDefault(name, declaredType string, notNull, primaryKey int, defaultSQL string) columnDefinition {
	return columnDefinition{name: name, declaredType: declaredType, notNull: notNull, primaryKey: primaryKey, defaultSQL: defaultSQL, hasDefault: true}
}

var commonRecordTable = tableDefinition{name: "chat_record", columns: []columnDefinition{
	column("request_id", "varchar(64)", 0, 1), column("session_id", "varchar(64)", 1, 0),
	column("chat_task", "varchar(64)", 1, 0), column("chat_context", "TEXT", 0, 0),
	column("system_role_content", "TEXT", 0, 0), column("question", "TEXT", 0, 0),
	column("answer", "TEXT", 0, 0), column("like_status", "INT", 0, 0),
	column("gmt_create", "INTEGER", 0, 0), column("gmt_modified", "INTEGER", 0, 0),
	column("finish_status", "INTEGER", 0, 0), columnDefault("filter_status", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("error_result", "VARCHAR(1024)", 0, 0, "'{}'"), columnDefault("code_language", "VARCHAR(62)", 0, 0, "''"),
	columnDefault("extra", "TEXT", 0, 0, "'{}'"), columnDefault("session_type", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("summary", "TEXT", 0, 0, "''"), columnDefault("intention_type", "VARCHAR(64)", 0, 0, "''"),
	column("reasoning_content", "TEXT", 0, 0), columnDefault("mode", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("chat_prompt", "TEXT", 0, 0, "''"), columnDefault("parent_session_id", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("parent_tool_call_id", "VARCHAR(128)", 0, 0, "''"),
}}

var commonMessageTable = tableDefinition{name: "chat_message", columns: []columnDefinition{
	column("id", "varchar(64)", 0, 1), column("session_id", "VARCHAR(64)", 0, 0),
	column("request_id", "VARCHAR(64)", 0, 0), column("role", "VARCHAR(64)", 0, 0),
	column("content", "TEXT", 0, 0), column("summary", "TEXT", 0, 0),
	column("summary_modified", "INTEGER", 0, 0), columnDefault("summary_trigger", "INTEGER", 0, 0, "0"),
	column("tool_result", "TEXT", 0, 0), column("token_info", "TEXT", 0, 0),
	column("model_info", "TEXT", 0, 0), columnDefault("extra", "TEXT", 0, 0, "''"),
	column("gmt_create", "INTEGER", 0, 0),
}}

var commonSnapshotTable = tableDefinition{name: "chat_snapshot", columns: []columnDefinition{
	column("snapshot_id", "varchar(64)", 0, 1), column("session_id", "varchar(64)", 1, 0),
	column("chat_record_id", "varchar(64)", 0, 0), column("status", "varchar(64)", 0, 0),
	column("name", "varchar(64)", 0, 0), column("description", "TEXT", 0, 0),
	column("gmt_create", "INTEGER", 0, 0), column("gmt_modified", "INTEGER", 0, 0),
}}

var lingmaSessionTable = tableDefinition{name: "chat_session", columns: []columnDefinition{
	column("session_id", "varchar(64)", 0, 1), column("user_id", "VARCHAR(64)", 1, 0),
	column("user_name", "varchar(64)", 0, 0), column("session_title", "varchar(256)", 1, 0),
	column("project_id", "varchar(64)", 1, 0), column("project_uri", "varchar(512)", 0, 0),
	column("project_name", "varchar(64)", 0, 0), column("gmt_create", "INTEGER", 0, 0),
	column("gmt_modified", "INTEGER", 0, 0), columnDefault("org_id", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("session_type", "VARCHAR(64)", 0, 0, "''"), columnDefault("mode", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("version", "VARCHAR(64)", 0, 0, "''"), columnDefault("preferred_model_info", "TEXT", 0, 0, "''"),
	columnDefault("stop_reason", "VARCHAR(20)", 0, 0, "''"), columnDefault("extra", "TEXT", 0, 0, "''"),
	columnDefault("parent_session_id", "VARCHAR(64)", 0, 0, "''"), columnDefault("parent_tool_call_id", "VARCHAR(128)", 0, 0, "''"),
}}

var qoderSessionTable = tableDefinition{name: "chat_session", columns: []columnDefinition{
	column("session_id", "varchar(64)", 0, 1), column("user_id", "VARCHAR(64)", 1, 0),
	column("user_name", "varchar(64)", 0, 0), column("session_title", "varchar(256)", 1, 0),
	column("project_id", "varchar(64)", 1, 0), column("project_uri", "varchar(512)", 0, 0),
	column("project_name", "varchar(64)", 0, 0), column("gmt_create", "INTEGER", 0, 0),
	column("gmt_modified", "INTEGER", 0, 0), columnDefault("org_id", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("session_type", "VARCHAR(64)", 0, 0, "''"), columnDefault("mode", "VARCHAR(64)", 0, 0, "''"),
	columnDefault("version", "VARCHAR(64)", 0, 0, "''"), columnDefault("preferred_model_info", "TEXT", 0, 0, "''"),
	columnDefault("status", "VARCHAR(32)", 0, 0, "''"), columnDefault("last_user_query_at", "INTEGER", 0, 0, "0"),
	columnDefault("stop_reason", "VARCHAR(20)", 0, 0, "''"), columnDefault("extra", "TEXT", 0, 0, "''"),
	columnDefault("parent_session_id", "VARCHAR(64)", 0, 0, "''"), columnDefault("parent_tool_call_id", "VARCHAR(128)", 0, 0, "''"),
}}

var schemaDefinitions = map[SchemaID]schemaDefinition{
	LingmaIDEV1: {id: LingmaIDEV1, tables: []tableDefinition{lingmaSessionTable, commonRecordTable, commonMessageTable, commonSnapshotTable}},
	QoderIDEV1:  {id: QoderIDEV1, tables: []tableDefinition{qoderSessionTable, commonRecordTable, commonMessageTable, commonSnapshotTable}},
}

type sqliteBudget struct {
	limits                       Limits
	sessions, rows               int
	payloadBytes, canonicalBytes int64
}

func (budget *sqliteBudget) consumeSession() error {
	if budget.sessions >= budget.limits.MaxSessions {
		return ErrBudgetExceeded
	}
	budget.sessions++
	return budget.consumeRow()
}

func (budget *sqliteBudget) consumeRow() error {
	if budget.rows >= budget.limits.MaxRows {
		return ErrBudgetExceeded
	}
	budget.rows++
	return nil
}

func consumeBytes(current *int64, maximum, amount int64) error {
	if amount < 0 || *current > maximum || amount > maximum-*current {
		return ErrBudgetExceeded
	}
	*current += amount
	return nil
}

func (budget *sqliteBudget) consumePayload(amount int64) error {
	return consumeBytes(&budget.payloadBytes, budget.limits.MaxPayloadBytes, amount)
}

func (budget *sqliteBudget) consumeCanonical(amount int64) error {
	return consumeBytes(&budget.canonicalBytes, budget.limits.MaxCanonicalBytes, amount)
}

type chatReader struct {
	gate     chan struct{}
	parent   context.Context
	tx       *sqliteread.ReadTx
	schema   SchemaID
	budget   *sqliteBudget
	observer func(QueryEvent)
}

// WithChatSnapshot validates the complete evidenced schema before exposing a
// fixed-query ChatReader inside a safe read-only transaction.
func WithChatSnapshot(ctx context.Context, root, databasePath string, schema SchemaID, limits Limits, fn func(ChatReader) error, options ...Option) error {
	if ctx == nil || fn == nil || limits.MaxDatabaseBytes <= 0 || limits.MaxSessions < 0 || limits.MaxRows < 0 || limits.MaxPayloadBytes < 0 || limits.MaxCanonicalBytes < 0 {
		return ErrInvalidLimits
	}
	definition, ok := schemaDefinitions[schema]
	if !ok {
		return ErrUnsupportedSchema
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	configured := readOptions{}
	for _, option := range options {
		if option != nil {
			option(&configured)
		}
	}
	callbackStarted := false
	err := sqliteread.WithReadOnlyTx(ctx, root, databasePath, limits.MaxDatabaseBytes, func(tx *sqliteread.ReadTx) error {
		if err := validateSchema(ctx, tx, definition, configured.observer); err != nil {
			return err
		}
		reader := &chatReader{gate: make(chan struct{}, 1), parent: ctx, tx: tx, schema: schema, budget: &sqliteBudget{limits: limits}, observer: configured.observer}
		callbackStarted = true
		if err := fn(reader); err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			return err
		}
		return ctx.Err()
	})
	if !callbackStarted && err != nil && err.Error() == "sqliteread: database snapshot exceeds limit" {
		return ErrBudgetExceeded
	}
	return err
}

func observe(observer func(QueryEvent), spec querySpec) {
	if observer != nil {
		observer(QueryEvent{Kind: spec.kind, Table: spec.table, Columns: append([]string(nil), spec.columns...)})
	}
}

func query(ctx context.Context, tx *sqliteread.ReadTx, observer func(QueryEvent), spec querySpec, arguments ...any) (*sql.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	observe(observer, spec)
	return tx.QueryContext(ctx, spec.statement, arguments...)
}

func validateSchema(ctx context.Context, tx *sqliteread.ReadTx, definition schemaDefinition, observer func(QueryEvent)) error {
	rows, err := query(ctx, tx, observer, fixedSchemaQueries[0])
	if err != nil {
		return schemaFailure(ctx)
	}
	objects := make(map[string]struct {
		kind                 string
		columns              int
		withoutRowID, strict int
	})
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			rows.Close()
			return err
		}
		var schemaName, name, kind string
		var columns, withoutRowID, strict int
		if err := rows.Scan(&schemaName, &name, &kind, &columns, &withoutRowID, &strict); err != nil {
			rows.Close()
			return schemaFailure(ctx)
		}
		if schemaName == "main" {
			objects[name] = struct {
				kind                 string
				columns              int
				withoutRowID, strict int
			}{kind: kind, columns: columns, withoutRowID: withoutRowID, strict: strict}
		}
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil {
		return schemaFailure(ctx)
	}
	for index, table := range definition.tables {
		object, ok := objects[table.name]
		if !ok || object.kind != "table" || object.columns != len(table.columns) || object.withoutRowID != table.withoutRowID || object.strict != table.strict {
			return ErrUnsupportedSchema
		}
		rows, err := query(ctx, tx, observer, fixedSchemaQueries[index+1])
		if err != nil {
			return schemaFailure(ctx)
		}
		actual := make([]columnDefinition, 0, len(table.columns))
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				rows.Close()
				return err
			}
			var cid int
			var item columnDefinition
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &item.name, &item.declaredType, &item.notNull, &defaultValue, &item.primaryKey, &item.hidden); err != nil || cid != len(actual) {
				rows.Close()
				return schemaFailure(ctx)
			}
			item.defaultSQL, item.hasDefault = defaultValue.String, defaultValue.Valid
			actual = append(actual, item)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return schemaFailure(ctx)
		}
		if len(actual) != len(table.columns) {
			return ErrUnsupportedSchema
		}
		for columnIndex := range table.columns {
			if actual[columnIndex] != table.columns[columnIndex] {
				return ErrUnsupportedSchema
			}
		}
	}
	return ctx.Err()
}

func schemaFailure(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrUnsupportedSchema
}

func (reader *chatReader) ListSessions(ctx context.Context) ([]SessionRow, error) {
	releaseGate, err := reader.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseGate()
	combined, release, err := reader.combinedContext(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = combined
	remaining := min(reader.budget.limits.MaxSessions-reader.budget.sessions, reader.budget.limits.MaxRows-reader.budget.rows)
	if remaining < 0 {
		return nil, ErrBudgetExceeded
	}
	spec := lingmaSessionListQuery
	if reader.schema == QoderIDEV1 {
		spec = qoderSessionListQuery
	}
	rows, err := query(ctx, reader.tx, reader.observer, spec, plusOne(remaining))
	if err != nil {
		return nil, dataFailure(reader.parent, ctx)
	}
	defer rows.Close()
	var sessions []SessionRow
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			return nil, err
		}
		session, err := scanSession(rows, reader.schema)
		if err != nil || session.ID == "" || session.ProjectID == "" {
			return nil, dataFailure(reader.parent, ctx)
		}
		if err := reader.budget.consumeSession(); err != nil {
			return nil, err
		}
		if err := reader.budget.consumeCanonicalValue(session); err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, dataFailure(reader.parent, ctx)
	}
	return sessions, reader.checkContexts(ctx)
}

func (reader *chatReader) ReadConversation(ctx context.Context, sessionID string) (Conversation, error) {
	releaseGate, err := reader.acquire(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer releaseGate()
	combined, release, err := reader.combinedContext(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer release()
	ctx = combined
	if sessionID == "" {
		return Conversation{}, ErrMalformedConversation
	}
	session, err := reader.readOneSession(ctx, sessionID)
	if err != nil {
		return Conversation{}, err
	}
	records, err := reader.readRecords(ctx, sessionID)
	if err != nil {
		return Conversation{}, err
	}
	messages, err := reader.readMessages(ctx, sessionID)
	if err != nil {
		return Conversation{}, err
	}
	snapshots, err := reader.readSnapshots(ctx, sessionID)
	if err != nil {
		return Conversation{}, err
	}
	if err := reader.checkContexts(ctx); err != nil {
		return Conversation{}, err
	}
	return Conversation{Session: session, Records: records, Messages: messages, Snapshots: snapshots}, nil
}

func (reader *chatReader) readOneSession(ctx context.Context, sessionID string) (SessionRow, error) {
	spec := lingmaSessionOneQuery
	if reader.schema == QoderIDEV1 {
		spec = qoderSessionOneQuery
	}
	rows, err := query(ctx, reader.tx, reader.observer, spec, sessionID, 2)
	if err != nil {
		return SessionRow{}, dataFailure(reader.parent, ctx)
	}
	defer rows.Close()
	if !rows.Next() {
		if rows.Err() != nil {
			return SessionRow{}, dataFailure(reader.parent, ctx)
		}
		return SessionRow{}, ErrMalformedConversation
	}
	session, err := scanSession(rows, reader.schema)
	if err != nil || session.ID != sessionID || session.ProjectID == "" {
		return SessionRow{}, dataFailure(reader.parent, ctx)
	}
	if rows.Next() {
		return SessionRow{}, ErrMalformedConversation
	}
	if err := rows.Err(); err != nil {
		return SessionRow{}, dataFailure(reader.parent, ctx)
	}
	if err := reader.budget.consumeSession(); err != nil {
		return SessionRow{}, err
	}
	if err := reader.budget.consumeCanonicalValue(session); err != nil {
		return SessionRow{}, err
	}
	return session, reader.checkContexts(ctx)
}

func scanSession(scanner interface{ Scan(...any) error }, schema SchemaID) (SessionRow, error) {
	count := len(lingmaSessionColumns)
	if schema == QoderIDEV1 {
		count = len(qoderSessionColumns)
	}
	decoder, err := scanStorage(scanner, count)
	if err != nil {
		return SessionRow{}, ErrMalformedConversation
	}
	id, idOK := decoder.text(true)
	projectID, projectOK := decoder.text(true)
	createdAt, createdOK := decoder.integer()
	modifiedAt, modifiedOK := decoder.integer()
	sessionType, sessionTypeOK := decoder.text(false)
	mode, modeOK := decoder.text(false)
	version, versionOK := decoder.text(false)
	status, statusOK := "", true
	lastUserQueryAt, lastUserQueryOK := int64(0), true
	if schema == QoderIDEV1 {
		status, statusOK = decoder.text(false)
		lastUserQueryAt, lastUserQueryOK = decoder.integer()
	}
	stopReason, stopReasonOK := decoder.text(false)
	parentSessionID, parentSessionOK := decoder.text(false)
	parentToolCallID, parentToolCallOK := decoder.text(false)
	if !idOK || !projectOK || !createdOK || !modifiedOK || !sessionTypeOK || !modeOK || !versionOK || !statusOK || !lastUserQueryOK || !stopReasonOK || !parentSessionOK || !parentToolCallOK {
		return SessionRow{}, ErrMalformedConversation
	}
	return SessionRow{
		ID: id, ProjectID: projectID, CreatedAt: createdAt, ModifiedAt: modifiedAt,
		LastUserQueryAt: lastUserQueryAt, SessionType: sessionType, Mode: mode,
		Version: version, Status: status, StopReason: stopReason,
		ParentSessionID: parentSessionID, ParentToolCallID: parentToolCallID,
	}, nil
}

type storageDecoder struct {
	values []any
	index  int
}

func scanStorage(scanner interface{ Scan(...any) error }, count int) (*storageDecoder, error) {
	values := make([]any, count)
	destinations := make([]any, count)
	for index := range values {
		destinations[index] = &values[index]
	}
	if err := scanner.Scan(destinations...); err != nil {
		return nil, err
	}
	return &storageDecoder{values: values}, nil
}

func (decoder *storageDecoder) text(required bool) (string, bool) {
	if decoder.index >= len(decoder.values) {
		return "", false
	}
	value := decoder.values[decoder.index]
	decoder.index++
	if value == nil {
		return "", !required
	}
	text, ok := value.(string)
	return text, ok
}

func (decoder *storageDecoder) integer() (int64, bool) {
	if decoder.index >= len(decoder.values) {
		return 0, false
	}
	value := decoder.values[decoder.index]
	decoder.index++
	if value == nil {
		return 0, true
	}
	integer, ok := value.(int64)
	return integer, ok
}

func (reader *chatReader) readRecords(ctx context.Context, sessionID string) ([]RecordRow, error) {
	rows, err := reader.queryRemainingRows(ctx, recordQuery, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RecordRow
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			return nil, err
		}
		decoder, err := scanStorage(rows, len(recordColumns))
		if err != nil {
			return nil, dataFailure(reader.parent, ctx)
		}
		requestID, requestOK := decoder.text(true)
		rowSessionID, sessionOK := decoder.text(true)
		question, questionOK := decoder.text(false)
		answer, answerOK := decoder.text(false)
		reasoning, reasoningOK := decoder.text(false)
		createdAt, createdOK := decoder.integer()
		modifiedAt, modifiedOK := decoder.integer()
		finishStatus, finishOK := decoder.integer()
		if !requestOK || !sessionOK || !questionOK || !answerOK || !reasoningOK || !createdOK || !modifiedOK || !finishOK || requestID == "" || rowSessionID != sessionID {
			return nil, dataFailure(reader.parent, ctx)
		}
		row := RecordRow{RequestID: requestID, SessionID: rowSessionID, Question: question, Answer: answer, ReasoningContent: reasoning, CreatedAt: createdAt, ModifiedAt: modifiedAt, FinishStatus: finishStatus}
		if err := reader.budget.consumeRow(); err != nil {
			return nil, err
		}
		if err := reader.budget.consumePayload(byteLength(row.Question, row.Answer, row.ReasoningContent)); err != nil {
			return nil, err
		}
		if err := reader.budget.consumeCanonicalValue(row); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if rows.Err() != nil {
		return nil, dataFailure(reader.parent, ctx)
	}
	return result, reader.checkContexts(ctx)
}

func (reader *chatReader) readMessages(ctx context.Context, sessionID string) ([]MessageRow, error) {
	rows, err := reader.queryRemainingRows(ctx, messageQuery, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []MessageRow
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			return nil, err
		}
		decoder, err := scanStorage(rows, len(messageColumns))
		if err != nil {
			return nil, dataFailure(reader.parent, ctx)
		}
		id, idOK := decoder.text(true)
		rowSessionID, sessionOK := decoder.text(true)
		requestID, requestOK := decoder.text(false)
		role, roleOK := decoder.text(false)
		content, contentOK := decoder.text(false)
		toolResult, toolOK := decoder.text(false)
		createdAt, createdOK := decoder.integer()
		if !idOK || !sessionOK || !requestOK || !roleOK || !contentOK || !toolOK || !createdOK || id == "" || rowSessionID != sessionID {
			return nil, dataFailure(reader.parent, ctx)
		}
		row := MessageRow{ID: id, SessionID: rowSessionID, RequestID: requestID, Role: role, Content: content, ToolResult: toolResult, CreatedAt: createdAt}
		if err := reader.budget.consumeRow(); err != nil {
			return nil, err
		}
		if err := reader.budget.consumePayload(byteLength(row.Content, row.ToolResult)); err != nil {
			return nil, err
		}
		if err := reader.budget.consumeCanonicalValue(row); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if rows.Err() != nil {
		return nil, dataFailure(reader.parent, ctx)
	}
	return result, reader.checkContexts(ctx)
}

func (reader *chatReader) readSnapshots(ctx context.Context, sessionID string) ([]SnapshotRow, error) {
	rows, err := reader.queryRemainingRows(ctx, snapshotQuery, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SnapshotRow
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			return nil, err
		}
		decoder, err := scanStorage(rows, len(snapshotColumns))
		if err != nil {
			return nil, dataFailure(reader.parent, ctx)
		}
		id, idOK := decoder.text(true)
		rowSessionID, sessionOK := decoder.text(true)
		recordID, recordOK := decoder.text(false)
		status, statusOK := decoder.text(false)
		createdAt, createdOK := decoder.integer()
		modifiedAt, modifiedOK := decoder.integer()
		if !idOK || !sessionOK || !recordOK || !statusOK || !createdOK || !modifiedOK || id == "" || rowSessionID != sessionID {
			return nil, dataFailure(reader.parent, ctx)
		}
		row := SnapshotRow{ID: id, SessionID: rowSessionID, RecordID: recordID, Status: status, CreatedAt: createdAt, ModifiedAt: modifiedAt}
		if err := reader.budget.consumeRow(); err != nil {
			return nil, err
		}
		if err := reader.budget.consumeCanonicalValue(row); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if rows.Err() != nil {
		return nil, dataFailure(reader.parent, ctx)
	}
	return result, reader.checkContexts(ctx)
}

func (reader *chatReader) queryRemainingRows(ctx context.Context, spec querySpec, sessionID string) (*sql.Rows, error) {
	if err := reader.checkContexts(ctx); err != nil {
		return nil, err
	}
	remaining := reader.budget.limits.MaxRows - reader.budget.rows
	if remaining < 0 {
		return nil, ErrBudgetExceeded
	}
	rows, err := query(ctx, reader.tx, reader.observer, spec, sessionID, plusOne(remaining))
	if err != nil {
		return nil, dataFailure(reader.parent, ctx)
	}
	return rows, nil
}

func (reader *chatReader) checkContexts(ctx context.Context) error {
	if err := reader.parent.Err(); err != nil {
		return err
	}
	if ctx == nil {
		return ErrMalformedConversation
	}
	return ctx.Err()
}

func (reader *chatReader) combinedContext(operation context.Context) (context.Context, func(), error) {
	if err := reader.checkContexts(operation); err != nil {
		return nil, nil, err
	}
	combined, cancel := context.WithCancel(operation)
	stop := context.AfterFunc(reader.parent, cancel)
	release := func() {
		stop()
		cancel()
	}
	return combined, release, nil
}

func (reader *chatReader) acquire(operation context.Context) (func(), error) {
	if err := reader.checkContexts(operation); err != nil {
		return nil, err
	}
	select {
	case reader.gate <- struct{}{}:
		if err := reader.checkContexts(operation); err != nil {
			<-reader.gate
			return nil, err
		}
		return func() { <-reader.gate }, nil
	case <-reader.parent.Done():
		return nil, reader.parent.Err()
	case <-operation.Done():
		return nil, operation.Err()
	}
}

func dataFailure(parent, operation context.Context) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if operation != nil {
		if err := operation.Err(); err != nil {
			return err
		}
	}
	return ErrMalformedConversation
}

func plusOne(value int) int {
	maximum := int(^uint(0) >> 1)
	if value >= maximum {
		return maximum
	}
	return value + 1
}

func (budget *sqliteBudget) consumeCanonicalValue(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ErrMalformedConversation
	}
	return budget.consumeCanonical(int64(len(encoded)))
}

func byteLength(values ...string) int64 {
	var total int64
	for _, value := range values {
		length := int64(len(value))
		if length > int64(^uint64(0)>>1)-total {
			return -1
		}
		total += length
	}
	return total
}
