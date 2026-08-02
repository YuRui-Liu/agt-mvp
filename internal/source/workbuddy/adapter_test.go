package workbuddy

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
)

func TestDiscoverOpenAndStrictFormat(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-a.jsonl")
	body := "{\"type\":\"message\",\"role\":\"user\",\"content\":\"hello\",\"cwd\":\"/workspace/demo\",\"timestamp\":\"2026-01-01T00:00:00Z\"}\n" +
		"{\"type\":\"function_call\",\"name\":\"read_file\",\"callId\":\"c1\",\"arguments\":{\"path\":\"demo.go\"},\"timestamp\":\"2026-01-01T00:00:01Z\"}\n" +
		"{\"type\":\"function_call_result\",\"callId\":\"c1\",\"output\":\"ok\",\"timestamp\":\"2026-01-01T00:00:02Z\"}\n" +
		"{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}],\"timestamp\":\"2026-01-01T00:00:03Z\"}\n{bad\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.ID != fallbackSessionID("demo", "session-a") || s.MessageCount != 4 || s.MalformedCount != 1 || s.Scope.Root != "/workspace/demo" {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec, n := json.NewDecoder(r), 0
	for {
		var e any
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		n++
	}
	if n != 4 {
		t.Fatalf("events=%d", n)
	}
	if _, err := New(root).Open(context.Background(), s); err == nil {
		t.Fatal("cross instance")
	}
	if err := os.WriteFile(path, []byte(body+" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), s); err == nil {
		t.Fatal("stale")
	}
}
func TestMultipleExplicitRootsDeduplicateUpstreamSessionID(t *testing.T) {
	roots := []string{t.TempDir(), t.TempDir()}
	body := []byte(`{"type":"message","sessionId":"shared-session","role":"user","content":"hello"}` + "\n")
	for _, root := range roots {
		dir := filepath.Join(root, "encoded-project")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "copy.jsonl"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := New(roots[0], roots[0], roots[1]).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID != upstreamSessionID("shared-session") {
		t.Fatalf("id=%q", got[0].ID)
	}
}

func TestDefaultRootsPreferWorkBuddyAIAndExplicitRootsStayIsolated(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WORKBUDDY_PROJECTS_DIR", "")
	current := filepath.Join(home, ".workbuddy-ai", "projects", "encoded")
	legacy := filepath.Join(home, ".workbuddy", "projects", "encoded")
	for _, dir := range []string{current, legacy} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte(`{"type":"message","sessionId":"same-upstream","role":"user","content":"ok"}` + "\n")
	if err := os.WriteFile(filepath.Join(current, "current.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "legacy.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New().Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	canonicalCurrent, err := filepath.EvalSymlinks(current)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].OpaqueRef != filepath.Join(canonicalCurrent, "current.jsonl") {
		t.Fatalf("opaque=%q", got[0].OpaqueRef)
	}

	explicit := t.TempDir()
	got, err = New(explicit).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("explicit sessions=%#v err=%v", got, err)
	}
}

func TestRejectsSymlinkRoot(t *testing.T) {
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "projects-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := New(link).Discover(context.Background()); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestRejectsSymlinkedRootAncestor(t *testing.T) {
	target := t.TempDir()
	root := filepath.Join(target, "projects")
	dir := filepath.Join(root, "encoded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(`{"type":"message","sessionId":"s","role":"user","content":"ok"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	container := t.TempDir()
	linkedAncestor := filepath.Join(container, "linked")
	if err := os.Symlink(target, linkedAncestor); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, err := New(filepath.Join(linkedAncestor, "projects")).Discover(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestOpenRejectsReplacedRootDirectory(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "projects")
	install := func() {
		dir := filepath.Join(root, "encoded")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"type":"message","sessionId":"s","role":"user","content":"same bytes","timestamp":"2026-01-01T00:00:00Z"}` + "\n")
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

func TestCurrentEnvelopeUsageMetadataAndReasoning(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join([]string{
		`{"type":"message","sessionId":"session-current","role":"user","content":[{"type":"input_text","text":"ask"}],"cwd":"/synthetic/project","timestamp":"2026-01-01T00:00:00Z"}`,
		`{"type":"reasoning","sessionId":"session-current","content":[],"rawContent":[{"type":"reasoning_text","text":"explicit thought"}],"timestamp":"2026-01-01T00:00:01Z"}`,
		`{"type":"function_call","sessionId":"session-current","name":"lookup","callId":"tool-1","arguments":{"query":"sample"}}`,
		`{"type":"function_call_result","sessionId":"session-current","callId":"tool-1","output":{"value":"result"}}`,
		`{"type":"message","sessionId":"session-current","role":"assistant","content":[{"type":"output_text","text":"answer","providerData":{"annotations":[]}}],"message":{"usage":{"inputTokens":2,"outputTokens":3}},"providerData":{"usage":{"inputTokens":5,"outputTokens":7,"outputTokensDetails":[{"reasoning_tokens":11}]}}}`,
		`{"type":"ai-title","sessionId":"forged-metadata-id","cwd":"/forged/metadata","content":"ignored"}`,
		`{"type":"file-history-snapshot","sessionId":"session-current","snapshot":{"path":"/must/not/be/opened"}}`,
		`{"type":"future-metadata","sessionId":"session-current","value":true}`,
	}, "\n") + "\n"
	path := filepath.Join(dir, "session-current.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.MalformedCount != 0 || s.MessageCount != 5 {
		t.Fatalf("session=%#v", s)
	}
	if s.Usage["input_tokens"] != 5 || s.Usage["output_tokens"] != 7 || s.Usage["reasoning_tokens"] != 11 {
		t.Fatalf("usage=%#v", s.Usage)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var events []map[string]any
	dec := json.NewDecoder(r)
	for {
		var e map[string]any
		if err := dec.Decode(&e); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if len(events) != 5 || events[1]["type"] != "message" {
		t.Fatalf("events=%#v", events)
	}
}

func TestOnlyValidatedProjectSessionLayoutsAreScanned(t *testing.T) {
	root := t.TempDir()
	write := func(rel, id string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := []byte(`{"type":"message","sessionId":"` + id + `","role":"user","content":"ok"}` + "\n")
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join("encoded", "valid.jsonl"), "valid")
	write(filepath.Join("connectors", "ignored.jsonl"), "ignored-connectors")
	write(filepath.Join("encoded", "sessions", "ignored.jsonl"), "ignored-sessions")
	write(filepath.Join("encoded", "parent", "subagents", "child.jsonl"), "child")
	write(filepath.Join("encoded", "parent", "tool-results", "ignored.jsonl"), "ignored-tool-results")
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestIndexAndDatabaseDirectoriesAreNeverProjects(t *testing.T) {
	root := t.TempDir()
	body := []byte(`{"type":"message","sessionId":"must-not-discover","role":"user","content":"ignored"}` + "\n")
	for _, reserved := range []string{"index", "workbuddy.db"} {
		for _, rel := range []string{"session.jsonl", filepath.Join("parent", "subagents", "child.jsonl")} {
			path := filepath.Join(root, reserved, rel)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestCapabilitiesReflectObservedEvents(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	messageOnly := []byte(`{"type":"message","sessionId":"message-only","role":"user","content":"hello"}` + "\n")
	withTool := []byte(`{"type":"message","sessionId":"with-tool","role":"user","content":"hello"}` + "\n" +
		`{"type":"function_call","sessionId":"with-tool","callId":"call-1","name":"lookup","arguments":{}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "message.jsonl"), messageOnly, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool.jsonl"), withTool, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	for _, s := range got {
		want := []source.Capability{source.CapabilityMessages}
		if s.ID == upstreamSessionID("with-tool") {
			want = append(want, source.CapabilityTools)
		}
		if !reflect.DeepEqual(s.Capabilities, want) {
			t.Fatalf("id=%q capabilities=%#v want=%#v", s.ID, s.Capabilities, want)
		}
	}
}

func TestUsageOnlyComesFromProviderData(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"type":"message","sessionId":"usage","role":"assistant","content":"answer","message":{"usage":{"inputTokens":101,"outputTokens":202}}}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Usage["input_tokens"] != 0 || got[0].Usage["output_tokens"] != 0 {
		t.Fatalf("usage=%#v", got[0].Usage)
	}
}

func TestDuplicateParentInLaterRootStillLinksUniqueChild(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	parent := []byte(`{"type":"message","sessionId":"canonical-parent","role":"user","content":"parent"}` + "\n")
	child := []byte(`{"type":"message","sessionId":"unique-child","role":"user","content":"child"}` + "\n")
	for _, root := range []string{first, second} {
		dir := filepath.Join(root, "encoded")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "parent.jsonl"), parent, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(second, "encoded", "parent", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "child.jsonl"), child, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(first, second).Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	for _, s := range got {
		if s.ID == upstreamSessionID("unique-child") && s.ParentID != upstreamSessionID("canonical-parent") {
			t.Fatalf("child=%#v", s)
		}
	}
}

func TestSessionIDDomainsPreventUpstreamFallbackCollisions(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "p")
	if err := os.MkdirAll(filepath.Join(project, "parent", "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(project, "x.jsonl"), `{"type":"message","role":"user","content":"fallback"}`)
	write(filepath.Join(project, "upstream.jsonl"), `{"type":"message","sessionId":"p:x","role":"user","content":"upstream"}`)
	write(filepath.Join(project, "parent.jsonl"), `{"type":"message","role":"user","content":"parent"}`)
	write(filepath.Join(project, "parent", "subagents", "child.jsonl"), `{"type":"message","role":"user","content":"child"}`)
	write(filepath.Join(project, "sub-upstream.jsonl"), `{"type":"message","sessionId":"p:parent:subagent:child","role":"user","content":"upstream child collision"}`)
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 5 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	ids := map[string]bool{}
	for _, s := range got {
		if ids[s.ID] {
			t.Fatalf("duplicate id %q", s.ID)
		}
		ids[s.ID] = true
	}
	var child source.Session
	for _, s := range got {
		if strings.Contains(s.OpaqueRef, filepath.Join("subagents", "child.jsonl")) {
			child = s
		}
	}
	if child.ID == "" || child.ParentID == "" || child.ID == child.ParentID {
		t.Fatalf("child=%#v", child)
	}
}

func TestRejectsOversizedUpstreamSessionID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"message","sessionId":"` + strings.Repeat("x", 513) + `","role":"user","content":"invalid"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "oversized.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestInvalidRecordCannotChooseIdentityOrScope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"type":"message","sessionId":"forged","role":"system","content":"invalid","cwd":"/forged/project"}` + "\n" +
		`{"type":"message","sessionId":"real","role":"user","content":"valid","cwd":"/trusted/project"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "real.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID != upstreamSessionID("real") || got[0].Scope.Root != "/trusted/project" || got[0].MalformedCount != 1 {
		t.Fatalf("session=%#v", got[0])
	}
}

func TestConflictingCWDUsesStableCollectionScope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"message\",\"sessionId\":\"s\",\"role\":\"user\",\"content\":\"a\",\"cwd\":\"/synthetic/a\"}\n" +
		"{\"type\":\"message\",\"sessionId\":\"s\",\"role\":\"assistant\",\"content\":\"b\",\"cwd\":\"/synthetic/b\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Scope.Type != source.ScopeSessionCollection || got[0].Scope.Root == "" {
		t.Fatalf("scope=%#v", got[0].Scope)
	}
}
func TestVersionedFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/workbuddy/v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "fixture-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].FormatVersion != "jsonl-v1" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
func TestSafetyContract(t *testing.T) {
	data := adaptertest.ReadFixture(t, "../testdata/workbuddy/v1.jsonl")
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, b []byte) error {
		d := filepath.Join(root, "fixture-project")
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "fixture.jsonl"), b, 0o600)
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
	dir := filepath.Join(root, "encoded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(`{"type":"message","sessionId":"s","role":"user","content":"ok"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
}

func TestProjectsAndSubagentsKeepDistinctStableIdentities(t *testing.T) {
	root := t.TempDir()
	body := []byte("{\"type\":\"message\",\"role\":\"user\",\"content\":\"x\"}\n")
	for _, project := range []string{"project-a", "project-b"} {
		dir := filepath.Join(root, project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "same.jsonl"), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sub := filepath.Join(root, "project-a", "parent-a", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "child-a.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s.ID] {
			t.Fatalf("duplicate %q", s.ID)
		}
		seen[s.ID] = true
	}
	if !seen[fallbackSessionID("project-a", "same")] || !seen[fallbackSessionID("project-b", "same")] || !seen[fallbackSubagentID("project-a", "parent-a", "child-a")] {
		t.Fatalf("ids=%#v", seen)
	}
	for _, s := range got {
		if s.ID == fallbackSubagentID("project-a", "parent-a", "child-a") && s.ParentID != "" {
			t.Fatalf("orphan parent forged: %#v", s)
		}
	}
}

func TestSubagentLinksCanonicalDiscoveredParent(t *testing.T) {
	root := t.TempDir()
	body := []byte("{\"type\":\"message\",\"role\":\"user\",\"content\":\"x\"}\n")
	project := filepath.Join(root, "project-a")
	if err := os.MkdirAll(filepath.Join(project, "parent-a", "subagents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "parent-a.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "parent-a", "subagents", "child.jsonl"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.ID == fallbackSubagentID("project-a", "parent-a", "child") && s.ParentID != fallbackSessionID("project-a", "parent-a") {
			t.Fatalf("child=%#v", s)
		}
	}
}
