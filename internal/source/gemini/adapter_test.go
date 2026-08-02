package gemini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func installGemini(t *testing.T, root, project, name string, data []byte) string {
	t.Helper()
	dir := filepath.Join(root, "tmp", project, "chats")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openEvents(t *testing.T, a *Adapter, s source.Session) []map[string]any {
	t.Helper()
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []map[string]any
	dec := json.NewDecoder(r)
	for {
		var e map[string]any
		if err := dec.Decode(&e); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
}

func TestObjectAndStreamFixtures(t *testing.T) {
	root := t.TempDir()
	object := adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json")
	stream := adaptertest.ReadFixture(t, "../testdata/gemini/stream-v1.jsonl")
	installGemini(t, root, "project-alpha", "session-object.json", object)
	installGemini(t, root, "project-beta", "session-stream.jsonl", append(stream, []byte("{\"type\":\"gemini\"\n")...))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	byFormat := map[string]source.Session{}
	for _, s := range got {
		byFormat[s.FormatVersion] = s
	}
	obj, ok1 := byFormat["object-v1"]
	str, ok2 := byFormat["stream-v1"]
	if !ok1 || !ok2 {
		t.Fatalf("formats=%#v", byFormat)
	}
	if obj.Product != "gemini-cli" || obj.MessageCount != 2 || obj.MalformedCount != 0 {
		t.Fatalf("object=%#v", obj)
	}
	if str.MessageCount != 2 || str.MalformedCount != 1 {
		t.Fatalf("stream=%#v", str)
	}
	if !slices.Contains(obj.Capabilities, source.CapabilityTools) || !slices.Contains(obj.Capabilities, source.CapabilityReasoning) {
		t.Fatalf("caps=%#v", obj.Capabilities)
	}
	events := openEvents(t, a, obj)
	want := []string{"message", "message", "message", "tool_use", "tool_result"}
	var types []string
	for _, e := range events {
		types = append(types, e["type"].(string))
		adaptertest.AssertNoPrivateFields(t, e)
	}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("types=%#v", types)
	}
	streamEvents := openEvents(t, a, str)
	if len(streamEvents) != 3 || streamEvents[1]["content"] == "superseded answer" {
		t.Fatalf("last-wins events=%#v", streamEvents)
	}
}

func TestProjectMappingAndAntigravityExclusion(t *testing.T) {
	root := t.TempDir()
	body := adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json")
	projectPath := "/synthetic/garden"
	sum := sha256.Sum256([]byte(projectPath))
	hash := hex.EncodeToString(sum[:])
	installGemini(t, root, hash, "session-object.json", body)
	installGemini(t, root, "antigravity", "session-object.json", body)
	if err := os.WriteFile(filepath.Join(root, "projects.json"), []byte(`{"projects":{"/synthetic/garden":"garden"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if strings.Contains(got[0].Scope.Root+got[0].Scope.Label+got[0].ID, hash) || filepath.IsAbs(got[0].Scope.Root) {
		t.Fatalf("private project leaked: %#v", got[0])
	}
	if got[0].Scope.Label != "garden" {
		t.Fatalf("mapping not applied: %#v", got[0].Scope)
	}
}

func TestGeminiMalformedPairsAndMetadata(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		`{"sessionId":"pair","type":"metadata","future":{"ok":true}}`,
		`{"sessionId":"pair","id":"u","type":"user","content":"start"}`,
		`{"sessionId":"pair","id":"bad","type":"gemini","toolCalls":[{"id":"c1","name":"one","args":{}},{"id":"c1","name":"two","args":{}}]}`,
		`{"sessionId":"pair","id":"ok","type":"gemini","content":"done"}`,
	}, "\n") + "\n"
	installGemini(t, root, "project", "session-pair.jsonl", []byte(body))
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 || got[0].MalformedCount != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
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

func TestGeminiLimitsCancelAndDynamicDirectoryCaps(t *testing.T) {
	t.Run("record-limit", func(t *testing.T) {
		root := t.TempDir()
		var b strings.Builder
		b.WriteString(`{"sessionId":"limit","type":"metadata"}` + "\n")
		for i := 0; i < maxSessionRecords+1; i++ {
			b.WriteString(`{"sessionId":"limit","type":"user","content":"x"}` + "\n")
		}
		installGemini(t, root, "project", "session-limit.jsonl", []byte(b.String()))
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		root := t.TempDir()
		installGemini(t, root, "project", "session-object.json", adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json"))
		got, err := New(root).Discover(&cancelContext{Context: context.Background(), after: 5})
		if !errors.Is(err, context.Canceled) || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("directory-cap", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "tmp"), 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i <= maxDirectoryEntries; i++ {
			if err := os.Mkdir(filepath.Join(root, "tmp", fmt.Sprintf("project-%04d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := New(root).Discover(context.Background()); err == nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
}

func TestGeminiAuthorizationTamperAndRootSwap(t *testing.T) {
	root := t.TempDir()
	body := adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json")
	path := installGemini(t, root, "project", "session-object.json", body)
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
	if r, err := New(root).Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("cross-instance accepted")
	}
	if err := os.WriteFile(path, append(body, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("tamper accepted")
	}
}

func TestGeminiAuthorizationDoesNotCacheBody(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches slice %s", typ.Field(i).Name)
		}
	}
}

func TestGeminiAdapterContracts(t *testing.T) {
	fixture := adaptertest.ReadFixture(t, "../testdata/gemini/stream-v1.jsonl")
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, data []byte) error {
		dir := filepath.Join(root, "tmp", "synthetic-project", "chats")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "session-stream.jsonl"), data, 0o600)
	}, fixture)

	root := t.TempDir()
	path := installGemini(t, root, "synthetic-project", "session-stream.jsonl", fixture)
	a, other := New(root), New(root)
	forged, err := other.Discover(context.Background())
	if err != nil || len(forged) != 1 {
		t.Fatalf("forged discovery sessions=%#v err=%v", forged, err)
	}
	adaptertest.AuthorizationContract(t, a, func() {
		if err := os.WriteFile(path, append(fixture, ' '), 0o600); err != nil {
			t.Fatal(err)
		}
	}, forged[0])
}

func TestGeminiRejectsSymlinksAndRootSwap(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "gemini")
	body := adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json")
	installGemini(t, root, "project", "session-object.json", body)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if err := os.Rename(root, filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	installGemini(t, root, "project", "session-object.json", body)
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("replacement root accepted")
	}

	outside := t.TempDir()
	installGemini(t, outside, "linked", "session-object.json", body)
	linkedRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(linkedRoot, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "tmp", "linked"), filepath.Join(linkedRoot, "tmp", "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if sessions, err := New(linkedRoot).Discover(context.Background()); err != nil || len(sessions) != 0 {
		t.Fatalf("symlink sessions=%#v err=%v", sessions, err)
	}
}

func TestGeminiOpenRejectsSameByteFileSwap(t *testing.T) {
	root := t.TempDir()
	body := adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json")
	path := installGemini(t, root, "project", "session-object.json", body)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("same-byte replacement accepted")
	}
}

func TestGeminiRootsDeduplicateWithoutIDCollisions(t *testing.T) {
	body := adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json")
	first, second := t.TempDir(), t.TempDir()
	installGemini(t, first, "project", "session-object.json", body)
	installGemini(t, second, "project", "session-object.json", body)
	got, err := New(first, first, second).Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID == got[1].ID {
		t.Fatalf("cross-root ID collision: %q", got[0].ID)
	}
}

func TestGeminiGlobalDirectoryCapSpansRoots(t *testing.T) {
	var roots []string
	for rootIndex := 0; rootIndex <= maxGlobalEntries/maxDirectoryEntries; rootIndex++ {
		root := t.TempDir()
		tmp := filepath.Join(root, "tmp")
		if err := os.Mkdir(tmp, 0o755); err != nil {
			t.Fatal(err)
		}
		for entry := 0; entry < maxDirectoryEntries; entry++ {
			if err := os.Mkdir(filepath.Join(tmp, fmt.Sprintf("project-%04d", entry)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		roots = append(roots, root)
	}
	if got, err := New(roots...).Discover(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
}

func TestGeminiCancellationInterruptsSingleCompositeRecord(t *testing.T) {
	parts := make([]string, 200)
	for i := range parts {
		parts[i] = `{"text":"synthetic"}`
	}
	body := []byte(`{"sessionId":"composite","messages":[{"type":"gemini","content":[` + strings.Join(parts, ",") + `]}]}`)
	ctx := &cancelContext{Context: context.Background(), after: 2}
	if _, ok := parse(ctx, body); ok {
		t.Fatal("canceled composite record accepted")
	}
}

func TestGeminiMappedScopesDoNotCollideOnLabel(t *testing.T) {
	body := adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json")
	var roots []string
	for _, projectPath := range []string{"/synthetic/project-one", "/synthetic/project-two"} {
		root := t.TempDir()
		sum := sha256.Sum256([]byte(projectPath))
		projectHash := hex.EncodeToString(sum[:])
		installGemini(t, root, projectHash, "session-object.json", body)
		mapping := []byte(fmt.Sprintf(`{"projects":{%q:"shared-label"}}`, projectPath))
		if err := os.WriteFile(filepath.Join(root, "projects.json"), mapping, 0o600); err != nil {
			t.Fatal(err)
		}
		roots = append(roots, root)
	}
	got, err := New(roots...).Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].Scope.Root == got[1].Scope.Root {
		t.Fatalf("mapped scope collision: %#v %#v", got[0].Scope, got[1].Scope)
	}
}

func TestGeminiSanitizesWindowsRootedPayloadKeysAndValues(t *testing.T) {
	cleaned := sanitizePayload(map[string]any{`\synthetic-key`: `\synthetic-value`}).(map[string]any)
	if _, exists := cleaned[`\synthetic-key`]; exists {
		t.Fatal("rooted map key preserved")
	}
	for _, value := range cleaned {
		if value == `\synthetic-value` {
			t.Fatal("rooted map value preserved")
		}
	}
}

func TestGeminiDeduplicatesCaseAliasesByRootIdentity(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(filepath.Dir(root), strings.ToUpper(filepath.Base(root)))
	left, leftErr := os.Stat(root)
	right, rightErr := os.Stat(alias)
	if leftErr != nil || rightErr != nil || !os.SameFile(left, right) || alias == root {
		t.Skip("filesystem has no distinct case alias")
	}
	installGemini(t, root, "project", "session-object.json", adaptertest.ReadFixture(t, "../testdata/gemini/object-v1.json"))
	got, err := New(root, alias).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
