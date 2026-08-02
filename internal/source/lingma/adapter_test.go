package lingma

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sharedclient"
	_ "modernc.org/sqlite"
)

func installCLI(t *testing.T, root, project, task string, data []byte) string {
	t.Helper()
	directory := filepath.Join(root, "cli", "projects", project)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, task+".session.execution.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readCLIFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", "lingma", "execution-v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateCLIFixture(t *testing.T, data []byte, mutate func(int, map[string]any)) []byte {
	t.Helper()
	var output bytes.Buffer
	for index, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		mutate(index, record)
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(encoded)
		output.WriteByte('\n')
	}
	return output.Bytes()
}

func TestCLIExactDiscoveryAndCanonicalOpen(t *testing.T) {
	root := t.TempDir()
	fixture := readCLIFixture(t)
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	if err := os.WriteFile(filepath.Join(root, "cli", "projects", "-synthetic-course-project", "credentials.json"), []byte(`{"token":"do-not-read"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cli", "logs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cli", "logs", "lingmacli.log"), []byte("do-not-read"), 0o600); err != nil {
		t.Fatal(err)
	}

	adapter := NewCLI(root)
	if adapter.Product() != "tongyi-lingma-cli" {
		t.Fatalf("product=%q", adapter.Product())
	}
	sessions, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%d", len(sessions))
	}
	session := sessions[0]
	if session.FormatVersion != "execution-v1" || session.AdapterVersion != "1" || session.MessageCount != 2 || session.MalformedCount != 0 {
		t.Fatalf("session=%#v", session)
	}
	if !slices.Equal(session.Capabilities, []source.Capability{source.CapabilityMessages}) {
		t.Fatalf("capabilities=%v", session.Capabilities)
	}
	if session.Scope.Type != source.ScopeProject || session.Scope.Root != sharedProjectRoot("/synthetic/course-project") || strings.Contains(session.Scope.Root, "synthetic") || session.Scope.Label != "course-project" {
		t.Fatalf("scope=%#v", session.Scope)
	}
	if session.ID == "task-fixture" || session.OpaqueRef == "" || session.SnapshotID == "" {
		t.Fatalf("private binding missing: %#v", session)
	}
	reader, err := adapter.Open(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	adaptertest.AssertCanonicalEvents(t, reader, map[string]bool{"message": true})
}

func TestCLIRejectsUnknownVersionAsFormatUnsupported(t *testing.T) {
	root := t.TempDir()
	fixture := readCLIFixture(t)
	fixture = bytes.ReplaceAll(fixture, []byte("0.11.3-quest"), []byte("99.0-unknown"))
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	result, err := source.NewRegistry(NewCLI(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-cli"].State; got != source.SourceFormatUnsupported {
		t.Fatalf("state=%q", got)
	}
}

func TestCLIIsolatesBadLinesButTerminatesBudgets(t *testing.T) {
	root := t.TempDir()
	fixture := readCLIFixture(t)
	fixture = append(append([]byte("{bad-json}\n{}\n"), fixture...), []byte("{bad-tail")...)
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].MalformedCount != 3 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}

	oversizeRoot := t.TempDir()
	installCLI(t, oversizeRoot, "project", "task-fixture", bytes.Repeat([]byte("x"), maxCLILineBytes+1))
	result, err := source.NewRegistry(NewCLI(oversizeRoot)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-cli"].State; got != source.SourceReadError {
		t.Fatalf("state=%q", got)
	}
}

func TestCLIRequiresExactEncodedProjectEvidence(t *testing.T) {
	root := t.TempDir()
	installCLI(t, root, "unrelated-directory", "task-fixture", readCLIFixture(t))
	result, err := source.NewRegistry(NewCLI(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-cli"].State; got != source.SourceReadError {
		t.Fatalf("state=%q", got)
	}
}

func TestCLIHealthyCandidateSurvivesBadCandidates(t *testing.T) {
	root := t.TempDir()
	fixture := readCLIFixture(t)
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	unknown := mutateCLIFixture(t, fixture, func(_ int, record map[string]any) {
		record["sessionId"] = "task-unknown"
		record["version"] = "99-unknown"
	})
	installCLI(t, root, "-synthetic-course-project", "task-unknown", unknown)
	installCLI(t, root, "-synthetic-course-project", "task-malformed", []byte("{}\n"))
	conflict := mutateCLIFixture(t, fixture, func(index int, record map[string]any) {
		record["sessionId"] = "task-conflict"
		if index == 2 {
			record["uuid"] = "event-user"
		}
	})
	installCLI(t, root, "-synthetic-course-project", "task-conflict", conflict)
	installCLI(t, root, "-synthetic-course-project", "task-oversize", bytes.Repeat([]byte("x"), maxCLILineBytes+1))

	result, err := source.NewRegistry(NewCLI(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-cli"].State; got != source.SourceReady {
		t.Fatalf("state=%q", got)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].MessageCount != 2 {
		t.Fatalf("sessions=%#v", result.Sessions)
	}
}

func TestCLIScanByteBudgetAllowsExactAndRejectsOneByteLess(t *testing.T) {
	root := t.TempDir()
	makeCandidate := func(task string) []byte {
		return mutateCLIFixture(t, readCLIFixture(t), func(_ int, record map[string]any) {
			record["sessionId"] = task
		})
	}
	first := makeCandidate("task-a")
	second := makeCandidate("task-b")
	installCLI(t, root, "-synthetic-course-project", "task-a", first)
	installCLI(t, root, "-synthetic-course-project", "task-b", second)
	total := int64(len(first) + len(second))

	exact := NewCLI(root)
	exact.scanLimits.maxTotalBytes = total
	sessions, err := exact.Discover(context.Background())
	if err != nil || len(sessions) != 2 {
		t.Fatalf("exact sessions=%#v err=%v", sessions, err)
	}

	over := NewCLI(root)
	over.scanLimits.maxTotalBytes = total - 1
	sessions, err = over.Discover(context.Background())
	if err == nil || sessions != nil {
		t.Fatalf("over-budget returned partial sessions=%#v err=%v", sessions, err)
	}
	result, registryErr := source.NewRegistry(over).Scan(context.Background())
	if registryErr != nil {
		t.Fatal(registryErr)
	}
	if got := result.Sources["tongyi-lingma-cli"].State; got != source.SourceReadError || len(result.Sessions) != 0 {
		t.Fatalf("state=%q sessions=%#v", got, result.Sessions)
	}
}

func TestCLIScanByteBudgetChargesUnsupportedCandidateAfterHealthy(t *testing.T) {
	root := t.TempDir()
	healthy := readCLIFixture(t)
	unknown := mutateCLIFixture(t, healthy, func(_ int, record map[string]any) {
		record["sessionId"] = "task-z-unknown"
		record["version"] = "99-unknown"
	})
	healthyA := mutateCLIFixture(t, healthy, func(_ int, record map[string]any) { record["sessionId"] = "task-a" })
	installCLI(t, root, "-synthetic-course-project", "task-a", healthyA)
	installCLI(t, root, "-synthetic-course-project", "task-z-unknown", unknown)
	adapter := NewCLI(root)
	adapter.scanLimits.maxTotalBytes = int64(len(healthyA) + len(unknown) - 1)
	if sessions, err := adapter.Discover(context.Background()); err == nil || sessions != nil {
		t.Fatalf("unsupported candidate was free: sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIScanByteBudgetChargesOversizeCandidate(t *testing.T) {
	root := t.TempDir()
	oversizePath := installCLI(t, root, "-synthetic-course-project", "task-a-oversize", []byte("{}\n"))
	if err := os.Truncate(oversizePath, maxCLIFileBytes+1); err != nil {
		t.Fatal(err)
	}
	healthy := mutateCLIFixture(t, readCLIFixture(t), func(_ int, record map[string]any) { record["sessionId"] = "task-z" })
	installCLI(t, root, "-synthetic-course-project", "task-z", healthy)
	adapter := NewCLI(root)
	adapter.scanLimits.maxTotalBytes = maxCLIFileBytes + 1 + int64(len(healthy)) - 1
	if sessions, err := adapter.Discover(context.Background()); err == nil || sessions != nil {
		t.Fatalf("oversize candidate was free: sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIByteBudgetIsOverflowSafeAndContextAware(t *testing.T) {
	budget := cliByteBudget{maximum: math.MaxInt64, used: math.MaxInt64 - 1}
	if err := budget.consume(context.Background(), 2); !errors.Is(err, errCLIScanByteBudget) {
		t.Fatalf("overflow err=%v", err)
	}
	if budget.used != math.MaxInt64-1 {
		t.Fatalf("overflow changed usage=%d", budget.used)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := budget.consume(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled err=%v", err)
	}
}

func TestCLIZeroHealthyMalformedCandidateIsReadError(t *testing.T) {
	root := t.TempDir()
	installCLI(t, root, "-synthetic-course-project", "task-malformed", []byte("{}\n"))
	result, err := source.NewRegistry(NewCLI(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-cli"].State; got != source.SourceReadError {
		t.Fatalf("state=%q", got)
	}
}

func TestCLIStrictMessageEnvelopeIsolatesInvalidLine(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "meta", mutate: func(record map[string]any) { record["isMeta"] = true }},
		{name: "sidechain", mutate: func(record map[string]any) { record["isSidechain"] = true }},
		{name: "user type", mutate: func(record map[string]any) { record["userType"] = "internal" }},
		{name: "missing agent", mutate: func(record map[string]any) { delete(record, "agentId") }},
		{name: "missing parent uuid", mutate: func(record map[string]any) { delete(record, "parentUuid") }},
		{name: "missing request set", mutate: func(record map[string]any) { delete(record, "requestSetId") }},
		{name: "missing message id", mutate: func(record map[string]any) { delete(record["message"].(map[string]any), "id") }},
		{name: "numeric message id", mutate: func(record map[string]any) { record["message"].(map[string]any)["id"] = 7 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			fixture := mutateCLIFixture(t, readCLIFixture(t), func(index int, record map[string]any) {
				if index == 0 {
					test.mutate(record)
				}
			})
			installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
			sessions, err := NewCLI(root).Discover(context.Background())
			if err != nil || len(sessions) != 1 {
				t.Fatalf("sessions=%#v err=%v", sessions, err)
			}
			if sessions[0].MessageCount != 1 || sessions[0].MalformedCount != 1 {
				t.Fatalf("session=%#v", sessions[0])
			}
		})
	}
}

func TestCLIAgentIDMustRemainStableWithinSession(t *testing.T) {
	root := t.TempDir()
	fixture := mutateCLIFixture(t, readCLIFixture(t), func(index int, record map[string]any) {
		if index == 2 {
			record["agentId"] = "other-agent"
		}
	})
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].MessageCount != 1 || sessions[0].MalformedCount != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIMalformedLineDoesNotBindSessionEnvelope(t *testing.T) {
	root := t.TempDir()
	fixture := mutateCLIFixture(t, readCLIFixture(t), func(index int, record map[string]any) {
		if index == 0 {
			record["timestamp"] = "not-a-time"
			record["agentId"] = "discarded-agent"
		}
	})
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].MessageCount != 1 || sessions[0].MalformedCount != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIAuthorizationIsInstanceAndSnapshotBound(t *testing.T) {
	root := t.TempDir()
	fixture := readCLIFixture(t)
	path := installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	first := NewCLI(root)
	otherSessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(otherSessions) != 1 {
		t.Fatal(err)
	}
	adaptertest.AuthorizationContract(t, first, func() {
		if err := os.WriteFile(path, append(fixture, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}, otherSessions[0])
}

func TestCLIDoesNotFollowSymlinkCandidates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	fixture := readCLIFixture(t)
	outsidePath := filepath.Join(outside, "task-fixture.session.execution.jsonl")
	if err := os.WriteFile(outsidePath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "cli", "projects", "-synthetic-course-project")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(directory, filepath.Base(outsidePath))); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func openLingmaDB(t *testing.T, root string, mutate func([]string) []string) (*sql.DB, string) {
	t.Helper()
	directory := filepath.Join(root, "cache", "db")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "local.db")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := lingmaDDL()
	if mutate != nil {
		statements = mutate(statements)
	}
	for _, statement := range append([]string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`}, statements...) {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("statement failed: %v", err)
		}
	}
	return database, path
}

