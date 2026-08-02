package qoder

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sharedclient"
	_ "modernc.org/sqlite"
)

func qoderFixture() []byte {
	return []byte(strings.Join([]string{
		`{"type":"session_meta","uuid":"meta-1","sessionId":"session-1","timestamp":"2026-01-01T00:00:00.000001Z","cwd":"/synthetic/course-project"}`,
		`{"type":"user","uuid":"user-1","sessionId":"session-1","timestamp":"2026-01-01T00:00:01.000001Z","cwd":"/synthetic/course-project","message":{"content":"build the feature"}}`,
		`{"type":"progress","uuid":"progress-1","sessionId":"session-1","timestamp":"2026-01-01T00:00:02.000001Z","cwd":"/synthetic/course-project","data":{"type":"hook_progress","hookName":"before","hookEvent":"run","command":"private command"}}`,
		`{"type":"assistant","uuid":"assistant-1","sessionId":"session-1","timestamp":"2026-01-01T00:00:03.000001Z","cwd":"/synthetic/course-project","message":{"content":[{"type":"thinking","thinking":"reason safely"},{"type":"text","text":"done"}]}}`,
	}, "\n") + "\n")
}

func mutateQoderFixture(t *testing.T, body []byte, mutate func(int, map[string]any)) []byte {
	t.Helper()
	var out strings.Builder
	for index, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatal(err)
		}
		mutate(index, value)
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return []byte(out.String())
}

func installQoderCLI(t *testing.T, root, project, session string, body []byte) string {
	t.Helper()
	dir := filepath.Join(root, "projects", project, "transcript")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, session+".jsonl")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLIMachineEvidenceFixture(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "testdata", "qoder", "transcript-v1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(body)), "\n")); got != 6 {
		t.Fatalf("physical records=%d", got)
	}
	root := t.TempDir()
	installQoderCLI(t, root, "-synthetic-course-project", "session-fixture", body)
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	if sessions[0].MessageCount != 3 || !slices.Equal(sessions[0].Capabilities, []source.Capability{source.CapabilityMessages, source.CapabilityReasoning}) {
		t.Fatalf("session=%#v", sessions[0])
	}
}

func TestCLIExactTranscriptAndCanonicalOpen(t *testing.T) {
	root := t.TempDir()
	installQoderCLI(t, root, "-synthetic-course-project", "session-1", qoderFixture())
	// A legacy root transcript is intentionally outside the evidenced layout.
	if err := os.WriteFile(filepath.Join(root, "legacy.jsonl"), qoderFixture(), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter := NewCLI(root)
	if adapter.Product() != "qoder-cli" {
		t.Fatalf("product=%q", adapter.Product())
	}
	sessions, err := adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions=%#v", sessions)
	}
	session := sessions[0]
	if session.FormatVersion != "transcript-v1" || session.MessageCount != 3 || !slices.Equal(session.Capabilities, []source.Capability{source.CapabilityMessages, source.CapabilityReasoning}) {
		t.Fatalf("session=%#v", session)
	}
	if session.Scope.Root != sharedProjectRoot("/synthetic/course-project") || strings.Contains(session.Scope.Root, "synthetic") {
		t.Fatalf("scope=%#v", session.Scope)
	}
	r, err := adapter.Open(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private command") || strings.Contains(string(data), "/synthetic/") || strings.Contains(string(data), "session-1") {
		t.Fatalf("private output=%s", data)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines=%q", lines)
	}
	var types []string
	for _, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		types = append(types, event["type"].(string))
	}
	if !slices.Equal(types, []string{"message", "reasoning", "message"}) {
		t.Fatalf("types=%v", types)
	}
}

func TestCLIDoesNotGuessLegacyRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "session-1.jsonl"), qoderFixture(), 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 0 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIStrictEnvelopeDuplicateAndCandidateIsolation(t *testing.T) {
	root := t.TempDir()
	installQoderCLI(t, root, "-synthetic-course-project", "session-1", qoderFixture())
	badEnvelope := mutateQoderFixture(t, qoderFixture(), func(index int, value map[string]any) {
		value["sessionId"] = "session-bad"
		if index == 1 {
			delete(value, "cwd")
		}
	})
	installQoderCLI(t, root, "-synthetic-course-project", "session-bad", badEnvelope)
	conflict := mutateQoderFixture(t, qoderFixture(), func(index int, value map[string]any) {
		value["sessionId"] = "session-conflict"
		if index == 3 {
			value["uuid"] = "user-1"
		}
	})
	installQoderCLI(t, root, "-synthetic-course-project", "session-conflict", conflict)
	unknown := mutateQoderFixture(t, qoderFixture(), func(index int, value map[string]any) {
		value["sessionId"] = "session-unknown"
		if index == 3 {
			value["message"] = map[string]any{"content": []any{map[string]any{"type": "future", "payload": "private"}}}
		}
	})
	installQoderCLI(t, root, "-synthetic-course-project", "session-unknown", unknown)
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 {
		t.Fatalf("healthy/malformed isolation sessions=%#v", sessions)
	}
	for _, session := range sessions {
		if strings.Contains(session.ID, "conflict") {
			t.Fatalf("conflict accepted=%#v", session)
		}
	}
}

