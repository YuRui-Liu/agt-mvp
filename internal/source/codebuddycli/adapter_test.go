package codebuddycli

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
)

const codeBuddyUUID = "11111111-1111-4111-8111-111111111111"
const codeBuddyUUID2 = "22222222-2222-4222-8222-222222222222"

func installCodeBuddy(t *testing.T, root, project, id string, data []byte) string {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func codeBuddyFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../testdata/codebuddycli/session-v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func codeBuddyEvents(t *testing.T, adapter *Adapter, session source.Session) []map[string]any {
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

func TestCodeBuddyCLIFourTypesDynamicCapabilitiesAndScope(t *testing.T) {
	fixture := codeBuddyFixture(t)
	root := t.TempDir()
	installCodeBuddy(t, root, "synthetic-project", strings.ToUpper(codeBuddyUUID), fixture)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	session := got[0]
	if session.Product != "codebuddy-cli" || session.FormatVersion != "jsonl-v1" || session.MessageCount != 3 || session.MalformedCount != 0 {
		t.Fatalf("session=%#v", session)
	}
	if session.Scope.Type != source.ScopeProject || session.Scope.Label != "project" || filepath.IsAbs(session.Scope.Root) {
		t.Fatalf("scope=%#v", session.Scope)
	}
	for _, capability := range []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning} {
		if !slices.Contains(session.Capabilities, capability) {
			t.Fatalf("capabilities=%#v", session.Capabilities)
		}
	}
	events := codeBuddyEvents(t, a, session)
	var types []string
	for _, event := range events {
		types = append(types, event["type"].(string))
	}
	if !reflect.DeepEqual(types, []string{"message", "message", "tool_use", "tool_result", "message"}) {
		t.Fatalf("types=%#v", types)
	}
	encoded, _ := json.Marshal(events)
	for _, private := range []string{"private-turn-1", "/synthetic/project", codeBuddyUUID, "must not appear", "/forged/project"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private metadata leaked")
		}
	}
}

func TestCompressCWDAndCollisionFallback(t *testing.T) {
	cases := map[string]string{
		"/alpha//beta":        "alpha-beta",
		`C:\alpha\beta`:       "C-alpha-beta",
		`\\server\share\dir`:  "server-share-dir",
		`//server/share/dir`:  "server-share-dir",
		"---/alpha:::beta---": "alpha-beta",
	}
	for input, want := range cases {
		if got := CompressCWD(input); got != want {
			t.Fatalf("CompressCWD(%q)=%q want=%q", input, got, want)
		}
	}
	root := t.TempDir()
	body := strings.Join([]string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}],"cwd":"/synthetic/a-b"}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"two"}],"cwd":"/synthetic/a/b"}`,
	}, "\n") + "\n"
	installCodeBuddy(t, root, "synthetic-a-b", codeBuddyUUID, []byte(body))
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Scope.Type != source.ScopeSessionCollection || got[0].Scope.Root == "" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}

	mismatchRoot := t.TempDir()
	installCodeBuddy(t, mismatchRoot, "different", codeBuddyUUID, []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}],"cwd":"/synthetic/project"}`+"\n"))
	got, err = New(mismatchRoot).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Scope.Type != source.ScopeSessionCollection {
		t.Fatalf("mismatch sessions=%#v err=%v", got, err)
	}

	uncRoot := t.TempDir()
	uncBody := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}],"cwd":"\\\\server\\share\\dir"}` + "\n")
	installCodeBuddy(t, uncRoot, "server-share-dir", codeBuddyUUID, uncBody)
	got, err = New(uncRoot).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Scope.Type != source.ScopeProject || got[0].Scope.Label != "dir" {
		t.Fatalf("UNC sessions=%#v err=%v", got, err)
	}
	backslashUNCScopeRoot := got[0].Scope.Root

	forwardUNCRoot := t.TempDir()
	forwardUNCBody := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}],"cwd":"//server/share/dir"}` + "\n")
	installCodeBuddy(t, forwardUNCRoot, "server-share-dir", codeBuddyUUID, forwardUNCBody)
	got, err = New(forwardUNCRoot).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Scope.Type != source.ScopeProject || got[0].Scope.Label != "dir" {
		t.Fatalf("forward UNC sessions=%#v err=%v", got, err)
	}
	if got[0].Scope.Root == "" || got[0].Scope.Root != backslashUNCScopeRoot {
		t.Fatalf("UNC representations did not normalize stably: %q != %q", got[0].Scope.Root, backslashUNCScopeRoot)
	}

	for _, invalid := range []string{`//server`, `\\server`, `//server//dir`, `\\server\\dir`} {
		t.Run("invalid-unc-"+CompressCWD(invalid), func(t *testing.T) {
			invalidRoot := t.TempDir()
			body, err := json.Marshal(map[string]any{
				"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "one"}}, "cwd": invalid,
			})
			if err != nil {
				t.Fatal(err)
			}
			installCodeBuddy(t, invalidRoot, CompressCWD(invalid), codeBuddyUUID, append(body, '\n'))
			sessions, err := New(invalidRoot).Discover(context.Background())
			if err != nil || len(sessions) != 1 || sessions[0].Scope.Type != source.ScopeSessionCollection {
				t.Fatalf("sessions=%#v err=%v", sessions, err)
			}
		})
	}
}

