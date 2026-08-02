package sharedclient

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/sqliteread"
	_ "modernc.org/sqlite"
)

func TestWalkJSONLFramesLinesWithoutParsing(t *testing.T) {
	input := "{broken json}\n\n{\"ok\":true}\nlast"
	var got []JSONLLine
	err := WalkJSONL(context.Background(), strings.NewReader(input), Limits{
		MaxTotalBytes: int64(len(input)),
		MaxLineBytes:  64,
		MaxRecords:    3,
	}, func(line JSONLLine) error {
		line.Bytes = bytes.Clone(line.Bytes)
		got = append(got, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []JSONLLine{
		{Number: 1, Bytes: []byte("{broken json}")},
		{Number: 3, Bytes: []byte(`{"ok":true}`)},
		{Number: 4, Bytes: []byte("last"), FinalUnterminated: true},
	}
	if len(got) != len(want) {
		t.Fatalf("lines=%#v", got)
	}
	for index := range want {
		if got[index].Number != want[index].Number ||
			!bytes.Equal(got[index].Bytes, want[index].Bytes) ||
			got[index].FinalUnterminated != want[index].FinalUnterminated {
			t.Fatalf("line[%d]=%#v want %#v", index, got[index], want[index])
		}
	}
}

func TestWalkJSONLLimitsUseExactLimitPlusOneDetection(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		limits Limits
		want   error
	}{
		{name: "exact total", input: "a\n", limits: Limits{MaxTotalBytes: 2, MaxLineBytes: 1, MaxRecords: 1}},
		{name: "total plus one", input: "a\n", limits: Limits{MaxTotalBytes: 1, MaxLineBytes: 1, MaxRecords: 1}, want: ErrBudgetExceeded},
		{name: "exact line", input: "abc\n", limits: Limits{MaxTotalBytes: 4, MaxLineBytes: 3, MaxRecords: 1}},
		{name: "line plus one", input: "abcd\n", limits: Limits{MaxTotalBytes: 5, MaxLineBytes: 3, MaxRecords: 1}, want: ErrBudgetExceeded},
		{name: "exact records", input: "a\nb\n", limits: Limits{MaxTotalBytes: 4, MaxLineBytes: 1, MaxRecords: 2}},
		{name: "records plus one", input: "a\nb\n", limits: Limits{MaxTotalBytes: 4, MaxLineBytes: 1, MaxRecords: 1}, want: ErrBudgetExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := WalkJSONL(context.Background(), strings.NewReader(test.input), test.limits, func(JSONLLine) error { return nil })
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
		})
	}
}

func TestWalkJSONLPropagatesReaderVisitorAndCancellationErrors(t *testing.T) {
	readerFailure := errors.New("synthetic reader failure")
	reader := io.MultiReader(strings.NewReader("first\n"), failingReader{err: readerFailure})
	err := WalkJSONL(context.Background(), reader, Limits{MaxTotalBytes: 64, MaxLineBytes: 16, MaxRecords: 2}, func(JSONLLine) error { return nil })
	if !errors.Is(err, readerFailure) {
		t.Fatalf("reader error=%v", err)
	}

	visitorFailure := errors.New("synthetic visitor failure")
	err = WalkJSONL(context.Background(), strings.NewReader("first\n"), Limits{MaxTotalBytes: 6, MaxLineBytes: 5, MaxRecords: 1}, func(JSONLLine) error {
		return visitorFailure
	})
	if !errors.Is(err, visitorFailure) {
		t.Fatalf("visitor error=%v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = WalkJSONL(ctx, strings.NewReader("first\n"), Limits{MaxTotalBytes: 6, MaxLineBytes: 5, MaxRecords: 1}, func(JSONLLine) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-read cancellation=%v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	err = WalkJSONL(ctx, strings.NewReader("first\nsecond\n"), Limits{MaxTotalBytes: 13, MaxLineBytes: 6, MaxRecords: 2}, func(JSONLLine) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post-visit cancellation=%v", err)
	}
}

func TestWalkJSONLHandlesLargeReaderChunksWithBoundedLines(t *testing.T) {
	const records = 20_000
	input := strings.Repeat("{}\n", records)
	seen := 0
	err := WalkJSONL(context.Background(), strings.NewReader(input), Limits{
		MaxTotalBytes: int64(len(input)),
		MaxLineBytes:  2,
		MaxRecords:    records,
	}, func(line JSONLLine) error {
		seen++
		if line.Number != seen || string(line.Bytes) != "{}" || line.FinalUnterminated {
			t.Fatalf("line=%#v seen=%d", line, seen)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != records {
		t.Fatalf("seen=%d", seen)
	}
}

func TestWalkJSONLMaximumTotalLimitDoesNotOverflowLimitPlusOneProbe(t *testing.T) {
	seen := 0
	err := WalkJSONL(context.Background(), strings.NewReader("x"), Limits{
		MaxTotalBytes: math.MaxInt64,
		MaxLineBytes:  1,
		MaxRecords:    1,
	}, func(line JSONLLine) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("seen=%d", seen)
	}
}

func TestWalkJSONLRetriesTransientZeroLengthRead(t *testing.T) {
	reader := &zeroThenReader{reader: strings.NewReader("x\n")}
	seen := 0
	err := WalkJSONL(context.Background(), reader, Limits{
		MaxTotalBytes: 2,
		MaxLineBytes:  1,
		MaxRecords:    1,
	}, func(JSONLLine) error {
		seen++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("seen=%d", seen)
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

type zeroThenReader struct {
	reader io.Reader
	zeroed bool
}

func (r *zeroThenReader) Read(buffer []byte) (int, error) {
	if !r.zeroed {
		r.zeroed = true
		return 0, nil
	}
	return r.reader.Read(buffer)
}

func TestWithChatSnapshotReadsOnlyFixedColumnsFromCommittedWAL(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	insertFixtureConversation(t, database, LingmaIDEV1)
	assertNonEmptyWAL(t, path)

	var trace []QueryEvent
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
		sessions, err := reader.ListSessions(context.Background())
		if err != nil {
			return err
		}
		if len(sessions) != 1 || sessions[0].ID != "session-1" || sessions[0].ProjectID != "project-hash" {
			t.Fatalf("sessions=%#v", sessions)
		}
		conversation, err := reader.ReadConversation(context.Background(), "session-1")
		if err != nil {
			return err
		}
		if conversation.Session.ID != "session-1" || len(conversation.Records) != 1 || len(conversation.Messages) != 1 || len(conversation.Snapshots) != 1 {
			t.Fatalf("conversation=%#v", conversation)
		}
		if conversation.Records[0].Question != "question" || conversation.Records[0].Answer != "answer" || conversation.Records[0].ReasoningContent != "reasoning" {
			t.Fatalf("record=%#v", conversation.Records[0])
		}
		if conversation.Messages[0].Role != "assistant" || conversation.Messages[0].Content != "message" || conversation.Messages[0].ToolResult != "tool" {
			t.Fatalf("message=%#v", conversation.Messages[0])
		}
		return nil
	}, WithQueryObserver(func(event QueryEvent) { trace = append(trace, event) }))
	if err != nil {
		t.Fatal(err)
	}
	assertSafeQueryTrace(t, trace)
}

func TestWithChatSnapshotSupportsQoderSessionSchema(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, QoderIDEV1, nil)
	defer database.Close()
	insertFixtureConversation(t, database, QoderIDEV1)

	err := WithChatSnapshot(context.Background(), root, path, QoderIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
		sessions, err := reader.ListSessions(context.Background())
		if err != nil {
			return err
		}
		if len(sessions) != 1 || sessions[0].Status != "active" || sessions[0].LastUserQueryAt != 25 {
			t.Fatalf("sessions=%#v", sessions)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatReaderQueriesEachTableOnceAcrossMultipleReads(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	insertFixtureConversation(t, database, LingmaIDEV1)
	if _, err := database.Exec(`INSERT INTO chat_session(session_id,user_id,session_title,project_id,gmt_create,gmt_modified) VALUES ('session-2','identity-secret','title-secret','project-hash',5,6)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO chat_record(request_id,session_id,chat_task,question,gmt_create) VALUES ('request-2','session-2','chat','second',4)`); err != nil {
		t.Fatal(err)
	}

	var trace []QueryEvent
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
		sessions, err := reader.ListSessions(context.Background())
		if err != nil {
			return err
		}
		if len(sessions) != 2 || sessions[0].ID != "session-2" || sessions[1].ID != "session-1" {
			t.Fatalf("sessions=%#v", sessions)
		}
		for _, id := range []string{"session-1", "session-1", "session-2"} {
			conversation, err := reader.ReadConversation(context.Background(), id)
			if err != nil {
				return err
			}
			if conversation.Session.ID != id {
				t.Fatalf("conversation=%#v", conversation)
			}
		}
		return nil
	}, WithQueryObserver(func(event QueryEvent) { trace = append(trace, event) }))
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, event := range trace {
		if event.Kind == QueryData {
			counts[event.Table]++
		}
	}
	for _, table := range []string{"chat_session", "chat_record", "chat_message", "chat_snapshot"} {
		if counts[table] != 1 {
			t.Fatalf("data query counts=%v", counts)
		}
	}
}

func TestChatReaderGlobalRowLimitCountsOtherSessionsBeforeConversation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	for _, statement := range []string{
		`INSERT INTO chat_session(session_id,user_id,session_title,project_id,gmt_create) VALUES ('session-1','u','t','p',1)`,
		`INSERT INTO chat_session(session_id,user_id,session_title,project_id,gmt_create) VALUES ('session-2','u','t','p',2)`,
		`INSERT INTO chat_record(request_id,session_id,chat_task,question,gmt_create) VALUES ('request-1','session-1','chat','one',1)`,
		`INSERT INTO chat_record(request_id,session_id,chat_task,question,gmt_create) VALUES ('request-2','session-2','chat','two',2)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	limits := generousDatabaseLimits()
	limits.MaxRows = 3
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, limits, func(reader ChatReader) error {
		_, err := reader.ReadConversation(context.Background(), "session-1")
		return err
	})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("error=%v", err)
	}
}

func TestChatReaderCachedReadsDoNotReconsumeBudgets(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	insertFixtureConversation(t, database, LingmaIDEV1)
	limits := generousDatabaseLimits()
	limits.MaxSessions = 1
	limits.MaxRows = 4
	limits.MaxPayloadBytes = 34
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, limits, func(reader ChatReader) error {
		if _, err := reader.ListSessions(context.Background()); err != nil {
			return err
		}
		for range 2 {
			if _, err := reader.ReadConversation(context.Background(), "session-1"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatReaderSortsGloballyLoadedConversationRowsInGo(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	for _, statement := range []string{
		`INSERT INTO chat_session(session_id,user_id,session_title,project_id,gmt_create) VALUES ('session-1','u','t','p',1)`,
		`INSERT INTO chat_record(request_id,session_id,chat_task,gmt_create) VALUES ('record-z','session-1','chat',3)`,
		`INSERT INTO chat_record(request_id,session_id,chat_task,gmt_create) VALUES ('record-a','session-1','chat',3)`,
		`INSERT INTO chat_record(request_id,session_id,chat_task,gmt_create) VALUES ('record-first','session-1','chat',2)`,
		`INSERT INTO chat_message(id,session_id,gmt_create) VALUES ('message-z','session-1',3)`,
		`INSERT INTO chat_message(id,session_id,gmt_create) VALUES ('message-a','session-1',3)`,
		`INSERT INTO chat_message(id,session_id,gmt_create) VALUES ('message-first','session-1',2)`,
		`INSERT INTO chat_snapshot(snapshot_id,session_id,gmt_create) VALUES ('snapshot-z','session-1',3)`,
		`INSERT INTO chat_snapshot(snapshot_id,session_id,gmt_create) VALUES ('snapshot-a','session-1',3)`,
		`INSERT INTO chat_snapshot(snapshot_id,session_id,gmt_create) VALUES ('snapshot-first','session-1',2)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
		conversation, err := reader.ReadConversation(context.Background(), "session-1")
		if err != nil {
			return err
		}
		if got := []string{conversation.Records[0].RequestID, conversation.Records[1].RequestID, conversation.Records[2].RequestID}; !slices.Equal(got, []string{"record-first", "record-a", "record-z"}) {
			t.Fatalf("records=%v", got)
		}
		if got := []string{conversation.Messages[0].ID, conversation.Messages[1].ID, conversation.Messages[2].ID}; !slices.Equal(got, []string{"message-first", "message-a", "message-z"}) {
			t.Fatalf("messages=%v", got)
		}
		if got := []string{conversation.Snapshots[0].ID, conversation.Snapshots[1].ID, conversation.Snapshots[2].ID}; !slices.Equal(got, []string{"snapshot-first", "snapshot-a", "snapshot-z"}) {
			t.Fatalf("snapshots=%v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWithChatSnapshotRejectsSchemaDriftBeforeDataQueries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]string) []string
	}{
		{name: "view", mutate: replaceTableWithView("chat_session")},
		{name: "virtual table", mutate: replaceTableWithVirtual("chat_snapshot")},
		{name: "missing column", mutate: replaceDDL("description TEXT,\n", "")},
		{name: "extra column", mutate: appendDDL(`ALTER TABLE chat_snapshot ADD COLUMN surprise TEXT`)},
		{name: "declared type", mutate: replaceDDL("question TEXT,", "question BLOB,")},
		{name: "not null", mutate: replaceDDL("question TEXT,", "question TEXT NOT NULL,")},
		{name: "primary key", mutate: replaceDDL("request_id varchar(64) PRIMARY KEY,", "request_id varchar(64),")},
		{name: "hidden", mutate: replaceDDL("description TEXT,", "description TEXT GENERATED ALWAYS AS (name) VIRTUAL,")},
		{name: "default", mutate: replaceDDL("filter_status VARCHAR(64) DEFAULT '',", "filter_status VARCHAR(64) DEFAULT 'drift',")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "local.db")
			database := openSharedClientFixture(t, path, LingmaIDEV1, test.mutate)
			database.Close()
			var trace []QueryEvent
			called := false
			err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(ChatReader) error {
				called = true
				return nil
			}, WithQueryObserver(func(event QueryEvent) { trace = append(trace, event) }))
			if !errors.Is(err, ErrUnsupportedSchema) {
				t.Fatalf("error=%v", err)
			}
			if called {
				t.Fatal("callback invoked for unsupported schema")
			}
			for _, event := range trace {
				if event.Kind != QuerySchema {
					t.Fatalf("data query before schema rejection: %#v", event)
				}
			}
		})
	}
}

func TestWithChatSnapshotClassifiesBudgetsMalformedRowsAndCancellation(t *testing.T) {
	t.Run("aggregate database sidecars", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "local.db")
		database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
		defer database.Close()
		insertFixtureConversation(t, database, LingmaIDEV1)
		total := databaseFileSetSize(t, path)
		limits := generousDatabaseLimits()
		limits.MaxDatabaseBytes = total - 1
		err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, limits, func(ChatReader) error { return nil })
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("error=%v", err)
		}
	})

	for _, budget := range []struct {
		name   string
		adjust func(*Limits)
	}{
		{name: "sessions", adjust: func(limits *Limits) { limits.MaxSessions = 0 }},
		{name: "rows", adjust: func(limits *Limits) { limits.MaxRows = 1 }},
		{name: "payload", adjust: func(limits *Limits) { limits.MaxPayloadBytes = 1 }},
		{name: "canonical", adjust: func(limits *Limits) { limits.MaxCanonicalBytes = 1 }},
	} {
		t.Run(budget.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "local.db")
			database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
			defer database.Close()
			insertFixtureConversation(t, database, LingmaIDEV1)
			limits := generousDatabaseLimits()
			budget.adjust(&limits)
			err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, limits, func(reader ChatReader) error {
				_, err := reader.ReadConversation(context.Background(), "session-1")
				return err
			})
			if !errors.Is(err, ErrBudgetExceeded) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	t.Run("malformed conversation", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "local.db")
		database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
		defer database.Close()
		insertFixtureConversation(t, database, LingmaIDEV1)
		if _, err := database.Exec(`UPDATE chat_record SET request_id=NULL WHERE request_id='request-1'`); err != nil {
			t.Fatal(err)
		}
		err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
			_, err := reader.ReadConversation(context.Background(), "session-1")
			return err
		})
		if !errors.Is(err, ErrMalformedConversation) {
			t.Fatalf("error=%v", err)
		}
	})

	for _, storage := range []struct {
		name      string
		statement string
	}{
		{name: "blob text", statement: `UPDATE chat_record SET question=x'7175657374696f6e' WHERE request_id='request-1'`},
		{name: "blob integer", statement: `UPDATE chat_record SET gmt_create=x'313233' WHERE request_id='request-1'`},
	} {
		t.Run(storage.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "local.db")
			database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
			defer database.Close()
			insertFixtureConversation(t, database, LingmaIDEV1)
			if _, err := database.Exec(storage.statement); err != nil {
				t.Fatal(err)
			}
			err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
				_, err := reader.ReadConversation(context.Background(), "session-1")
				return err
			})
			if !errors.Is(err, ErrMalformedConversation) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	t.Run("callback error is not reclassified by text", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "local.db")
		database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
		database.Close()
		callbackError := errors.New("database snapshot exceeds limit")
		err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(ChatReader) error {
			return callbackError
		})
		if !errors.Is(err, callbackError) || errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("callback sqliteread sentinel is preserved", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "local.db")
		database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
		database.Close()
		err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(ChatReader) error {
			return sqliteread.ErrBudgetExceeded
		})
		if err != sqliteread.ErrBudgetExceeded || errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("canonical escaping", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "local.db")
		database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
		defer database.Close()
		insertFixtureConversation(t, database, LingmaIDEV1)
		if _, err := database.Exec(`UPDATE chat_record SET question=? WHERE request_id='request-1'`, strings.Repeat("\x00", 100)); err != nil {
			t.Fatal(err)
		}
		limits := generousDatabaseLimits()
		limits.MaxCanonicalBytes = 321 // Exact raw-field estimate; JSON escaping is larger.
		err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, limits, func(reader ChatReader) error {
			_, err := reader.ReadConversation(context.Background(), "session-1")
			return err
		})
		if !errors.Is(err, ErrBudgetExceeded) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := WithChatSnapshot(ctx, t.TempDir(), filepath.Join(t.TempDir(), "missing.db"), LingmaIDEV1, generousDatabaseLimits(), func(ChatReader) error { return nil })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	})
}

