package codeflicker

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestJSONLProductNamespaceIsIndependent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "p", "same.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join("..", "testdata", "flicker", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Product != "codeflicker" || got[0].ID != "codeflicker:same" {
		t.Fatalf("session=%#v", got[0])
	}
	if got[0].Scope.Root != "/workspace/example-project" || got[0].MessageCount != 4 {
		t.Fatalf("fixture not parsed: %#v", got[0])
	}
}

func createDB(t *testing.T, path string, blobs map[string]string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE KwaipilotKV (key TEXT PRIMARY KEY, value BLOB, updatedAt INTEGER)`); err != nil {
		t.Fatal(err)
	}
	for key, blob := range blobs {
		if _, err := db.Exec(`INSERT INTO KwaipilotKV(key,value,updatedAt) VALUES(?,?,?)`, key, []byte(blob), int64(123)); err != nil {
			t.Fatal(err)
		}
	}
}

func validDBBlob(id, cwd, text string) string {
	value, _ := json.Marshal(map[string]any{
		"sessionId": id, "workspaceUri": "file://" + cwd, "chatModel": "CLAUDE_4_6",
		"localMessages": []any{
			map[string]any{"ts": int64(1000), "role": "user", "say": "text", "text": text},
			map[string]any{"ts": int64(1500), "say": "text", "text": "assistant reply"},
			map[string]any{"ts": int64(2000), "say": "tool", "jsonText": map[string]any{"tool": "Bash", "input": "go test", "output": "PASS"}, "toolCallId": "call-1"},
			map[string]any{"ts": int64(3000), "role": "assistant", "say": "thinking", "text": "reason"},
		},
	})
	return string(value)
}

func TestDiscoverAndOpenReadOnlyDatabase(t *testing.T) {
	root := t.TempDir()
	dbPath := filepath.Join(t.TempDir(), "composer_data.sqlite")
	createDB(t, dbPath, map[string]string{
		"composerData:db-1": validDBBlob("db-1", "/workspace/db", "hello"),
		"other:key":         `{}`,
	})
	before := snapshotSQLiteFiles(t, dbPath)
	got, err := New(root, dbPath).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.ID != "codeflicker:db-1" || s.Product != "codeflicker" ||
		s.FormatVersion != "sqlite" || s.Scope.Root != "/workspace/db" ||
		s.MessageCount != 4 {
		t.Fatalf("session=%#v", s)
	}
	r, err := New(root, dbPath).Open(context.Background(), s)
	if err == nil {
		r.Close()
		t.Fatal("cross-instance DB session accepted")
	}
	a := New(root, dbPath)
	got, err = a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	r, err = a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var kinds []string
	dec := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, event["type"].(string))
	}
	if !reflect.DeepEqual(kinds, []string{"message", "message", "tool_use", "tool_result", "message"}) {
		t.Fatalf("kinds=%v", kinds)
	}
	after := snapshotSQLiteFiles(t, dbPath)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("database files mutated: before=%#v after=%#v", before, after)
	}
}

type sqliteFileSnapshot struct {
	Exists bool
	Size   int64
	Mtime  time.Time
	Digest [sha256.Size]byte
}

func snapshotSQLiteFiles(t *testing.T, dbPath string) map[string]sqliteFileSnapshot {
	t.Helper()
	out := make(map[string]sqliteFileSnapshot, 3)
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		name := filepath.Base(path)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			out[name] = sqliteFileSnapshot{}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		snapshot := sqliteFileSnapshot{
			Exists: true,
			Size:   info.Size(),
			Mtime:  info.ModTime(),
		}
		// WAL-index reader marks live in mmap-backed -shm lock state and can
		// change without a persistent file write or mtime change. Hash the
		// durable database and WAL bytes; compare -shm existence/size/mtime.
		if !strings.HasSuffix(path, "-shm") {
			snapshot.Digest = sha256.Sum256(data)
		}
		out[name] = snapshot
	}
	return out
}

func TestDatabaseWinsDuplicateJSONLID(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "p", "same.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"type":"config","config":{"cwd":"/jsonl"}}`+"\n"+`{"type":"message","role":"user","content":"jsonl"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "composer_data.sqlite")
	createDB(t, dbPath, map[string]string{"composerData:same": validDBBlob("same", "/database", "db")})
	got, err := New(root, dbPath).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].FormatVersion != "sqlite" || got[0].Scope.Root != "/database" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestDatabaseRejectsMaliciousKeysIdentityAndOversize(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "composer_data.sqlite")
	createDB(t, dbPath, map[string]string{
		"composerData:../evil":  validDBBlob("../evil", "/p", "evil"),
		"composerData:mismatch": validDBBlob("different", "/p", "bad"),
		"composerData:large":    strings.Repeat("x", maxDatabaseBlobBytes+1),
	})
	got, err := New(t.TempDir(), dbPath).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestDatabaseOpenRejectsChangedBlobAndMetadata(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "composer_data.sqlite")
	createDB(t, dbPath, map[string]string{"composerData:s": validDBBlob("s", "/p", "one")})
	a := New(t.TempDir(), dbPath)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	changed := got[0]
	changed.MessageCount++
	if _, err := a.Open(context.Background(), changed); err == nil {
		t.Fatal("modified metadata accepted")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE KwaipilotKV SET value=?, updatedAt=? WHERE key=?`,
		[]byte(validDBBlob("s", "/p", "two")), time.Now().UnixMilli(), "composerData:s"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := a.Open(context.Background(), got[0]); err == nil {
		t.Fatal("changed DB blob accepted")
	}
}

