package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sqliteread"
	_ "modernc.org/sqlite"
)

type recordingSQLiteQueryer struct {
	sqliteQueryer
	queries *[]string
}

type injectingSQLiteQueryer struct{ sqliteQueryer }

func (q injectingSQLiteQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if query == sqliteMessageQuery.statement && len(args) > 0 && args[0] == "s1" {
		return q.sqliteQueryer.QueryContext(ctx, `SELECT id,session_id,time_created,data FROM message WHERE session_id=? UNION ALL SELECT NULL,'s1',70,'{"role":"user"}' UNION ALL SELECT 'bad-message-time','s1','bad-time','{"role":"user"}' UNION ALL SELECT 'bad-message-json','s1',72,'{' ORDER BY time_created,id LIMIT ?`, args...)
	}
	if query == sqlitePartQuery.statement && len(args) > 0 && args[0] == "s1" {
		return q.sqliteQueryer.QueryContext(ctx, `SELECT id,message_id,session_id,time_created,data FROM part WHERE session_id=? UNION ALL SELECT NULL,'m-z-user','s1',70,'{"type":"text","text":"bad"}' UNION ALL SELECT 'bad-part-time','m-z-user','s1','bad-time','{"type":"text","text":"bad"}' UNION ALL SELECT 'bad-part-json','m-z-user','s1',72,'{' ORDER BY time_created,id LIMIT ?`, args...)
	}
	return q.sqliteQueryer.QueryContext(ctx, query, args...)
}

type injectingMetadataQueryer struct{ sqliteQueryer }

func (q injectingMetadataQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if strings.Contains(query, `FROM session AS s LEFT JOIN project AS p`) && strings.Contains(query, `ORDER BY`) {
		return q.sqliteQueryer.QueryContext(ctx, `SELECT s.id,s.project_id,COALESCE(s.parent_id,''),s.directory,p.worktree,s.time_created,s.time_updated,s.tokens_input,s.tokens_output,s.tokens_reasoning,s.tokens_cache_read,s.tokens_cache_write FROM session AS s LEFT JOIN project AS p ON p.id=s.project_id UNION ALL SELECT NULL,'p1','','/synthetic/project','/synthetic/project',120,130,0,0,0,0,0 UNION ALL SELECT 'bad-time','p1','','/synthetic/project','/synthetic/project','not-an-integer',140,0,0,0,0,0 ORDER BY 6,1 LIMIT ?`, args...)
	}
	return q.sqliteQueryer.QueryContext(ctx, query, args...)
}

func (q recordingSQLiteQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	*q.queries = append(*q.queries, query)
	return q.sqliteQueryer.QueryContext(ctx, query, args...)
}

func (q recordingSQLiteQueryer) QueryRowContext(ctx context.Context, query string, args ...any) (*sql.Row, error) {
	*q.queries = append(*q.queries, query)
	return q.sqliteQueryer.QueryRowContext(ctx, query, args...)
}

func installSQLite(t *testing.T, root string) *sql.DB {
	return installSQLiteFixture(t, root, true)
}

func installSQLiteWithUsage(t *testing.T, root string, withUsage bool) *sql.DB {
	return installSQLiteFixture(t, root, withUsage)
}