func lingmaDDL() []string {
	return []string{
		`CREATE TABLE chat_session (session_id varchar(64) PRIMARY KEY,user_id VARCHAR(64) NOT NULL,user_name varchar(64),session_title varchar(256) NOT NULL,project_id varchar(64) NOT NULL,project_uri varchar(512),project_name varchar(64),gmt_create INTEGER,gmt_modified INTEGER,org_id VARCHAR(64) DEFAULT '',session_type VARCHAR(64) DEFAULT '',mode VARCHAR(64) DEFAULT '',version VARCHAR(64) DEFAULT '',preferred_model_info TEXT DEFAULT '',stop_reason VARCHAR(20) DEFAULT '',extra TEXT DEFAULT '',parent_session_id VARCHAR(64) DEFAULT '',parent_tool_call_id VARCHAR(128) DEFAULT '')`,
		`CREATE TABLE chat_record (request_id varchar(64) PRIMARY KEY,session_id varchar(64) NOT NULL,chat_task varchar(64) NOT NULL,chat_context TEXT,system_role_content TEXT,question TEXT,answer TEXT,like_status INT,gmt_create INTEGER,gmt_modified INTEGER,finish_status INTEGER,filter_status VARCHAR(64) DEFAULT '',error_result VARCHAR(1024) DEFAULT '{}',code_language VARCHAR(62) DEFAULT '',extra TEXT DEFAULT '{}',session_type VARCHAR(64) DEFAULT '',summary TEXT DEFAULT '',intention_type VARCHAR(64) DEFAULT '',reasoning_content TEXT,mode VARCHAR(64) DEFAULT '',chat_prompt TEXT DEFAULT '',parent_session_id VARCHAR(64) DEFAULT '',parent_tool_call_id VARCHAR(128) DEFAULT '')`,
		`CREATE TABLE chat_message (id varchar(64) PRIMARY KEY,session_id VARCHAR(64),request_id VARCHAR(64),role VARCHAR(64),content TEXT,summary TEXT,summary_modified INTEGER,summary_trigger INTEGER DEFAULT 0,tool_result TEXT,token_info TEXT,model_info TEXT,extra TEXT DEFAULT '',gmt_create INTEGER)`,
		`CREATE TABLE chat_snapshot (snapshot_id varchar(64) PRIMARY KEY,session_id varchar(64) NOT NULL,chat_record_id varchar(64),status varchar(64),name varchar(64),description TEXT,gmt_create INTEGER,gmt_modified INTEGER)`,
		`CREATE TABLE account (secret TEXT)`, `CREATE TABLE token (secret TEXT)`, `CREATE TABLE goal (secret TEXT)`, `CREATE TABLE notification (secret TEXT)`,
	}
}

