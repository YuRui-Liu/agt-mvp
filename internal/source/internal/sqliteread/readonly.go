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

// ErrBudgetExceeded reports that the validated main database, WAL, or SHM
// file exceeds its byte limit, individually or in aggregate.
var ErrBudgetExceeded = errors.New("sqliteread: database snapshot exceeds limit")

var afterInitialValidation func()
var afterSQLiteOpen func()

type validatedFile struct {
	file *os.File
	info os.FileInfo
}

type validatedFileSet map[string]validatedFile

func openValidatedFileSet(root, path string, maxBytes int64) (validatedFileSet, error) {
	set := validatedFileSet{}
	closeSet := func() {
		for _, item := range set {
			item.file.Close()
		}
	}
	var total int64
	for index, suffix := range []string{"", "-wal", "-shm"} {
		file, err := safeopen.Open(root, path+suffix, maxBytes)
		if err != nil {
			if index != 0 && os.IsNotExist(err) {
				continue
			}
			closeSet()
			if errors.Is(err, safeopen.ErrFileSizeLimit) {
				return nil, ErrBudgetExceeded
			}
			return nil, err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			closeSet()
			return nil, err
		}
		if info.Size() > maxBytes-total {
			file.Close()
			closeSet()
			return nil, ErrBudgetExceeded
		}
		total += info.Size()
		set[suffix] = validatedFile{file: file, info: info}
	}
	return set, nil
}

func (set validatedFileSet) close() {
	for _, item := range set {
		item.file.Close()
	}
}

func sameValidatedFileSet(first, second validatedFileSet) bool {
	if len(first) != len(second) {
		return false
	}
	for suffix, original := range first {
		current, ok := second[suffix]
		if !ok || !os.SameFile(original.info, current.info) {
			return false
		}
	}
	return true
}

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
	validated, err := openValidatedFileSet(root, path, maxBytes)
	if err != nil {
		return err
	}
	defer validated.close()
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
	current, err := openValidatedFileSet(root, path, maxBytes)
	if err != nil {
		return err
	}
	defer current.close()
	if !sameValidatedFileSet(validated, current) {
		return errors.New("sqliteread: database or sidecar changed during open")
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
	for _, token := range tokens {
		if token.value == "LOAD_EXTENSION" && (!token.quotedIdentifier || token.call) {
			return errors.New("sqliteread: prohibited function")
		}
	}
	switch tokens[0].value {
	case "SELECT", "VALUES":
		return nil
	case "WITH":
		if containsWriteToken(tokens[1:]) {
			return errors.New("sqliteread: write query rejected")
		}
		return nil
	case "EXPLAIN":
		for index, token := range tokens[1:] {
			if token.quotedIdentifier {
				continue
			}
			if token.value == "SELECT" {
				return nil
			}
			if token.value == "WITH" {
				if containsWriteToken(tokens[index+2:]) {
					return errors.New("sqliteread: write query rejected")
				}
				return nil
			}
		}
	case "PRAGMA":
		if !hasAssignment && len(tokens) >= 2 {
			name := tokens[1].value
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

func containsWriteToken(tokens []queryToken) bool {
	for _, token := range tokens {
		if token.quotedIdentifier {
			continue
		}
		switch token.value {
		case "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP", "ALTER", "VACUUM", "ATTACH", "DETACH", "PRAGMA":
			return true
		}
	}
	return false
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

type queryToken struct {
	value            string
	quotedIdentifier bool
	call             bool
}

func readQueryTokens(query string) ([]queryToken, bool, error) {
	var tokens []queryToken
	var token strings.Builder
	assignment := false
	ended := false
	flush := func() {
		if token.Len() != 0 {
			tokens = append(tokens, queryToken{value: strings.ToUpper(token.String())})
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
			quotedIdentifier := character != '\''
			var quoted strings.Builder
			index++
			for {
				if index >= len(query) {
					return nil, false, errors.New("sqliteread: unterminated quote")
				}
				if query[index] == closing {
					if closing != ']' && index+1 < len(query) && query[index+1] == closing {
						quoted.WriteByte(closing)
						index += 2
						continue
					}
					index++
					break
				}
				quoted.WriteByte(query[index])
				index++
			}
			if quotedIdentifier {
				tokens = append(tokens, queryToken{value: strings.ToUpper(quoted.String()), quotedIdentifier: true})
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
		if character == '(' {
			flush()
			if len(tokens) != 0 {
				tokens[len(tokens)-1].call = true
			}
			index++
			continue
		}
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' || strings.ContainsRune("),=", rune(character)) {
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
	if goos != "windows" {
		if !pathpkg.IsAbs(databasePath) || pathpkg.Clean(databasePath) != databasePath {
			return "", errors.New("sqliteread: invalid Unix database path")
		}
		uri.Path = databasePath
		uri.RawQuery = "mode=ro&immutable=0"
		return uri.String(), nil
	}

	slashed := strings.ReplaceAll(databasePath, `\`, "/")
	if strings.HasPrefix(slashed, "//") {
		parts := strings.SplitN(strings.TrimPrefix(slashed, "//"), "/", 2)
		if len(parts) != 2 || !validFileHost(parts[0]) || parts[1] == "" || pathpkg.Clean("/"+parts[1]) != "/"+parts[1] {
			return "", errors.New("sqliteread: invalid UNC database path")
		}
		uri.Host = parts[0]
		uri.Path = "/" + parts[1]
	} else {
		if len(slashed) < 4 || !isASCIIAlpha(slashed[0]) || slashed[1] != ':' || slashed[2] != '/' || pathpkg.Clean(slashed) != slashed {
			return "", errors.New("sqliteread: invalid Windows database path")
		}
		uri.Path = "/" + slashed
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
