package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

const syntheticResponder = `"responderUsername":"GitHub Copilot"`
const syntheticAgent = `"agent":{"id":"github.copilot.default","name":"GitHubCopilot","extensionId":{"value":"GitHub.copilot-chat","_lower":"github.copilot-chat"},"extensionPublisherId":"GitHub"}`

func TestDiscoverRequiresVerifiedCopilotProvenance(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	verified := `{"version":3,"sessionId":"verified","responderUsername":"GitHub Copilot","requests":[{"requestId":"r1","message":{"text":"synthetic user message"},"agent":{"id":"github.copilot.default","name":"GitHubCopilot","extensionId":{"value":"GitHub.copilot-chat","_lower":"github.copilot-chat"},"extensionPublisherId":"GitHub"},"response":[{"kind":"markdownContent","value":"synthetic assistant message"}]}]}`
	generic := `{"version":3,"sessionId":"generic","responderUsername":"VS Code Chat","requests":[{"requestId":"r2","message":{"text":"synthetic generic message"},"response":[{"kind":"markdownContent","value":"synthetic generic response"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "verified.json"), []byte(verified), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generic.json"), []byte(generic), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "vscode-copilot:verified" {
		t.Fatalf("sessions=%d verified=%v", len(got), len(got) == 1 && got[0].ID == "vscode-copilot:verified")
	}
}

func TestCopilotProvenanceRejectsAmbiguousUnknownAndMixedSessions(t *testing.T) {
	validAgent := chatAgent{ID: "github.copilot.default", ExtensionID: extensionIdentifier{Value: "GitHub.copilot-chat", Lower: "github.copilot-chat"}, ExtensionPublisherID: "GitHub"}
	tests := []struct {
		name   string
		mutate func(*sess)
		want   bool
	}{
		{name: "verified default", want: true},
		{name: "verified edits agent", mutate: func(s *sess) { s.Requests[0].Agent.ID = "github.copilot.editsAgent" }, want: true},
		{name: "missing responder", mutate: func(s *sess) { s.Responder = "" }},
		{name: "missing agent", mutate: func(s *sess) { s.Requests[0].Agent = chatAgent{} }},
		{name: "unknown participant", mutate: func(s *sess) { s.Requests[0].Agent.ID = "third.party.agent" }},
		{name: "unknown extension", mutate: func(s *sess) { s.Requests[0].Agent.ExtensionID.Value = "example.copilot-chat" }},
		{name: "conflicting normalized extension", mutate: func(s *sess) { s.Requests[0].Agent.ExtensionID.Lower = "example.copilot-chat" }},
		{name: "mixed requests", mutate: func(s *sess) {
			s.Requests = append(s.Requests, request{Agent: chatAgent{ID: validAgent.ID, ExtensionID: extensionIdentifier{Value: "example.copilot-chat"}, ExtensionPublisherID: "GitHub"}})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := sess{Responder: "GitHub Copilot", Requests: []request{{Agent: validAgent}}}
			if test.mutate != nil {
				test.mutate(&session)
			}
			if got := hasCopilotProvenance(session); got != test.want {
				t.Fatalf("verified=%v want=%v", got, test.want)
			}
		})
	}
}

func TestManifestCopilotParticipantIDsAreAccepted(t *testing.T) {
	ids := []string{
		"github.copilot.default",
		"github.copilot.editingSession",
		"github.copilot.editingSessionEditor",
		"github.copilot.editsAgent",
		"github.copilot.notebook",
		"github.copilot.notebookEditorAgent",
		"github.copilot.vscode",
		"github.copilot.terminal",
		"github.copilot.terminalPanel",
	}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			session := sess{Responder: "GitHub Copilot", Requests: []request{{Agent: chatAgent{
				ID:                   id,
				ExtensionID:          extensionIdentifier{Value: "GitHub.copilot-chat", Lower: "github.copilot-chat"},
				ExtensionPublisherID: "GitHub",
			}}}}
			if !hasCopilotProvenance(session) {
				t.Fatal("manifest participant rejected")
			}
		})
	}
}

func TestDiscoverRejectsMisleadingCopilotPathTitleAndBody(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "github-copilot", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"misleading","customTitle":"GitHub Copilot","responderUsername":"VS Code Chat","requests":[{"requestId":"r1","message":{"text":"GitHub Copilot in ordinary body"},"response":[{"kind":"markdownContent","value":"Copilot text is not provenance"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "github-copilot.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
}

func TestDiscoverMessageOnlyCapabilitiesAreEvidenceBased(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"message-only",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic user message"},` + syntheticAgent + `,"response":[{"kind":"markdownContent","value":"synthetic assistant message"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "message-only.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
	want := []source.Capability{source.CapabilityMessages}
	if !reflect.DeepEqual(got[0].Capabilities, want) {
		t.Fatalf("capabilities=%v", got[0].Capabilities)
	}
}

func TestDiscoverReasoningCapabilityRequiresThinkingEvent(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"reasoning",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic user message"},` + syntheticAgent + `,"response":[{"kind":"thinking","value":"synthetic reasoning"},{"kind":"markdownContent","value":"synthetic answer"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "reasoning.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
	want := []source.Capability{source.CapabilityMessages, source.CapabilityReasoning}
	if !reflect.DeepEqual(got[0].Capabilities, want) {
		t.Fatalf("capabilities=%v", got[0].Capabilities)
	}
}

func TestDiscoverAndOpenJSON(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash")
	if err := os.MkdirAll(filepath.Join(dir, "chatSessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace.json"), []byte(`{"folder":"file:///workspace/campus-app"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"s1",` + syntheticResponder + `,"creationDate":1000,"lastMessageDate":2000,"requests":[{"requestId":"r1","message":{"text":"hello"},` + syntheticAgent + `,"timestamp":1000,"response":[{"kind":"toolInvocationSerialized","toolId":"run_in_terminal","toolCallId":"c1","value":"done"},{"kind":"markdownContent","value":"ok"}]}]}`
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
	body := `{"version":3,"sessionId":"s",` + syntheticResponder + `,"requests":[{"requestId":"r","message":{"text":"json"},` + syntheticAgent + `}]}`
	jp := filepath.Join(dir, "s.json")
	if err := os.WriteFile(jp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	log := `{"kind":0,"v":{"version":3,"sessionId":"s",` + syntheticResponder + `,"requests":[{"requestId":"r","message":{"text":"jsonl"},` + syntheticAgent + `}]}}` + "\n"
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

func TestOpenRejectsMetadataAndCrossInstanceForgery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"authorized",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic message"},` + syntheticAgent + `}]}`
	if err := os.WriteFile(filepath.Join(dir, "authorized.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
	tampered := got[0]
	tampered.Scope.Label = "forged label"
	if r, err := a.Open(context.Background(), tampered); err == nil {
		r.Close()
		t.Fatal("tampered metadata accepted")
	}
	if r, err := New(root).Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("cross-instance reference accepted")
	}
}

func TestOpenRejectsReplacedSessionAncestorWithIdenticalContent(t *testing.T) {
	root := t.TempDir()
	hashDir := filepath.Join(root, "workspaceStorage", "hash")
	sessionDir := filepath.Join(hashDir, "chatSessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":3,"sessionId":"path-identity",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic message"},` + syntheticAgent + `}]}`)
	path := filepath.Join(sessionDir, "path-identity.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
	if err := os.Rename(hashDir, hashDir+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); !errors.Is(err, errChangedSource) {
		if r != nil {
			r.Close()
		}
		t.Fatalf("replaced ancestor err=%v", err)
	} else if r != nil {
		r.Close()
		t.Fatal("replaced ancestor returned reader")
	}
}

func TestDiscoverBoundRootStaysOnOriginalAfterAncestorSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics differ on Windows")
	}
	base := t.TempDir()
	root := filepath.Join(base, "user-data")
	replacement := filepath.Join(base, "replacement")
	write := func(tree, id string) {
		t.Helper()
		dir := filepath.Join(tree, "workspaceStorage", "hash", "chatSessions")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"version":3,"sessionId":"` + id + `",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic message"},` + syntheticAgent + `}]}`
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(root, "original")
	write(replacement, "replacement")
	a := New(root)
	a.afterBind = func(string) {
		if err := os.Rename(root, root+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, root); err != nil {
			t.Fatal(err)
		}
	}
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "vscode-copilot:original" {
		t.Fatalf("sessions=%d original=%v err=%v", len(got), len(got) == 1 && got[0].ID == "vscode-copilot:original", err)
	}
	if r, err := a.Open(context.Background(), got[0]); !errors.Is(err, errChangedSource) {
		if r != nil {
			r.Close()
		}
		t.Fatalf("replacement root err=%v", err)
	} else if r != nil {
		r.Close()
		t.Fatal("replacement root returned reader")
	}
}

func TestDiscoverRejectsSymlinkRootAndSessionFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require additional privileges on Windows")
	}
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "linked-root")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := New(linkRoot).Discover(context.Background()); !errors.Is(err, errInvalidRoot) {
		t.Fatalf("symlink root err=%v", err)
	}

	root := filepath.Join(base, "safe-root")
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(base, "outside.json")
	body := `{"version":3,"sessionId":"outside",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic message"},` + syntheticAgent + `}]}`
	if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "github-copilot.json")); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
}

