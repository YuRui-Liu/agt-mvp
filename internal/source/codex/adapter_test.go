package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

func TestDiscoverLiveWinsAndOpenPreservesTools(t *testing.T) {
	live, archived := t.TempDir(), t.TempDir()
	day := filepath.Join(live, "2026", "01", "02")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../testdata/codex/standard.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(day, "rollout-live.jsonl"), filepath.Join(archived, "rollout-old.jsonl")} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := New(live, archived)
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.Product() != "codex" || len(got) != 1 {
		t.Fatalf("product=%q sessions=%#v", a.Product(), got)
	}
	s := got[0]
	if s.ID != "codex:session-1" || s.Scope.Root != "/workspace/campus-app" || s.MessageCount != 2 {
		t.Fatalf("session=%#v", s)
	}
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	dec, count, tool, message := json.NewDecoder(r), 0, false, false
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		count++
		if event["type"] == "function_call" {
			tool = true
		}
		if event["type"] == "message" {
			message = true
		}
		if _, exists := event["extra_secret"]; exists {
			t.Fatalf("private metadata leaked: %#v", event)
		}
	}
	if count != 4 || !tool || !message {
		t.Fatalf("count=%d tool=%v message=%v", count, tool, message)
	}
}

func TestInvalidMessageContentIsIgnoredDeterministically(t *testing.T) {
	live := t.TempDir()
	day := filepath.Join(live, "2026", "01", "02")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"session_meta\",\"timestamp\":\"2026-01-01T00:00:00Z\",\"payload\":{\"id\":\"invalid\",\"cwd\":\"/workspace/campus-app\"}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"user\",\"content\":null}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"assistant\",\"content\":[]}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"user\",\"content\":{}}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"assistant\",\"content\":\"\"}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":null}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[]}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"type\":\"unknown\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"ignored\"}]}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"valid\"}]}}\n{\n"
	for _, name := range []string{"rollout-b.jsonl", "rollout-a.jsonl"} {
		if err := os.WriteFile(filepath.Join(day, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := New(live, t.TempDir())
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MessageCount != 1 || !strings.HasSuffix(got[0].OpaqueRef, "rollout-a.jsonl") {
		t.Fatalf("got %#v", got)
	}
	if got[0].MalformedCount != 7 {
		t.Fatalf("MalformedCount=%d", got[0].MalformedCount)
	}
	r, err := a.Open(context.Background(), got[0])
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	decoder := json.NewDecoder(r)
	messageEvents := 0
	for {
		var event map[string]any
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if event["type"] == "message" {
			messageEvents++
		}
	}
	if messageEvents != got[0].MessageCount {
		t.Fatalf("message events=%d MessageCount=%d", messageEvents, got[0].MessageCount)
	}
}

func TestConflictingMetadataFallsBackToPhysicalIdentityAndCollection(t *testing.T) {
	live := t.TempDir()
	day := filepath.Join(live, "2026", "01", "02")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"first\",\"cwd\":\"relative/path\"}}\n" +
		"{\"type\":\"session_meta\",\"payload\":{\"id\":\"second\",\"cwd\":\"/workspace/campus-app\"}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"ok\"}]}}\n"
	if err := os.WriteFile(filepath.Join(day, "rollout-physical.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(live, t.TempDir()).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID != "codex:rollout-physical" || got[0].Scope.Type != source.ScopeSessionCollection {
		t.Fatalf("session=%#v", got[0])
	}
}

func TestDiscoverFailsClosedOnOversizedLineAndOpenRejectsForgedReference(t *testing.T) {
	live := t.TempDir()
	day := filepath.Join(live, "2026", "01", "02")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	valid := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"large\",\"cwd\":\"/workspace/campus-app\"}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"ok\"}]}}\n"
	body := valid + strings.Repeat("x", maxLineBytes+1) + "\n"
	if err := os.WriteFile(filepath.Join(day, "rollout-large.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(live, t.TempDir())
	got, err := a.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("oversized transcript discovered: %#v", got)
	}
	outside := filepath.Join(t.TempDir(), "rollout-outside.jsonl")
	if err := os.WriteFile(outside, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), source.Session{Product: a.Product(), OpaqueRef: outside}); err == nil {
		t.Fatal("forged outside reference accepted")
	}
}

func TestEmptyCWDUsesStableSessionCollection(t *testing.T) {
	live := t.TempDir()
	day := filepath.Join(live, "2026", "01", "02")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"no-cwd\"}}\n" +
		"{\"type\":\"response_item\",\"payload\":{\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"ok\"}]}}\n"
	if err := os.WriteFile(filepath.Join(day, "rollout-no-cwd.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(live, t.TempDir()).Discover(context.Background())
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
	if _, err := New(t.TempDir(), t.TempDir()).Discover(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenCanceledContextWinsValidation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(t.TempDir(), t.TempDir()).Open(ctx, source.Session{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenRequiresReferenceFromSameAdapterDiscovery(t *testing.T) {
	live, archived := t.TempDir(), t.TempDir()
	day := filepath.Join(live, "2026", "01", "02")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../testdata/codex/standard.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-known.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(live, archived)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if _, err := New(live, archived).Open(context.Background(), got[0]); err == nil {
		t.Fatal("cross-instance reference accepted")
	}
	tampered := got[0]
	tampered.ID = "codex:tampered"
	if _, err := a.Open(context.Background(), tampered); err == nil {
		t.Fatal("tampered discovered reference accepted")
	}
	arbitrary := filepath.Join(day, "rollout-arbitrary.jsonl")
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

func TestLimitedScannerDetectsSessionGrowthBeyondLimit(t *testing.T) {
	body := strings.Repeat("{}\n", maxSessionBytes/3+1)
	scanner, limited := newSessionScanner(strings.NewReader(body))
	for scanner.Scan() {
	}
	if limited.N != 0 {
		t.Fatalf("remaining limit=%d", limited.N)
	}
}
