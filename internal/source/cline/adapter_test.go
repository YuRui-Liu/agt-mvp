package cline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
)

func installFixture(t *testing.T, root, id string, manifest, messages []byte) (string, string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, id+".json")
	messagesPath := filepath.Join(dir, id+".messages.json")
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(messagesPath, messages, 0o600); err != nil {
		t.Fatal(err)
	}
	return manifestPath, messagesPath
}

func fixture(t *testing.T) ([]byte, []byte) {
	t.Helper()
	manifest := adaptertest.ReadFixture(t, "../testdata/cline/v1/session-alpha.json")
	messages := adaptertest.ReadFixture(t, "../testdata/cline/v1/session-alpha.messages.json")
	return manifest, messages
}

func events(t *testing.T, a *Adapter, s source.Session) []map[string]any {
	t.Helper()
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []map[string]any
	dec := json.NewDecoder(r)
	for {
		var event map[string]any
		if err := dec.Decode(&event); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, event)
	}
}

func TestFixtureStrictPairAndCanonicalEvents(t *testing.T) {
	root := t.TempDir()
	manifest, messages := fixture(t)
	installFixture(t, root, "session-alpha", manifest, messages)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	s := got[0]
	if s.Product != "cline" || s.FormatVersion != "v1" || s.MessageCount != 4 || s.Scope.Type != source.ScopeProject || s.Scope.Label != "garden" {
		t.Fatalf("session=%#v", s)
	}
	if !slices.Equal(s.Capabilities, []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}) {
		t.Fatalf("caps=%#v", s.Capabilities)
	}
	gotEvents := events(t, a, s)
	var types []string
	for _, event := range gotEvents {
		types = append(types, event["type"].(string))
		adaptertest.AssertNoPrivateFields(t, event)
		encoded, _ := json.Marshal(event)
		if strings.Contains(string(encoded), "private system") || strings.Contains(string(encoded), "must never") {
			t.Fatal("private metadata exported")
		}
	}
	if !reflect.DeepEqual(types, []string{"message", "reasoning", "message", "tool_use", "tool_result", "message", "message"}) {
		t.Fatalf("types=%#v", types)
	}
}