func TestReadOnlyDatabaseSeesExistingWAL(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "composer_data.sqlite")
	writer, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`CREATE TABLE KwaipilotKV (key TEXT PRIMARY KEY, value BLOB, updatedAt INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`INSERT INTO KwaipilotKV VALUES(?,?,?)`,
		"composerData:wal", []byte(validDBBlob("wal", "/wal", "visible")), int64(1)); err != nil {
		t.Fatal(err)
	}
	before := snapshotSQLiteFiles(t, dbPath)
	got, err := New(t.TempDir(), dbPath).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "codeflicker:wal" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	after := snapshotSQLiteFiles(t, dbPath)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("WAL files mutated: before=%#v after=%#v", before, after)
	}
}

func TestDatabaseHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := New(t.TempDir(), filepath.Join(t.TempDir(), "composer_data.sqlite"))
	if _, err := a.Discover(ctx); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkspaceStorageExtensionURIIsNotTreatedAsProject(t *testing.T) {
	blob := validDBBlob("s", "/Users/u/Library/Application Support/MyFlicker/User/workspaceStorage/abcdef/kuaishou.codeflicker", "x")
	session, _, _, err := parseDatabaseBlob([]byte(blob), "composerData:s")
	if err != nil {
		t.Fatal(err)
	}
	if session.Scope.Type != "session_collection" ||
		strings.Contains(session.Scope.Root, "/Users/") ||
		session.Scope.Label != "CodeFlicker sessions" {
		t.Fatalf("scope=%#v", session.Scope)
	}
}

func TestDatabaseBlobParsingHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := parseDatabaseBlobContext(ctx,
		[]byte(validDBBlob("s", "/p", "x")), "composerData:s"); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkspaceURIPlatformMatrix(t *testing.T) {
	tests := []struct {
		name, raw, goos, want string
	}{
		{"windows drive", "file:///C:/work/example", "windows", `C:\work\example`},
		{"windows escaped", "file:///C:/work/a%20b", "windows", `C:\work\a b`},
		{"windows UNC", "file://server/share/team", "windows", `\\server\share\team`},
		{"unix", "file:///work/example", "linux", "/work/example"},
		{"unix localhost", "file://localhost/work/example", "darwin", "/work/example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := workspacePathForOS(test.raw, test.goos)
			if err != nil || got != test.want {
				t.Fatalf("got=%q want=%q err=%v", got, test.want, err)
			}
		})
	}
	for _, raw := range []string{
		"relative/path",
		"https://server/work",
		"file:user@/work",
		"file://user@server/share",
		"file:///C:/work?mode=rw",
		"file:///C:/work#fragment",
		"file:///C:/work/../secret",
		"file://server/",
	} {
		t.Run("reject "+raw, func(t *testing.T) {
			if _, err := workspacePathForOS(raw, "windows"); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
	if _, err := workspacePathForOS("file://server/share", "linux"); err == nil {
		t.Fatal("Unix accepted remote file authority")
	}
}

func TestSQLiteReadOnlyURIPlatformMatrix(t *testing.T) {
	tests := []struct {
		name, path, goos, want string
	}{
		{"windows drive", `C:\db\a b#c.sqlite`, "windows", "file:///C:/db/a%20b%23c.sqlite?mode=ro"},
		{"windows UNC", `\\server\share\a b.sqlite`, "windows", "file://server/share/a%20b.sqlite?mode=ro"},
		{"unix", "/var/db/a b#c.sqlite", "linux", "file:///var/db/a%20b%23c.sqlite?mode=ro"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := sqliteReadOnlyURIForOS(test.path, test.goos)
			if err != nil || got != test.want {
				t.Fatalf("got=%q want=%q err=%v", got, test.want, err)
			}
			parsed, err := url.Parse(got)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parsed.Query(), url.Values{"mode": []string{"ro"}}) {
				t.Fatalf("query=%v", parsed.Query())
			}
		})
	}
	for _, path := range []string{`C:relative.sqlite`, `C:\db\..\evil.sqlite`, `\\server`, `relative.sqlite`} {
		t.Run("reject "+path, func(t *testing.T) {
			if _, err := sqliteReadOnlyURIForOS(path, "windows"); err == nil {
				t.Fatalf("accepted %q", path)
			}
		})
	}
}