func TestClassifyDatabaseInitializationErrorUsesTypedSentinelOnly(t *testing.T) {
	typed := fmt.Errorf("wrapped: %w", sqliteread.ErrBudgetExceeded)
	if err := classifyDatabaseInitializationError(typed, false); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("typed error=%v", err)
	}
	if err := classifyDatabaseInitializationError(typed, true); err != typed {
		t.Fatalf("callback error=%v", err)
	}
	sameText := errors.New(sqliteread.ErrBudgetExceeded.Error())
	if err := classifyDatabaseInitializationError(sameText, false); err != sameText || errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("same-text error=%v", err)
	}
}

func TestChatReaderSerializesConcurrentBudgetUse(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	insertFixtureConversation(t, database, LingmaIDEV1)
	limits := generousDatabaseLimits()
	limits.MaxSessions = 1

	entered := make(chan struct{})
	release := make(chan struct{})
	var blocked atomic.Bool
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, limits, func(reader ChatReader) error {
		firstDone := make(chan error, 1)
		secondDone := make(chan error, 1)
		go func() {
			_, err := reader.ListSessions(context.Background())
			firstDone <- err
		}()
		<-entered
		go func() {
			_, err := reader.ListSessions(context.Background())
			secondDone <- err
		}()
		select {
		case err := <-secondDone:
			close(release)
			<-firstDone
			t.Fatalf("second call was not serialized: %v", err)
		case <-time.After(100 * time.Millisecond):
		}
		close(release)
		results := []error{<-firstDone, <-secondDone}
		successes := 0
		for _, err := range results {
			switch {
			case err == nil:
				successes++
			default:
				t.Fatalf("result=%v", err)
			}
		}
		if successes != 2 {
			t.Fatalf("results=%v", results)
		}
		return nil
	}, WithQueryObserver(func(event QueryEvent) {
		if event.Kind == QueryData && event.Table == "chat_session" && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatReaderCanceledCallDoesNotWaitForConcurrentRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	insertFixtureConversation(t, database, LingmaIDEV1)

	entered := make(chan struct{})
	release := make(chan struct{})
	var blocked atomic.Bool
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
		firstDone := make(chan error, 1)
		go func() {
			_, err := reader.ListSessions(context.Background())
			firstDone <- err
		}()
		<-entered
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		secondDone := make(chan error, 1)
		go func() {
			_, err := reader.ListSessions(ctx)
			secondDone <- err
		}()
		select {
		case err := <-secondDone:
			if !errors.Is(err, context.Canceled) {
				close(release)
				<-firstDone
				t.Fatalf("error=%v", err)
			}
		case <-time.After(100 * time.Millisecond):
			close(release)
			<-firstDone
			t.Fatal("canceled call remained blocked behind concurrent read")
		}
		close(release)
		return <-firstDone
	}, WithQueryObserver(func(event QueryEvent) {
		if event.Kind == QueryData && event.Table == "chat_session" && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestChatReaderInvalidatesAfterCancellationInterruptsOneTimeLoad(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.db")
	database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
	defer database.Close()
	insertFixtureConversation(t, database, LingmaIDEV1)

	ctx, cancel := context.WithCancel(context.Background())
	queries := 0
	err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, generousDatabaseLimits(), func(reader ChatReader) error {
		if _, err := reader.ReadConversation(ctx, "session-1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("interrupted load error=%v", err)
		}
		if _, err := reader.ListSessions(context.Background()); !errors.Is(err, ErrReaderInvalidated) {
			t.Fatalf("list after interrupted load error=%v", err)
		}
		if _, err := reader.ReadConversation(context.Background(), "session-1"); !errors.Is(err, ErrReaderInvalidated) {
			t.Fatalf("read after interrupted load error=%v", err)
		}
		return nil
	}, WithQueryObserver(func(event QueryEvent) {
		if event.Kind == QueryData && event.Table == "chat_record" {
			queries++
			cancel()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if queries != 1 {
		t.Fatalf("record queries=%d", queries)
	}
}

func TestFixedQueryPlansContainNoWildcardDynamicOrProhibitedData(t *testing.T) {
	if len(fixedSchemaQueries) != 5 {
		t.Fatalf("schema queries=%d", len(fixedSchemaQueries))
	}
	for _, query := range fixedSchemaQueries {
		if query.kind != QuerySchema || (query.table != "" && !slices.Contains([]string{"chat_session", "chat_record", "chat_message", "chat_snapshot"}, query.table)) {
			t.Fatalf("schema query=%#v", query)
		}
	}
	banned := []string{
		"user_id", "user_name", "org_id", "session_title", "project_uri", "project_name",
		"preferred_model_info", "token_info", "model_info", "extra", "system_role_content",
		"chat_context", "chat_prompt", "error_result",
	}
	for _, query := range fixedDataQueries {
		lower := strings.ToLower(query.statement)
		if query.kind != QueryData || !slices.Contains([]string{"chat_session", "chat_record", "chat_message", "chat_snapshot"}, query.table) {
			t.Fatalf("data query=%#v", query)
		}
		if strings.Contains(lower, "select *") || strings.ContainsAny(lower, "$@:") || strings.Contains(lower, " order by ") || strings.Contains(lower, " where ") {
			t.Fatalf("unsafe query=%q", query.statement)
		}
		for _, column := range banned {
			if strings.Contains(lower, column) || slices.Contains(query.columns, column) {
				t.Fatalf("query exposes %q: %#v", column, query)
			}
		}
	}
}

func TestBodyQueriesGuardEveryTextCellBeforeMaterialization(t *testing.T) {
	for _, test := range []struct {
		name    string
		spec    querySpec
		columns []string
	}{
		{name: "record", spec: recordQuery, columns: []string{"question", "answer", "reasoning_content"}},
		{name: "message", spec: messageQuery, columns: []string{"content", "tool_result"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			statement := strings.ToLower(test.spec.statement)
			for _, column := range test.columns {
				if !strings.Contains(statement, "typeof("+column+")") {
					t.Fatalf("query does not expose %s storage type: %q", column, test.spec.statement)
				}
				if !strings.Contains(statement, "length(cast("+column+" as blob))") {
					t.Fatalf("query does not expose %s byte length: %q", column, test.spec.statement)
				}
				if !strings.Contains(statement, "case when typeof("+column+")='text'") || !strings.Contains(statement, "then "+column+" else null end") {
					t.Fatalf("query does not guard %s value: %q", column, test.spec.statement)
				}
			}
			if strings.Count(statement, "case when") != len(test.columns) {
				t.Fatalf("query guards=%d want %d: %q", strings.Count(statement, "case when"), len(test.columns), test.spec.statement)
			}
			if strings.Count(statement, "<=?") != len(test.columns) {
				t.Fatalf("query guard limits=%d want %d: %q", strings.Count(statement, "<=?"), len(test.columns), test.spec.statement)
			}
		})
	}
}

func TestBodyCellPayloadLimitsAreExactAndApplyToEveryColumn(t *testing.T) {
	const limit = 32
	for _, test := range []struct {
		name, table, idColumn, bodyColumn string
	}{
		{name: "record question", table: "chat_record", idColumn: "request_id", bodyColumn: "question"},
		{name: "record answer", table: "chat_record", idColumn: "request_id", bodyColumn: "answer"},
		{name: "record reasoning", table: "chat_record", idColumn: "request_id", bodyColumn: "reasoning_content"},
		{name: "message content", table: "chat_message", idColumn: "id", bodyColumn: "content"},
		{name: "message tool result", table: "chat_message", idColumn: "id", bodyColumn: "tool_result"},
	} {
		for _, size := range []int{limit, limit + 1} {
			t.Run(fmt.Sprintf("%s/%d", test.name, size), func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "local.db")
				database := openSharedClientFixture(t, path, LingmaIDEV1, nil)
				defer database.Close()
				if _, err := database.Exec(`INSERT INTO chat_session(session_id,user_id,session_title,project_id,gmt_create) VALUES ('session-1','u','t','p',1)`); err != nil {
					t.Fatal(err)
				}
				body := strings.Repeat("x", size)
				var statement string
				if test.table == "chat_record" {
					statement = fmt.Sprintf("INSERT INTO chat_record(%s,session_id,chat_task,%s,gmt_create) VALUES ('row-1','session-1','chat',?,2)", test.idColumn, test.bodyColumn)
				} else {
					statement = fmt.Sprintf("INSERT INTO chat_message(%s,session_id,%s,gmt_create) VALUES ('row-1','session-1',?,2)", test.idColumn, test.bodyColumn)
				}
				if _, err := database.Exec(statement, body); err != nil {
					t.Fatal(err)
				}
				limits := generousDatabaseLimits()
				limits.MaxPayloadBytes = limit
				var got string
				err := WithChatSnapshot(context.Background(), root, path, LingmaIDEV1, limits, func(reader ChatReader) error {
					conversation, err := reader.ReadConversation(context.Background(), "session-1")
					if err != nil {
						return err
					}
					if test.table == "chat_record" {
						switch test.bodyColumn {
						case "question":
							got = conversation.Records[0].Question
						case "answer":
							got = conversation.Records[0].Answer
						case "reasoning_content":
							got = conversation.Records[0].ReasoningContent
						}
					} else if test.bodyColumn == "content" {
						got = conversation.Messages[0].Content
					} else {
						got = conversation.Messages[0].ToolResult
					}
					return err
				})
				if size == limit && err != nil {
					t.Fatal(err)
				}
				if size == limit && got != body {
					t.Fatalf("body length=%d want %d", len(got), len(body))
				}
				if size == limit+1 && !errors.Is(err, ErrBudgetExceeded) {
					t.Fatalf("error=%v", err)
				}
			})
		}
	}
}

func TestGuardedTextDistinguishesNullOversizeAndMalformedStorage(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []any
		limit  int64
		want   string
		err    error
	}{
		{name: "null", values: []any{"null", nil, nil}, limit: 0},
		{name: "exact", values: []any{"text", int64(3), "abc"}, limit: 3, want: "abc"},
		{name: "oversize value suppressed by SQL", values: []any{"text", int64(4), nil}, limit: 3, err: ErrBudgetExceeded},
		{name: "blob", values: []any{"blob", int64(3), nil}, limit: 3, err: ErrMalformedConversation},
		{name: "integer", values: []any{"integer", int64(3), nil}, limit: 3, err: ErrMalformedConversation},
		{name: "guard contract broken", values: []any{"text", int64(4), "abcd"}, limit: 3, err: ErrMalformedConversation},
		{name: "byte length mismatch", values: []any{"text", int64(2), "abc"}, limit: 3, err: ErrMalformedConversation},
	} {
		t.Run(test.name, func(t *testing.T) {
			decoder := &storageDecoder{values: test.values}
			got, err := decoder.guardedText(test.limit)
			if got != test.want || !errors.Is(err, test.err) {
				t.Fatalf("text=%q error=%v want text=%q error=%v", got, err, test.want, test.err)
			}
		})
	}
}

func TestFixedDataQueriesDoNotUseTemporarySorts(t *testing.T) {
	for _, query := range fixedDataQueries {
		t.Run(query.table+fmt.Sprint(len(query.columns)), func(t *testing.T) {
			schema := LingmaIDEV1
			if query.statement == qoderSessionQuery.statement {
				schema = QoderIDEV1
			}
			root := t.TempDir()
			path := filepath.Join(root, "local.db")
			database := openSharedClientFixture(t, path, schema, nil)
			defer database.Close()
			arguments := make([]any, strings.Count(query.statement, "?"))
			for index := range arguments {
				arguments[index] = 1
			}
			rows, err := database.Query(`EXPLAIN QUERY PLAN `+query.statement, arguments...)
			if err != nil {
				t.Fatalf("query %q: %v", query.statement, err)
			}
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					rows.Close()
					t.Fatal(err)
				}
				if strings.Contains(strings.ToUpper(detail), "TEMP B-TREE") {
					rows.Close()
					t.Fatalf("query %q plan=%q", query.statement, detail)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
		})
	}
}

func generousDatabaseLimits() Limits {
	return Limits{
		MaxDatabaseBytes:  16 << 20,
		MaxSessions:       100,
		MaxRows:           1000,
		MaxPayloadBytes:   1 << 20,
		MaxCanonicalBytes: 1 << 20,
	}
}

func assertSafeQueryTrace(t *testing.T, trace []QueryEvent) {
	t.Helper()
	if len(trace) != 9 {
		t.Fatalf("trace=%#v", trace)
	}
	if trace[0].Kind != QuerySchema || trace[0].Table != "" {
		t.Fatalf("table_list trace=%#v", trace[0])
	}
	for index, table := range []string{"chat_session", "chat_record", "chat_message", "chat_snapshot"} {
		event := trace[index+1]
		if event.Kind != QuerySchema || event.Table != table {
			t.Fatalf("xinfo[%d]=%#v", index, event)
		}
	}
	for _, event := range trace[5:] {
		if event.Kind != QueryData || !slices.Contains([]string{"chat_session", "chat_record", "chat_message", "chat_snapshot"}, event.Table) {
			t.Fatalf("unsafe data trace=%#v", event)
		}
		if slices.Contains([]string{"account", "token", "goal", "notification"}, event.Table) {
			t.Fatalf("decoy queried=%#v", event)
		}
	}
}

func openSharedClientFixture(t *testing.T, path string, schema SchemaID, mutate func([]string) []string) *sql.DB {
	t.Helper()
	uri := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	database, err := sql.Open("sqlite", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	statements := fixtureSchemaDDL(schema)
	if mutate != nil {
		statements = mutate(statements)
	}
	statements = append([]string{`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`}, statements...)
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("statement %q: %v", statement, err)
		}
	}
	return database
}

func fixtureSchemaDDL(schema SchemaID) []string {
	sessionExtra := ""
	if schema == QoderIDEV1 {
		sessionExtra = "status VARCHAR(32) DEFAULT '',\nlast_user_query_at INTEGER DEFAULT 0,\n"
	}
	return []string{
		fmt.Sprintf(`CREATE TABLE chat_session (
session_id varchar(64) PRIMARY KEY,
user_id VARCHAR(64) NOT NULL,
user_name varchar(64),
session_title varchar(256) NOT NULL,
project_id varchar(64) NOT NULL,
project_uri varchar(512),
project_name varchar(64),
gmt_create INTEGER,
gmt_modified INTEGER,
org_id VARCHAR(64) DEFAULT '',
session_type VARCHAR(64) DEFAULT '',
mode VARCHAR(64) DEFAULT '',
version VARCHAR(64) DEFAULT '',
preferred_model_info TEXT DEFAULT '',
%sstop_reason VARCHAR(20) DEFAULT '',
extra TEXT DEFAULT '',
parent_session_id VARCHAR(64) DEFAULT '',
parent_tool_call_id VARCHAR(128) DEFAULT ''
)`, sessionExtra),
		`CREATE TABLE chat_record (
request_id varchar(64) PRIMARY KEY,
session_id varchar(64) NOT NULL,
chat_task varchar(64) NOT NULL,
chat_context TEXT,
system_role_content TEXT,
question TEXT,
answer TEXT,
like_status INT,
gmt_create INTEGER,
gmt_modified INTEGER,
finish_status INTEGER,
filter_status VARCHAR(64) DEFAULT '',
error_result VARCHAR(1024) DEFAULT '{}',
code_language VARCHAR(62) DEFAULT '',
extra TEXT DEFAULT '{}',
session_type VARCHAR(64) DEFAULT '',
summary TEXT DEFAULT '',
intention_type VARCHAR(64) DEFAULT '',
reasoning_content TEXT,
mode VARCHAR(64) DEFAULT '',
chat_prompt TEXT DEFAULT '',
parent_session_id VARCHAR(64) DEFAULT '',
parent_tool_call_id VARCHAR(128) DEFAULT ''
)`,
		`CREATE TABLE chat_message (
id varchar(64) PRIMARY KEY,
session_id VARCHAR(64),
request_id VARCHAR(64),
role VARCHAR(64),
content TEXT,
summary TEXT,
summary_modified INTEGER,
summary_trigger INTEGER DEFAULT 0,
tool_result TEXT,
token_info TEXT,
model_info TEXT,
extra TEXT DEFAULT '',
gmt_create INTEGER
)`,
		`CREATE TABLE chat_snapshot (
snapshot_id varchar(64) PRIMARY KEY,
session_id varchar(64) NOT NULL,
chat_record_id varchar(64),
status varchar(64),
name varchar(64),
description TEXT,
gmt_create INTEGER,
gmt_modified INTEGER
)`,
		`CREATE TABLE account (secret TEXT)`,
		`CREATE TABLE token (secret TEXT)`,
		`CREATE TABLE goal (secret TEXT)`,
		`CREATE TABLE notification (secret TEXT)`,
	}
}

func insertFixtureConversation(t *testing.T, database *sql.DB, schema SchemaID) {
	t.Helper()
	sessionColumns := "session_id,user_id,session_title,project_id,gmt_create,gmt_modified,session_type,mode,version,stop_reason,parent_session_id,parent_tool_call_id"
	sessionValues := "'session-1','identity-secret','title-secret','project-hash',10,20,'agent','code','v1','done','',''"
	if schema == QoderIDEV1 {
		sessionColumns += ",status,last_user_query_at"
		sessionValues += ",'active',25"
	}
	statements := []string{
		fmt.Sprintf("INSERT INTO chat_session(%s) VALUES (%s)", sessionColumns, sessionValues),
		`INSERT INTO chat_record(request_id,session_id,chat_task,question,answer,reasoning_content,gmt_create,gmt_modified,finish_status) VALUES ('request-1','session-1','chat','question','answer','reasoning',11,12,1)`,
		`INSERT INTO chat_message(id,session_id,request_id,role,content,tool_result,gmt_create) VALUES ('message-1','session-1','request-1','assistant','message','tool',13)`,
		`INSERT INTO chat_snapshot(snapshot_id,session_id,chat_record_id,status,gmt_create,gmt_modified) VALUES ('snapshot-1','session-1','request-1','ready',14,15)`,
		`INSERT INTO account(secret) VALUES ('do-not-query')`,
		`INSERT INTO token(secret) VALUES ('do-not-query')`,
		`INSERT INTO goal(secret) VALUES ('do-not-query')`,
		`INSERT INTO notification(secret) VALUES ('do-not-query')`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func replaceDDL(old, replacement string) func([]string) []string {
	return func(statements []string) []string {
		result := slices.Clone(statements)
		for index, statement := range result {
			if strings.Contains(statement, old) {
				result[index] = strings.Replace(statement, old, replacement, 1)
				return result
			}
		}
		panic("fixture fragment not found: " + old)
	}
}

func appendDDL(statement string) func([]string) []string {
	return func(statements []string) []string { return append(slices.Clone(statements), statement) }
}

func replaceTableWithView(table string) func([]string) []string {
	return func(statements []string) []string {
		result := make([]string, 0, len(statements))
		prefix := "CREATE TABLE " + table + " "
		for _, statement := range statements {
			if strings.HasPrefix(statement, prefix) {
				result = append(result, "CREATE VIEW "+table+" AS SELECT 1 AS synthetic")
				continue
			}
			result = append(result, statement)
		}
		return result
	}
}

func replaceTableWithVirtual(table string) func([]string) []string {
	return func(statements []string) []string {
		result := make([]string, 0, len(statements))
		prefix := "CREATE TABLE " + table + " "
		for _, statement := range statements {
			if strings.HasPrefix(statement, prefix) {
				result = append(result, "CREATE VIRTUAL TABLE "+table+" USING fts5(snapshot_id,session_id,chat_record_id,status,name,description,gmt_create,gmt_modified)")
				continue
			}
			result = append(result, statement)
		}
		return result
	}
}

func assertNonEmptyWAL(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path + "-wal")
	if err != nil || info.Size() == 0 {
		t.Fatalf("non-empty WAL required: info=%v err=%v", info, err)
	}
}

func databaseFileSetSize(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	return total
}
