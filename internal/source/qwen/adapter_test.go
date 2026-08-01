package qwen

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
