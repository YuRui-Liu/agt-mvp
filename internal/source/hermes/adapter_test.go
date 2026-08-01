package hermes

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
)

func TestDiscoverOpenStrictAndStale(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "20260403_153620_alpha.jsonl")
	body := "{\"role\":\"session_meta\",\"model\":\"model-a\",\"platform\":\"local\",\"timestamp\":\"2026-04-03T15:36:20Z\"}\n" +
		"{\"role\":\"user\",\"content\":\"inspect demo\",\"timestamp\":\"2026-04-03T15:36:21Z\"}\n" +
		"{\"role\":\"assistant\",\"content\":\"checking\",\"tool_calls\":[{\"id\":\"c1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"demo.go\\\"}\"}}],\"timestamp\":\"2026-04-03T15:36:22Z\"}\n" +
		"{\"role\":\"tool\",\"tool_call_id\":\"c1\",\"content\":\"package demo\",\"timestamp\":\"2026-04-03T15:36:23Z\"}\n{bad\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.ID != "hermes-agent:20260403_153620_alpha" || s.MessageCount != 3 || s.MalformedCount != 1 {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec, n := json.NewDecoder(r), 0
	for {
		var e any
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 4 {
		t.Fatalf("events=%d", n)
	}
	if _, err := New(root).Open(context.Background(), s); err == nil {
		t.Fatal("cross-instance accepted")
	}
	if err := os.WriteFile(path, []byte(body+" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), s); err == nil {
		t.Fatal("stale accepted")
	}
}
func TestRejectsMultipleRoots(t *testing.T) {
	if _, err := New(t.TempDir(), t.TempDir()).Discover(context.Background()); err == nil {
		t.Fatal("multiple roots accepted")
	}
}
func TestVersionedFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/hermes/v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].FormatVersion != "jsonl-v1" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
func TestSafetyContract(t *testing.T) {
	data := adaptertest.ReadFixture(t, "../testdata/hermes/v1.jsonl")
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, b []byte) error { return os.WriteFile(filepath.Join(root, "fixture.jsonl"), b, 0o600) }, data)
}

func TestStateDBReadOnlyParentAndUsage(t *testing.T) {
	base := t.TempDir()
	sessions := filepath.Join(base, "sessions")
	if err := os.Mkdir(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE sessions(id TEXT PRIMARY KEY,parent_session_id TEXT,started_at REAL,ended_at REAL,message_count INTEGER,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,reasoning_tokens INTEGER)`,
		`CREATE TABLE messages(id INTEGER PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,tool_call_id TEXT,tool_calls TEXT,timestamp REAL)`,
		`INSERT INTO sessions VALUES('child','parent',1760000000,1760000001,2,10,5,2,1,3)`,
		`INSERT INTO sessions VALUES('parent','',1759999990,1759999991,1,1,1,0,0,0)`,
		`INSERT INTO messages VALUES(1,'child','user','hello','','',1760000000)`,
		`INSERT INTO messages VALUES(2,'child','assistant','done','','',1760000001)`,
		`INSERT INTO messages VALUES(3,'parent','user','parent','','',1759999990)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a := New(sessions)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	var s source.Session
	for _, candidate := range got {
		if candidate.ID == "hermes-agent:child" {
			s = candidate
		}
	}
	if s.ID != "hermes-agent:child" || s.ParentID != "hermes-agent:parent" || s.Usage["input_tokens"] != 10 || s.FormatVersion != "state-db-v1" {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := os.Stat(filepath.Join(base, "state.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("state db wal created: %v", err)
	}
}

func TestStateOnlyArchiveWithoutSessionsDirectory(t *testing.T) {
	base := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{`CREATE TABLE sessions(id TEXT PRIMARY KEY,parent_session_id TEXT,started_at REAL,ended_at REAL,message_count INTEGER,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,reasoning_tokens INTEGER)`, `CREATE TABLE messages(id INTEGER PRIMARY KEY,session_id TEXT,role TEXT,content TEXT,tool_call_id TEXT,tool_calls TEXT,timestamp REAL)`, `INSERT INTO sessions VALUES('only','',1760000000,1760000001,1,1,1,0,0,0)`, `INSERT INTO messages VALUES(1,'only','user','hello','','',1760000000)`} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := New(filepath.Join(base, "sessions")).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "hermes-agent:only" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestAuthorizationDoesNotStoreOutput(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches slice field %s", typ.Field(i).Name)
		}
	}
}

func TestTranscriptParentLinksOnlyWhenDiscovered(t *testing.T) {
	root := t.TempDir()
	meta := func(parent string) []byte {
		return []byte("{\"role\":\"session_meta\",\"parent_session_id\":\"" + parent + "\"}\n{\"role\":\"user\",\"content\":\"x\"}\n")
	}
	if err := os.WriteFile(filepath.Join(root, "parent.jsonl"), meta(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "child.jsonl"), meta("parent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "orphan.jsonl"), meta("missing"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		switch s.ID {
		case "hermes-agent:child":
			if s.ParentID != "hermes-agent:parent" {
				t.Fatalf("child=%#v", s)
			}
		case "hermes-agent:orphan":
			if s.ParentID != "" {
				t.Fatalf("orphan=%#v", s)
			}
		}
	}
}

func TestRejectsMissingMetaAndSymlink(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fake.jsonl"), []byte("{\"role\":\"user\",\"content\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out.jsonl")
	if err := os.WriteFile(out, []byte("{\"role\":\"session_meta\"}\n{\"role\":\"user\",\"content\":\"x\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(root, "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestJSONSessionAndStableConversationGroup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session_alpha.json")
	body := `{"platform":"local","session_start":"2026-01-01T00:00:00Z","last_updated":"2026-01-01T00:00:01Z","messages":[{"role":"user","content":"hello","timestamp":"2026-01-01T00:00:00Z"},{"role":"assistant","content":"done","timestamp":"2026-01-01T00:00:01Z"}]}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID != "hermes-agent:alpha" || got[0].FormatVersion != "json-v1" || got[0].Scope.Type != "conversation_group" {
		t.Fatalf("session=%#v", got[0])
	}
	key := got[0].Scope.Root
	body = strings.Replace(body, "done", "done appended", 1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = a.Discover(context.Background())
	if err != nil || got[0].Scope.Root != key {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
