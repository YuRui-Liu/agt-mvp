package kimicode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/safeopen"
)

const kimiSessionID = "session_11111111-1111-4111-8111-111111111111"

func installKimi(t *testing.T, root, bucket, id, indexWorkDir string, state, wire []byte) (string, string, string) {
	t.Helper()
	dir := filepath.Join(root, "sessions", bucket, id)
	if err := os.MkdirAll(filepath.Join(dir, "agents", "main"), 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(dir, "state.json")
	wirePath := filepath.Join(dir, "agents", "main", "wire.jsonl")
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wirePath, wire, 0o600); err != nil {
		t.Fatal(err)
	}
	entry := fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":%q}`+"\n", id, dir, indexWorkDir)
	if err := os.WriteFile(filepath.Join(root, "session_index.jsonl"), []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, statePath, wirePath
}

func kimiFixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	state, err := os.ReadFile("../testdata/kimicode/state-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile("../testdata/kimicode/wire-v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return state, wire
}

func readKimiEvents(t *testing.T, adapter *Adapter, session source.Session) []map[string]any {
	t.Helper()
	r, err := adapter.Open(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []map[string]any
	decoder := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		adaptertest.AssertNoPrivateFields(t, event)
		out = append(out, event)
	}
}

func TestKimiCodeIndexStateWireContract(t *testing.T) {
	state, wire := kimiFixture(t)
	if _, ok := parseState(context.Background(), state, false); !ok {
		t.Fatal("state fixture rejected")
	}
	if parsed, ok := parseWire(context.Background(), wire); !ok {
		t.Fatalf("wire fixture rejected: %#v", parsed)
	}
	root := t.TempDir()
	dir, _, _ := installKimi(t, root, "wd_custom", kimiSessionID, "/synthetic/project", state, wire)
	cleanRoot, err := canonicalizeRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := safeopen.Bind(cleanRoot)
	if err != nil {
		t.Fatal(err)
	}
	indexFile, ok := readBoundFile(context.Background(), bound, "session_index.jsonl", maxIndexBytes)
	if !ok {
		t.Fatal("index fixture rejected")
	}
	if entry, ok := validatedIndexEntry(cleanRoot, kimiSessionID, dir, "/synthetic/project"); !ok {
		t.Fatalf("direct index entry rejected: root=%q clean=%q dir=%q entry=%#v", root, cleanRoot, dir, entry)
	}
	var raw map[string]any
	if !jsonDepthOK(context.Background(), bytes.TrimSpace(indexFile.data), maxJSONDepth) {
		t.Fatal("index depth rejected")
	}
	if err := json.Unmarshal(indexFile.data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["sessionId"] != kimiSessionID || raw["sessionDir"] != dir {
		t.Fatalf("raw index=%#v", raw)
	}
	if entry, ok := validatedIndexEntry(cleanRoot, raw["sessionId"].(string), raw["sessionDir"].(string), raw["workDir"].(string)); !ok {
		t.Fatalf("raw index entry rejected: %#v", entry)
	}
	if entry, deleted, ok := parseIndexLine(context.Background(), cleanRoot, bytes.TrimSpace(indexFile.data)); !ok || deleted {
		t.Fatalf("raw index line rejected: entry=%#v deleted=%v ok=%v", entry, deleted, ok)
	}
	entries, ok := parseIndex(context.Background(), cleanRoot, indexFile.data)
	if !ok || len(entries) != 1 {
		t.Fatalf("index entries=%#v ok=%v", entries, ok)
	}
	item := candidate{root: cleanRoot, rootIdentity: bound.Identity(), bound: bound, index: indexFile, entry: entries[0]}
	if _, _, _, ok := New(root).snapshot(context.Background(), item); !ok {
		t.Fatal("snapshot fixture rejected")
	}
	bound.Close()
	adapter := New(root)
	sessions, err := adapter.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%d err=%v", len(sessions), err)
	}
	session := sessions[0]
	if session.Product != "kimi-code" || session.FormatVersion != "wire-1.4" || session.MessageCount != 2 || session.MalformedCount != 0 {
		t.Fatalf("session=%#v", session)
	}
	if session.Scope.Type != source.ScopeProject || session.Scope.Label != "project" || filepath.IsAbs(session.Scope.Root) {
		t.Fatalf("scope=%#v", session.Scope)
	}
	if !slices.Contains(session.Capabilities, source.CapabilityMessages) || !slices.Contains(session.Capabilities, source.CapabilityTools) || !slices.Contains(session.Capabilities, source.CapabilityReasoning) {
		t.Fatalf("capabilities=%#v", session.Capabilities)
	}
	if session.Usage["input_tokens"] != 10 || session.Usage["output_tokens"] != 5 || session.Usage["cache_read_tokens"] != 2 || session.Usage["cache_write_tokens"] != 1 {
		t.Fatalf("usage=%#v", session.Usage)
	}
	events := readKimiEvents(t, adapter, session)
	types := make([]string, len(events))
	for i := range events {
		types[i], _ = events[i]["type"].(string)
	}
	if !reflect.DeepEqual(types, []string{"message", "message", "message", "tool_use", "tool_result"}) {
		t.Fatalf("types=%#v", types)
	}
	encoded, _ := json.Marshal(events)
	for _, secret := range []string{"must not be exposed", "/synthetic/project", "/private/agent-home", "/must/not/leak", kimiSessionID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private value leaked")
		}
	}
}