func TestCollectionFallbackGroupsProjectWithoutSessionUUID(t *testing.T) {
	root := t.TempDir()
	installCodeBuddy(t, root, "same-project", codeBuddyUUID, []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":"missing cwd"}]}`+"\n"))
	conflicting := strings.Join([]string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"first"}],"cwd":"/same/project"}`,
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"second"}],"cwd":"/same-project"}`,
	}, "\n") + "\n"
	installCodeBuddy(t, root, "same-project", codeBuddyUUID2, []byte(conflicting))

	sessions, err := New(root).Discover(context.Background())
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	if sessions[0].Scope.Type != source.ScopeSessionCollection || sessions[1].Scope.Type != source.ScopeSessionCollection {
		t.Fatalf("scopes=%#v %#v", sessions[0].Scope, sessions[1].Scope)
	}
	if sessions[0].Scope.Root != sessions[1].Scope.Root {
		t.Fatalf("collection roots differ: %q != %q", sessions[0].Scope.Root, sessions[1].Scope.Root)
	}
	if sessions[0].ID == sessions[1].ID {
		t.Fatalf("session IDs collide: %q", sessions[0].ID)
	}
}

func TestInvalidRecordCannotChooseCWDAndUnknownSameIDCannotReplace(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		`{"id":"same","type":"message","role":"user","content":[{"type":"input_text","text":"kept"}],"cwd":"/synthetic/project"}`,
		`{"id":"same","type":"custom-title","content":"replace","cwd":"/forged/project"}`,
		`{"type":"message","role":"system","content":[{"type":"input_text","text":"invalid"}],"cwd":"/forged/project"}`,
		`{"type":"topic","content":"ignored"}`,
		`{"type":"file-history-snapshot","snapshot":{"path":"/must/not/open"}}`,
		`{bad`,
	}, "\n") + "\n"
	installCodeBuddy(t, root, "synthetic-project", codeBuddyUUID, []byte(body))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].MalformedCount != 2 || got[0].Scope.Type != source.ScopeProject {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	events := codeBuddyEvents(t, a, got[0])
	if len(events) != 1 || events[0]["content"] != "kept" {
		t.Fatalf("events=%#v", events)
	}
}

