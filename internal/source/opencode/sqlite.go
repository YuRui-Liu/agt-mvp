package opencode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sqliteread"
)

const (
	maxDatabaseBytes    int64 = 256 << 20
	maxDatabaseSessions       = 10_000
	maxDatabaseMessages       = 10_000
	maxDatabaseParts          = 50_000
	databaseFormat            = "db-v2"
	databaseRefPrefix         = "db:"
)

type sqliteQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) (*sql.Row, error)
}

type sqliteReadFunc func(context.Context, string, string, int64, func(sqliteQueryer) error) error

func defaultSQLiteRead(ctx context.Context, root, path string, maxBytes int64, fn func(sqliteQueryer) error) error {
	return sqliteread.WithReadOnlyTx(ctx, root, path, maxBytes, func(tx *sqliteread.ReadTx) error { return fn(tx) })
}

var requiredSQLiteColumns = map[string][]string{
	"project": {"id", "worktree"},
	"session": {"id", "project_id", "parent_id", "directory", "time_created", "time_updated", "tokens_input", "tokens_output", "tokens_reasoning", "tokens_cache_read", "tokens_cache_write"},
	"message": {"id", "session_id", "time_created", "data"},
	"part":    {"id", "message_id", "session_id", "time_created", "data"},
}

type sqliteMeta struct {
	id, projectID, parentID, directory, worktree    string
	created, updated                                int64
	input, output, reasoning, cacheRead, cacheWrite int64
}

func validateSQLiteSchema(ctx context.Context, tx sqliteQueryer) error {
	for _, table := range []string{"project", "session", "message", "part"} {
		rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return errors.New("opencode: unsupported database schema")
		}
		columns := map[string]bool{}
		for rows.Next() {
			var cid, notnull, primaryKey int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return errors.New("opencode: unsupported database schema")
			}
			columns[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return errors.New("opencode: unsupported database schema")
		}
		for _, name := range requiredSQLiteColumns[table] {
			if !columns[name] {
				return errors.New("opencode: unsupported database schema")
			}
		}
	}
	return nil
}

func scanSQLiteMeta(scanner interface{ Scan(...any) error }) (sqliteMeta, error) {
	var m sqliteMeta
	err := scanner.Scan(&m.id, &m.projectID, &m.parentID, &m.directory, &m.worktree, &m.created, &m.updated,
		&m.input, &m.output, &m.reasoning, &m.cacheRead, &m.cacheWrite)
	return m, err
}

const sqliteMetaColumns = `s.id,s.project_id,COALESCE(s.parent_id,''),s.directory,p.worktree,s.time_created,s.time_updated,s.tokens_input,s.tokens_output,s.tokens_reasoning,s.tokens_cache_read,s.tokens_cache_write`

