package copilot

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndOpenJSON(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash")
	if err := os.MkdirAll(filepath.Join(dir, "chatSessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(`{"folder":"file:///workspace/campus-app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"s1","creationDate":1000,"lastMessageDate":2000,"requests":[{"requestId":"r1","message":{"text":"hello"},"timestamp":1000,"response":[{"kind":"toolInvocationSerialized","toolId":"run_in_terminal","toolCallId":"c1","value":"done"},{"kind":"markdownContent","value":"ok"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "chatSessions", "s1.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID != "vscode-copilot:s1" || got[0].Scope.Root != "/workspace/campus-app" {
		t.Fatal(got[0])
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for d := json.NewDecoder(r); ; {
		var e map[string]any
		if err := d.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n < 2 {
		t.Fatalf("events=%d", n)
	}
}

func TestJSONWinsJSONLAndReplacementRequiresRediscovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "h", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"s","requests":[{"requestId":"r","message":{"text":"json"}}]}`
	jp := filepath.Join(dir, "s.json")
	if err := os.WriteFile(jp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	log := `{"kind":0,"v":{"version":3,"sessionId":"s","requests":[{"requestId":"r","message":{"text":"jsonl"}}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	ss, err := a.Discover(context.Background())
	if err != nil || len(ss) != 1 || ss[0].OpaqueRef != jp {
		t.Fatalf("%#v %v", ss, err)
	}
	if err := os.WriteFile(jp, []byte(strings.Replace(body, "json", "changed", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), ss[0]); err == nil {
		t.Fatal("replacement accepted")
	}
}

func TestStrictRequestAndMutationValidation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "h", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"kind":0,"v":{"version":3,"sessionId":"s","requests":[]}}` + "\n" +
		`{"kind":2,"k":["requests"],"v":[{"requestId":"r","message":{"text":"ok"}}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	ss, err := New(root).Discover(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatalf("%#v %v", ss, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"version":3,"sessionId":"bad","requests":[{"message":{"text":"not valid"}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ss, err = New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if s.ID == "vscode-copilot:bad" {
			t.Fatal("missing requestId accepted")
		}
	}
}

func TestMutationSetPushDeleteAcrossMapsAndArrays(t *testing.T) {
	var doc any = map[string]any{"requests": []any{map[string]any{"response": []any{"a", "b"}}}}
	if !mutate(&doc, 1, []any{"requests", float64(0), "requestId"}, "r", nil) {
		t.Fatal("set")
	}
	i := 1
	if !mutate(&doc, 2, []any{"requests", float64(0), "response"}, []any{"x"}, &i) {
		t.Fatal("push")
	}
	if !mutate(&doc, 3, []any{"requests", float64(0), "response", float64(0)}, nil, nil) {
		t.Fatal("delete")
	}
	b, _ := json.Marshal(doc)
	if string(b) != `{"requests":[{"requestId":"r","response":["x","b"]}]}` {
		t.Fatal(string(b))
	}
	if validKeys([]any{1.5}) {
		t.Fatal("fractional index accepted")
	}
	if mutate(&doc, 3, []any{"requests", float64(9)}, nil, nil) {
		t.Fatal("out of range mutation accepted")
	}
}

func TestEventsCountsRecognizedInvalidExactly(t *testing.T) {
	var s sess
	s.Requests = []request{{RequestID: "ok"}}
	s.Requests[0].Message.Text = "user"
	s.Requests[0].Response = []json.RawMessage{
		json.RawMessage(`{"kind":"markdownContent","value":"assistant"}`),
		json.RawMessage(`{"kind":"markdownContent","value":""}`),
		json.RawMessage(`{"kind":"toolInvocationSerialized","toolId":"run","toolCallId":"c","invocationMessage":{"value":"cmd"},"toolSpecificData":{"command":"go test"},"isComplete":true,"value":"ok"}`),
		json.RawMessage(`{"kind":"toolInvocationSerialized","toolId":"","toolCallId":"bad"}`),
		json.RawMessage(`{"kind":"futureKnownShape","value":""}`),
		json.RawMessage(`{`),
	}
	ev, bad := events(s)
	if bad != 3 {
		t.Fatalf("bad=%d events=%#v", bad, ev)
	}
	if len(ev) != 4 {
		t.Fatalf("events=%#v", ev)
	}
	if ev[2]["input"].(map[string]any)["tool_specific_data"].(map[string]any)["command"] != "go test" || ev[3]["result"] != "ok" {
		t.Fatalf("tool events=%#v", ev)
	}
}

func TestChatSessionsWinsEditingAndSameRankConflictIsExcluded(t *testing.T) {
	root := t.TempDir()
	body := func(id, text string) []byte {
		return []byte(`{"version":3,"sessionId":"` + id + `","requests":[{"requestId":"r","message":{"text":"` + text + `"}}]}`)
	}
	write := func(p string, b []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "workspaceStorage", "h", "chatEditingSessions", "s.json"), body("s", "editing"))
	chat := filepath.Join(root, "workspaceStorage", "h", "chatSessions", "s.json")
	write(chat, body("s", "chat"))
	ss, err := New(root).Discover(context.Background())
	if err != nil || len(ss) != 1 || ss[0].OpaqueRef != chat {
		t.Fatalf("%#v %v", ss, err)
	}
	root2 := t.TempDir()
	write(filepath.Join(root2, "workspaceStorage", "h2", "chatSessions", "s.json"), body("s", "other"))
	ss, err = New(root, root2).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 0 {
		t.Fatalf("same-rank cross-root conflict accepted: %#v", ss)
	}
}

func TestJSONLFileReplaysAllMutationsAndRejectsInvalidWithoutPollution(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "h", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"kind":0,"v":{"version":3,"sessionId":"s","requests":[{"requestId":"r","message":{"text":"old"},"response":[]}]}}`,
		`{"kind":1,"k":["requests",0,"message","text"],"v":"new"}`,
		`{"kind":2,"k":["requests",0,"response"],"v":[{"kind":"markdownContent","value":"one"},{"kind":"markdownContent","value":"drop"}]}`,
		`{"kind":3,"k":["requests",0,"response",1]}`,
		`{"kind":1,"k":["requests",1.5,"message"],"v":"poison"}`,
		`{"kind":2,"k":["requests"],"v":"wrong-type"}`,
	}
	p := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	ss, err := a.Discover(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatalf("%#v %v", ss, err)
	}
	if ss[0].MalformedCount != 1 {
		t.Fatalf("malformed=%d", ss[0].MalformedCount)
	}
	r, err := a.Open(context.Background(), ss[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, `"content":"new"`) || !strings.Contains(text, `"content":"one"`) || strings.Contains(text, "drop") || strings.Contains(text, "poison") {
		t.Fatal(text)
	}
}
