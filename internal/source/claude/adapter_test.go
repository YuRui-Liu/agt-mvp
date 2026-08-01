package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

func TestDiscoverAndOpen(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-workspace-campus-app")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../testdata/claude/valid.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "session-1.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	a := New(root)
	sessions, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.Product() != "claude-code" || len(sessions) != 1 {
		t.Fatalf("product=%q sessions=%#v", a.Product(), sessions)
	}
	s := sessions[0]
	if s.ID != "claude-code:session-1" || s.Scope.Root != "/workspace/campus-app" || s.MessageCount != 2 {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec := json.NewDecoder(r)
	var events []map[string]any
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if len(events) != 2 || events[1]["type"] != "assistant" {
		t.Fatalf("events=%#v", events)
	}
	if _, exists := events[0]["cwd"]; exists {
		t.Fatalf("private metadata leaked: %#v", events[0])
	}
}

func TestDiscoverSkipsAgentMainAndMalformedOnly(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "agent-x.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "broken.jsonl"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestDiscoverKeepsSameLogicalIDFromDifferentParentsDeterministically(t *testing.T) {
	root := t.TempDir()
	data, err := os.ReadFile("../testdata/claude/valid.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for index, parent := range []string{"session-a", "session-b"} {
		project := filepath.Join(root, fmt.Sprintf("project-%d", index))
		dir := filepath.Join(project, parent, "subagents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		main := fmt.Sprintf("{\"type\":\"user\",\"sessionId\":\"parent-%d\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"role\":\"user\",\"content\":\"parent\"}}\n", index)
		if err := os.WriteFile(filepath.Join(project, parent+".jsonl"), []byte(main), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "agent-shared.jsonl"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("got %#v", got)
	}
	for _, session := range got {
		r, err := New(root).Open(context.Background(), session)
		if err == nil {
			r.Close()
			t.Fatal("session from another adapter instance unexpectedly trusted")
		}
	}
	trusted := New(root)
	discovered, err := trusted.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	childCount := 0
	for _, session := range discovered {
		if !strings.Contains(session.OpaqueRef, "agent-shared") {
			continue
		}
		childCount++
		r, err := trusted.Open(context.Background(), session)
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err := json.NewDecoder(r).Decode(&event); err != nil {
			r.Close()
			t.Fatal(err)
		}
		r.Close()
		wantParent := "parent-0"
		if strings.Contains(session.OpaqueRef, "session-b") {
			wantParent = "parent-1"
		}
		if event["parent_id"] != wantParent {
			t.Fatalf("event=%#v want parent_id=%q", event, wantParent)
		}
	}
	if childCount != 2 {
		t.Fatalf("childCount=%d sessions=%#v", childCount, discovered)
	}
}

func TestOrphanOrInvalidParentDoesNotBindParentID(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	for _, parent := range []string{"missing", "invalid"} {
		dir := filepath.Join(project, parent, "subagents")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "{\"type\":\"user\",\"sessionId\":\"child-" + parent + "\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"role\":\"user\",\"content\":\"child\"}}\n"
		if err := os.WriteFile(filepath.Join(dir, "agent-"+parent+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(project, "invalid.jsonl"), []byte("{\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	for _, session := range got {
		r, err := a.Open(context.Background(), session)
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if err := json.NewDecoder(r).Decode(&event); err != nil {
			r.Close()
			t.Fatal(err)
		}
		r.Close()
		if _, exists := event["parent_id"]; exists {
			t.Fatalf("orphan acquired parent: %#v", event)
		}
	}
}

func TestInvalidContentAndTruncatedTailAreIgnored(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":null}}\n" +
		"{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":[]}}\n" +
		"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":{}}}\n" +
		"{\"type\":\"assistant\",\"message\":{\"role\":\"assistant\",\"content\":\"\"}}\n" +
		"{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"valid\"},\"cwd\":\"/workspace/campus-app\"}\n{\n"
	path := filepath.Join(project, "session.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageCount != 1 {
		t.Fatalf("got %#v", got)
	}
	if got[0].MalformedCount != 5 {
		t.Fatalf("MalformedCount=%d", got[0].MalformedCount)
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	decoded, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(decoded, []byte{'\n'}) != 1 {
		t.Fatalf("events=%s", decoded)
	}
}

func TestConflictingSessionIDAndUntrustedCWDDoNotMasquerade(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"user\",\"sessionId\":\"first\",\"cwd\":\"relative/path\",\"message\":{\"role\":\"user\",\"content\":\"one\"}}\n" +
		"{\"type\":\"assistant\",\"sessionId\":\"second\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"role\":\"assistant\",\"content\":\"two\"}}\n"
	if err := os.WriteFile(filepath.Join(project, "physical.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID != "claude-code:physical" || got[0].Scope.Type != source.ScopeSessionCollection {
		t.Fatalf("session=%#v", got[0])
	}
}

func TestOpenRejectsForgedAndReplacedReferences(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	if _, err := a.Open(context.Background(), source.Session{Product: a.Product(), OpaqueRef: outside}); err == nil {
		t.Fatal("forged outside reference accepted")
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"user\",\"message\":{\"role\":\"user\",\"content\":\"ok\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), got[0]); err == nil {
		t.Fatal("replacement symlink accepted")
	}
}

func TestEmptyCWDUsesStableSessionCollection(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"user\",\"sessionId\":\"no-cwd\",\"message\":{\"role\":\"user\",\"content\":\"ok\"}}\n"
	if err := os.WriteFile(filepath.Join(project, "session.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Scope.Type != source.ScopeSessionCollection || got[0].Scope.Root == "" || got[0].Scope.Label == "." {
		t.Fatalf("scope=%#v", got[0].Scope)
	}
}

func TestDiscoverHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(t.TempDir()).Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenCanceledContextWinsValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(t.TempDir()).Open(ctx, source.Session{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestDiscoverySnapshotRevokesRemovedReference(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "session.jsonl")
	body := []byte("{\"type\":\"user\",\"sessionId\":\"one\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"role\":\"user\",\"content\":\"ok\"}}\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), got[0]); err == nil {
		t.Fatal("reference survived a discovery snapshot that removed it")
	}
}

func TestLimitedScannerDetectsSessionGrowthBeyondLimit(t *testing.T) {
	body := strings.Repeat("{}\n", maxSessionBytes/3+1)
	scanner, limited := newSessionScanner(strings.NewReader(body))
	for scanner.Scan() {
	}
	if limited.N != 0 {
		t.Fatalf("remaining limit=%d", limited.N)
	}
}
