package hermes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
	_ "modernc.org/sqlite"
)

const maxStateFileBytes int64 = 64 << 20

func readOnlyDB(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("hermes-agent: unsafe state database")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	return db, nil
}
func stableRead(root, path string) ([]byte, error) {
	f, err := safeopen.Open(root, path, maxStateFileBytes)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	first, err := io.ReadAll(io.LimitReader(f, maxStateFileBytes+1))
	if err != nil || int64(len(first)) > maxStateFileBytes {
		return nil, errors.New("hermes-agent: state snapshot exceeds limit")
	}
	if _, err = f.Seek(0, 0); err != nil {
		return nil, err
	}
	second, err := io.ReadAll(io.LimitReader(f, maxStateFileBytes+1))
	if err != nil || !bytes.Equal(first, second) {
		return nil, errors.New("hermes-agent: state changed during snapshot")
	}
	return first, nil
}
func snapshotDB(path string) (*sql.DB, func(), error) {
	root := filepath.Dir(path)
	var main, wal []byte
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		main, err = stableRead(root, path)
		if err != nil {
			continue
		}
		wal, err = stableRead(root, path+"-wal")
		if os.IsNotExist(err) {
			wal = nil
			err = nil
		}
		if err != nil {
			continue
		}
		again, e := stableRead(root, path)
		if e == nil && bytes.Equal(main, again) {
			err = nil
			break
		}
		err = errors.New("hermes-agent: inconsistent state snapshot")
	}
	if err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "kuai-hermes-state-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	copyPath := filepath.Join(dir, "state.db")
	if err = os.WriteFile(copyPath, main, 0o600); err != nil {
		cleanup()
		return nil, nil, err
	}
	if wal != nil {
		if err = os.WriteFile(copyPath+"-wal", wal, 0o600); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	db, err := readOnlyDB(copyPath)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return db, func() { _ = db.Close(); cleanup() }, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (a *Adapter) discoverState(ctx context.Context) ([]source.Session, map[string]authorization, map[string][]byte, error) {
	dbPath := filepath.Join(filepath.Dir(a.root), "state.db")
	if filepath.Base(a.root) != "sessions" {
		dbPath = filepath.Join(a.root, "state.db")
	}
	if _, err := os.Lstat(dbPath); os.IsNotExist(err) {
		return nil, nil, nil, nil
	}
	db, cleanup, err := snapshotDB(dbPath)
	if err != nil {
		return nil, nil, nil, err
	}
	defer cleanup()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(parent_session_id,''),started_at,COALESCE(ended_at,0),COALESCE(message_count,0),COALESCE(input_tokens,0),COALESCE(output_tokens,0),COALESCE(cache_read_tokens,0),COALESCE(cache_write_tokens,0),COALESCE(reasoning_tokens,0) FROM sessions ORDER BY started_at,id`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	type stateRow struct {
		id, parent                         string
		started, ended                     float64
		count                              int
		in, outTok, cacheR, cacheW, reason int64
	}
	var stateRows []stateRow
	for rows.Next() {
		var r stateRow
		if err := rows.Scan(&r.id, &r.parent, &r.started, &r.ended, &r.count, &r.in, &r.outTok, &r.cacheR, &r.cacheW, &r.reason); err != nil {
			return nil, nil, nil, err
		}
		if r.id != "" {
			stateRows = append(stateRows, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, nil, err
	}
	var out []source.Session
	auths := map[string]authorization{}
	outputs := map[string][]byte{}
	for _, r := range stateRows {
		events, bad, err := stateEvents(ctx, tx, r.id)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(events) == 0 {
			continue
		}
		var output bytes.Buffer
		enc := json.NewEncoder(&output)
		for _, e := range events {
			_ = enc.Encode(e)
		}
		if output.Len() > maxSessionBytes {
			continue
		}
		ref := "state:" + r.id
		s := source.Session{ID: "hermes-agent:" + r.id, Product: "hermes-agent", FormatVersion: "state-db-v1", AdapterVersion: "1", Capabilities: a.Capabilities(), Scope: stateScope(a.root), StartedAt: unixFloat(r.started), EndedAt: unixFloat(r.ended), MessageCount: r.count, MalformedCount: bad, ParentID: r.parent, Usage: map[string]int64{"input_tokens": r.in, "output_tokens": r.outTok, "cache_read_tokens": r.cacheR, "cache_write_tokens": r.cacheW, "reasoning_tokens": r.reason}, OpaqueRef: ref}
		out = append(out, s)
		fingerprint, _ := json.Marshal(struct {
			Session source.Session
			Output  []byte
		}{s, output.Bytes()})
		digest := sha256.Sum256(fingerprint)
		s.SnapshotID = fmt.Sprintf("%x", digest[:])
		out[len(out)-1] = s
		auths[ref] = authorization{id: s.ID, digest: s.SnapshotID, sourcePath: dbPath}
		outputs[ref] = append([]byte(nil), output.Bytes()...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, auths, outputs, nil
}
func stateScope(root string) source.ScopeRef {
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return source.ScopeRef{Type: source.ScopeConversationGroup, Root: fmt.Sprintf("hermes-agent:%x", sum[:12]), Label: "Hermes Agent conversations"}
}
func unixFloat(v float64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	sec := int64(v)
	return time.Unix(sec, int64((v-float64(sec))*1e9)).UTC()
}
func stateEvents(ctx context.Context, db queryer, id string) ([]event, int, error) {
	rows, err := db.QueryContext(ctx, `SELECT role,COALESCE(content,''),COALESCE(tool_call_id,''),COALESCE(tool_calls,''),timestamp FROM messages WHERE session_id=? ORDER BY timestamp,id`, id)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []event
	bad := 0
	for rows.Next() {
		var role, content, callID, calls string
		var ts float64
		if rows.Scan(&role, &content, &callID, &calls, &ts) != nil {
			bad++
			continue
		}
		if len(content) > maxLineBytes || len(calls) > maxLineBytes {
			bad++
			continue
		}
		stamp := unixFloat(ts).Format(time.RFC3339Nano)
		switch role {
		case "user":
			if content == "" {
				bad++
				continue
			}
			out = append(out, event{Type: "message", Role: "user", Content: content, Timestamp: stamp})
		case "assistant":
			valid := false
			if content != "" {
				out = append(out, event{Type: "message", Role: "assistant", Content: content, Timestamp: stamp})
				valid = true
			}
			if calls != "" {
				var list []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				}
				if json.Unmarshal([]byte(calls), &list) != nil {
					bad++
				} else {
					for _, c := range list {
						if c.ID == "" || c.Function.Name == "" {
							bad++
							continue
						}
						var input any = map[string]any{}
						_ = json.Unmarshal([]byte(c.Function.Arguments), &input)
						out = append(out, event{Type: "tool_use", Timestamp: stamp, CallID: c.ID, Name: c.Function.Name, Input: input})
						valid = true
					}
				}
			}
			if !valid {
				bad++
			}
		case "tool":
			if callID == "" {
				bad++
				continue
			}
			out = append(out, event{Type: "tool_result", Timestamp: stamp, CallID: callID, Result: content})
		default:
			bad++
		}
	}
	return out, bad, rows.Err()
}