func TestStrictNamesEnvelopeAndManifestValidation(t *testing.T) {
	manifest, messages := fixture(t)
	cases := []struct {
		name               string
		id                 string
		manifest, messages []byte
	}{
		{"directory-mismatch", "other", manifest, messages},
		{"wrapper-mismatch", "session-alpha", manifest, []byte(strings.Replace(string(messages), `"sessionId": "session-alpha"`, `"sessionId": "wrong"`, 1))},
		{"version", "session-alpha", []byte(strings.Replace(string(manifest), `"version": 1`, `"version": 2`, 1)), messages},
		{"pid-type", "session-alpha", []byte(strings.Replace(string(manifest), `"pid": 4242`, `"pid": "4242"`, 1)), messages},
		{"status", "session-alpha", []byte(strings.Replace(string(manifest), `"status": "completed"`, `"status": "unknown"`, 1)), messages},
		{"time", "session-alpha", []byte(strings.Replace(string(manifest), `2026-06-01T10:00:00Z`, `not-a-time`, 1)), messages},
		{"agent", "session-alpha", manifest, []byte(strings.Replace(string(messages), `"agent": "lead"`, `"agent": "subagent"`, 1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			installFixture(t, root, tc.id, tc.manifest, tc.messages)
			got, err := New(root).Discover(context.Background())
			if err != nil || len(got) != 0 {
				t.Fatalf("sessions=%#v err=%v", got, err)
			}
		})
	}
}

func TestToolPairsRollbackAndDynamicCapabilities(t *testing.T) {
	manifest, messages := fixture(t)
	base := string(messages)
	cases := []struct{ name, replacement string }{
		{"orphan", `{"type":"tool_result","tool_use_id":"orphan","name":"read_file","content":"healthy","is_error":false}`},
		{"name-mismatch", `{"type":"tool_result","tool_use_id":"call-1","name":"write_file","content":"healthy","is_error":false}`},
		{"duplicate", `{"type":"tool_result","tool_use_id":"call-1","name":"read_file","content":"healthy","is_error":false},{"type":"tool_result","tool_use_id":"call-1","name":"read_file","content":"again","is_error":false}`},
	}
	needle := `{"type":"tool_result","tool_use_id":"call-1","name":"read_file","content":"healthy","is_error":false}`
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			installFixture(t, root, "session-alpha", manifest, []byte(strings.Replace(base, needle, tc.replacement, 1)))
			a := New(root)
			got, err := a.Discover(context.Background())
			if err != nil || len(got) != 1 || got[0].MalformedCount == 0 {
				t.Fatalf("sessions=%#v err=%v", got, err)
			}
			_ = events(t, a, got[0])
		})
	}
	plain := []byte(`{"version":1,"updated_at":"2026-06-01T10:01:00Z","agent":"lead","sessionId":"session-alpha","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`)
	root := t.TempDir()
	installFixture(t, root, "session-alpha", manifest, plain)
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || !slices.Equal(got[0].Capabilities, []source.Capability{source.CapabilityMessages}) {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestIgnoredBlocksAndStrictToolUseEnvelope(t *testing.T) {
	manifest, _ := fixture(t)
	t.Run("ignored-only", func(t *testing.T) {
		messages := []byte(`{"version":1,"updated_at":"2026-06-01T10:01:00Z","agent":"lead","sessionId":"session-alpha","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"redacted_thinking","data":"secret"},{"type":"image","source":"ignored"},{"type":"file","path":"ignored"}]},{"role":"assistant","content":"done"}]}`)
		root := t.TempDir()
		installFixture(t, root, "session-alpha", manifest, messages)
		a := New(root)
		got, err := a.Discover(context.Background())
		if err != nil || len(got) != 1 || got[0].MessageCount != 2 || got[0].MalformedCount != 0 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
		if len(events(t, a, got[0])) != 2 {
			t.Fatal("ignored blocks emitted")
		}
	})
	for _, tc := range []struct{ name, tool string }{
		{"missing-input", `{"type":"tool_use","id":"call-1","name":"read_file"}`},
		{"conflicting-ids", `{"type":"tool_use","id":"call-1","call_id":"call-2","name":"read_file","input":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := []byte(fmt.Sprintf(`{"version":1,"updated_at":"2026-06-01T10:01:00Z","agent":"lead","sessionId":"session-alpha","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[%s]},{"role":"assistant","content":"done"}]}`, tc.tool))
			root := t.TempDir()
			installFixture(t, root, "session-alpha", manifest, messages)
			got, err := New(root).Discover(context.Background())
			if err != nil || len(got) != 1 || got[0].MalformedCount == 0 || !slices.Equal(got[0].Capabilities, []source.Capability{source.CapabilityMessages}) {
				t.Fatalf("sessions=%#v err=%v", got, err)
			}
		})
	}
}

func TestAuthorizationBindsBothFilesAndMetadata(t *testing.T) {
	manifest, messages := fixture(t)
	root := t.TempDir()
	manifestPath, messagesPath := installFixture(t, root, "session-alpha", manifest, messages)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatal(err)
	}
	forged := got[0]
	forged.MessageCount++
	if r, err := a.Open(context.Background(), forged); err == nil {
		r.Close()
		t.Fatal("forgery accepted")
	}
	if r, err := New(root).Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("cross instance accepted")
	}
	if err := os.WriteFile(messagesPath, append(messages, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("messages tamper accepted")
	}
	if err := os.WriteFile(messagesPath, messages, 0o600); err != nil {
		t.Fatal(err)
	}
	a = New(root)
	got, _ = a.Discover(context.Background())
	if err := os.Rename(manifestPath, manifestPath+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("same-content inode swap accepted")
	}
}