func TestKimiCodeIndexLastWinsTombstoneAndValidation(t *testing.T) {
	state, wire := kimiFixture(t)
	t.Run("last-wins-and-tombstone", func(t *testing.T) {
		root := t.TempDir()
		dir, _, _ := installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
		otherID := "session_22222222-2222-4222-8222-222222222222"
		otherDir := filepath.Join(root, "sessions", "wd_b", otherID)
		lines := []string{
			`{bad`,
			`{"sessionId":"missing"}`,
			fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":"/synthetic/project"}`, kimiSessionID, filepath.Join(root, "outside", kimiSessionID)),
			fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":"/synthetic/project"}`, kimiSessionID, dir),
			fmt.Sprintf(`{"sessionId":%q,"deleted":true}`, kimiSessionID),
			fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":"/synthetic/project"}`, kimiSessionID, dir),
			fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":"/synthetic/project"}`, otherID, otherDir),
			fmt.Sprintf(`{"sessionId":%q,"deleted":true}`, otherID),
		}
		if err := os.WriteFile(filepath.Join(root, "session_index.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
	})
	t.Run("basename-and-id", func(t *testing.T) {
		root := t.TempDir()
		dir, _, _ := installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
		bad := fmt.Sprintf(`{"sessionId":"../%s","sessionDir":%q,"workDir":"/synthetic/project"}`+"\n", kimiSessionID, dir)
		if err := os.WriteFile(filepath.Join(root, "session_index.jsonl"), []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
	})
	t.Run("no-index-no-scan", func(t *testing.T) {
		root := t.TempDir()
		installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
		if err := os.Remove(filepath.Join(root, "session_index.jsonl")); err != nil {
			t.Fatal(err)
		}
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
	})
}

func TestKimiCodeWorkDirConflictFailsClosedAndLegacyCustomCWDIsNarrow(t *testing.T) {
	_, wire := kimiFixture(t)
	root := t.TempDir()
	state := []byte(`{"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:01Z","workDir":"/synthetic/state"}`)
	installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/index", state, wire)
	if got, err := New(root).Discover(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}

	legacyRoot := t.TempDir()
	legacy := []byte(`{"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:01Z","custom":{"cwd":"/synthetic/legacy","other":"ignored"}}`)
	installKimi(t, legacyRoot, "wd_a", kimiSessionID, "/synthetic/legacy", legacy, wire)
	if got, err := NewLegacy(legacyRoot).Discover(context.Background()); err != nil || len(got) != 1 || got[0].Scope.Label != "legacy" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got, err := New(legacyRoot).Discover(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("non-legacy sessions=%#v err=%v", got, err)
	}

	uncRoot := t.TempDir()
	uncState := []byte(`{"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:01Z","workDir":"\\\\server\\share\\project"}`)
	installKimi(t, uncRoot, "wd_unc", kimiSessionID, `\\server\share\project`, uncState, wire)
	if got, err := New(uncRoot).Discover(context.Background()); err != nil || len(got) != 1 || got[0].Scope.Type != source.ScopeProject || got[0].Scope.Label != "project" {
		t.Fatalf("UNC sessions=%#v err=%v", got, err)
	}
}

func TestKimiCodeMalformedUnknownPartialCanceledAndUsageFallback(t *testing.T) {
	state, _ := kimiFixture(t)
	body := strings.Join([]string{
		`{"type":"metadata","protocol_version":"1.4","created_at":1767225600000}`,
		`{"type":"future.record","secret":"ignored"}`,
		`{"type":"context.append_message","message":{"role":"assistant","content":[{"type":"text","text":"ignored"}]}}`,
		`{"type":"context.append_message","message":{"role":"user","content":42}}`,
		`{"type":"context.append_loop_event","event":{"type":"step.begin","uuid":"cancel-step"}}`,
		`{"type":"context.append_loop_event","event":{"type":"content.part","uuid":"part","stepUuid":"cancel-step","part":{"type":"text","text":"partial answer"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"tool.result","uuid":"orphan-result","parentUuid":"missing","toolCallId":"orphan","result":{"output":"bad"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"future.event"}}`,
		`{"type":"usage.record","usage":{"inputOther":"bad"}}`,
		`{bad`,
	}, "\n") + "\n"
	root := t.TempDir()
	installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, []byte(body))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].MessageCount != 1 || got[0].MalformedCount != 4 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	events := readKimiEvents(t, a, got[0])
	if len(events) != 1 || events[0]["content"] != "partial answer" {
		t.Fatalf("events=%#v", events)
	}

	fallback := strings.Join([]string{
		`{"type":"metadata","protocol_version":"1.4","created_at":1767225600000}`,
		`{"type":"context.append_loop_event","event":{"type":"step.begin","uuid":"s"}}`,
		`{"type":"context.append_loop_event","event":{"type":"content.part","uuid":"p","stepUuid":"s","part":{"type":"text","text":"answer"}}}`,
		`{"type":"context.append_loop_event","event":{"type":"step.end","uuid":"s","usage":{"inputOther":3,"inputCacheRead":2,"output":4}}}`,
	}, "\n") + "\n"
	fallbackRoot := t.TempDir()
	installKimi(t, fallbackRoot, "wd_a", kimiSessionID, "/synthetic/project", state, []byte(fallback))
	got, err = New(fallbackRoot).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Usage["input_tokens"] != 3 || got[0].Usage["cache_read_tokens"] != 2 || got[0].Usage["output_tokens"] != 4 {
		t.Fatalf("fallback=%#v err=%v", got, err)
	}
}

func TestKimiCodeKnownRecordMillisAreValidatedAndIsolated(t *testing.T) {
	t.Run("known-record-time", func(t *testing.T) {
		body := strings.Join([]string{
			`{"type":"metadata","protocol_version":"1.4","created_at":1767225600000}`,
			`{"type":"context.append_message","message":{"role":"user","content":[{"type":"text","text":"negative"}]},"time":-1}`,
			`{"type":"context.append_message","message":{"role":"user","content":[{"type":"text","text":"optional time"}]}}`,
			`{"type":"context.append_message","message":{"role":"user","content":[{"type":"text","text":"kept"}]},"time":1767225600000}`,
			`{"type":"context.append_loop_event","event":{"type":"step.begin","uuid":"step"},"time":1.5}`,
			`{"type":"context.append_loop_event","event":{"type":"step.begin","uuid":"step"},"time":1767225601000}`,
			`{"type":"context.append_loop_event","event":{"type":"content.part","uuid":"part","stepUuid":"step","part":{"type":"text","text":"bad"}},"time":"1767225602000"}`,
			`{"type":"context.append_loop_event","event":{"type":"content.part","uuid":"part","stepUuid":"step","part":{"type":"text","text":"answer"}},"time":1767225602000}`,
			`{"type":"usage.record","usage":{"inputOther":99},"time":9223372036854775808}`,
			`{"type":"usage.record","usage":{"inputOther":3},"time":1767225603000}`,
		}, "\n") + "\n"
		parsed, ok := parseWire(context.Background(), []byte(body))
		if !ok || parsed.malformed != 4 || len(parsed.events) != 3 || parsed.usage["input_tokens"] != 3 {
			t.Fatalf("parsed=%#v ok=%v", parsed, ok)
		}
		if parsed.events[0].Content != "optional time" || parsed.events[0].Timestamp != "" || parsed.events[1].Content != "kept" || parsed.events[2].Content != "answer" {
			t.Fatalf("events=%#v", parsed.events)
		}
	})

	t.Run("metadata-created-at-required", func(t *testing.T) {
		body := strings.Join([]string{
			`{"type":"metadata","protocol_version":"1.4"}`,
			`{"type":"context.append_message","message":{"role":"user","content":[{"type":"text","text":"kept"}]},"time":1767225600000}`,
		}, "\n") + "\n"
		parsed, ok := parseWire(context.Background(), []byte(body))
		if !ok || parsed.malformed != 1 || len(parsed.events) != 1 {
			t.Fatalf("parsed=%#v ok=%v", parsed, ok)
		}
	})

	for _, createdAt := range []string{"null", `"1767225600000"`, "-1", "1.5", "9223372036854775808"} {
		t.Run("metadata-created-at-"+strings.NewReplacer(`"`, "quote", ".", "dot", "-", "negative").Replace(createdAt), func(t *testing.T) {
			body := strings.Join([]string{
				`{"type":"metadata","protocol_version":"1.4","created_at":` + createdAt + `}`,
				`{"type":"context.append_message","message":{"role":"user","content":[{"type":"text","text":"kept"}]},"time":1767225600000}`,
			}, "\n") + "\n"
			parsed, ok := parseWire(context.Background(), []byte(body))
			if !ok || parsed.malformed != 1 || len(parsed.events) != 1 || parsed.events[0].Content != "kept" {
				t.Fatalf("created_at=%s parsed=%#v ok=%v", createdAt, parsed, ok)
			}
		})
	}
}

func TestKimiCodeCompositeAuthorization(t *testing.T) {
	state, wire := kimiFixture(t)
	root := t.TempDir()
	_, statePath, wirePath := installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	forged := got[0]
	forged.MessageCount++
	if r, err := a.Open(context.Background(), forged); err == nil {
		r.Close()
		t.Fatal("forged metadata accepted")
	}
	if r, err := New(root).Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("cross-instance session accepted")
	}
	if err := os.WriteFile(statePath, append(state, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("state tamper accepted")
	}
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	a, got = New(root), nil
	got, err = a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	if err := os.WriteFile(wirePath, append(wire, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("wire tamper accepted")
	}
}

func TestKimiCodeOpenRejectsIndexRemapSameByteReplacementAndRootSwap(t *testing.T) {
	state, wire := kimiFixture(t)
	t.Run("index-remap", func(t *testing.T) {
		root := t.TempDir()
		installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
		a := New(root)
		got, err := a.Discover(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
		installKimi(t, root, "wd_b", kimiSessionID, "/synthetic/project", state, wire)
		if r, err := a.Open(context.Background(), got[0]); err == nil {
			r.Close()
			t.Fatal("changed index mapping accepted")
		}
	})
	t.Run("same-byte-replacement", func(t *testing.T) {
		root := t.TempDir()
		_, _, wirePath := installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
		a := New(root)
		got, err := a.Discover(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
		if err := os.Rename(wirePath, wirePath+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wirePath, wire, 0o600); err != nil {
			t.Fatal(err)
		}
		if r, err := a.Open(context.Background(), got[0]); err == nil {
			r.Close()
			t.Fatal("same-byte replacement accepted")
		}
	})
	t.Run("root-swap", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "kimi-code")
		installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
		a := New(root)
		got, err := a.Discover(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
		if err := os.Rename(root, filepath.Join(base, "original")); err != nil {
			t.Fatal(err)
		}
		installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
		if r, err := a.Open(context.Background(), got[0]); err == nil {
			r.Close()
			t.Fatal("replacement root accepted")
		}
	})
}

type cancelContext struct {
	context.Context
	mu           sync.Mutex
	calls, after int
}

func (c *cancelContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}

func (c *cancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func TestKimiCodeLimitsCancellationAndSafetyContract(t *testing.T) {
	state, wire := kimiFixture(t)
	root := t.TempDir()
	installKimi(t, root, "wd_a", kimiSessionID, "/synthetic/project", state, wire)
	if got, err := New(root).Discover(&cancelContext{Context: context.Background(), after: 5}); !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	roots := make([]string, maxRoots+1)
	for i := range roots {
		roots[i] = filepath.Join(t.TempDir(), "missing")
	}
	if got, err := New(roots...).Discover(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("root limit sessions=%#v err=%v", got, err)
	}
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, data []byte) error {
		dir := filepath.Join(root, "sessions", "wd_fixture", kimiSessionID)
		if err := os.MkdirAll(filepath.Join(dir, "agents", "main"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), state, 0o600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "agents", "main", "wire.jsonl"), data, 0o600); err != nil {
			return err
		}
		entry := fmt.Sprintf(`{"sessionId":%q,"sessionDir":%q,"workDir":"/synthetic/project"}`+"\n", kimiSessionID, dir)
		return os.WriteFile(filepath.Join(root, "session_index.jsonl"), []byte(entry), 0o600)
	}, wire)
}

func TestKimiCodeAuthorizationDoesNotCacheBodies(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches slice %s", typ.Field(i).Name)
		}
	}
}
