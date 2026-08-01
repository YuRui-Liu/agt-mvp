package sqliteread

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	_ "modernc.org/sqlite"
)

func TestWithReadOnlyTxReadsCommittedWALAndRejectsWrites(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state #?%.sqlite")
	writer := openWALDatabase(t, path)
	defer writer.Close()

	err := WithReadOnlyTx(context.Background(), root, path, 1<<20, func(tx *ReadTx) error {
		var value string
		row, err := tx.QueryRowContext(context.Background(), `SELECT value FROM items WHERE id = 1`)
		if err != nil {
			return err
		}
		if err := row.Scan(&value); err != nil {
			return err
		}
		if value != "visible-from-wal" {
			t.Fatalf("value=%q", value)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWithReadOnlyTxRejectsUnsafeInputs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.sqlite")
	writer := openWALDatabase(t, path)
	defer writer.Close()

	t.Run("outside root", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.sqlite")
		other := openWALDatabase(t, outside)
		defer other.Close()
		if err := WithReadOnlyTx(context.Background(), root, outside, 1<<20, noOpTx); err == nil {
			t.Fatal("outside path accepted")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("creating symlinks requires privileges on Windows")
		}
		link := filepath.Join(root, "linked.sqlite")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if err := WithReadOnlyTx(context.Background(), root, link, 1<<20, noOpTx); err == nil {
			t.Fatal("symlink accepted")
		}
	})

	t.Run("size limit", func(t *testing.T) {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := WithReadOnlyTx(context.Background(), root, path, info.Size()-1, noOpTx); err == nil {
			t.Fatal("oversized database accepted")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := WithReadOnlyTx(ctx, root, path, 1<<20, noOpTx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestWithReadOnlyTxReturnsCallbackError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.sqlite")
	writer := openWALDatabase(t, path)
	defer writer.Close()
	want := errors.New("synthetic callback failure")
	err := WithReadOnlyTx(context.Background(), root, path, 1<<20, func(*ReadTx) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}

func TestValidateReadQueryRejectsWriteAndMultiStatementBypasses(t *testing.T) {
	for _, query := range []string{
		`VACUUM INTO '/tmp/synthetic.sqlite'`,
		`ATTACH DATABASE ':memory:' AS extra`,
		`PRAGMA writable_schema=ON`,
		`WITH picked AS (SELECT 1) DELETE FROM items`,
		`SELECT 1; VACUUM`,
		`SELECT LOAD_EXTENSION('synthetic')`,
		`VALUES (LOAD_EXTENSION('synthetic'))`,
		`WITH picked AS (SELECT 1) SELECT LOAD_EXTENSION('synthetic') FROM picked`,
		`SELECT "load_extension"('synthetic')`,
		"VALUES (`load_extension`('synthetic'))",
		`WITH picked AS (SELECT 1) SELECT [load_extension]('synthetic') FROM picked`,
	} {
		if err := validateReadQuery(query); err == nil {
			t.Fatal("unsafe SQL accepted")
		}
	}
	for _, query := range []string{
		`SELECT value FROM items`,
		`/* synthetic */ WITH picked AS (SELECT 1) SELECT * FROM picked`,
		`EXPLAIN QUERY PLAN SELECT value FROM items`,
		`PRAGMA table_info(items)`,
		`SELECT "LOAD_EXTENSION" FROM items`,
		`EXPLAIN SELECT "LOAD_EXTENSION" FROM items`,
		`VALUES ('LOAD_EXTENSION')`,
		`WITH picked AS (SELECT 'LOAD_EXTENSION') SELECT * FROM picked`,
		`SELECT 'load_extension'`,
		"WITH picked AS (SELECT 1 AS \"DELETE\", 2 AS `PRAGMA`) SELECT \"DELETE\", `PRAGMA` FROM picked",
		`EXPLAIN QUERY PLAN WITH picked AS (SELECT 1 AS [UPDATE]) SELECT [UPDATE] FROM picked`,
	} {
		if err := validateReadQuery(query); err != nil {
			t.Fatalf("read query rejected: %v", err)
		}
	}
}

func TestWithReadOnlyTxPreservesUnixBackslashesInDatabaseName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix filename semantics")
	}
	base := t.TempDir()
	root := filepath.Join(base, "root", "nested")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.sqlite")
	outsideDB := openWALDatabase(t, outside)
	if _, err := outsideDB.Exec(`UPDATE items SET value = 'outside' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	outsideDB.Close()

	decoy := filepath.Join(root, `\..\..\outside.sqlite`)
	decoyDB := openWALDatabase(t, decoy)
	if _, err := decoyDB.Exec(`UPDATE items SET value = 'decoy' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	decoyDB.Close()

	var got string
	err := WithReadOnlyTx(context.Background(), root, decoy, 1<<20, func(tx *ReadTx) error {
		row, err := tx.QueryRowContext(context.Background(), `SELECT value FROM items WHERE id = 1`)
		if err != nil {
			return err
		}
		return row.Scan(&got)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "decoy" {
		t.Fatal("database path escaped root")
	}
}

func TestReadTxRejectsWriteBypassesBeforeSQLiteExecution(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.sqlite")
	writer := openWALDatabase(t, path)
	defer writer.Close()
	output := filepath.Join(t.TempDir(), "vacuum-output.sqlite")
	queries := []string{
		fmt.Sprintf(`VACUUM INTO %q`, output),
		`ATTACH DATABASE ':memory:' AS extra`,
		`PRAGMA writable_schema=ON`,
		`WITH picked AS (SELECT 1) DELETE FROM items`,
	}
	err := WithReadOnlyTx(context.Background(), root, path, 1<<20, func(tx *ReadTx) error {
		for _, query := range queries {
			if rows, err := tx.QueryContext(context.Background(), query); err == nil {
				rows.Close()
				t.Fatal("unsafe SQL reached SQLite")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("VACUUM output was created")
	}
}

func TestWithReadOnlyTxRejectsPathReplacementAtBothStages(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("deterministic rename replacement is Unix-only")
	}
	for _, stage := range []string{"after-validation", "after-open"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "state.sqlite")
			writer := openWALDatabase(t, path)
			writer.Close()
			replacement := filepath.Join(root, "replacement.sqlite")
			other := openWALDatabase(t, replacement)
			other.Close()
			replace := func() {
				if err := os.Rename(path, filepath.Join(root, "original.sqlite")); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "after-validation" {
				afterInitialValidation = replace
			} else {
				afterSQLiteOpen = replace
			}
			defer func() {
				afterInitialValidation = nil
				afterSQLiteOpen = nil
			}()
			if err := WithReadOnlyTx(context.Background(), root, path, 1<<20, noOpTx); err == nil {
				t.Fatal("replaced database accepted")
			}
		})
	}
}

func TestWithReadOnlyTxReturnsCancellationDuringCallback(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.sqlite")
	writer := openWALDatabase(t, path)
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	err := WithReadOnlyTx(ctx, root, path, 1<<20, func(*ReadTx) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
}

func TestReadOnlyURIForOS(t *testing.T) {
	tests := []struct {
		name string
		path string
		goos string
		want string
	}{
		{name: "Unix escaping", path: "/tmp/state #?%.sqlite", goos: "darwin", want: "file:///tmp/state%20%23%3F%25.sqlite?mode=ro&immutable=0"},
		{name: "Unix backslash", path: `/tmp/state\..\secret.sqlite`, goos: "linux", want: "file:///tmp/state%5C..%5Csecret.sqlite?mode=ro&immutable=0"},
		{name: "Windows drive", path: `C:\Users\A B\state#?.sqlite`, goos: "windows", want: "file:///C:/Users/A%20B/state%23%3F.sqlite?mode=ro&immutable=0"},
		{name: "Windows UNC", path: `\\server\share\state %.sqlite`, goos: "windows", want: "file://server/share/state%20%25.sqlite?mode=ro&immutable=0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := readOnlyURIForOS(test.path, test.goos)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("URI=%q want %q", got, test.want)
			}
		})
	}
}

func openWALDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA wal_autocheckpoint=0`,
		`CREATE TABLE items (id INTEGER PRIMARY KEY, value TEXT)`,
		`INSERT INTO items(id, value) VALUES (1, 'visible-from-wal')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func noOpTx(*ReadTx) error { return nil }
