package copilotcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/internal/adaptertest"
)

const (
	uuidShared    = "11111111-1111-4111-8111-111111111111"
	uuidFlat      = "22222222-2222-4222-8222-222222222222"
	uuidDirectory = "33333333-3333-4333-8333-333333333333"
	uuidCWD       = "44444444-4444-4444-8444-444444444444"
	uuidTools     = "55555555-5555-4555-8555-555555555555"
	uuidLimit     = "66666666-6666-4666-8666-666666666666"
	uuidFixture   = "77777777-7777-4777-8777-777777777777"
	uuidOutside   = "88888888-8888-4888-8888-888888888888"
	uuidSame      = "99999999-9999-4999-8999-999999999999"
	uuidComposite = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

func installFlat(t *testing.T, root, id string, data []byte) string {
	t.Helper()
	d := filepath.Join(root, "session-state")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, id+".jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
func installDirectory(t *testing.T, root, id string, data []byte) string {
	t.Helper()
	d := filepath.Join(root, "session-state", id)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(d, "events.jsonl")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
func copilotEvents(t *testing.T, a *Adapter, s source.Session) []map[string]any {
	t.Helper()
	r, err := a.Open(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out []map[string]any
	d := json.NewDecoder(r)
	for {
		var e map[string]any
		if err := d.Decode(&e); err == io.EOF {
			return out
		} else if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
}

func TestFlatDirectoryPriorityAndCapabilities(t *testing.T) {
	root := t.TempDir()
	flat := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	dir := adaptertest.ReadFixture(t, "../testdata/copilotcli/directory-v2/events.jsonl")
	installFlat(t, root, uuidShared, flat)
	installDirectory(t, root, uuidShared, dir)
	installFlat(t, root, uuidFlat, flat)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	byFormat := map[string]source.Session{}
	for _, s := range got {
		byFormat[s.FormatVersion] = s
	}
	if byFormat["directory-v2"].OpaqueRef == "" || byFormat["flat-v1"].OpaqueRef == "" {
		t.Fatalf("formats=%#v", byFormat)
	}
	flatSession := byFormat["flat-v1"]
	if flatSession.Product != "copilot-cli" || !slices.Contains(flatSession.Capabilities, source.CapabilityTools) || !slices.Contains(flatSession.Capabilities, source.CapabilityReasoning) {
		t.Fatalf("session=%#v", flatSession)
	}
	events := copilotEvents(t, a, flatSession)
	var types []string
	for _, e := range events {
		types = append(types, e["type"].(string))
		adaptertest.AssertNoPrivateFields(t, e)
	}
	if !reflect.DeepEqual(types, []string{"message", "message", "message", "tool_use", "tool_result"}) {
		t.Fatalf("types=%#v", types)
	}
}

func TestDirectoryPriorityIndependentOfCreationOrder(t *testing.T) {
	root := t.TempDir()
	flat := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	directory := adaptertest.ReadFixture(t, "../testdata/copilotcli/directory-v2/events.jsonl")
	installDirectory(t, root, uuidShared, directory)
	installFlat(t, root, uuidShared, flat)
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].FormatVersion != "directory-v2" {
		t.Fatalf("format=%q", got[0].FormatVersion)
	}
}

func TestUUIDCaseNormalizesPriorityAndStableID(t *testing.T) {
	flat := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	directory := adaptertest.ReadFixture(t, "../testdata/copilotcli/directory-v2/events.jsonl")
	for _, directoryFirst := range []bool{false, true} {
		root := t.TempDir()
		if directoryFirst {
			installDirectory(t, root, strings.ToLower(uuidComposite), directory)
			installFlat(t, root, strings.ToUpper(uuidComposite), flat)
		} else {
			installFlat(t, root, strings.ToUpper(uuidComposite), flat)
			installDirectory(t, root, strings.ToLower(uuidComposite), directory)
		}
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 1 || got[0].FormatVersion != "directory-v2" {
			t.Fatalf("directoryFirst=%v sessions=%#v err=%v", directoryFirst, got, err)
		}
	}

	root := t.TempDir()
	upperPath := installFlat(t, root, strings.ToUpper(uuidComposite), flat)
	a := New(root)
	first, err := a.Discover(context.Background())
	if err != nil || len(first) != 1 {
		t.Fatalf("upper sessions=%#v err=%v", first, err)
	}
	reader, err := a.Open(context.Background(), first[0])
	if err != nil {
		t.Fatalf("open uppercase UUID session: %v", err)
	}
	reader.Close()
	lowerPath := filepath.Join(filepath.Dir(upperPath), strings.ToLower(uuidComposite)+".jsonl")
	if err := os.Rename(upperPath, lowerPath); err != nil {
		t.Fatal(err)
	}
	second, err := New(root).Discover(context.Background())
	if err != nil || len(second) != 1 {
		t.Fatalf("lower sessions=%#v err=%v", second, err)
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("UUID case changed ID: %q != %q", first[0].ID, second[0].ID)
	}
}

func TestNonUUIDSessionNamesAreIgnored(t *testing.T) {
	root := t.TempDir()
	fixture := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	installFlat(t, root, "not-a-uuid", fixture)
	installDirectory(t, root, "also-not-a-uuid", fixture)
	got, err := New(root).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("non-UUID sessions accepted: %#v", got)
	}
}

func TestUnknownMetadataCannotReplaceRecognizedEventWithSameID(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"11111111-1111-4111-8111-111111111111"}}`,
		`{"id":"event-1","type":"user.message","data":{"content":"kept message"}}`,
		`{"id":"event-1","type":"future.metadata","data":{"content":"must not replace"}}`,
		`{"id":"event-bad","type":"user.message","data":{"content":42}}`,
	}, "\n") + "\n"
	installFlat(t, root, uuidShared, []byte(body))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].MalformedCount != 1 {
		t.Fatalf("session=%#v", got[0])
	}
	events := copilotEvents(t, a, got[0])
	if len(events) != 1 || events[0]["content"] != "kept message" {
		t.Fatalf("events=%#v", events)
	}
}

func TestCWDOnlyFromValidEnvelopeAndNoAbsoluteScope(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		`{"type":"session.start","data":{"context":{"cwd":"relative/project"}}}`,
		`{"type":"session.start","data":{"sessionId":"44444444-4444-4444-8444-444444444444","context":{"cwd":"/synthetic/project"}}}`,
		`{"type":"user.message","data":{"content":"hello"}}`,
	}, "\n") + "\n"
	installFlat(t, root, uuidCWD, []byte(body))
	got, err := New(root).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if filepath.IsAbs(got[0].Scope.Root) || got[0].Scope.Label != "project" {
		t.Fatalf("scope=%#v", got[0].Scope)
	}
}

func TestToolPairingRollbackPartialTailAndMetadata(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"55555555-5555-4555-8555-555555555555"}}`,
		`{"type":"user.message","data":{"content":"start"}}`,
		`{"type":"future.metadata","data":{"path":"ignored"}}`,
		`{"type":"assistant.message","data":{"toolRequests":[{"toolCallId":"c1","name":"one","arguments":{}},{"toolCallId":"c2","name":"two","arguments":{}}]}}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"c1","result":{}},"extra":{"ok":true}}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"orphan","result":{}}}`,
		`{"type":"assistant.message","data":{"toolRequests":[{"toolCallId":"c2","name":"duplicate","arguments":{}}]}}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"c2","name":"mismatch","result":{}}}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"c2","result":{}}}`,
		`{"type":"assistant.message"`,
	}, "\n") + "\n"
	installDirectory(t, root, uuidTools, []byte(body))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].MalformedCount != 4 {
		t.Fatalf("session=%#v", got[0])
	}
	events := copilotEvents(t, a, got[0])
	results := 0
	for _, e := range events {
		if e["type"] == "tool_result" {
			results++
		}
	}
	if results != 2 {
		t.Fatalf("results=%d events=%#v", results, events)
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

func TestCopilotLimitsCancelAndDirectoryCaps(t *testing.T) {
	t.Run("record-limit", func(t *testing.T) {
		root := t.TempDir()
		var b strings.Builder
		b.WriteString(`{"type":"session.start","data":{"sessionId":"66666666-6666-4666-8666-666666666666"}}` + "\n")
		for i := 0; i < maxSessionRecords+1; i++ {
			fmt.Fprintf(&b, `{"type":"user.message","data":{"content":"%d"}}`+"\n", i)
		}
		installFlat(t, root, uuidLimit, []byte(b.String()))
		got, err := New(root).Discover(context.Background())
		if err != nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		root := t.TempDir()
		installFlat(t, root, uuidFlat, adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl"))
		got, err := New(root).Discover(&cancelContext{Context: context.Background(), after: 5})
		if !errors.Is(err, context.Canceled) || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
	t.Run("directory-cap", func(t *testing.T) {
		root := t.TempDir()
		d := filepath.Join(root, "session-state")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i <= maxDirectoryEntries; i++ {
			if err := os.Mkdir(filepath.Join(d, fmt.Sprintf("session-%04d", i)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := New(root).Discover(context.Background()); err == nil || len(got) != 0 {
			t.Fatalf("sessions=%d err=%v", len(got), err)
		}
	})
}

func TestCopilotAuthorizationTamperAndRootSwap(t *testing.T) {
	root := t.TempDir()
	body := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	path := installFlat(t, root, uuidFlat, body)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	forged := got[0]
	forged.MalformedCount++
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

func TestCopilotAuthorizationDoesNotCacheBody(t *testing.T) {
	typ := reflect.TypeOf(authorization{})
	for i := 0; i < typ.NumField(); i++ {
		if typ.Field(i).Type.Kind() == reflect.Slice {
			t.Fatalf("authorization caches slice %s", typ.Field(i).Name)
		}
	}
}

func TestCopilotAdapterContracts(t *testing.T) {
	fixture := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	adaptertest.SafetyContract(t, func(root string) source.Adapter { return New(root) }, func(root string, data []byte) error {
		dir := filepath.Join(root, "session-state")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, uuidFixture+".jsonl"), data, 0o600)
	}, fixture)

	root := t.TempDir()
	path := installFlat(t, root, uuidFixture, fixture)
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

func TestCopilotRejectsSymlinksAndRootSwap(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "copilot")
	body := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	installFlat(t, root, uuidFixture, body)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if err := os.Rename(root, filepath.Join(base, "original")); err != nil {
		t.Fatal(err)
	}
	installFlat(t, root, uuidFixture, body)
	if r, err := a.Open(context.Background(), got[0]); err == nil {
		r.Close()
		t.Fatal("replacement root accepted")
	}

	outside := t.TempDir()
	outsidePath := installFlat(t, outside, uuidOutside, body)
	linkedRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(linkedRoot, "session-state"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(linkedRoot, "session-state", uuidOutside+".jsonl")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if sessions, err := New(linkedRoot).Discover(context.Background()); err != nil || len(sessions) != 0 {
		t.Fatalf("symlink sessions=%#v err=%v", sessions, err)
	}
}

func TestCopilotOpenRejectsSameByteFileSwap(t *testing.T) {
	root := t.TempDir()
	body := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	path := installFlat(t, root, uuidFixture, body)
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

func TestCopilotRootsDeduplicateWithoutIDCollisions(t *testing.T) {
	body := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	first, second := t.TempDir(), t.TempDir()
	installFlat(t, first, uuidSame, body)
	installFlat(t, second, uuidSame, body)
	got, err := New(first, first, second).Discover(context.Background())
	if err != nil || len(got) != 2 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	if got[0].ID == got[1].ID {
		t.Fatalf("cross-root ID collision: %q", got[0].ID)
	}
}

func TestCopilotGlobalDirectoryCapSpansRoots(t *testing.T) {
	var roots []string
	for rootIndex := 0; rootIndex <= maxGlobalEntries/maxDirectoryEntries; rootIndex++ {
		root := t.TempDir()
		state := filepath.Join(root, "session-state")
		if err := os.Mkdir(state, 0o755); err != nil {
			t.Fatal(err)
		}
		for entry := 0; entry < maxDirectoryEntries; entry++ {
			if err := os.Mkdir(filepath.Join(state, fmt.Sprintf("session-%04d", entry)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		roots = append(roots, root)
	}
	if got, err := New(roots...).Discover(context.Background()); err == nil || len(got) != 0 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
}

func TestCopilotCancellationInterruptsSingleCompositeRecord(t *testing.T) {
	requests := make([]string, 200)
	for i := range requests {
		requests[i] = fmt.Sprintf(`{"toolCallId":"call-%d","name":"lookup","arguments":{}}`, i)
	}
	body := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}`,
		`{"type":"assistant.message","data":{"toolRequests":[` + strings.Join(requests, ",") + `]}}`,
	}, "\n") + "\n"
	ctx := &cancelContext{Context: context.Background(), after: 6}
	if _, ok := parse(ctx, []byte(body)); ok {
		t.Fatal("canceled composite record accepted")
	}
}

func TestCopilotSanitizesWindowsRootedPayloadKeysAndValues(t *testing.T) {
	raw, ok := sanitizePayload(context.Background(), map[string]any{`\synthetic-key`: `\synthetic-value`})
	if !ok {
		t.Fatal("sanitization canceled")
	}
	cleaned := raw.(map[string]any)
	if _, exists := cleaned[`\synthetic-key`]; exists {
		t.Fatal("rooted map key preserved")
	}
	for _, value := range cleaned {
		if value == `\synthetic-value` {
			t.Fatal("rooted map value preserved")
		}
	}
}

func TestCopilotRootedMapKeysAreDistinctDeterministicAndCollisionSafe(t *testing.T) {
	input := map[string]any{
		`\alpha`: map[string]any{`\nested`: "one"},
		`\beta`:  "two",
	}
	firstRaw, ok := sanitizePayload(context.Background(), input)
	if !ok {
		t.Fatal("distinct rooted keys rejected")
	}
	secondRaw, ok := sanitizePayload(context.Background(), input)
	if !ok {
		t.Fatal("repeat sanitization rejected")
	}
	first, second := firstRaw.(map[string]any), secondRaw.(map[string]any)
	if len(first) != 2 || !reflect.DeepEqual(first, second) {
		t.Fatalf("sanitized maps differ: %#v %#v", first, second)
	}
	for key, value := range first {
		if strings.Contains(key, `\alpha`) || strings.Contains(key, `\beta`) {
			t.Fatalf("rooted key leaked: %q", key)
		}
		if nested, ok := value.(map[string]any); ok {
			for nestedKey := range nested {
				if strings.Contains(nestedKey, `\nested`) {
					t.Fatalf("nested rooted key leaked: %q", nestedKey)
				}
			}
		}
	}
	original := `\alpha`
	target := "[redacted-path-key:" + digestPrefix(original, 16) + "]"
	if _, ok := sanitizePayload(context.Background(), map[string]any{original: "one", target: "literal"}); ok {
		t.Fatal("target-key collision accepted")
	}
}

func TestCopilotOpenIsStableWithMultipleRootedKeys(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"11111111-1111-4111-8111-111111111111"}}`,
		`{"type":"user.message","data":{"content":"question"}}`,
		`{"type":"assistant.message","data":{"toolRequests":[{"toolCallId":"call-1","name":"lookup","arguments":{"\\alpha":"one","\\beta":{"\\nested":"two"}}}]}}`,
	}, "\n") + "\n"
	installFlat(t, root, uuidShared, []byte(body))
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	read := func() string {
		r, err := a.Open(context.Background(), got[0])
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	first, second := read(), read()
	if first != second || strings.Contains(first, `\\alpha`) || strings.Contains(first, `\\beta`) || strings.Contains(first, `\\nested`) {
		t.Fatalf("unstable or private output: %q %q", first, second)
	}
}

func TestCopilotTraversalHelpersHonorCancellation(t *testing.T) {
	deep := make([]any, 1024)
	for i := range deep {
		deep[i] = map[string]any{"value": "synthetic"}
	}
	ctx := &cancelContext{Context: context.Background(), after: 3}
	if _, ok := sanitizePayload(ctx, deep); ok {
		t.Fatal("canceled deep payload accepted")
	}
	depthCtx := &cancelContext{Context: context.Background(), after: 3}
	if jsonDepthOK(depthCtx, []byte(strings.Repeat(" ", 20<<10)+"[]"), maxJSONDepth) {
		t.Fatal("canceled JSON depth scan accepted")
	}
}

func TestCopilotLargeNestedCancellationDoesNotAuthorize(t *testing.T) {
	root := t.TempDir()
	values := strings.Repeat(`"synthetic",`, 70000) + `"synthetic"`
	body := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}}`,
		`{"type":"assistant.message","data":{"toolRequests":[{"toolCallId":"call-1","name":"lookup","arguments":{"values":[` + values + `]}}]}}`,
	}, "\n") + "\n"
	installFlat(t, root, uuidComposite, []byte(body))
	a := New(root)
	ctx := &cancelContext{Context: context.Background(), after: 100}
	got, err := a.Discover(ctx)
	if !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("sessions=%d err=%v", len(got), err)
	}
	if len(a.known) != 0 {
		t.Fatal("canceled discovery installed authorization")
	}
}

func TestCopilotFinalCancellationPreservesKnown(t *testing.T) {
	root := t.TempDir()
	fixture := adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl")
	installFlat(t, root, uuidFixture, fixture)
	a := New(root)
	got, err := a.Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
	before := a.known[got[0].OpaqueRef]
	if err := os.Rename(filepath.Join(root, "session-state"), filepath.Join(root, "session-state-before-cancel")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "session-state"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelContext{Context: context.Background(), after: 3}
	if sessions, err := a.Discover(ctx); !errors.Is(err, context.Canceled) || len(sessions) != 0 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
	after, exists := a.known[got[0].OpaqueRef]
	if !exists || len(a.known) != 1 || after.id != before.id || after.digest != before.digest || after.metadata != before.metadata {
		t.Fatal("canceled discovery replaced known snapshot")
	}
}

func TestCopilotRejectsTooManyRootsBeforeScan(t *testing.T) {
	roots := make([]string, maxRoots+1)
	for i := range roots {
		roots[i] = filepath.Join(t.TempDir(), "missing")
	}
	if sessions, err := New(roots...).Discover(context.Background()); err == nil || len(sessions) != 0 {
		t.Fatalf("sessions=%#v err=%v", sessions, err)
	}
}

func TestCopilotManyRootsUnderLowFDLimit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX ulimit")
	}
	if os.Getenv("COPILOTCLI_LOW_FD_CHILD") == "1" {
		before := openFDCount(t)
		if sessions, err := New(filepath.SplitList(os.Getenv("COPILOTCLI_LOW_FD_ROOTS"))...).Discover(context.Background()); err != nil || len(sessions) != 0 {
			t.Fatalf("sessions=%#v err=%v", sessions, err)
		}
		after := openFDCount(t)
		if after > before+2 {
			t.Fatalf("file descriptors leaked: before=%d after=%d", before, after)
		}
		return
	}
	roots := make([]string, maxRoots-16)
	for i := range roots {
		roots[i] = t.TempDir()
		if err := os.Mkdir(filepath.Join(roots[i], "session-state"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("/bin/sh", "-c", `ulimit -n 32; exec "$TEST_BINARY" -test.run '^TestCopilotManyRootsUnderLowFDLimit$'`)
	cmd.Env = append(os.Environ(), "COPILOTCLI_LOW_FD_CHILD=1", "COPILOTCLI_LOW_FD_ROOTS="+strings.Join(roots, string(os.PathListSeparator)), "TEST_BINARY="+os.Args[0])
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("low-fd child failed: %v: %s", err, output)
	}
}

func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skipf("fd accounting unavailable: %v", err)
	}
	return len(entries)
}

func TestCopilotDeduplicatesCaseAliasesByRootIdentity(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(filepath.Dir(root), strings.ToUpper(filepath.Base(root)))
	left, leftErr := os.Stat(root)
	right, rightErr := os.Stat(alias)
	if leftErr != nil || rightErr != nil || !os.SameFile(left, right) || alias == root {
		t.Skip("filesystem has no distinct case alias")
	}
	installFlat(t, root, uuidSame, adaptertest.ReadFixture(t, "../testdata/copilotcli/flat-v1.jsonl"))
	got, err := New(root, alias).Discover(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions=%#v err=%v", got, err)
	}
}
