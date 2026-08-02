package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
)

type cancelAfterContext struct {
	context.Context
	mu    sync.Mutex
	calls int
	after int
}

func (c *cancelAfterContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}

func (c *cancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func TestQwenCodeContract(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded-demo", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-a.jsonl")
	body := "{\"uuid\":\"u1\",\"sessionId\":\"session-a\",\"timestamp\":\"2026-05-15T10:00:00Z\",\"type\":\"user\",\"cwd\":\"/workspace/demo\",\"message\":{\"role\":\"user\",\"parts\":[{\"text\":\"hello\"}]}}\n" +
		"{\"uuid\":\"u2\",\"parentUuid\":\"u1\",\"sessionId\":\"session-a\",\"timestamp\":\"2026-05-15T10:00:01Z\",\"type\":\"assistant\",\"cwd\":\"/workspace/demo\",\"model\":\"qwen\",\"message\":{\"role\":\"model\",\"parts\":[{\"text\":\"thinking\",\"thought\":true},{\"functionCall\":{\"id\":\"c1\",\"name\":\"read_file\",\"args\":{\"path\":\"demo.go\"}}}]}}\n" +
		"{\"uuid\":\"u3\",\"parentUuid\":\"u2\",\"sessionId\":\"session-a\",\"timestamp\":\"2026-05-15T10:00:02Z\",\"type\":\"user\",\"cwd\":\"/workspace/demo\",\"message\":{\"role\":\"user\",\"parts\":[{\"functionResponse\":{\"id\":\"c1\",\"name\":\"read_file\",\"response\":{\"output\":\"ok\"}}}]}}\n" +
		"{\"uuid\":\"u4\",\"parentUuid\":\"u3\",\"sessionId\":\"session-a\",\"timestamp\":\"2026-05-15T10:00:03Z\",\"type\":\"assistant\",\"cwd\":\"/workspace/demo\",\"model\":\"qwen\",\"message\":{\"role\":\"model\",\"parts\":[{\"text\":\"done\"}]}}\n{bad\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.ID != "qwen-code:encoded-demo:session-a" || s.MessageCount != 3 || s.MalformedCount != 1 || s.Scope.Root != "/workspace/demo" {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	d, n := json.NewDecoder(r), 0
	for {
		var e any
		if err := d.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 5 {
		t.Fatalf("events=%d", n)
	}
	if _, err := New(root).Open(context.Background(), s); err == nil {
		t.Fatal("cross")
	}
	if err := os.WriteFile(path, []byte(body+" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), s); err == nil {
		t.Fatal("stale")
	}
}
func TestRejectsMultipleRoots(t *testing.T) {
	if _, err := New(t.TempDir(), t.TempDir()).Discover(context.Background()); err == nil {
		t.Fatal("multiple roots accepted")
	}
}

func TestRejectsSymlinkedRootAncestor(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(target, "projects")
	dir := filepath.Join(root, "encoded", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(`{"sessionId":"s","type":"user","message":{"role":"user","parts":[{"text":"ok"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, err := New(filepath.Join(linked, "projects")).Discover(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestOpenRejectsReplacedRootDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	install := func() {
		dir := filepath.Join(root, "encoded", "chats")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"sessionId":"s","timestamp":"2026-01-01T00:00:00Z","type":"user","message":{"role":"user","parts":[{"text":"same bytes"}]}}` + "\n")
		if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	install()
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if err := os.Rename(root, filepath.Join(base, "original-projects")); err != nil {
		t.Fatal(err)
	}
	install()
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("replacement root accepted")
	}
}
func TestVersionedFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/qwen/v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "fixture-project", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-fixture.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].FormatVersion != "chat-jsonl-v1" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
func TestSafetyContract(t *testing.T) {
	data := adaptertest.ReadFixture(t, "../testdata/qwen/v1.jsonl")
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, b []byte) error {
		d := filepath.Join(root, "fixture-project", "chats")
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "session-fixture.jsonl"), b, 0o600)
	}, data)
}
func TestAuthorizationDoesNotStoreOutput(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches slice field %s", typ.Field(i).Name)
		}
	}
}

func TestOpenRejectsForgedMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"sessionId":"s","type":"user","message":{"role":"user","parts":[{"text":"ok"}]}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	forged := got[0]
	forged.MalformedCount++
	if r, err := a.Open(context.Background(), forged); err == nil {
		r.Close()
		t.Fatal("forged metadata accepted")
	}
}