func TestExplicitRootsAndCancellation(t *testing.T) {
	root := t.TempDir()
	a := New(root)
	if len(a.roots) != 1 || a.roots[0] != filepath.Clean(root) {
		t.Fatalf("explicit roots=%d", len(a.roots))
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Discover(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("discover cancellation err=%v", err)
	}
}

func TestDiscoverCancellationBeforeAuthorizationCommitFailsClosed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"version":3,"sessionId":"cancel-window",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic message"},` + syntheticAgent + `}]}`
	if err := os.WriteFile(filepath.Join(dir, "cancel-window.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	initial, err := a.Discover(context.Background())
	if err != nil || len(initial) != 1 {
		t.Fatalf("initial sessions=%d err=%v", len(initial), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.beforeCommit = cancel
	got, err := a.Discover(ctx)
	if !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("canceled sessions=%d err=%v", len(got), err)
	}
	if r, err := a.Open(context.Background(), initial[0]); !errors.Is(err, errUnknownReference) {
		if r != nil {
			r.Close()
		}
		t.Fatalf("canceled discovery retained authorization: %v", err)
	}
}

func TestReplayAndEventLimitsCancellationAndMalformedTail(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := replay(canceled, []byte(`{"kind":0,"v":{}}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled replay err=%v", err)
	}
	overRecords := strings.Repeat("{}\n", maxSessionRecords+1)
	if _, _, err := replay(context.Background(), []byte(overRecords)); !errors.Is(err, errScanLimit) {
		t.Fatalf("record limit err=%v", err)
	}
	var limited sess
	limited.Requests = []request{{RequestID: "r", Agent: chatAgent{}}}
	limited.Requests[0].Message.Text = "synthetic user message"
	item := json.RawMessage(`{"kind":"markdownContent","value":"synthetic assistant message"}`)
	limited.Requests[0].Response = make([]json.RawMessage, maxSessionEvents+1)
	for i := range limited.Requests[0].Response {
		limited.Requests[0].Response[i] = item
	}
	if _, _, ok := eventsContext(context.Background(), limited); ok {
		t.Fatal("event limit accepted")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"kind":0,"v":{"version":3,"sessionId":"tail",` + syntheticResponder + `,"requests":[{"requestId":"r1","message":{"text":"synthetic message"},` + syntheticAgent + `}]}}` + "\n{" + "\n"
	if err := os.WriteFile(filepath.Join(dir, "tail.jsonl"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].MalformedCount != 1 {
		t.Fatalf("sessions=%d malformed=%v err=%v", len(got), len(got) == 1 && got[0].MalformedCount == 1, err)
	}
}

func TestDiscoverCountsNonCandidateDirectoryEntriesAgainstLimit(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "hash", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxSessionFiles; i++ {
		name := filepath.Join(dir, fmt.Sprintf("ignored-%04d.txt", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(root).Discover(context.Background())
	if !errors.Is(err, errScanLimit) || len(got) != 0 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
}

func TestStrictRequestAndMutationValidation(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspaceStorage", "h", "chatSessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := `{"kind":0,"v":{"version":3,"sessionId":"s",` + syntheticResponder + `,"requests":[]}}` + "\n" +
		`{"kind":2,"k":["requests"],"v":[{"requestId":"r","message":{"text":"ok"},` + syntheticAgent + `}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	ss, err := New(root).Discover(context.Background())
	if err != nil || len(ss) != 1 {
		t.Fatalf("%#v %v", ss, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte(`{"version":3,"sessionId":"bad",`+syntheticResponder+`,"requests":[{"message":{"text":"not valid"},`+syntheticAgent+`} ]}`), 0o600); err != nil {
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
	tooDeep := make([]any, 65)
	for i := range tooDeep {
		tooDeep[i] = "nested"
	}
	if validKeys(tooDeep) {
		t.Fatal("over-deep mutation path accepted")
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
		return []byte(`{"version":3,"sessionId":"` + id + `",` + syntheticResponder + `,"requests":[{"requestId":"r","message":{"text":"` + text + `"},` + syntheticAgent + `}]}`)
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
		`{"kind":0,"v":{"version":3,"sessionId":"s",` + syntheticResponder + `,"requests":[{"requestId":"r","message":{"text":"old"},` + syntheticAgent + `,"response":[]}]}}`,
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