func listSQLiteMeta(ctx context.Context, tx sqliteQueryer) ([]sqliteMeta, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+sqliteMetaColumns+` FROM session AS s JOIN project AS p ON p.id=s.project_id ORDER BY s.time_created,s.id LIMIT ?`, maxDatabaseSessions+1)
	if err != nil {
		return nil, errors.New("opencode: unsupported database schema")
	}
	defer rows.Close()
	var out []sqliteMeta
	for rows.Next() {
		m, err := scanSQLiteMeta(rows)
		if err != nil {
			return nil, errors.New("opencode: unsupported database schema")
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil || len(out) > maxDatabaseSessions {
		return nil, errors.New("opencode: unsupported database schema")
	}
	return out, nil
}

func getSQLiteMeta(ctx context.Context, tx sqliteQueryer, id string) (sqliteMeta, error) {
	row, err := tx.QueryRowContext(ctx, `SELECT `+sqliteMetaColumns+` FROM session AS s JOIN project AS p ON p.id=s.project_id WHERE s.id=?`, id)
	if err != nil {
		return sqliteMeta{}, err
	}
	return scanSQLiteMeta(row)
}

func (a *Adapter) discoverSQLite(ctx context.Context) ([]source.Session, map[string]authorization, error) {
	path := filepath.Join(a.root, "opencode.db")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, map[string]authorization{}, nil
	} else if err != nil {
		return nil, nil, err
	}
	var sessions []source.Session
	auths := map[string]authorization{}
	err := a.sqliteRead(ctx, a.root, path, maxDatabaseBytes, func(tx sqliteQueryer) error {
		if err := validateSQLiteSchema(ctx, tx); err != nil {
			return err
		}
		metas, err := listSQLiteMeta(ctx, tx)
		if err != nil {
			return err
		}
		for _, meta := range metas {
			if err := ctx.Err(); err != nil {
				return err
			}
			session, _, digest, err := loadSQLiteSession(ctx, tx, meta)
			if err != nil {
				continue
			}
			sessions = append(sessions, session)
			auths[session.OpaqueRef] = authorization{id: session.ID, digest: digest, format: databaseFormat, ref: meta.id}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, nil, err
		}
		return nil, nil, source.NewDiscoveryError(source.SourceFormatUnsupported, errors.New("opencode: unsupported database schema"))
	}
	return sessions, auths, nil
}

func loadSQLiteSession(ctx context.Context, tx sqliteQueryer, meta sqliteMeta) (source.Session, []byte, string, error) {
	if meta.id == "" || strings.ContainsAny(meta.id, `/\\#`) {
		return source.Session{}, nil, "", errors.New("opencode: invalid database session")
	}
	sf := sessionFile{ID: meta.id, Directory: meta.directory, ParentID: meta.parentID}
	sf.Time.Created, sf.Time.Updated = meta.created, meta.updated
	sessionJSON, _ := json.Marshal(sf)
	snap := snapshot{session: sessionJSON, files: map[string][]byte{}}
	bad := 0
	messageRows, err := tx.QueryContext(ctx, `SELECT id,session_id,time_created,data FROM message WHERE session_id=? ORDER BY time_created,id LIMIT ?`, meta.id, maxDatabaseMessages+1)
	if err != nil {
		return source.Session{}, nil, "", err
	}
	messageCount := 0
	for messageRows.Next() {
		var id, sessionID, data string
		var created int64
		if err := messageRows.Scan(&id, &sessionID, &created, &data); err != nil {
			messageRows.Close()
			return source.Session{}, nil, "", err
		}
		messageCount++
		if len(data) > int(maxFileBytes) {
			bad++
			continue
		}
		var envelope messageFile
		if json.Unmarshal([]byte(data), &envelope) != nil || envelope.ID != id || envelope.SessionID != sessionID || sessionID != meta.id {
			bad++
			continue
		}
		snap.files[filepath.Join("db", "message", meta.id, id+".json")] = []byte(data)
	}
	err = messageRows.Err()
	messageRows.Close()
	if err != nil || messageCount > maxDatabaseMessages {
		return source.Session{}, nil, "", errors.New("opencode: database session exceeds limit")
	}
	partRows, err := tx.QueryContext(ctx, `SELECT id,message_id,session_id,time_created,data FROM part WHERE session_id=? ORDER BY time_created,id LIMIT ?`, meta.id, maxDatabaseParts+1)
	if err != nil {
		return source.Session{}, nil, "", err
	}
	partCount := 0
	for partRows.Next() {
		var id, messageID, sessionID, data string
		var created int64
		if err := partRows.Scan(&id, &messageID, &sessionID, &created, &data); err != nil {
			partRows.Close()
			return source.Session{}, nil, "", err
		}
		partCount++
		if len(data) > int(maxFileBytes) {
			bad++
			continue
		}
		var envelope partFile
		if json.Unmarshal([]byte(data), &envelope) != nil || envelope.ID != id || envelope.MessageID != messageID || envelope.SessionID != sessionID || sessionID != meta.id {
			bad++
			continue
		}
		snap.files[filepath.Join("db", "part", messageID, id+".json")] = []byte(data)
	}
	err = partRows.Err()
	partRows.Close()
	if err != nil || partCount > maxDatabaseParts {
		return source.Session{}, nil, "", errors.New("opencode: database session exceeds limit")
	}
	_, events, messages, malformed, err := parseSnapshot(snap)
	if err != nil || messages == 0 || len(events) == 0 {
		return source.Session{}, nil, "", errors.New("opencode: no valid database messages")
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil || output.Len() > int(maxSessionBytes) {
			return source.Session{}, nil, "", errors.New("opencode: database session exceeds limit")
		}
	}
	scopeRoot := meta.directory
	if !filepath.IsAbs(scopeRoot) || filepath.Clean(scopeRoot) != scopeRoot {
		scopeRoot = meta.worktree
	}
	scope := source.ScopeRef{Type: source.ScopeProject, Root: scopeRoot, Label: filepath.Base(scopeRoot)}
	if !filepath.IsAbs(scopeRoot) || filepath.Clean(scopeRoot) != scopeRoot {
		sum := sha256.Sum256([]byte("opencode\x00" + meta.projectID))
		scope = source.ScopeRef{Type: source.ScopeSessionCollection, Root: fmt.Sprintf("%x", sum[:12]), Label: "OpenCode sessions"}
	}
	parent := ""
	if meta.parentID != "" {
		parent = "opencode:" + meta.parentID
	}
	session := source.Session{
		ID: "opencode:" + meta.id, Product: "opencode", FormatVersion: databaseFormat, AdapterVersion: "1",
		Capabilities: []source.Capability{"messages", "tools"}, Scope: scope,
		StartedAt: time.UnixMilli(meta.created), EndedAt: time.UnixMilli(meta.updated), MessageCount: messages,
		MalformedCount: malformed + bad, ParentID: parent,
		Usage:     map[string]int64{"input_tokens": meta.input, "output_tokens": meta.output, "reasoning_tokens": meta.reasoning, "cache_read_tokens": meta.cacheRead, "cache_write_tokens": meta.cacheWrite},
		OpaqueRef: databaseRefPrefix + meta.id,
	}
	metadata, err := json.Marshal(struct {
		ID, Parent string
		Scope      source.ScopeRef
		Started    int64
		Ended      int64
		Messages   int
		Malformed  int
		Usage      map[string]int64
	}{session.ID, session.ParentID, session.Scope, meta.created, meta.updated, session.MessageCount, session.MalformedCount, session.Usage})
	if err != nil {
		return source.Session{}, nil, "", errors.New("opencode: database metadata encoding failed")
	}
	hash := sha256.New()
	hash.Write(metadata)
	hash.Write([]byte{0})
	hash.Write(output.Bytes())
	digest := hash.Sum(nil)
	session.SnapshotID = fmt.Sprintf("%x", digest[:])
	return session, output.Bytes(), session.SnapshotID, nil
}

func (a *Adapter) openSQLite(ctx context.Context, auth authorization, expected source.Session) (io.ReadCloser, error) {
	path := filepath.Join(a.root, "opencode.db")
	var output []byte
	err := a.sqliteRead(ctx, a.root, path, maxDatabaseBytes, func(tx sqliteQueryer) error {
		if err := validateSQLiteSchema(ctx, tx); err != nil {
			return err
		}
		meta, err := getSQLiteMeta(ctx, tx, auth.ref)
		if err != nil {
			return err
		}
		session, current, digest, err := loadSQLiteSession(ctx, tx, meta)
		if err != nil || session.ID != expected.ID || digest != auth.digest || digest != expected.SnapshotID {
			return errors.New("opencode: source changed since discovery")
		}
		output = append([]byte(nil), current...)
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, errors.New("opencode: source changed since discovery")
	}
	return io.NopCloser(bytes.NewReader(output)), nil
}
