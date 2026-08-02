package sharedclient

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sqliteread"
)

var (
	ErrUnsupportedSchema     = errors.New("sharedclient: unsupported database schema")
	ErrMalformedConversation = errors.New("sharedclient: malformed conversation")
	// ErrReaderInvalidated reports that an operation-local cancellation
	// interrupted a one-time cache load, so the reader cannot safely retry it.
	ErrReaderInvalidated = errors.New("sharedclient: reader invalidated")
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
	recordColumns        = []string{"request_id", "session_id", "question_type", "question_bytes", "question", "answer_type", "answer_bytes", "answer", "reasoning_content_type", "reasoning_content_bytes", "reasoning_content", "gmt_create", "gmt_modified", "finish_status"}
	messageColumns       = []string{"id", "session_id", "request_id", "role", "content_type", "content_bytes", "content", "tool_result_type", "tool_result_bytes", "tool_result", "gmt_create"}
	snapshotColumns      = []string{"snapshot_id", "session_id", "chat_record_id", "status", "gmt_create", "gmt_modified"}
)

var (
	lingmaSessionQuery = querySpec{kind: QueryData, table: "chat_session", columns: lingmaSessionColumns,
		statement: `SELECT session_id,project_id,gmt_create,gmt_modified,session_type,mode,version,stop_reason,parent_session_id,parent_tool_call_id FROM chat_session LIMIT ?`}
	qoderSessionQuery = querySpec{kind: QueryData, table: "chat_session", columns: qoderSessionColumns,
		statement: `SELECT session_id,project_id,gmt_create,gmt_modified,session_type,mode,version,status,last_user_query_at,stop_reason,parent_session_id,parent_tool_call_id FROM chat_session LIMIT ?`}
	recordQuery = querySpec{kind: QueryData, table: "chat_record", columns: recordColumns,
		statement: `SELECT request_id,session_id,typeof(question),length(CAST(question AS BLOB)),CASE WHEN typeof(question)='text' AND length(CAST(question AS BLOB))<=? THEN question ELSE NULL END,typeof(answer),length(CAST(answer AS BLOB)),CASE WHEN typeof(answer)='text' AND length(CAST(answer AS BLOB))<=? THEN answer ELSE NULL END,typeof(reasoning_content),length(CAST(reasoning_content AS BLOB)),CASE WHEN typeof(reasoning_content)='text' AND length(CAST(reasoning_content AS BLOB))<=? THEN reasoning_content ELSE NULL END,gmt_create,gmt_modified,finish_status FROM chat_record LIMIT ?`}
	messageQuery = querySpec{kind: QueryData, table: "chat_message", columns: messageColumns,
		statement: `SELECT id,session_id,request_id,role,typeof(content),length(CAST(content AS BLOB)),CASE WHEN typeof(content)='text' AND length(CAST(content AS BLOB))<=? THEN content ELSE NULL END,typeof(tool_result),length(CAST(tool_result AS BLOB)),CASE WHEN typeof(tool_result)='text' AND length(CAST(tool_result AS BLOB))<=? THEN tool_result ELSE NULL END,gmt_create FROM chat_message LIMIT ?`}
	snapshotQuery = querySpec{kind: QueryData, table: "chat_snapshot", columns: snapshotColumns,
		statement: `SELECT snapshot_id,session_id,chat_record_id,status,gmt_create,gmt_modified FROM chat_snapshot LIMIT ?`}
)