func TestCLIAggregateActualIOBudgetExactAndOneLess(t *testing.T) {
	root := t.TempDir()
	first := mutateQoderFixture(t, qoderFixture(), func(_ int, value map[string]any) { value["sessionId"] = "session-a" })
	second := mutateQoderFixture(t, qoderFixture(), func(_ int, value map[string]any) { value["sessionId"] = "session-b" })
	installQoderCLI(t, root, "-synthetic-course-project", "session-a", first)
	installQoderCLI(t, root, "-synthetic-course-project", "session-b", second)
	exact := NewCLI(root)
	exact.scanLimits.maxTotalBytes = int64(len(first) + len(second))
	if sessions, err := exact.Discover(context.Background()); err != nil || len(sessions) != 2 {
		t.Fatalf("exact sessions=%#v err=%v", sessions, err)
	}
	over := NewCLI(root)
	over.scanLimits.maxTotalBytes = int64(len(first) + len(second) - 1)
	if sessions, err := over.Discover(context.Background()); err == nil || sessions != nil {
		t.Fatalf("partial sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIAuthorizationTOCTOUAndCrossInstance(t *testing.T) {
	root := t.TempDir()
	path := installQoderCLI(t, root, "-synthetic-course-project", "session-1", qoderFixture())
	first := NewCLI(root)
	otherSessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(otherSessions) != 1 {
		t.Fatal(err)
	}
	adaptertest.AuthorizationContract(t, first, func() {
		if err := os.WriteFile(path, append(qoderFixture(), []byte("\n")...), 0o600); err != nil {
			t.Fatal(err)
		}
	}, otherSessions[0])
}

func TestCLIGrowthAfterStatCannotEscapeBudget(t *testing.T) {
	root := t.TempDir()
	body := qoderFixture()
	path := installQoderCLI(t, root, "-synthetic-course-project", "session-1", body)
	adapter := NewCLI(root)
	adapter.scanLimits.maxTotalBytes = int64(len(body))
	adapter.afterCandidateStat = func() {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = file.Write([]byte("x"))
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("write=%v close=%v", err, closeErr)
		}
	}
	if sessions, err := adapter.Discover(context.Background()); err == nil || sessions != nil {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestCLIBoundsMalformedAndSymlinkCandidates(t *testing.T) {
	root := t.TempDir()
	installQoderCLI(t, root, "-synthetic-course-project", "session-good", mutateQoderFixture(t, qoderFixture(), func(_ int, value map[string]any) { value["sessionId"] = "session-good" }))
	installQoderCLI(t, root, "-synthetic-course-project", "session-line", append([]byte(`{"type":"user","uuid":"x","sessionId":"session-line","timestamp":"2026-01-01T00:00:00Z","cwd":"/synthetic/course-project","message":{"content":"`), append(make([]byte, maxCLILineBytes), []byte(`"}}`)...)...))
	installQoderCLI(t, root, "-synthetic-course-project", "session-depth", append(bytesRepeat('[', maxJSONDepth+1), bytesRepeat(']', maxJSONDepth+1)...))
	target := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(target, qoderFixture(), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "projects", "-synthetic-course-project", "transcript", "session-link.jsonl")
	if err := os.Symlink(target, link); err != nil && !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
	sessions, err := NewCLI(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	if sessions[0].MessageCount != 3 {
		t.Fatalf("session=%#v", sessions[0])
	}
}

func bytesRepeat(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestCLIWindowsPathStableSortingAndForgedSession(t *testing.T) {
	root := t.TempDir()
	windows := mutateQoderFixture(t, qoderFixture(), func(_ int, value map[string]any) {
		value["sessionId"] = "session-z"
		value["cwd"] = `C:\Users\student\course`
	})
	installQoderCLI(t, root, "C--Users-student-course", "session-z", windows)
	alpha := mutateQoderFixture(t, qoderFixture(), func(_ int, value map[string]any) { value["sessionId"] = "session-a" })
	installQoderCLI(t, root, "-synthetic-course-project", "session-a", alpha)
	adapter := NewCLI(root)
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	if sessions[0].ID > sessions[1].ID {
		t.Fatalf("unstable order=%#v", sessions)
	}
	forged := sessions[0]
	forged.SnapshotID = "forged"
	if reader, err := adapter.Open(context.Background(), forged); err == nil || reader != nil {
		t.Fatal("forged session accepted")
	}
}

func qoderDDL() []string {
	return []string{
		`CREATE TABLE chat_session (session_id varchar(64) PRIMARY KEY,user_id VARCHAR(64) NOT NULL,user_name varchar(64),session_title varchar(256) NOT NULL,project_id varchar(64) NOT NULL,project_uri varchar(512),project_name varchar(64),gmt_create INTEGER,gmt_modified INTEGER,org_id VARCHAR(64) DEFAULT '',session_type VARCHAR(64) DEFAULT '',mode VARCHAR(64) DEFAULT '',version VARCHAR(64) DEFAULT '',preferred_model_info TEXT DEFAULT '',status VARCHAR(32) DEFAULT '',last_user_query_at INTEGER DEFAULT 0,stop_reason VARCHAR(20) DEFAULT '',extra TEXT DEFAULT '',parent_session_id VARCHAR(64) DEFAULT '',parent_tool_call_id VARCHAR(128) DEFAULT '')`,
		`CREATE TABLE chat_record (request_id varchar(64) PRIMARY KEY,session_id varchar(64) NOT NULL,chat_task varchar(64) NOT NULL,chat_context TEXT,system_role_content TEXT,question TEXT,answer TEXT,like_status INT,gmt_create INTEGER,gmt_modified INTEGER,finish_status INTEGER,filter_status VARCHAR(64) DEFAULT '',error_result VARCHAR(1024) DEFAULT '{}',code_language VARCHAR(62) DEFAULT '',extra TEXT DEFAULT '{}',session_type VARCHAR(64) DEFAULT '',summary TEXT DEFAULT '',intention_type VARCHAR(64) DEFAULT '',reasoning_content TEXT,mode VARCHAR(64) DEFAULT '',chat_prompt TEXT DEFAULT '',parent_session_id VARCHAR(64) DEFAULT '',parent_tool_call_id VARCHAR(128) DEFAULT '')`,
		`CREATE TABLE chat_message (id varchar(64) PRIMARY KEY,session_id VARCHAR(64),request_id VARCHAR(64),role VARCHAR(64),content TEXT,summary TEXT,summary_modified INTEGER,summary_trigger INTEGER DEFAULT 0,tool_result TEXT,token_info TEXT,model_info TEXT,extra TEXT DEFAULT '',gmt_create INTEGER)`,
		`CREATE TABLE chat_snapshot (snapshot_id varchar(64) PRIMARY KEY,session_id varchar(64) NOT NULL,chat_record_id varchar(64),status varchar(64),name varchar(64),description TEXT,gmt_create INTEGER,gmt_modified INTEGER)`,
		`CREATE TABLE account(secret TEXT)`, `CREATE TABLE token(secret TEXT)`, `CREATE TABLE goal(secret TEXT)`, `CREATE TABLE notification(secret TEXT)`,
	}
}

func openQoderDB(t *testing.T, root string, mutate func([]string) []string) (*sql.DB, string) {
	t.Helper()
	dir := filepath.Join(root, "cache", "db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "local.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := qoderDDL()
	if mutate != nil {
		statements = mutate(statements)
	}
	for _, statement := range append([]string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`}, statements...) {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("statement=%q err=%v", statement, err)
		}
	}
	return db, path
}

func insertQoderConversation(t *testing.T, db *sql.DB, sessionID, projectID string, offset int64) {
	t.Helper()
	base := int64(1_720_000_000_000) + offset
	statements := []string{
		fmt.Sprintf(`INSERT INTO chat_session(session_id,user_id,session_title,project_id,gmt_create,gmt_modified,status,last_user_query_at) VALUES (%q,'private-user','private-title',%q,%d,%d,'done',1720000000)`, sessionID, projectID, base, base+60),
		fmt.Sprintf(`INSERT INTO chat_record(request_id,session_id,chat_task,question,answer,reasoning_content,gmt_create,gmt_modified,finish_status) VALUES (%q,%q,'chat','conflicting record question','','',%d,%d,1)`, "request-"+sessionID, sessionID, base+10, base+20),
		fmt.Sprintf(`INSERT INTO chat_message(id,session_id,request_id,role,content,tool_result,gmt_create) VALUES (%q,%q,%q,'user','message question','',%d)`, "user-"+sessionID, sessionID, "request-"+sessionID, base+30),
		fmt.Sprintf(`INSERT INTO chat_message(id,session_id,request_id,role,content,tool_result,gmt_create) VALUES (%q,%q,%q,'assistant','message answer','',%d)`, "assistant-"+sessionID, sessionID, "request-"+sessionID, base+31),
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIDEQoderSchemaWALMessagesOnlyAndNoDecoys(t *testing.T) {
	root := t.TempDir()
	db, _ := openQoderDB(t, root, nil)
	defer db.Close()
	insertQoderConversation(t, db, "session-1", "/synthetic/course-project", 0)
	var queries []sharedclient.QueryEvent
	adapter := NewIDE(root)
	adapter.options = []sharedclient.Option{sharedclient.WithQueryObserver(func(event sharedclient.QueryEvent) { queries = append(queries, event) })}
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	session := sessions[0]
	if session.Product != "qoder-ide" || session.MessageCount != 2 || !slices.Equal(session.Capabilities, []source.Capability{source.CapabilityMessages}) {
		t.Fatalf("session=%#v", session)
	}
	if !strings.HasPrefix(session.Scope.Root, "qoder-ide:project:") || session.Scope.Root == sharedProjectRoot("/synthetic/course-project") {
		t.Fatalf("scope guessed path=%#v", session.Scope)
	}
	r, err := adapter.Open(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "conflicting record") || strings.Contains(string(data), "private") {
		t.Fatalf("record/private leaked")
	}
	for _, query := range queries {
		for _, decoy := range []string{"account", "token", "goal", "notification"} {
			if query.Table == decoy {
				t.Fatalf("queried %s", decoy)
			}
		}
	}
}

func TestIDEUnknownSchemaAndMalformedIsolation(t *testing.T) {
	unknownRoot := t.TempDir()
	db, _ := openQoderDB(t, unknownRoot, func(statements []string) []string {
		return append(statements, `ALTER TABLE chat_session ADD COLUMN surprise TEXT`)
	})
	db.Close()
	result, err := source.NewRegistry(NewIDE(unknownRoot)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources["qoder-ide"].State != source.SourceFormatUnsupported {
		t.Fatalf("state=%s", result.Sources["qoder-ide"].State)
	}
	root := t.TempDir()
	db, _ = openQoderDB(t, root, nil)
	defer db.Close()
	insertQoderConversation(t, db, "good", "opaque-project", 0)
	insertQoderConversation(t, db, "bad", "opaque-project", 100)
	if _, err := db.Exec(`UPDATE chat_message SET content=x'00' WHERE session_id='bad'`); err != nil {
		t.Fatal(err)
	}
	sessions, err := NewIDE(root).Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestIDEAllMalformedIsReadErrorButEmptyIsNotFound(t *testing.T) {
	emptyRoot := t.TempDir()
	emptyDB, _ := openQoderDB(t, emptyRoot, nil)
	emptyDB.Close()
	empty, err := source.NewRegistry(NewIDE(emptyRoot)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if empty.Sources["qoder-ide"].State != source.SourceNotFound {
		t.Fatalf("empty state=%s", empty.Sources["qoder-ide"].State)
	}
	adapter := NewIDE(emptyRoot)
	if sessions, discoverErr := adapter.Discover(context.Background()); discoverErr != nil || len(sessions) != 0 {
		t.Fatalf("first repeat sessions=%#v err=%v", sessions, discoverErr)
	}
	if sessions, discoverErr := adapter.Discover(context.Background()); discoverErr != nil || len(sessions) != 0 {
		t.Fatalf("second repeat sessions=%#v err=%v", sessions, discoverErr)
	}
	root := t.TempDir()
	db, _ := openQoderDB(t, root, nil)
	defer db.Close()
	insertQoderConversation(t, db, "bad", "opaque-project", 0)
	if _, err := db.Exec(`UPDATE chat_message SET content=x'00'`); err != nil {
		t.Fatal(err)
	}
	result, err := source.NewRegistry(NewIDE(root)).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Sources["qoder-ide"].State != source.SourceReadError {
		t.Fatalf("malformed state=%s", result.Sources["qoder-ide"].State)
	}
}

func TestIDEAuthorizationAndSameSessionDifferentDB(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	firstDB, _ := openQoderDB(t, firstRoot, nil)
	defer firstDB.Close()
	insertQoderConversation(t, firstDB, "same", "opaque-project", 0)
	secondDB, _ := openQoderDB(t, secondRoot, nil)
	defer secondDB.Close()
	insertQoderConversation(t, secondDB, "same", "opaque-project", 0)
	first := NewIDE(firstRoot)
	secondSessions, err := NewIDE(secondRoot).Discover(context.Background())
	if err != nil || len(secondSessions) != 1 {
		t.Fatal(err)
	}
	firstSessions, err := first.Discover(context.Background())
	if err != nil || len(firstSessions) != 1 {
		t.Fatal(err)
	}
	if firstSessions[0].ID == secondSessions[0].ID || firstSessions[0].Scope.Root == secondSessions[0].Scope.Root {
		t.Fatal("different databases collided")
	}
	if reader, err := first.Open(context.Background(), secondSessions[0]); err == nil || reader != nil {
		t.Fatal("cross-instance accepted")
	}
	if _, err := firstDB.Exec(`UPDATE chat_message SET content='changed' WHERE session_id='same'`); err != nil {
		t.Fatal(err)
	}
	if reader, err := first.Open(context.Background(), firstSessions[0]); err == nil || reader != nil {
		t.Fatal("changed file-set accepted")
	}
}

func TestMissingAndCanceledRoots(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, adapter := range []source.Adapter{NewCLI(missing), NewIDE(missing)} {
		if sessions, err := adapter.Discover(context.Background()); err != nil || len(sessions) != 0 {
			t.Fatalf("sessions=%#v err=%v", sessions, err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := adapter.Discover(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	}
}