func TestToolPairingIsStrictAtomicAndIncompleteIsSafeError(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]}`,
		`{"type":"function_call","callId":"c1","name":"one","arguments":{}}`,
		`{"type":"function_call","callId":"c2","name":"two","arguments":{}}`,
		`{"type":"function_call_result","callId":"orphan","name":"one","status":"completed","output":"bad"}`,
		`{"type":"function_call","callId":"c1","name":"duplicate","arguments":{}}`,
		`{"type":"function_call_result","callId":"c1","name":"mismatch","status":"completed","output":"bad"}`,
		`{"type":"function_call_result","callId":"c1","name":"one","status":"completed","output":"ok"}`,
		`{"type":"function_call_result","callId":"c2","name":"two","status":"incomplete","output":"partial","providerData":{"toolResult":{"error":"interrupted"}}}`,
	}, "\n") + "\n"
	installCodeBuddy(t, root, "collection", codeBuddyUUID, []byte(body))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].MalformedCount != 3 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	events := codeBuddyEvents(t, a, got[0])
	if len(events) != 5 {
		t.Fatalf("events=%#v", events)
	}
	if events[4]["type"] != "tool_result" || events[4]["is_error"] != true {
		t.Fatalf("incomplete result=%#v", events[4])
	}
}

func TestToolResultStatusProviderErrorTypesAndAtomicRollback(t *testing.T) {
	t.Run("provider-error-types", func(t *testing.T) {
		cases := []struct {
			name      string
			provider  any
			wantError bool
			wantValid bool
		}{
			{name: "missing", provider: nil, wantValid: true},
			{name: "null", provider: map[string]any{"toolResult": map[string]any{"error": nil}}, wantValid: true},
			{name: "false", provider: map[string]any{"toolResult": map[string]any{"error": false}}, wantValid: true},
			{name: "true", provider: map[string]any{"toolResult": map[string]any{"error": true}}, wantError: true, wantValid: true},
			{name: "empty-string", provider: map[string]any{"toolResult": map[string]any{"error": ""}}, wantValid: true},
			{name: "string", provider: map[string]any{"toolResult": map[string]any{"error": "failed"}}, wantError: true, wantValid: true},
			{name: "object", provider: map[string]any{"toolResult": map[string]any{"error": map[string]any{}}}},
			{name: "number", provider: map[string]any{"toolResult": map[string]any{"error": float64(1)}}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				gotError, gotValid := resultError(tc.provider, "completed")
				if gotError != tc.wantError || gotValid != tc.wantValid {
					t.Fatalf("resultError=(%v,%v) want=(%v,%v)", gotError, gotValid, tc.wantError, tc.wantValid)
				}
			})
		}
		if gotError, gotValid := resultError(nil, "incomplete"); !gotError || !gotValid {
			t.Fatalf("incomplete resultError=(%v,%v)", gotError, gotValid)
		}
	})

	t.Run("recognized-invalid-does-not-resolve-call", func(t *testing.T) {
		root := t.TempDir()
		body := strings.Join([]string{
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"start"}]}`,
			`{"type":"function_call","callId":"c1","name":"one","arguments":{}}`,
			`{"type":"function_call_result","callId":"c1","name":"one","status":"success","output":"alias"}`,
			`{"type":"function_call_result","callId":"c1","name":"one","status":"in_progress","output":"not-result"}`,
			`{"type":"function_call_result","callId":"c1","name":"one","status":"completed","output":"object","providerData":{"toolResult":{"error":{}}}}`,
			`{"type":"function_call_result","callId":"c1","name":"one","status":"completed","output":"number","providerData":{"toolResult":{"error":1}}}`,
			`{"type":"function_call_result","callId":"c1","name":"one","status":"completed","output":"kept"}`,
		}, "\n") + "\n"
		installCodeBuddy(t, root, "collection", codeBuddyUUID, []byte(body))
		a := New(root)
		sessions, err := a.Discover(context.Background())
		if err != nil || len(sessions) != 1 || sessions[0].MalformedCount != 4 {
			t.Fatalf("sessions=%#v err=%v", sessions, err)
		}
		events := codeBuddyEvents(t, a, sessions[0])
		if len(events) != 3 || events[2]["type"] != "tool_result" || events[2]["result"] != "kept" {
			t.Fatalf("events=%#v", events)
		}
	})
}