func TestSameStemAcrossProjectsIsDistinctAndFallbackStable(t *testing.T) {
	root := t.TempDir()
	body := func(id, text string) []byte {
		return []byte("{\"sessionId\":\"" + id + "\",\"type\":\"user\",\"message\":{\"role\":\"user\",\"parts\":[{\"text\":\"" + text + "\"}]}}\n")
	}
	for _, project := range []string{"project-a", "project-b"} {
		dir := filepath.Join(root, project, "chats")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "same.jsonl"), body("same", "x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	keys := map[string]string{}
	for _, s := range got {
		keys[s.ID] = s.Scope.Root
	}
	if keys["qwen-code:project-a:same"] == "" || keys["qwen-code:project-b:same"] == "" {
		t.Fatalf("keys=%#v", keys)
	}
	p := filepath.Join(root, "project-a", "chats", "same.jsonl")
	if err := os.WriteFile(p, body("same", "appended"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if old := keys[s.ID]; old != "" && old != s.Scope.Root {
			t.Fatalf("scope changed %#v", s)
		}
	}
}

func TestCapabilitiesReflectObservedEvents(t *testing.T) {
	root := t.TempDir()
	write := func(stem, lines string) {
		dir := filepath.Join(root, "encoded-"+stem, "chats")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, stem+".jsonl"), []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("message", `{"sessionId":"message","type":"user","message":{"role":"user","parts":[{"text":"hello"}]}}`+"\n")
	write("tool", strings.Join([]string{
		`{"sessionId":"tool","type":"user","message":{"role":"user","parts":[{"text":"hello"}]}}`,
		`{"sessionId":"tool","type":"assistant","message":{"role":"model","parts":[{"functionCall":{"id":"c1","name":"lookup","args":{}}}]}}`,
		`{"sessionId":"tool","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"lookup","response":{}}}]}}`,
	}, "\n")+"\n")
	write("reasoning", strings.Join([]string{
		`{"sessionId":"reasoning","type":"user","message":{"role":"user","parts":[{"text":"hello"}]}}`,
		`{"sessionId":"reasoning","type":"assistant","message":{"role":"model","parts":[{"thought":"explicit"}]}}`,
	}, "\n")+"\n")
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	wants := map[string][]source.Capability{
		"qwen-code:encoded-message:message":     {source.CapabilityMessages},
		"qwen-code:encoded-tool:tool":           {source.CapabilityMessages, source.CapabilityTools},
		"qwen-code:encoded-reasoning:reasoning": {source.CapabilityMessages, source.CapabilityReasoning},
	}
	for _, s := range got {
		if !reflect.DeepEqual(s.Capabilities, wants[s.ID]) {
			t.Fatalf("id=%q capabilities=%#v want=%#v", s.ID, s.Capabilities, wants[s.ID])
		}
	}
}

func TestCompositeContentAndMetadataClassification(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"sessionId":"composite","type":"user","cwd":"/synthetic/project","message":{"role":"user","parts":[{"text":"ask"}]}}`,
		`{"sessionId":"composite","type":"assistant","message":{"role":"model","parts":[{"thought":"explicit thought"},{"functionCall":{"id":"call-1","name":"lookup","args":{"query":"sample"}}}]}}`,
		`{"sessionId":"composite","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"call-1","response":{"value":"result"}}}]}}`,
		`{"sessionId":"composite","type":"assistant","message":{"role":"model","content":[{"type":"thinking","thinking":"second thought"},{"type":"text","text":"answer"}]}}`,
		`{"sessionId":"forged-metadata-id","type":"system","cwd":"/forged/metadata","systemPayload":{"path":"/must/not/be-opened"}}`,
		`{"sessionId":"composite","type":"snapshot","snapshot":{"path":"/must/not-be-opened"}}`,
		`{"sessionId":"composite","type":"uiEvent","uiEvent":{"name":"render"}}`,
		`{"sessionId":"composite","type":"telemetry","telemetry":{"name":"metric"}}`,
		`{"sessionId":"composite","type":"futureMetadata","payload":{"ok":true}}`,
		`{"sessionId":"composite","type":"assistant","message":{"role":"model","parts":[{"functionCall":{"name":"missing-id"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "composite.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].MalformedCount != 1 {
		t.Fatalf("session=%#v", got[0])
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var types []string
	dec := json.NewDecoder(r)
	for {
		var e map[string]any
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		types = append(types, e["type"].(string))
	}
	want := []string{"message", "message", "tool_use", "tool_result", "message", "message"}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("types=%#v", types)
	}
}

func TestRecognizedInvalidEnvelopeCountsMalformedButMetadataDoesNot(t *testing.T) {
	_, _, _, malformed, _, _, ok := parse(context.Background(), []byte(
		`{"sessionId":"bad","type":"user","message":{"role":"user","parts":[{"text":"valid"}]}}`+"\n"+
			`{"sessionId":"bad","type":"assistant","message":{"role":"user","parts":[{"text":"wrong role"}]}}`+"\n"+
			`{"sessionId":"bad","type":"system","systemPayload":{}}`+"\n"+
			`{"sessionId":"bad","type":"futureMetadata","payload":{}}`+"\n"))
	if !ok || malformed != 1 {
		t.Fatalf("ok=%v malformed=%d", ok, malformed)
	}
}

func TestToolResponsesRequireMatchingOutstandingCall(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"sessionId":"pairing","type":"user","message":{"role":"user","parts":[{"text":"ask"}]}}`,
		`{"sessionId":"pairing","type":"assistant","message":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"lookup","args":{}}}]}}`,
		`{"sessionId":"pairing","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"orphan","name":"lookup","response":{}}}]}}`,
		`{"sessionId":"pairing","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"other","response":{}}}]}}`,
		`{"sessionId":"pairing","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"lookup","response":{"ok":true}}}]}}`,
		`{"sessionId":"pairing","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"lookup","response":{"ok":true}}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "pairing.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].MalformedCount != 3 {
		t.Fatalf("session=%#v", got[0])
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var toolResults int
	dec := json.NewDecoder(r)
	for {
		var e map[string]any
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if e["type"] == "tool_result" {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Fatalf("tool results=%d", toolResults)
	}
}

func TestCompositeBlockValidityRules(t *testing.T) {
	_, events, _, malformed, _, _, ok := parse(context.Background(), []byte(strings.Join([]string{
		`{"sessionId":"blocks","type":"user","message":{"role":"user","parts":[{"text":"valid"}]}}`,
		`{"sessionId":"blocks","type":"assistant","message":{"role":"model","parts":[]}}`,
		`{"sessionId":"blocks","type":"assistant","message":{"role":"model","parts":[{"type":"text"}]}}`,
		`{"sessionId":"blocks","type":"assistant","message":{"role":"model","parts":[{"thought":false,"text":"ordinary text"}]}}`,
		`{"sessionId":"blocks","type":"assistant","message":{"role":"model","parts":[{"futureBlock":{"value":true}}]}}`,
	}, "\n")+"\n"))
	if !ok || malformed != 2 {
		t.Fatalf("ok=%v malformed=%d", ok, malformed)
	}
	var ordinary bool
	for _, e := range events {
		if e.Type == "message" && e.Content == "ordinary text" {
			ordinary = true
		}
	}
	if !ordinary {
		t.Fatalf("events=%#v", events)
	}
}

func TestInvalidFirstConversationRecordCannotChooseIdentityOrScope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"sessionId":"forged","type":"user","cwd":"/forged/project","message":{"role":"user","parts":[]}}` + "\n" +
		`{"sessionId":"real","type":"user","cwd":"/trusted/project","message":{"role":"user","parts":[{"text":"valid"}]}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "real.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Scope.Root != "/trusted/project" || got[0].MalformedCount != 1 {
		t.Fatalf("session=%#v", got[0])
	}
}

func TestCancellationInterruptsLargeToolHistory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body strings.Builder
	body.WriteString(`{"sessionId":"history","type":"user","message":{"role":"user","parts":[{"text":"start"}]}}` + "\n")
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("call-%d", i)
		fmt.Fprintf(&body, "{\"sessionId\":\"history\",\"type\":\"assistant\",\"message\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":%q,\"name\":\"lookup\",\"args\":{}}}]}}\n", id)
		fmt.Fprintf(&body, "{\"sessionId\":\"history\",\"type\":\"user\",\"message\":{\"role\":\"user\",\"parts\":[{\"functionResponse\":{\"id\":%q,\"name\":\"lookup\",\"response\":{}}}]}}\n", id)
	}
	if err := os.WriteFile(filepath.Join(dir, "history.jsonl"), []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterContext{Context: context.Background(), after: 8}
	got, err := New(root).Discover(ctx)
	if !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestRecordAndEventLimits(t *testing.T) {
	t.Run("records", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "encoded", "chats")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var body strings.Builder
		for i := 0; i < 5000; i++ {
			fmt.Fprintf(&body, "{\"sessionId\":\"records\",\"type\":\"user\",\"message\":{\"role\":\"user\",\"parts\":[{\"text\":\"%d\"}]}}\n", i)
		}
		if err := os.WriteFile(filepath.Join(dir, "records.jsonl"), []byte(body.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
	})
	t.Run("events", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "encoded", "chats")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, 9000)
		for i := range parts {
			parts[i] = `{"text":"x"}`
		}
		body := `{"sessionId":"events","type":"user","message":{"role":"user","parts":[` + strings.Join(parts, ",") + `]}}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
	})
}

func TestSingleRecordToolFailureRollsBackAllDeltas(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded", "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"sessionId":"rollback","type":"user","message":{"role":"user","parts":[{"text":"start"}]}}`,
		`{"sessionId":"rollback","type":"assistant","message":{"role":"model","parts":[{"functionCall":{"id":"c1","name":"one","args":{}}},{"functionCall":{"id":"c2","name":"two","args":{}}}]}}`,
		`{"sessionId":"rollback","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"one","response":{}}},{"functionResponse":{"id":"c2","name":"wrong","response":{}}}]}}`,
		`{"sessionId":"rollback","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"one","response":{}}}]}}`,
		`{"sessionId":"rollback","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"c2","name":"two","response":{}}}]}}`,
		`{"sessionId":"rollback","type":"user","message":{"role":"user","parts":[{"functionResponse":{"id":"orphan","name":"one","response":{}}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollback.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].MalformedCount != 2 {
		t.Fatalf("session=%#v", got[0])
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var results int
	dec := json.NewDecoder(r)
	for {
		var e map[string]any
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if e["type"] == "tool_result" {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("results=%d", results)
	}
}