func insertLingmaConversation(t *testing.T, db *sql.DB, sessionID, projectID string, offset int64) {
	t.Helper()
	base := int64(1_720_000_000_000) + offset
	statements := []string{
		fmt.Sprintf(`INSERT INTO chat_session(session_id,user_id,session_title,project_id,gmt_create,gmt_modified,session_type,mode,version,stop_reason,parent_session_id,parent_tool_call_id) VALUES (%q,'private-user','private-title',%q,%d,%d,'agent','code','v1','done','','')`, sessionID, projectID, base, base+60),
		fmt.Sprintf(`INSERT INTO chat_record(request_id,session_id,chat_task,question,answer,reasoning_content,gmt_create,gmt_modified,finish_status) VALUES (%q,%q,'chat','question','answer','reasoning',%d,%d,1)`, "request-"+sessionID, sessionID, base+10, base+20),
		fmt.Sprintf(`INSERT INTO chat_message(id,session_id,request_id,role,content,tool_result,gmt_create) VALUES (%q,%q,%q,'user','message question','',%d)`, "message-user-"+sessionID, sessionID, "request-"+sessionID, base+30),
		fmt.Sprintf(`INSERT INTO chat_message(id,session_id,request_id,role,content,tool_result,gmt_create) VALUES (%q,%q,%q,'assistant','message answer','',%d)`, "message-assistant-"+sessionID, sessionID, "request-"+sessionID, base+31),
		fmt.Sprintf(`INSERT INTO chat_snapshot(snapshot_id,session_id,chat_record_id,status,gmt_create,gmt_modified) VALUES (%q,%q,%q,'ready',%d,%d)`, "snapshot-"+sessionID, sessionID, "request-"+sessionID, base+40, base+50),
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIDEExactSchemaWALAndCanonicalOpen(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "session-1", "/synthetic/course-project", 0)
	adapter := NewIDE(root)
	if adapter.Product() != "tongyi-lingma-ide" {
		t.Fatalf("product=%q", adapter.Product())
	}
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	session := sessions[0]
	if session.FormatVersion != "sharedclient-db-v1" || session.AdapterVersion != "1" || session.MessageCount != 2 {
		t.Fatalf("session=%#v", session)
	}
	if !slices.Equal(session.Capabilities, []source.Capability{source.CapabilityMessages}) {
		t.Fatalf("capabilities=%v", session.Capabilities)
	}
	if session.Scope.Type != source.ScopeProject || session.Scope.Root != sharedProjectRoot("/synthetic/course-project") || session.Scope.Label != "course-project" {
		t.Fatalf("scope=%#v", session.Scope)
	}
	reader, err := adapter.Open(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	adaptertest.AssertCanonicalEvents(t, reader, map[string]bool{"message": true})
}

func TestIDEUsesOneSnapshotLoadAndNeverQueriesDecoys(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "session-1", "/synthetic/course-project", 0)
	insertLingmaConversation(t, db, "session-2", "/synthetic/course-project", 100)
	var events []sharedclient.QueryEvent
	adapter := NewIDE(root)
	adapter.options = []sharedclient.Option{sharedclient.WithQueryObserver(func(event sharedclient.QueryEvent) { events = append(events, event) })}
	if sessions, err := adapter.Discover(context.Background()); err != nil || len(sessions) != 2 {
		t.Fatalf("sessions=%d err=%v", len(sessions), err)
	}
	counts := map[string]int{}
	for _, event := range events {
		counts[event.Table]++
		for _, forbidden := range []string{"account", "token", "goal", "notification"} {
			if event.Table == forbidden {
				t.Fatalf("queried %s", forbidden)
			}
		}
	}
	for _, table := range []string{"chat_session", "chat_record", "chat_message", "chat_snapshot"} {
		if counts[table] != 2 {
			t.Fatalf("table %s queries=%d events=%#v", table, counts[table], events)
		}
	}
}

func TestIDEUnknownSchemaIsFormatUnsupported(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, func(statements []string) []string {
		return append(statements, `ALTER TABLE chat_session ADD COLUMN surprise TEXT`)
	})
	db.Close()
	result, err := source.NewRegistry(NewIDE(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-ide"].State; got != source.SourceFormatUnsupported {
		t.Fatalf("state=%q", got)
	}
}

func TestIDEMalformedConversationIsIsolated(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "good", "/synthetic/course-project", 0)
	insertLingmaConversation(t, db, "bad", "/synthetic/course-project", 100)
	if _, err := db.Exec(`UPDATE chat_message SET content=x'00' WHERE session_id='bad'`); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewIDE(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestIDEAllMalformedRowsReturnReadError(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "bad", "/synthetic/course-project", 0)
	if _, err := db.Exec(`UPDATE chat_message SET content=x'00' WHERE session_id='bad'`); err != nil {
		t.Fatal(err)
	}
	result, err := source.NewRegistry(NewIDE(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-ide"].State; got != source.SourceReadError {
		t.Fatalf("state=%q", got)
	}
}

func TestIDERelativeProjectIDUsesIDEOnlyScope(t *testing.T) {
	root := t.TempDir()
	installCLI(t, root, "-synthetic-course-project", "task-fixture", readCLIFixture(t))
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "session-1", "project-hash", 0)
	sessions, err := NewIDE(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	if got := sessions[0].Scope; got.Type != source.ScopeProject || !strings.HasPrefix(got.Root, "tongyi-lingma-ide:project:") || got.Label != "Lingma IDE project" {
		t.Fatalf("scope=%#v", got)
	}
	if got := sessions[0].Scope.Root; got == sharedProjectRoot("project-hash") {
		t.Fatal("relative project ID joined shared scope")
	}
	cliSessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(cliSessions) != 1 {
		t.Fatalf("CLI sessions=%#v err=%v", cliSessions, err)
	}
	scopes, err := source.GroupScopes(append(cliSessions, sessions...), bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 {
		t.Fatalf("relative IDE project was guessed into CLI scope: %#v", scopes)
	}
}

func TestIDEAuthorizationIsInstanceAndFileSetBound(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, nil)
	insertLingmaConversation(t, db, "session-1", "/synthetic/course-project", 0)
	first := NewIDE(root)
	otherSessions, err := NewIDE(root).Discover(context.Background())
	if err != nil || len(otherSessions) != 1 {
		t.Fatal(err)
	}
	adaptertest.AuthorizationContract(t, first, func() {
		insertLingmaConversation(t, db, "session-2", "/synthetic/course-project", 100)
	}, otherSessions[0])
	db.Close()
}

func TestMissingRootsReturnNoSessionsAndCancellationWins(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, adapter := range []source.Adapter{NewCLI(missing), NewIDE(missing)} {
		sessions, err := adapter.Discover(context.Background())
		if err != nil || len(sessions) != 0 {
			t.Fatalf("product=%s sessions=%d err=%v", adapter.Product(), len(sessions), err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := adapter.Discover(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("product=%s err=%v", adapter.Product(), err)
		}
	}
}

type staticAdapter struct {
	product  string
	sessions []source.Session
}

func (a staticAdapter) Product() string                                    { return a.product }
func (a staticAdapter) Capabilities() []source.Capability                  { return nil }
func (a staticAdapter) Discover(context.Context) ([]source.Session, error) { return a.sessions, nil }
func (a staticAdapter) Open(context.Context, source.Session) (io.ReadCloser, error) {
	return nil, errors.New("unused")
}

func TestRegistryAndGroupScopesUseOnlyExactProjectEvidence(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "same", "/synthetic/course-project", 0)
	insertLingmaConversation(t, db, "different", "/synthetic/other-project", 100)
	lingma := NewIDE(root)
	qoderEvidence := source.Session{ID: "qoder-same", Product: "qoder-ide", Capabilities: []source.Capability{source.CapabilityMessages}, Scope: source.ScopeRef{Type: source.ScopeProject, Root: sharedProjectRoot("/synthetic/course-project"), Label: "course-project"}}
	noEvidence := source.Session{ID: "qoder-none", Product: "qoder-cli", Capabilities: []source.Capability{source.CapabilityMessages}, Scope: source.ScopeRef{Type: source.ScopeSessionCollection, Root: "qoder-cli:collection:private", Label: "Qoder CLI sessions"}}
	result, err := source.NewRegistry(lingma, staticAdapter{product: "qoder-ide", sessions: []source.Session{qoderEvidence}}, staticAdapter{product: "qoder-cli", sessions: []source.Session{noEvidence}}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := source.GroupScopes(result.Sessions, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var merged, separate int
	for _, scope := range scopes {
		if scope.SessionCount == 2 && slices.Equal(scope.Products, []string{"qoder-ide", "tongyi-lingma-ide"}) {
			merged++
		}
		if scope.SessionCount == 1 {
			separate++
		}
	}
	if merged != 1 || separate != 2 {
		encoded, _ := json.Marshal(scopes)
		t.Fatalf("merged=%d separate=%d scopes=%s", merged, separate, encoded)
	}
}

func TestRegistryGroupsCLIAndIDEOnlyWhenExactProjectPathMatches(t *testing.T) {
	root := t.TempDir()
	installCLI(t, root, "-synthetic-course-project", "task-fixture", readCLIFixture(t))
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "session-1", "/synthetic/course-project", 0)
	result, err := source.NewRegistry(NewCLI(root), NewIDE(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scopes, err := source.GroupScopes(result.Sessions, bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0].SessionCount != 2 || !slices.Equal(scopes[0].Products, []string{"tongyi-lingma-cli", "tongyi-lingma-ide"}) {
		t.Fatalf("scopes=%#v", scopes)
	}
}

func TestMillisecondTimeConversionIsStable(t *testing.T) {
	got := milliseconds(1_720_000_000_123)
	if got.Format(time.RFC3339Nano) != "2024-07-03T09:46:40.123Z" {
		t.Fatalf("time=%s", got.Format(time.RFC3339Nano))
	}
	if !milliseconds(0).IsZero() {
		t.Fatal("zero timestamp was invented")
	}
}

func TestEventOrderingComparatorIsTotal(t *testing.T) {
	items := []canonicalEvent{{Type: "message", StableID: "b", at: 2, rank: 2}, {Type: "message", StableID: "a", at: 2, rank: 2}, {Type: "tool_result", StableID: "z", at: 1, rank: 3}}
	sortCanonical(items)
	if got := []string{items[0].StableID, items[1].StableID, items[2].StableID}; !slices.Equal(got, []string{"z", "a", "b"}) {
		t.Fatalf("order=%v", got)
	}
}

func TestNoFixtureOrCanonicalValueContainsPrivateData(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "testdata", "lingma", "execution-v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"13800138000", "token-", "/Users/", `C:\\Users\\`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("fixture contains private marker %q", forbidden)
		}
	}
}

func TestCanonicalTextAndProjectLabelRedactDirectPrivateValues(t *testing.T) {
	if got := sanitizeCanonicalText("/Users/private/project"); !strings.HasPrefix(got, "[redacted-path:") {
		t.Fatalf("text=%q", got)
	}
	if got := sanitizeCanonicalText(`C:\\Users\\private`); !strings.HasPrefix(got, "[redacted-path:") {
		t.Fatalf("text=%q", got)
	}
	if got := projectScope("/synthetic/13800138000").Label; got != "Lingma project" {
		t.Fatalf("phone label=%q", got)
	}
	if got := projectScope("/synthetic/token-secret").Label; got != "Lingma project" {
		t.Fatalf("secret label=%q", got)
	}
}

func TestEncodedProjectKeyWindowsStyle(t *testing.T) {
	if got := encodedProjectKey(`C:\work`); got != "C--work" {
		t.Fatalf("encoded=%q", got)
	}
}

func TestCLIWindowsStyleCWDEvidenceMatchesEncodedDirectory(t *testing.T) {
	root := t.TempDir()
	fixture := mutateCLIFixture(t, readCLIFixture(t), func(_ int, record map[string]any) {
		record["cwd"] = `C:\work`
	})
	installCLI(t, root, "C--work", "task-fixture", fixture)
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	if sessions[0].Scope.Label != "work" {
		t.Fatalf("scope=%#v", sessions[0].Scope)
	}
}

func TestCLIStableIDConflictKeepsFirstHealthyCandidate(t *testing.T) {
	root := t.TempDir()
	otherRoot := t.TempDir()
	fixture := readCLIFixture(t)
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	installCLI(t, otherRoot, "-synthetic-course-project", "task-fixture", fixture)
	sessions, err := NewCLI(root, otherRoot).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIConflictingEventIDFailsClosed(t *testing.T) {
	root := t.TempDir()
	fixture := readCLIFixture(t)
	fixture = bytes.Replace(fixture, []byte(`"uuid":"event-assistant"`), []byte(`"uuid":"event-user"`), 1)
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	result, err := source.NewRegistry(NewCLI(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sources["tongyi-lingma-cli"].State; got != source.SourceReadError {
		t.Fatalf("state=%q", got)
	}
}

func TestIDEFileReplacementIsRejected(t *testing.T) {
	root := t.TempDir()
	db, path := openLingmaDB(t, root, nil)
	insertLingmaConversation(t, db, "session-1", "/synthetic/course-project", 0)
	adapter := NewIDE(root)
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatal(err)
	}
	db.Close()
	replacementRoot := t.TempDir()
	replacement, replacementPath := openLingmaDB(t, replacementRoot, nil)
	insertLingmaConversation(t, replacement, "session-1", "/synthetic/course-project", 0)
	replacement.Close()
	data, err := os.ReadFile(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := adapter.Open(context.Background(), sessions[0])
	if reader != nil {
		reader.Close()
	}
	if err == nil {
		t.Fatal("replacement database accepted")
	}
}

func TestCLIAdjacentUnknownFilesAreIgnored(t *testing.T) {
	root := t.TempDir()
	fixture := readCLIFixture(t)
	installCLI(t, root, "-synthetic-course-project", "task-fixture", fixture)
	for _, name := range []string{"task-fixture.session.execution.json", "task-fixture.timeline.jsonl", "config.json", "token.jsonl"} {
		if err := os.WriteFile(filepath.Join(root, "cli", "projects", "-synthetic-course-project", name), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestIDECanonicalOutputDoesNotExposeStorageIdentifiers(t *testing.T) {
	root := t.TempDir()
	db, _ := openLingmaDB(t, root, nil)
	defer db.Close()
	insertLingmaConversation(t, db, "private-session-id", "/synthetic/private-project-id", 0)
	adapter := NewIDE(root)
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatal(err)
	}
	reader, err := adapter.Open(context.Background(), sessions[0])
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-session-id", "private-project-id", "private-user", "private-title", "request-private-session-id", "message-private-session-id"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("canonical output leaked identifier %q", forbidden)
		}
	}
}
