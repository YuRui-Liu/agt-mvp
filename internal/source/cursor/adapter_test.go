package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

func TestDiscoverPrefersJSONLAndOpen(t *testing.T) {
	root := t.TempDir()
	transcripts := filepath.Join(root, "workspace-campus-app", "agent-transcripts")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcripts, "session-1.txt"), []byte("user: stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcripts, "session-1.jsonl"), []byte("{\"role\":\"user\",\"message\":{\"content\":\"Map campus\"}}\n{\"role\":\"assistant\",\"message\":{\"content\":\"Done\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	if !slices.Contains(a.Capabilities(), source.Capability("tools")) {
		t.Fatalf("adapter capabilities=%#v", a.Capabilities())
	}
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.Product() != "cursor" || len(got) != 1 {
		t.Fatalf("product=%q sessions=%#v", a.Product(), got)
	}
	s := got[0]
	if !slices.Contains(s.Capabilities, source.Capability("tools")) {
		t.Fatalf("session capabilities=%#v", s.Capabilities)
	}
	if s.ID != "cursor:session-1" || s.MessageCount != 2 || s.Scope.Type != source.ScopeSessionCollection {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec, count := json.NewDecoder(r), 0
	for {
		var event any
		if err := dec.Decode(&event); err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("count=%d", count)
	}
}

func TestDiscoverRejectsSymlinkAndInvalidNestedName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project", "agent-transcripts")
	if err := os.MkdirAll(filepath.Join(dir, "session-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "session-a", "other.jsonl"), []byte("{\"role\":\"user\",\"message\":{\"content\":\"x\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "outside.jsonl")
	if err := os.WriteFile(target, []byte("{\"role\":\"user\",\"message\":{\"content\":\"x\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked.jsonl")); err != nil {
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

func TestDiscoverRejectsSymlinkedTranscriptsDirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "escaped.jsonl"), []byte("{\"role\":\"user\",\"message\":{\"content\":\"x\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "agent-transcripts")); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestContentSignatureOverridesExtension(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-workspace-campus-app", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plain := "<user_query>Find a building</user_query>\nassistant: Found it\n"
	if err := os.WriteFile(filepath.Join(dir, "session-plain.jsonl"), []byte("user: "+plain), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageCount != 2 || got[0].Scope.Type != source.ScopeSessionCollection || got[0].Scope.Root == "" {
		t.Fatalf("got %#v", got)
	}
}

func TestTrustedConsistentCWDCreatesProjectScope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded-project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"role\":\"user\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"content\":\"question\"}}\n" +
		"{\"role\":\"assistant\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"content\":\"answer\"}}\n"
	if err := os.WriteFile(filepath.Join(dir, "trusted.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Scope.Type != source.ScopeProject || got[0].Scope.Root != "/workspace/campus-app" {
		t.Fatalf("scope=%#v", got[0].Scope)
	}
}

func TestConflictingOrUnsignedCWDIsNotTrusted(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded-project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	conflict := "{\"role\":\"user\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"content\":\"question\"}}\n" +
		"{\"role\":\"assistant\",\"cwd\":\"/workspace/other\",\"message\":{\"content\":\"answer\"}}\n"
	if err := os.WriteFile(filepath.Join(dir, "conflict.jsonl"), []byte(conflict), 0o600); err != nil {
		t.Fatal(err)
	}
	unsigned := "{\"cwd\":\"/workspace/campus-app\",\"message\":{\"content\":\"question\"}}\n"
	if err := os.WriteFile(filepath.Join(dir, "unsigned.jsonl"), []byte(unsigned), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "cursor:conflict" || got[0].Scope.Type != source.ScopeSessionCollection {
		t.Fatalf("sessions=%#v", got)
	}
}

func TestJSONSignatureRejectsCWDWithInvalidMessageEnvelope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded-project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"role\":\"system\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"content\":\"forged\"}}\n" +
		"{\"role\":\"user\",\"cwd\":\"/workspace/campus-app\",\"message\":{\"content\":\"later valid\"}}\n"
	if err := os.WriteFile(filepath.Join(dir, "forged.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("forged first envelope accepted: %#v", got)
	}
}

func TestJSONLCountsMalformedRecordsAfterStrictCursorEnvelope(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "encoded-project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The source-specific signature is the trusted Cursor transcript directory
	// layout combined with a strict role+message.content envelope.
	body := "{\"role\":\"user\",\"message\":{\"content\":\"valid\"}}\n" +
		"{\"type\":\"system\",\"status\":\"ok\"}\n" +
		"{\"metadata\":{\"version\":1}}\n" +
		"[]\n\"scalar\"\n42\nnull\n" +
		"{\n{\"role\":\"assistant\",\"message\":{\"content\":[]}}\n"
	if err := os.WriteFile(filepath.Join(dir, "quality.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].MessageCount != 1 || got[0].MalformedCount != 2 {
		t.Fatalf("session=%#v", got[0])
	}
}

func TestOpenRequiresReferenceFromSameAdapterDiscovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"role\":\"user\",\"message\":{\"content\":\"known\"}}\n")
	path := filepath.Join(dir, "known.jsonl")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if _, err := New(root).Open(context.Background(), got[0]); err == nil {
		t.Fatal("cross-instance reference accepted")
	}
	tampered := got[0]
	tampered.ID = "cursor:tampered"
	if _, err := a.Open(context.Background(), tampered); err == nil {
		t.Fatal("tampered discovered reference accepted")
	}
	arbitrary := filepath.Join(dir, "arbitrary.jsonl")
	if err := os.WriteFile(arbitrary, data, 0o600); err != nil {
		t.Fatal(err)
	}
	forged := got[0]
	forged.OpaqueRef = arbitrary
	if _, err := a.Open(context.Background(), forged); err == nil {
		t.Fatal("undiscovered same-root reference accepted")
	}
	if err := os.Remove(got[0].OpaqueRef); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(got[0].OpaqueRef, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), got[0]); err == nil {
		t.Fatal("reference survived a discovery snapshot that removed it")
	}
}

func TestJSONArrayContentPreservesToolsAndCleansUserQuery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "workspace-campus-app", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../testdata/cursor/session.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "array.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageCount != 2 {
		t.Fatalf("got %#v", got)
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("<user_query>")) || bytes.Contains(out, []byte("extra_secret")) || !bytes.Contains(out, []byte("\"tool_use\"")) || !bytes.Contains(out, []byte("\"tool_result\"")) {
		t.Fatalf("events=%s", out)
	}
}

func TestMalformedJSONLDoesNotBecomePlaintext(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.jsonl"), []byte("{\nuser: must not recover\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestDiscoverSortsAndPrefersJSONLDeterministically(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.txt", "a.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("user: plain\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte("{\"role\":\"user\",\"message\":{\"content\":\"json\"}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	first, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].ID != "cursor:a" || !strings.HasSuffix(first[0].OpaqueRef, ".jsonl") || !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
}

func TestDiscoverFailsClosedOnSessionLimitAndOpenRejectsForgedReference(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project", "agent-transcripts")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := append([]byte("user: valid\n"), bytes.Repeat([]byte("x\n"), maxSessionBytes/2+1)...)
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("oversized transcript discovered: %#v", got)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("user: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), source.Session{Product: a.Product(), OpaqueRef: outside}); err == nil {
		t.Fatal("forged outside reference accepted")
	}
}

func TestDiscoverHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(t.TempDir()).Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