func installSQLiteFixture(t *testing.T, root string, withUsage bool) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sessionSchema := `CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT, directory TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`
	parentInsert := `INSERT INTO session VALUES ('parent','p1',NULL,'/synthetic/project',10,20)`
	sessionInsert := `INSERT INTO session VALUES ('s1','p1','parent','/synthetic/project',30,90)`
	if withUsage {
		sessionSchema = `CREATE TABLE session (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, parent_id TEXT, directory TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL, tokens_input INTEGER NOT NULL DEFAULT 0, tokens_output INTEGER NOT NULL DEFAULT 0, tokens_reasoning INTEGER NOT NULL DEFAULT 0, tokens_cache_read INTEGER NOT NULL DEFAULT 0, tokens_cache_write INTEGER NOT NULL DEFAULT 0)`
		parentInsert = `INSERT INTO session VALUES ('parent','p1',NULL,'/synthetic/project',10,20,0,0,0,0,0)`
		sessionInsert = `INSERT INTO session VALUES ('s1','p1','parent','/synthetic/project',30,90,11,22,3,4,5)`
	}
	statements := []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`,
		`CREATE TABLE project (id TEXT PRIMARY KEY, worktree TEXT NOT NULL, time_created INTEGER NOT NULL, time_updated INTEGER NOT NULL)`,
		sessionSchema,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT NOT NULL, session_id TEXT NOT NULL, time_created INTEGER NOT NULL, data TEXT NOT NULL)`,
		`CREATE TABLE account (id TEXT, secret TEXT)`, `CREATE TABLE account_state (id TEXT, token TEXT)`,
		`CREATE TABLE control_account (id TEXT, credential TEXT)`, `CREATE TABLE credential (id TEXT, token TEXT)`,
		`CREATE TABLE token (id TEXT, value TEXT)`, `CREATE TABLE session_share (session_id TEXT, url TEXT)`,
		`CREATE TABLE permission (session_id TEXT, data TEXT)`, `CREATE TABLE event (id TEXT, data TEXT)`,
		`CREATE TABLE session_input (session_id TEXT, data TEXT)`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
		`INSERT INTO account VALUES ('bait','synthetic-secret')`,
		`INSERT INTO project VALUES ('p1','/synthetic/project',10,90)`,
		parentInsert, sessionInsert,
		`INSERT INTO message VALUES ('m-z-user','s1',40,'{"role":"user"}')`,
		`INSERT INTO message VALUES ('m-a-assistant','s1',30,'{"role":"assistant","modelID":"synthetic-model"}')`,
		`INSERT INTO part VALUES ('p-z-think','m-a-assistant','s1',50,'{"type":"reasoning","text":"synthetic thought"}')`,
		`INSERT INTO part VALUES ('p-a-user','m-z-user','s1',50,'{"type":"text","text":"synthetic user"}')`,
		`INSERT INTO part VALUES ('p-m-tool','m-a-assistant','s1',60,'{"type":"tool","tool":"shell","callID":"call-1","state":{"status":"completed","input":{"command":"synthetic"},"output":"synthetic result"}}')`,
		`INSERT INTO part VALUES ('p-patch','m-a-assistant','s1',61,'{"type":"patch","hash":"synthetic"}')`,
		`INSERT INTO part VALUES ('p-step-start','m-a-assistant','s1',62,'{"type":"step-start"}')`,
		`INSERT INTO part VALUES ('p-future','m-a-assistant','s1',63,'{"type":"future-native-part","value":1}')`,
		`INSERT INTO part VALUES ('p-bad-tool','m-a-assistant','s1',64,'{"type":"tool","tool":"shell","state":{"status":"completed","output":"bad"}}')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("synthetic sqlite setup: %v", err)
		}
	}
	return db
}

func findSession(t *testing.T, sessions []source.Session, id string) source.Session {
	t.Helper()
	for _, session := range sessions {
		if session.ID == id {
			return session
		}
	}
	t.Fatalf("missing session %s", id)
	return source.Session{}
}

func TestSQLiteDiscoverOpenWALAndRestrictedQueries(t *testing.T) {
	root := t.TempDir()
	installSQLite(t, root)
	a := New(root)
	var queries []string
	var specs []sqliteQuerySpec
	previousObserver := observeSQLiteQuery
	observeSQLiteQuery = func(spec sqliteQuerySpec) { specs = append(specs, spec) }
	t.Cleanup(func() { observeSQLiteQuery = previousObserver })
	a.sqliteRead = func(ctx context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
		return sqliteread.WithReadOnlyTx(ctx, root, path, maxBytes, func(tx *sqliteread.ReadTx) error {
			return fn(recordingSQLiteQueryer{sqliteQueryer: tx, queries: &queries})
		})
	}
	sessions, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := findSession(t, sessions, "opencode:s1")
	if s.FormatVersion != "db-v2" || s.Scope.Type != source.ScopeProject || s.Scope.Root != "/synthetic/project" || s.ParentID != "opencode:parent" || s.MessageCount != 2 || s.SnapshotID == "" {
		t.Fatalf("metadata=%#v", s)
	}
	if s.MalformedCount != 1 {
		t.Fatalf("valid unexported parts counted malformed: %d", s.MalformedCount)
	}
	if s.Usage["input_tokens"] != 11 || s.Usage["output_tokens"] != 22 || s.Usage["reasoning_tokens"] != 3 || s.Usage["cache_read_tokens"] != 4 || s.Usage["cache_write_tokens"] != 5 {
		t.Fatalf("usage=%#v", s.Usage)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var types, markers, timestamps []string
	decoder := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		types = append(types, event["type"].(string))
		timestamps = append(timestamps, event["timestamp"].(string))
		if event["type"] == "message" {
			content := event["content"].([]any)[0].(map[string]any)
			if thinking, ok := content["thinking"].(string); ok {
				markers = append(markers, thinking)
			} else {
				markers = append(markers, content["text"].(string))
			}
		}
	}
	if strings.Join(types, ",") != "message,message,tool_use,tool_result" {
		t.Fatalf("event order/types=%v", types)
	}
	if strings.Join(markers, ",") != "synthetic thought,synthetic user" {
		t.Fatalf("row timestamp order ignored: %v", markers)
	}
	if strings.Join(timestamps, ",") != "1970-01-01T00:00:00.05Z,1970-01-01T00:00:00.05Z,1970-01-01T00:00:00.06Z,1970-01-01T00:00:00.06Z" {
		t.Fatalf("payload rather than row timestamps used: %v", timestamps)
	}
	if len(queries) == 0 {
		t.Fatal("no sqlite queries recorded")
	}
	if len(specs) == 0 {
		t.Fatal("no structured sqlite query audit")
	}
	allowed := map[string][]string{
		"project": {"id", "worktree"},
		"session": {"id", "project_id", "parent_id", "directory", "time_created", "time_updated", "tokens_input", "tokens_output", "tokens_reasoning", "tokens_cache_read", "tokens_cache_write"},
		"message": {"id", "session_id", "time_created", "data"},
		"part":    {"id", "message_id", "session_id", "time_created", "data"},
	}
	for _, spec := range specs {
		if spec.kind != sqliteQueryData {
			continue
		}
		permitted, ok := allowed[spec.table]
		if !ok || strings.Join(spec.columns, ",") != strings.Join(permitted, ",") {
			t.Fatalf("unexpected structured query: %#v", spec)
		}
	}
	for _, query := range queries {
		lower := strings.ToLower(query)
		if strings.Contains(lower, "select *") {
			t.Fatalf("wildcard query: %s", query)
		}
		for _, forbidden := range []string{"account", "account_state", "control_account", "credential", " token", "session_share", "permission", " event", "session_input"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("forbidden table in query: %s", query)
			}
		}
	}
}

func TestSQLiteRejectsCoreViewsBeforeReadingTheirContent(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		`CREATE TABLE credential (id TEXT,worktree TEXT,project_id TEXT,parent_id TEXT,directory TEXT,time_created INTEGER,time_updated INTEGER,tokens_input INTEGER,tokens_output INTEGER,tokens_reasoning INTEGER,tokens_cache_read INTEGER,tokens_cache_write INTEGER,session_id TEXT,message_id TEXT,data TEXT)`,
		`CREATE VIEW project AS SELECT id,worktree FROM credential`,
		`CREATE VIEW session AS SELECT id,project_id,parent_id,directory,time_created,time_updated,tokens_input,tokens_output,tokens_reasoning,tokens_cache_read,tokens_cache_write FROM credential`,
		`CREATE VIEW message AS SELECT id,session_id,time_created,data FROM credential`,
		`CREATE VIEW part AS SELECT id,message_id,session_id,time_created,data FROM credential`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var specs []sqliteQuerySpec
	previousObserver := observeSQLiteQuery
	observeSQLiteQuery = func(spec sqliteQuerySpec) { specs = append(specs, spec) }
	t.Cleanup(func() { observeSQLiteQuery = previousObserver })
	if _, err := New(root).Discover(context.Background()); err == nil {
		t.Fatal("core views accepted")
	}
	for _, spec := range specs {
		if spec.kind == sqliteQueryData {
			t.Fatalf("view content query attempted: %#v", spec)
		}
	}
}

func TestSQLiteRejectsSessionIdentityWithoutPrimaryKey(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE project (id TEXT PRIMARY KEY,worktree TEXT NOT NULL)`,
		`CREATE TABLE session (id TEXT,project_id TEXT NOT NULL,parent_id TEXT,directory TEXT NOT NULL,time_created INTEGER NOT NULL,time_updated INTEGER NOT NULL)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY,session_id TEXT NOT NULL,time_created INTEGER NOT NULL,data TEXT NOT NULL)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY,message_id TEXT NOT NULL,session_id TEXT NOT NULL,time_created INTEGER NOT NULL,data TEXT NOT NULL)`,
		`INSERT INTO project VALUES ('p1','/synthetic/one'),('p2','/synthetic/two')`,
		`INSERT INTO session VALUES ('duplicate','p1',NULL,'/synthetic/one',1,2),('duplicate','p2',NULL,'/synthetic/two',3,4)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := New(root).Discover(context.Background()); err == nil {
		t.Fatal("non-unique session identity accepted")
	}
}

func TestSQLiteMalformedMessageAndPartRowsAreIsolated(t *testing.T) {
	root := t.TempDir()
	installSQLite(t, root)
	a := New(root)
	a.sqliteRead = func(ctx context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
		return sqliteread.WithReadOnlyTx(ctx, root, path, maxBytes, func(tx *sqliteread.ReadTx) error {
			return fn(injectingSQLiteQueryer{sqliteQueryer: tx})
		})
	}
	sessions, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := findSession(t, sessions, "opencode:s1")
	if s.MessageCount != 2 || s.MalformedCount != 7 {
		t.Fatalf("healthy rows lost or malformed rows missed: %#v", s)
	}
	if r, err := a.Open(context.Background(), s); err != nil {
		t.Fatal(err)
	} else {
		r.Close()
	}
}

func TestSQLiteCancellationDuringRowProcessingPropagates(t *testing.T) {
	root := t.TempDir()
	installSQLite(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	a := New(root)
	a.sqliteRead = func(inner context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
		return sqliteread.WithReadOnlyTx(inner, root, path, maxBytes, func(tx *sqliteread.ReadTx) error {
			return fn(cancelingSQLiteQueryer{sqliteQueryer: tx, cancel: cancel})
		})
	}
	if _, err := a.Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation swallowed: %v", err)
	}
}

func TestSQLiteDiscoverEnforcesGlobalBudgets(t *testing.T) {
	root := t.TempDir()
	installSQLite(t, root)
	tests := map[string]sqliteScanLimits{
		"sessions":  {maxSessions: 1, maxRows: 100, maxPayloadBytes: 1 << 20, maxCanonicalBytes: 1 << 20},
		"rows":      {maxSessions: 10, maxRows: 3, maxPayloadBytes: 1 << 20, maxCanonicalBytes: 1 << 20},
		"payload":   {maxSessions: 10, maxRows: 100, maxPayloadBytes: 8, maxCanonicalBytes: 1 << 20},
		"canonical": {maxSessions: 10, maxRows: 100, maxPayloadBytes: 1 << 20, maxCanonicalBytes: 8},
	}
	for name, limits := range tests {
		t.Run(name, func(t *testing.T) {
			if _, _, err := New(root).discoverSQLiteWithLimits(context.Background(), limits); err == nil {
				t.Fatal("global scan budget bypassed")
			}
		})
	}
}

type cancelingSQLiteQueryer struct {
	sqliteQueryer
	cancel context.CancelFunc
}

func (q cancelingSQLiteQueryer) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if query == sqliteMessageQuery.statement {
		q.cancel()
	}
	return q.sqliteQueryer.QueryContext(ctx, query, args...)
}

func TestSQLiteAuthorizationContractRequeriesAndRejectsMutation(t *testing.T) {
	root := t.TempDir()
	db := installSQLite(t, root)
	other := New(root)
	forgedSessions, err := other.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	forged := findSession(t, forgedSessions, "opencode:s1")
	originalDigest := forged.SnapshotID
	forged.SnapshotID = "forged-by-another-instance"
	a := New(root)
	adaptertest.AuthorizationContract(t, a, func() {
		result, err := db.Exec(`UPDATE part SET data='{"type":"text","text":"changed"}' WHERE id='p-a-user'`)
		if err != nil {
			t.Fatal(err)
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			t.Fatalf("mutation rows=%d err=%v", changed, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		fresh, err := New(root).Discover(context.Background())
		freshSession := findSession(t, fresh, "opencode:s1")
		if err != nil || freshSession.SnapshotID == originalDigest {
			t.Fatalf("mutation not reflected by fresh discovery: err=%v input=%d old=%s new=%s", err, freshSession.Usage["input_tokens"], originalDigest, freshSession.SnapshotID)
		}
	}, forged)
}

func TestSQLiteWithoutUsageColumnsIsSupported(t *testing.T) {
	root := t.TempDir()
	installSQLiteWithUsage(t, root, false)
	a := New(root)
	var queries []string
	a.sqliteRead = func(ctx context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
		return sqliteread.WithReadOnlyTx(ctx, root, path, maxBytes, func(tx *sqliteread.ReadTx) error {
			return fn(recordingSQLiteQueryer{sqliteQueryer: tx, queries: &queries})
		})
	}
	sessions, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := findSession(t, sessions, "opencode:s1")
	if len(s.Usage) != 0 {
		t.Fatalf("optional usage should be absent: %#v", s.Usage)
	}
	if r, err := a.Open(context.Background(), s); err != nil {
		t.Fatal(err)
	} else {
		r.Close()
	}
	for _, query := range queries {
		lower := strings.ToLower(query)
		if strings.Contains(lower, "select *") || strings.Contains(lower, "s.tokens_") {
			t.Fatalf("unsafe optional usage projection: %s", query)
		}
	}
}

func TestSQLiteMalformedSessionDoesNotHideHealthySession(t *testing.T) {
	root := t.TempDir()
	db := installSQLite(t, root)
	if _, err := db.Exec(`INSERT INTO session VALUES ('bad','p1',NULL,'/synthetic/project',100,110,0,0,0,0,0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO message VALUES ('bad-message','bad',101,'{"unexpected":"envelope"}')`); err != nil {
		t.Fatal(err)
	}
	sessions, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	findSession(t, sessions, "opencode:s1")
	for _, session := range sessions {
		if session.ID == "opencode:bad" {
			t.Fatal("malformed session was accepted")
		}
	}
}

func TestSQLiteMalformedMetadataRowsAreIsolated(t *testing.T) {
	root := t.TempDir()
	installSQLite(t, root)
	a := New(root)
	a.sqliteRead = func(ctx context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
		return sqliteread.WithReadOnlyTx(ctx, root, path, maxBytes, func(tx *sqliteread.ReadTx) error {
			return fn(injectingMetadataQueryer{sqliteQueryer: tx})
		})
	}
	sessions, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	findSession(t, sessions, "opencode:s1")
	healthy := findSession(t, sessions, "opencode:s1")
	if r, err := a.Open(context.Background(), healthy); err != nil {
		t.Fatal(err)
	} else {
		r.Close()
	}
	for _, session := range sessions {
		if session.ID == "opencode:bad-time" || session.ID == "opencode:" {
			t.Fatalf("invalid metadata row accepted: %#v", session)
		}
	}
}

func TestSQLiteOpenRejectsMetadataTampering(t *testing.T) {
	root := t.TempDir()
	installSQLite(t, root)
	a := New(root)
	sessions, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := findSession(t, sessions, "opencode:s1")
	if r, err := a.Open(context.Background(), original); err != nil {
		t.Fatal(err)
	} else {
		r.Close()
	}
	tests := map[string]func(*source.Session){
		"scope":  func(s *source.Session) { s.Scope.Root = "/synthetic/forged" },
		"parent": func(s *source.Session) { s.ParentID = "opencode:forged" },
		"usage": func(s *source.Session) {
			s.Usage = cloneUsage(s.Usage)
			s.Usage["input_tokens"]++
		},
		"message-count": func(s *source.Session) { s.MessageCount++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			forged := original
			forged.Capabilities = append([]source.Capability(nil), original.Capabilities...)
			forged.Usage = cloneUsage(original.Usage)
			mutate(&forged)
			if r, err := a.Open(context.Background(), forged); err == nil || r != nil {
				if r != nil {
					r.Close()
				}
				t.Fatal("tampered metadata accepted")
			}
		})
	}
}

func cloneUsage(input map[string]int64) map[string]int64 {
	output := make(map[string]int64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func TestSQLitePreferredOverLegacyDuplicate(t *testing.T) {
	root := t.TempDir()
	installSQLite(t, root)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"s1", "legacy-only"} {
		write(filepath.Join(root, "storage", "session", "p", id+".json"), `{"id":"`+id+`","directory":"/legacy"}`)
		write(filepath.Join(root, "storage", "message", id, "m.json"), `{"id":"m","sessionID":"`+id+`","role":"user"}`)
		write(filepath.Join(root, "storage", "part", "m", id+".json"), `{"id":"`+id+`-part","sessionID":"`+id+`","messageID":"m","type":"text","text":"legacy synthetic"}`)
	}
	sessions, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, session := range sessions {
		if session.ID == "opencode:s1" {
			count++
			if session.FormatVersion != "db-v2" {
				t.Fatalf("legacy won: %#v", session)
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate count=%d sessions=%#v", count, sessions)
	}
	findSession(t, sessions, "opencode:legacy-only")
}

func TestSQLiteUnsupportedSchemaIsClassifiedWithoutRawError(t *testing.T) {
	root := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(root, "opencode.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session (private_column TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = New(root).Discover(context.Background())
	var discovery *source.DiscoveryError
	if !errors.As(err, &discovery) {
		t.Fatalf("expected discovery classification, got %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "private_column") || err.Error() != "opencode: unsupported database schema" {
		t.Fatalf("raw schema error leaked: %v", err)
	}
}