func TestDecoysNestedAndRootLimit(t *testing.T) {
	manifest, messages := fixture(t)
	root := t.TempDir()
	installFixture(t, root, "session-alpha", manifest, messages)
	for _, name := range []string{"db", "settings", "providers", "connectors", "logs", "cache", "locks", "cron", "teams"} {
		installFixture(t, filepath.Join(root, name), "session-alpha", manifest, messages)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	roots := make([]string, 65)
	for i := range roots {
		roots[i] = root
	}
	if _, err := New(roots...).Discover(context.Background()); err == nil {
		t.Fatal("root limit accepted")
	}
}

type cancelContext struct {
	context.Context
	mu           sync.Mutex
	calls, after int
}

func (c *cancelContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}
func (c *cancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func TestLimitsCancellationAndRootIdentityDedup(t *testing.T) {
	manifest, messages := fixture(t)
	t.Run("messages", func(t *testing.T) {
		root := t.TempDir()
		var records []string
		for i := 0; i <= maxMessages; i++ {
			records = append(records, `{"role":"user","content":"x"}`)
		}
		body := []byte(fmt.Sprintf(`{"version":1,"updated_at":"2026-06-01T10:01:00Z","agent":"lead","sessionId":"session-alpha","messages":[%s]}`, strings.Join(records, ",")))
		installFixture(t, root, "session-alpha", manifest, body)
		if got, err := New(root).Discover(context.Background()); err != nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("directory", func(t *testing.T) {
		root := t.TempDir()
		for i := 0; i <= maxDirectoryEntries; i++ {
			if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("decoy-%04d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := New(root).Discover(context.Background()); err == nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		root := t.TempDir()
		installFixture(t, root, "session-alpha", manifest, messages)
		got, err := New(root).Discover(&cancelContext{Context: context.Background(), after: 4})
		if !errors.Is(err, context.Canceled) || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("dedup", func(t *testing.T) {
		root := t.TempDir()
		installFixture(t, root, "session-alpha", manifest, messages)
		got, err := New(root, root).Discover(context.Background())
		if err != nil || len(got) != 1 {
			t.Fatalf("sessions=%#v err=%v", got, err)
		}
	})
}

func TestScopeFallbackAndMetadataCannotPollute(t *testing.T) {
	manifest, messages := fixture(t)
	body := strings.Replace(string(manifest), `"workspace_root": "/synthetic/garden"`, `"workspace_root": "relative/poison"`, 1)
	body = strings.Replace(body, `"cwd": "/synthetic/garden"`, `"cwd": "/synthetic/fallback"`, 1)
	body = strings.Replace(body, `"prompt": "must never be exported"`, `"prompt":"/private/poison","metadata":{"workspace_root":"/private/wrong"}`, 1)
	root := t.TempDir()
	installFixture(t, root, "session-alpha", []byte(body), messages)
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Scope.Label != "fallback" || filepath.IsAbs(got[0].Scope.Root) {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestAuthorizationContractAndRootSwap(t *testing.T) {
	manifest, messages := fixture(t)
	root := t.TempDir()
	_, path := installFixture(t, root, "session-alpha", manifest, messages)
	other := New(root)
	forged, err := other.Discover(context.Background())
	if err != nil || len(forged) != 1 {
		t.Fatal(err)
	}
	a := New(root)
	adaptertest.AuthorizationContract(t, a, func() {
		if err := os.WriteFile(path, append(messages, ' '), 0o600); err != nil {
			t.Fatal(err)
		}
	}, forged[0])

	base := t.TempDir()
	live := filepath.Join(base, "sessions")
	installFixture(t, live, "session-alpha", manifest, messages)
	swap := New(live)
	sessions, err := swap.Discover(context.Background())
	if err != nil || len(sessions) != 1 {
		t.Fatal(err)
	}
	if err := os.Rename(live, filepath.Join(base, "old")); err != nil {
		t.Fatal(err)
	}
	installFixture(t, live, "session-alpha", manifest, messages)
	if r, err := swap.Open(context.Background(), sessions[0]); err == nil {
		r.Close()
		t.Fatal("replacement root accepted")
	}
}

func TestSymlinkCandidatesAreIgnored(t *testing.T) {
	manifest, messages := fixture(t)
	outside := t.TempDir()
	installFixture(t, outside, "session-alpha", manifest, messages)
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "session-alpha"), filepath.Join(root, "session-alpha")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestEachCompositeFileRejectsSameContentReplacement(t *testing.T) {
	manifest, messages := fixture(t)
	for _, replaceManifest := range []bool{true, false} {
		t.Run(fmt.Sprintf("manifest=%v", replaceManifest), func(t *testing.T) {
			root := t.TempDir()
			manifestPath, messagesPath := installFixture(t, root, "session-alpha", manifest, messages)
			a := New(root)
			got, err := a.Discover(context.Background())
			if err != nil || len(got) != 1 {
				t.Fatal(err)
			}
			path, body := messagesPath, messages
			if replaceManifest {
				path, body = manifestPath, manifest
			}
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if r, err := a.Open(context.Background(), got[0]); err == nil {
				r.Close()
				t.Fatal("same-content replacement accepted")
			}
		})
	}
}

func TestDefaultRootIsStablePrivateCollection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	manifest, messages := fixture(t)
	installFixture(t, filepath.Join(home, ".cline", "data", "sessions"), "session-alpha", manifest, messages)
	got, err := New().Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].Scope.Type != source.ScopeProject || filepath.IsAbs(got[0].Scope.Root) {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}

func TestAuthorizationDoesNotCacheBodiesAndIDsSeparateRoots(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches body in %s", typ.Field(i).Name)
		}
	}
	manifest, messages := fixture(t)
	first, second := t.TempDir(), t.TempDir()
	installFixture(t, first, "session-alpha", manifest, messages)
	installFixture(t, second, "session-alpha", manifest, messages)
	got, err := New(first, second).Discover(context.Background())
	if err != nil || len(got) != 2 || got[0].ID == got[1].ID {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
