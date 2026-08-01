package sqliteread

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
	_ "modernc.org/sqlite"
)

var afterInitialValidation func()
var afterSQLiteOpen func()

// ReadTx exposes only guarded query operations from the underlying SQLite
// transaction. It deliberately provides no Exec, Commit, connection, or raw
// transaction access.
type ReadTx struct{ tx *sql.Tx }

func (tx *ReadTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if err := validateReadQuery(query); err != nil {
		return nil, err
	}
	return tx.tx.QueryContext(ctx, query, args...)
}

func (tx *ReadTx) QueryRowContext(ctx context.Context, query string, args ...any) (*sql.Row, error) {
	if err := validateReadQuery(query); err != nil {
		return nil, err
	}
	return tx.tx.QueryRowContext(ctx, query, args...), nil
}

// WithReadOnlyTx validates a database below root and invokes fn with a
// query-only transaction. The database remains non-immutable so committed WAL
// records are visible to the transaction.
func WithReadOnlyTx(ctx context.Context, root, path string, maxBytes int64, fn func(*ReadTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("sqliteread: nil callback")
	}
	validated, err := safeopen.Open(root, path, maxBytes)
	if err != nil {
		return err
	}
	defer validated.Close()
	validatedInfo, err := validated.Stat()
	if err != nil {
		return err
	}
	if afterInitialValidation != nil {
		afterInitialValidation()
	}

	dsn, err := readOnlyURI(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()

	if _, err := db.ExecContext(ctx, `PRAGMA query_only=ON`); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if afterSQLiteOpen != nil {
		afterSQLiteOpen()
	}
	current, err := safeopen.Open(root, path, maxBytes)
	if err != nil {
		return err
	}
	currentInfo, err := current.Stat()
	current.Close()
	if err != nil {
		return err
	}
	if !os.SameFile(validatedInfo, currentInfo) {
		return errors.New("sqliteread: database changed during open")
	}

	if err := fn(&ReadTx{tx: tx}); err != nil {
		return err
	}
	return ctx.Err()
}

func validateReadQuery(query string) error {
	tokens, hasAssignment, err := readQueryTokens(query)
	if err != nil || len(tokens) == 0 {
		return errors.New("sqliteread: invalid read query")
	}
	switch tokens[0] {
	case "SELECT", "VALUES":
		return nil
	case "WITH":
		for _, token := range tokens[1:] {
			switch token {
			case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP", "ALTER", "VACUUM", "ATTACH", "DETACH", "PRAGMA":
				return errors.New("sqliteread: write query rejected")
			}
		}
		return nil
	case "EXPLAIN":
		for index, token := range tokens[1:] {
			if token == "SELECT" || token == "WITH" {
				return validateReadQuery(strings.Join(tokens[index+1:], " "))
			}
		}
	case "PRAGMA":
		if !hasAssignment && len(tokens) >= 2 {
			name := tokens[1]
			if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
				name = name[dot+1:]
			}
			if readOnlyPragma[name] {
				return nil
			}
		}
	}
	return errors.New("sqliteread: non-read query rejected")
}

var readOnlyPragma = map[string]bool{
	"DATABASE_LIST":    true,
	"FOREIGN_KEY_LIST": true,
	"INDEX_INFO":       true,
	"INDEX_LIST":       true,
	"INDEX_XINFO":      true,
	"TABLE_INFO":       true,
	"TABLE_LIST":       true,
	"TABLE_XINFO":      true,
}

func readQueryTokens(query string) ([]string, bool, error) {
	var tokens []string
	var token strings.Builder
	assignment := false
	ended := false
	flush := func() {
		if token.Len() != 0 {
			tokens = append(tokens, strings.ToUpper(token.String()))
			token.Reset()
		}
	}
	for index := 0; index < len(query); {
		character := query[index]
		if character == '-' && index+1 < len(query) && query[index+1] == '-' {
			flush()
			index += 2
			for index < len(query) && query[index] != '\n' {
				index++
			}
			continue
		}
		if character == '/' && index+1 < len(query) && query[index+1] == '*' {
			flush()
			end := strings.Index(query[index+2:], "*/")
			if end < 0 {
				return nil, false, errors.New("sqliteread: unterminated comment")
			}
			index += end + 4
			continue
		}
		if character == '\'' || character == '"' || character == '`' || character == '[' {
			if ended {
				return nil, false, errors.New("sqliteread: multiple statements")
			}
			flush()
			closing := character
			if character == '[' {
				closing = ']'
			}
			index++
			for {
				if index >= len(query) {
					return nil, false, errors.New("sqliteread: unterminated quote")
				}
				if query[index] == closing {
					if closing != ']' && index+1 < len(query) && query[index+1] == closing {
						index += 2
						continue
					}
					index++
					break
				}
				index++
			}
			continue
		}
		if character == ';' {
			flush()
			if ended {
				return nil, false, errors.New("sqliteread: multiple statements")
			}
			ended = true
			index++
			continue
		}
		if character == '=' {
			assignment = true
		}
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' || strings.ContainsRune("(),=", rune(character)) {
			flush()
			index++
			continue
		}
		if ended {
			return nil, false, errors.New("sqliteread: multiple statements")
		}
		token.WriteByte(character)
		index++
	}
	flush()
	return tokens, assignment, nil
}

func readOnlyURI(databasePath string) (string, error) {
	if !filepath.IsAbs(databasePath) || filepath.Clean(databasePath) != databasePath || strings.IndexByte(databasePath, 0) >= 0 {
		return "", errors.New("sqliteread: invalid database path")
	}
	return readOnlyURIForOS(databasePath, runtime.GOOS)
}

func readOnlyURIForOS(databasePath, goos string) (string, error) {
	if strings.IndexByte(databasePath, 0) >= 0 {
		return "", errors.New("sqliteread: invalid database path")
	}
	uri := url.URL{Scheme: "file"}
	slashed := strings.ReplaceAll(databasePath, `\`, "/")
	if goos == "windows" && strings.HasPrefix(slashed, "//") {
		parts := strings.SplitN(strings.TrimPrefix(slashed, "//"), "/", 2)
		if len(parts) != 2 || !validFileHost(parts[0]) || parts[1] == "" || pathpkg.Clean("/"+parts[1]) != "/"+parts[1] {
			return "", errors.New("sqliteread: invalid UNC database path")
		}
		uri.Host = parts[0]
		uri.Path = "/" + parts[1]
	} else if goos == "windows" {
		if len(slashed) < 4 || !isASCIIAlpha(slashed[0]) || slashed[1] != ':' || slashed[2] != '/' || pathpkg.Clean(slashed) != slashed {
			return "", errors.New("sqliteread: invalid Windows database path")
		}
		uri.Path = "/" + slashed
	} else {
		if !pathpkg.IsAbs(databasePath) || pathpkg.Clean(databasePath) != databasePath {
			return "", errors.New("sqliteread: invalid Unix database path")
		}
		uri.Path = slashed
	}
	uri.RawQuery = "mode=ro&immutable=0"
	return uri.String(), nil
}

func validFileHost(host string) bool {
	if host == "" || host == "." || host == ".." {
		return false
	}
	for _, character := range host {
		if character <= ' ' || character == '/' || character == '\\' || character == '?' || character == '#' || character == '@' {
			return false
		}
	}
	return true
}

func isASCIIAlpha(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}
