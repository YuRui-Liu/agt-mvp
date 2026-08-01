package myflicker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

func writeSession(t *testing.T, root, project, id, body string) string {
	t.Helper()
	path := filepath.Join(root, project, id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverAndOpenStrictJSONL(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "campus-app", "abc123", strings.Join([]string{
		`{"type":"config","config":{"cwd":"/workspace/campus-app"}}`,
		`{"type":"message","role":"user","timestamp":"2026-07-30T01:00:00Z","content":"hello"}`,
		`not json`,
		`{"type":"message","role":"assistant","timestamp":"2026-07-30T01:00:01Z","model":"m","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"call-1","name":"Bash","input":{"command":"go test"}}]}`,
		`{"type":"message","role":"tool","timestamp":"2026-07-30T01:00:02Z","content":[{"type":"tool-result","toolCallId":"call-1","result":{"status":"ok"}}]}`,
	}, "\n")+"\n")

	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.Product != "myflicker" || s.ID != "myflicker:abc123" ||
		s.Scope.Type != source.ScopeProject || s.Scope.Root != "/workspace/campus-app" ||
		s.MessageCount != 3 || s.MalformedCount != 1 {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var events []map[string]any
	dec := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 4 || events[1]["type"] != "message" ||
		events[2]["type"] != "tool_use" || events[3]["type"] != "tool_result" {
		t.Fatalf("events=%#v", events)
	}
}

func TestVersionedFixtureSupportsAssistantStringAndMessageCWD(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join("..", "testdata", "flicker", "session.jsonl")
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	writeSession(t, root, "example-project", "fixture-v1", string(body))
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Scope.Type != source.ScopeProject ||
		got[0].Scope.Root != "/workspace/example-project" ||
		got[0].MessageCount != 4 {
		t.Fatalf("session=%#v", got[0])
	}
	r, err := New(root).Open(context.Background(), got[0])
	if err == nil {
		r.Close()
		t.Fatal("cross-instance fixture session accepted")
	}
	a := New(root)
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
	if strings.Join(kinds, ",") != "message,message,tool_use,tool_result" {
		t.Fatalf("kinds=%v", kinds)
	}
}

func TestCWDConflictAndRelativePathFallBackToSessionCollection(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{
			name: "conflict",
			body: `{"type":"config","config":{"cwd":"/one"}}` + "\n" +
				`{"type":"message","role":"user","cwd":"/two","content":"x"}` + "\n",
		},
		{
			name: "relative",
			body: `{"type":"config","config":{}}` + "\n" +
				`{"type":"message","role":"user","cwd":"relative/path","content":"x"}` + "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeSession(t, root, "p", "s", test.body)
			got, err := New(root).Discover(context.Background())
			if err != nil || len(got) != 1 {
				t.Fatalf("sessions=%#v err=%v", got, err)
			}
			if got[0].Scope.Type != source.ScopeSessionCollection ||
				got[0].Scope.Label != "MyFlicker sessions" ||
				strings.Contains(got[0].Scope.Root, "/") {
				t.Fatalf("scope=%#v", got[0].Scope)
			}
		})
	}
}

func TestRejectsWeakSignatureAndUnsafeFiles(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "p", "weak", `{"type":"message","role":"user","content":"no config"}`+"\n")
	writeSession(t, root, "p", "../not-used", "")
	outside := filepath.Join(t.TempDir(), "evil.jsonl")
	if err := os.WriteFile(outside, []byte(`{"type":"config","config":{"cwd":"/p"}}`+"\n"+`{"type":"message","role":"user","content":"evil"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "p", "evil.jsonl")); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestOpenBindsDiscoveryIdentityAndContent(t *testing.T) {
	root := t.TempDir()
	path := writeSession(t, root, "p", "s1", `{"type":"config","config":{"cwd":"/p"}}`+"\n"+`{"type":"message","role":"user","content":"one"}`+"\n")
	a := New(root)
	sessions, err := a.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatal(sessions, err)
	}
	copySession := sessions[0]
	copySession.Product = "codeflicker"
	if _, err := a.Open(context.Background(), copySession); err == nil {
		t.Fatal("cross-product session accepted")
	}
	copySession = sessions[0]
	copySession.Scope.Root = "/different"
	if _, err := a.Open(context.Background(), copySession); err == nil {
		t.Fatal("modified session metadata accepted")
	}
	if _, err := New(root).Open(context.Background(), sessions[0]); err == nil {
		t.Fatal("cross-instance session accepted")
	}
	if err := os.WriteFile(path, []byte(`{"type":"config","config":{"cwd":"/p"}}`+"\n"+`{"type":"message","role":"user","content":"two"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), sessions[0]); err == nil {
		t.Fatal("modified source accepted")
	}
}

func TestContextAndLimits(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "p", "large", `{"type":"config","config":{"cwd":"/p"}}`+"\n"+`{"type":"message","role":"user","content":"`+strings.Repeat("x", maxLineBytes)+`"}`+"\n")
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Discover(ctx); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
	if _, err := a.Open(ctx, source.Session{}); err != context.Canceled {
		t.Fatalf("err=%v", err)
	}
}

func TestDuplicateSessionIDConflictIsRejected(t *testing.T) {
	root := t.TempDir()
	writeSession(t, root, "a", "same", `{"type":"config","config":{"cwd":"/a"}}`+"\n"+`{"type":"message","role":"user","content":"a"}`+"\n")
	writeSession(t, root, "b", "same", `{"type":"config","config":{"cwd":"/b"}}`+"\n"+`{"type":"message","role":"user","content":"b"}`+"\n")
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