func TestOnlyDirectUUIDJSONLFilesAreScanned(t *testing.T) {
	fixture := codeBuddyFixture(t)
	root := t.TempDir()
	installCodeBuddy(t, root, "synthetic-project", codeBuddyUUID, fixture)
	write := func(rel string, data []byte) {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("synthetic-project", codeBuddyUUID, "tool-results", "result.jsonl"), fixture)
	write(filepath.Join("synthetic-project", "rollback.ndjson"), fixture)
	write(filepath.Join("synthetic-project", "meta.jsonl"), fixture)
	write(filepath.Join("synthetic-project", "session.vscdb"), fixture)
	write(filepath.Join("cache", codeBuddyUUID+".jsonl"), fixture)
	write(filepath.Join("plugins", codeBuddyUUID+".jsonl"), fixture)
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestReservedProjectDirectoriesAreExcludedCaseInsensitively(t *testing.T) {
	fixture := codeBuddyFixture(t)
	for _, project := range []string{"rollback", "ROLLBACK", "Rollback", "meta", "META", "MeTa"} {
		t.Run(project, func(t *testing.T) {
			root := t.TempDir()
			installCodeBuddy(t, root, project, codeBuddyUUID, fixture)
			sessions, err := New(root).Discover(context.Background())
			if err != nil || len(sessions) != 0 {
				t.Fatalf("reserved directory was scanned: sessions=%#v err=%v", sessions, err)
			}
		})
	}
}

func TestStableIDIncludesRootProjectAndStem(t *testing.T) {
	fixture := codeBuddyFixture(t)
	first, second := t.TempDir(), t.TempDir()
	installCodeBuddy(t, first, "synthetic-project", codeBuddyUUID, fixture)
	installCodeBuddy(t, second, "synthetic-project", codeBuddyUUID, fixture)
	installCodeBuddy(t, first, "synthetic-other", codeBuddyUUID, bytes.ReplaceAll(fixture, []byte(`/synthetic/project`), []byte(`/synthetic/other`)))
	got, err := New(first, first, second).Discover(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	ids := map[string]bool{}
	for _, session := range got {
		if ids[session.ID] {
			t.Fatalf("duplicate ID %q", session.ID)
		}
		ids[session.ID] = true
	}
}

func TestCodeBuddyCompositeAuthorizationAndSameByteReplacement(t *testing.T) {
	fixture := codeBuddyFixture(t)
	root := t.TempDir()
	file := installCodeBuddy(t, root, "synthetic-project", codeBuddyUUID, fixture)
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
		t.Fatal("cross-instance accepted")
	}
	if err := os.WriteFile(file, append(fixture, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("tamper accepted")
	}

	if err := os.WriteFile(file, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	a = New(root)
	got, err = a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	if err := os.Rename(file, file+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("same-byte replacement accepted")
	}
}

func TestCodeBuddyOpenRejectsRootSwapAndSymlinkSource(t *testing.T) {
	fixture := codeBuddyFixture(t)
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	installCodeBuddy(t, root, "synthetic-project", codeBuddyUUID, fixture)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if err := os.Rename(root, filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	installCodeBuddy(t, root, "synthetic-project", codeBuddyUUID, fixture)
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("replacement root accepted")
	}

	outside := t.TempDir()
	outsideFile := installCodeBuddy(t, outside, "synthetic-project", codeBuddyUUID, fixture)
	linkedRoot := t.TempDir()
	project := filepath.Join(linkedRoot, "synthetic-project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideFile, filepath.Join(project, codeBuddyUUID+".jsonl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if sessions, err := New(linkedRoot).Discover(context.Background()); err != nil || len(sessions) != 0 {
		t.Fatalf("symlink sessions=%#v err=%v", sessions, err)
	}
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

func TestCodeBuddyLimitsCancellationAndSafetyContract(t *testing.T) {
	fixture := codeBuddyFixture(t)
	root := t.TempDir()
	installCodeBuddy(t, root, "synthetic-project", codeBuddyUUID, fixture)
	if got, err := New(root).Discover(&cancelContext{Context: context.Background(), after: 5}); !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	roots := make([]string, maxRoots+1)
	for i := range roots {
		roots[i] = filepath.Join(t.TempDir(), "missing")
	}
	if got, err := New(roots...).Discover(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, data []byte) error {
		dir := filepath.Join(root, "synthetic-project")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, codeBuddyUUID+".jsonl"), data, 0o600)
	}, fixture)

	limitRoot := t.TempDir()
	var body strings.Builder
	for i := 0; i <= maxSessionRecords; i++ {
		fmt.Fprintf(&body, `{"type":"message","role":"user","content":[{"type":"input_text","text":"%d"}]}`+"\n", i)
	}
	installCodeBuddy(t, limitRoot, "collection", codeBuddyUUID, []byte(body.String()))
	if got, err := New(limitRoot).Discover(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("record limit sessions=%#v err=%v", got, err)
	}
}

func TestCodeBuddyAuthorizationDoesNotCacheBody(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches slice %s", typ.Field(i).Name)
		}
	}
}