var fixedDataQueries = []querySpec{
	lingmaSessionQuery, qoderSessionQuery, recordQuery, messageQuery, snapshotQuery,
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

func (budget *sqliteBudget) remainingBodyBytes() (int64, error) {
	payload := budget.limits.MaxPayloadBytes - budget.payloadBytes
	canonical := budget.limits.MaxCanonicalBytes - budget.canonicalBytes
	if payload < 0 || canonical < 0 {
		return 0, ErrBudgetExceeded
	}
	return min(payload, canonical), nil
}

type chatReader struct {
	gate        chan struct{}
	parent      context.Context
	tx          *sqliteread.ReadTx
	schema      SchemaID
	budget      *sqliteBudget
	observer    func(QueryEvent)
	terminalErr error

	sessionsLoaded, recordsLoaded, messagesLoaded, snapshotsLoaded bool
	sessionsErr, recordsErr, messagesErr, snapshotsErr             error
	sessions                                                       []SessionRow
	sessionsByID                                                   map[string]SessionRow
	recordsBySession                                               map[string][]RecordRow
	messagesBySession                                              map[string][]MessageRow
	snapshotsBySession                                             map[string][]SnapshotRow
	malformed                                                      map[string]bool
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
	return classifyDatabaseInitializationError(err, callbackStarted)
}

func classifyDatabaseInitializationError(err error, callbackStarted bool) error {
	if !callbackStarted && errors.Is(err, sqliteread.ErrBudgetExceeded) {
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
	if reader.terminalErr != nil {
		return nil, reader.terminalErr
	}
	combined, release, err := reader.combinedContext(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = combined
	if err := reader.loadSessions(ctx); err != nil {
		return nil, reader.finishLoad(ctx, err)
	}
	return append([]SessionRow(nil), reader.sessions...), reader.checkContexts(ctx)
}

func (reader *chatReader) ReadConversation(ctx context.Context, sessionID string) (Conversation, error) {
	releaseGate, err := reader.acquire(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer releaseGate()
	if reader.terminalErr != nil {
		return Conversation{}, reader.terminalErr
	}
	combined, release, err := reader.combinedContext(ctx)
	if err != nil {
		return Conversation{}, err
	}
	defer release()
	ctx = combined
	if sessionID == "" {
		return Conversation{}, ErrMalformedConversation
	}
	for _, load := range []func(context.Context) error{reader.loadSessions, reader.loadRecords, reader.loadMessages, reader.loadSnapshots} {
		if err := load(ctx); err != nil {
			return Conversation{}, reader.finishLoad(ctx, err)
		}
	}
	if err := reader.checkContexts(ctx); err != nil {
		return Conversation{}, err
	}
	session, ok := reader.sessionsByID[sessionID]
	if !ok || reader.malformed[sessionID] {
		return Conversation{}, ErrMalformedConversation
	}
	return Conversation{
		Session:   session,
		Records:   append([]RecordRow(nil), reader.recordsBySession[sessionID]...),
		Messages:  append([]MessageRow(nil), reader.messagesBySession[sessionID]...),
		Snapshots: append([]SnapshotRow(nil), reader.snapshotsBySession[sessionID]...),
	}, nil
}

func (reader *chatReader) finishLoad(ctx context.Context, err error) error {
	if err != nil && reader.parent.Err() == nil && ctx != nil && ctx.Err() != nil {
		reader.terminalErr = ErrReaderInvalidated
		return ctx.Err()
	}
	return err
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

func (decoder *storageDecoder) raw() (any, bool) {
	if decoder.index >= len(decoder.values) {
		return nil, false
	}
	value := decoder.values[decoder.index]
	decoder.index++
	return value, true
}

func (decoder *storageDecoder) guardedText(maxBytes int64) (string, error) {
	storageType, typeOK := decoder.text(true)
	lengthValue, lengthOK := decoder.raw()
	textValue, valueOK := decoder.raw()
	if !typeOK || !lengthOK || !valueOK {
		return "", ErrMalformedConversation
	}
	switch storageType {
	case "null":
		if lengthValue != nil || textValue != nil {
			return "", ErrMalformedConversation
		}
		return "", nil
	case "text":
		length, ok := lengthValue.(int64)
		if !ok || length < 0 {
			return "", ErrMalformedConversation
		}
		if length > maxBytes {
			if textValue != nil {
				return "", ErrMalformedConversation
			}
			return "", ErrBudgetExceeded
		}
		text, ok := textValue.(string)
		if !ok || int64(len(text)) != length {
			return "", ErrMalformedConversation
		}
		return text, nil
	default:
		return "", ErrMalformedConversation
	}
}

func (reader *chatReader) loadSessions(ctx context.Context) error {
	if reader.sessionsLoaded {
		return reader.sessionsErr
	}
	reader.sessionsLoaded = true
	reader.sessionsByID = make(map[string]SessionRow)
	reader.malformed = make(map[string]bool)

	spec := lingmaSessionQuery
	if reader.schema == QoderIDEV1 {
		spec = qoderSessionQuery
	}
	remaining := min(reader.budget.limits.MaxSessions-reader.budget.sessions, reader.budget.limits.MaxRows-reader.budget.rows)
	if remaining < 0 {
		reader.sessionsErr = ErrBudgetExceeded
		return reader.sessionsErr
	}
	rows, err := query(ctx, reader.tx, reader.observer, spec, plusOne(remaining))
	if err != nil {
		reader.sessionsErr = dataFailure(reader.parent, ctx)
		return reader.sessionsErr
	}
	defer rows.Close()
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			reader.sessionsErr = err
			return err
		}
		row, err := scanSession(rows, reader.schema)
		if err != nil || row.ID == "" || row.ProjectID == "" {
			reader.sessionsErr = dataFailure(reader.parent, ctx)
			return reader.sessionsErr
		}
		if err := reader.budget.consumeSession(); err != nil {
			reader.sessionsErr = err
			return err
		}
		if _, duplicate := reader.sessionsByID[row.ID]; duplicate {
			reader.sessionsErr = ErrMalformedConversation
			return reader.sessionsErr
		}
		if err := reader.budget.consumeCanonicalValue(row); err != nil {
			reader.sessionsErr = err
			return err
		}
		reader.sessions = append(reader.sessions, row)
		reader.sessionsByID[row.ID] = row
	}
	if rows.Err() != nil {
		reader.sessionsErr = dataFailure(reader.parent, ctx)
		return reader.sessionsErr
	}
	sort.SliceStable(reader.sessions, func(left, right int) bool {
		if reader.sessions[left].CreatedAt != reader.sessions[right].CreatedAt {
			return reader.sessions[left].CreatedAt < reader.sessions[right].CreatedAt
		}
		return reader.sessions[left].ID < reader.sessions[right].ID
	})
	reader.sessionsErr = reader.checkContexts(ctx)
	return reader.sessionsErr
}

func (reader *chatReader) loadRecords(ctx context.Context) error {
	if reader.recordsLoaded {
		return reader.recordsErr
	}
	reader.recordsLoaded = true
	reader.recordsBySession = make(map[string][]RecordRow)
	cellLimit, err := reader.budget.remainingBodyBytes()
	if err != nil {
		reader.recordsErr = err
		return err
	}
	rows, err := reader.queryRemainingRows(ctx, recordQuery, cellLimit, cellLimit, cellLimit)
	if err != nil {
		reader.recordsErr = err
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			reader.recordsErr = err
			return err
		}
		decoder, err := scanStorage(rows, len(recordColumns))
		if err != nil {
			reader.recordsErr = dataFailure(reader.parent, ctx)
			return reader.recordsErr
		}
		requestID, requestOK := decoder.text(true)
		rowSessionID, sessionOK := decoder.text(true)
		question, questionErr := decoder.guardedText(cellLimit)
		answer, answerErr := decoder.guardedText(cellLimit)
		reasoning, reasoningErr := decoder.guardedText(cellLimit)
		createdAt, createdOK := decoder.integer()
		modifiedAt, modifiedOK := decoder.integer()
		finishStatus, finishOK := decoder.integer()
		if !sessionOK || rowSessionID == "" {
			reader.recordsErr = dataFailure(reader.parent, ctx)
			return reader.recordsErr
		}
		if err := reader.budget.consumeRow(); err != nil {
			reader.recordsErr = err
			return err
		}
		if errors.Is(questionErr, ErrBudgetExceeded) || errors.Is(answerErr, ErrBudgetExceeded) || errors.Is(reasoningErr, ErrBudgetExceeded) {
			reader.recordsErr = ErrBudgetExceeded
			return reader.recordsErr
		}
		if !requestOK || questionErr != nil || answerErr != nil || reasoningErr != nil || !createdOK || !modifiedOK || !finishOK || requestID == "" {
			reader.malformed[rowSessionID] = true
			continue
		}
		row := RecordRow{RequestID: requestID, SessionID: rowSessionID, Question: question, Answer: answer, ReasoningContent: reasoning, CreatedAt: createdAt, ModifiedAt: modifiedAt, FinishStatus: finishStatus}
		if err := reader.budget.consumePayload(byteLength(row.Question, row.Answer, row.ReasoningContent)); err != nil {
			reader.recordsErr = err
			return err
		}
		if err := reader.budget.consumeCanonicalValue(row); err != nil {
			reader.recordsErr = err
			return err
		}
		reader.recordsBySession[rowSessionID] = append(reader.recordsBySession[rowSessionID], row)
	}
	if rows.Err() != nil {
		reader.recordsErr = dataFailure(reader.parent, ctx)
		return reader.recordsErr
	}
	for sessionID := range reader.recordsBySession {
		rows := reader.recordsBySession[sessionID]
		sort.SliceStable(rows, func(left, right int) bool {
			if rows[left].CreatedAt != rows[right].CreatedAt {
				return rows[left].CreatedAt < rows[right].CreatedAt
			}
			return rows[left].RequestID < rows[right].RequestID
		})
	}
	reader.recordsErr = reader.checkContexts(ctx)
	return reader.recordsErr
}

func (reader *chatReader) loadMessages(ctx context.Context) error {
	if reader.messagesLoaded {
		return reader.messagesErr
	}
	reader.messagesLoaded = true
	reader.messagesBySession = make(map[string][]MessageRow)
	cellLimit, err := reader.budget.remainingBodyBytes()
	if err != nil {
		reader.messagesErr = err
		return err
	}
	rows, err := reader.queryRemainingRows(ctx, messageQuery, cellLimit, cellLimit)
	if err != nil {
		reader.messagesErr = err
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			reader.messagesErr = err
			return err
		}
		decoder, err := scanStorage(rows, len(messageColumns))
		if err != nil {
			reader.messagesErr = dataFailure(reader.parent, ctx)
			return reader.messagesErr
		}
		id, idOK := decoder.text(true)
		rowSessionID, sessionOK := decoder.text(true)
		requestID, requestOK := decoder.text(false)
		role, roleOK := decoder.text(false)
		content, contentErr := decoder.guardedText(cellLimit)
		toolResult, toolErr := decoder.guardedText(cellLimit)
		createdAt, createdOK := decoder.integer()
		if !sessionOK || rowSessionID == "" {
			reader.messagesErr = dataFailure(reader.parent, ctx)
			return reader.messagesErr
		}
		if err := reader.budget.consumeRow(); err != nil {
			reader.messagesErr = err
			return err
		}
		if errors.Is(contentErr, ErrBudgetExceeded) || errors.Is(toolErr, ErrBudgetExceeded) {
			reader.messagesErr = ErrBudgetExceeded
			return reader.messagesErr
		}
		if !idOK || !requestOK || !roleOK || contentErr != nil || toolErr != nil || !createdOK || id == "" {
			reader.malformed[rowSessionID] = true
			continue
		}
		row := MessageRow{ID: id, SessionID: rowSessionID, RequestID: requestID, Role: role, Content: content, ToolResult: toolResult, CreatedAt: createdAt}
		if err := reader.budget.consumePayload(byteLength(row.Content, row.ToolResult)); err != nil {
			reader.messagesErr = err
			return err
		}
		if err := reader.budget.consumeCanonicalValue(row); err != nil {
			reader.messagesErr = err
			return err
		}
		reader.messagesBySession[rowSessionID] = append(reader.messagesBySession[rowSessionID], row)
	}
	if rows.Err() != nil {
		reader.messagesErr = dataFailure(reader.parent, ctx)
		return reader.messagesErr
	}
	for sessionID := range reader.messagesBySession {
		rows := reader.messagesBySession[sessionID]
		sort.SliceStable(rows, func(left, right int) bool {
			if rows[left].CreatedAt != rows[right].CreatedAt {
				return rows[left].CreatedAt < rows[right].CreatedAt
			}
			return rows[left].ID < rows[right].ID
		})
	}
	reader.messagesErr = reader.checkContexts(ctx)
	return reader.messagesErr
}

func (reader *chatReader) loadSnapshots(ctx context.Context) error {
	if reader.snapshotsLoaded {
		return reader.snapshotsErr
	}
	reader.snapshotsLoaded = true
	reader.snapshotsBySession = make(map[string][]SnapshotRow)
	rows, err := reader.queryRemainingRows(ctx, snapshotQuery)
	if err != nil {
		reader.snapshotsErr = err
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := reader.checkContexts(ctx); err != nil {
			reader.snapshotsErr = err
			return err
		}
		decoder, err := scanStorage(rows, len(snapshotColumns))
		if err != nil {
			reader.snapshotsErr = dataFailure(reader.parent, ctx)
			return reader.snapshotsErr
		}
		id, idOK := decoder.text(true)
		rowSessionID, sessionOK := decoder.text(true)
		recordID, recordOK := decoder.text(false)
		status, statusOK := decoder.text(false)
		createdAt, createdOK := decoder.integer()
		modifiedAt, modifiedOK := decoder.integer()
		if !sessionOK || rowSessionID == "" {
			reader.snapshotsErr = dataFailure(reader.parent, ctx)
			return reader.snapshotsErr
		}
		if err := reader.budget.consumeRow(); err != nil {
			reader.snapshotsErr = err
			return err
		}
		if !idOK || !recordOK || !statusOK || !createdOK || !modifiedOK || id == "" {
			reader.malformed[rowSessionID] = true
			continue
		}
		row := SnapshotRow{ID: id, SessionID: rowSessionID, RecordID: recordID, Status: status, CreatedAt: createdAt, ModifiedAt: modifiedAt}
		if err := reader.budget.consumeCanonicalValue(row); err != nil {
			reader.snapshotsErr = err
			return err
		}
		reader.snapshotsBySession[rowSessionID] = append(reader.snapshotsBySession[rowSessionID], row)
	}
	if rows.Err() != nil {
		reader.snapshotsErr = dataFailure(reader.parent, ctx)
		return reader.snapshotsErr
	}
	for sessionID := range reader.snapshotsBySession {
		rows := reader.snapshotsBySession[sessionID]
		sort.SliceStable(rows, func(left, right int) bool {
			if rows[left].CreatedAt != rows[right].CreatedAt {
				return rows[left].CreatedAt < rows[right].CreatedAt
			}
			return rows[left].ID < rows[right].ID
		})
	}
	reader.snapshotsErr = reader.checkContexts(ctx)
	return reader.snapshotsErr
}

func (reader *chatReader) queryRemainingRows(ctx context.Context, spec querySpec, arguments ...any) (*sql.Rows, error) {
	if err := reader.checkContexts(ctx); err != nil {
		return nil, err
	}
	remaining := reader.budget.limits.MaxRows - reader.budget.rows
	if remaining < 0 {
		return nil, ErrBudgetExceeded
	}
	arguments = append(arguments, plusOne(remaining))
	rows, err := query(ctx, reader.tx, reader.observer, spec, arguments...)
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
