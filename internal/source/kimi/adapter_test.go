package kimi

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

func TestKimiWireContract(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project-hash", "session-a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "wire.jsonl")
	body := "{\"type\":\"metadata\",\"version\":1}\n" +
		"{\"timestamp\":1760000000,\"message\":{\"type\":\"TurnBegin\",\"payload\":{\"user_input\":[{\"type\":\"text\",\"text\":\"hello\"}]}}}\n" +
		"{\"timestamp\":1760000001,\"message\":{\"type\":\"ContentPart\",\"payload\":{\"type\":\"text\",\"text\":\"checking\"}}}\n" +
		"{\"timestamp\":1760000002,\"message\":{\"type\":\"ToolCall\",\"payload\":{\"id\":\"c1\",\"name\":\"read_file\",\"arguments\":{\"path\":\"demo.go\"}}}}\n" +
		"{\"timestamp\":1760000003,\"message\":{\"type\":\"ToolResult\",\"payload\":{\"tool_call_id\":\"c1\",\"content\":\"ok\"}}}\n" +
		"{\"timestamp\":1760000004,\"message\":{\"type\":\"TurnEnd\",\"payload\":{}}}\n{bad\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.ID != "kimi-cli:project-hash:session-a" || s.MessageCount != 2 || s.MalformedCount != 1 {
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
	if n != 4 {
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
	data, err := os.ReadFile("../testdata/kimi/v1-wire.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "fixture-project", "fixture-session")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wire.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].FormatVersion != "wire-v1" {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
func TestSafetyContract(t *testing.T) {
	data := adaptertest.ReadFixture(t, "../testdata/kimi/v1-wire.jsonl")
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, b []byte) error {
		d := filepath.Join(root, "fixture-project", "fixture-session")
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "wire.jsonl"), b, 0o600)
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
