package openclaw

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

func TestDiscoverOpenAndAuthorization(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "writer", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "session-a.jsonl")
	body := "{\"type\":\"session\",\"id\":\"session-a\",\"cwd\":\"/workspace/demo\",\"timestamp\":\"2026-01-01T00:00:00Z\"}\n" +
		"{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:01Z\",\"message\":{\"role\":\"user\",\"content\":\"hello\"}}\n" +
		"{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:02Z\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"done\"},{\"type\":\"toolCall\",\"id\":\"call-1\",\"name\":\"read_file\",\"arguments\":{\"path\":\"demo.go\"}}]}}\n" +
		"{\"type\":\"message\",\"timestamp\":\"2026-01-01T00:00:03Z\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"call-1\",\"content\":\"ok\"}}\n" +
		"{bad\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.ID != "openclaw:writer:session-a" || s.MessageCount != 3 || s.MalformedCount != 1 || s.Scope.Root != "/workspace/demo" {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec, count := json.NewDecoder(r), 0
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 4 {
		t.Fatalf("events=%d", count)
	}
	if _, err := New(root).Open(context.Background(), s); err == nil {
		t.Fatal("cross-instance reference accepted")
	}
	if err := os.WriteFile(path, []byte(body+" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), s); err == nil {
		t.Fatal("stale reference accepted")
	}
}
func TestRejectsMultipleRoots(t *testing.T) {
	if _, err := New(t.TempDir(), t.TempDir()).Discover(context.Background()); err == nil {
		t.Fatal("multiple roots accepted")
	}
}
func TestVersionedFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/openclaw/v1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "fixture-agent", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-fixture.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].FormatVersion != "jsonl-v1" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
func TestSafetyContract(t *testing.T) {
	data := adaptertest.ReadFixture(t, "../testdata/openclaw/v1.jsonl")
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, b []byte) error {
		d := filepath.Join(root, "fixture-agent", "sessions")
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

func TestStrictSignatureAndSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake.jsonl"), []byte("{\"role\":\"user\",\"content\":\"not openclaw\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("{\"type\":\"session\",\"id\":\"x\"}\n{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":\"x\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "linked.jsonl")); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestArchiveSelectionAndStableFallbackScope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "agent-a", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	makeBody := func(id, text string) []byte {
		return []byte("{\"type\":\"session\",\"id\":\"" + id + "\"}\n{\"type\":\"message\",\"message\":{\"role\":\"user\",\"content\":\"" + text + "\"}}\n")
	}
	active := filepath.Join(dir, "same.jsonl")
	if err := os.WriteFile(active, makeBody("same", "active"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "same.jsonl.deleted.2026-01-01T00-00-00Z"), makeBody("same", "old"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].OpaqueRef != active {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	key := got[0].Scope.Root
	if err := os.WriteFile(active, makeBody("same", "active appended"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = a.Discover(context.Background())
	if err != nil || got[0].Scope.Root != key {
		t.Fatalf("scope changed: %#v", got)
	}
	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	got, err = a.Discover(context.Background())
	if err != nil || len(got) != 1 || !strings.Contains(got[0].OpaqueRef, ".deleted.") {
		t.Fatalf("archive=%#v err=%v", got, err)
	}
}
