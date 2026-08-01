package workbuddy

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
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
	if s.ID != "workbuddy:demo:session-a" || s.MessageCount != 4 || s.MalformedCount != 1 || s.Scope.Root != "/workspace/demo" {
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
func TestRejectsMultipleRoots(t *testing.T) {
	if _, err := New(t.TempDir(), t.TempDir()).Discover(context.Background()); err == nil {
		t.Fatal("multiple roots accepted")
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
	if !seen["workbuddy:project-a:same"] || !seen["workbuddy:project-b:same"] || !seen["workbuddy:project-a:parent-a:subagent:child-a"] {
		t.Fatalf("ids=%#v", seen)
	}
	for _, s := range got {
		if s.ID == "workbuddy:project-a:parent-a:subagent:child-a" && s.ParentID != "" {
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
		if s.ID == "workbuddy:project-a:parent-a:subagent:child" && s.ParentID != "workbuddy:project-a:parent-a" {
			t.Fatalf("child=%#v", s)
		}
	}
}
