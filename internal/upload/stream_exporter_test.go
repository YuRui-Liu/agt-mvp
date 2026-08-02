package upload

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

type fakeSessionOpener struct {
	data  map[string]string
	errAt string
	err   error
	opens []string
}

func (f *fakeSessionOpener) Open(_ context.Context, session source.Session) (io.ReadCloser, error) {
	f.opens = append(f.opens, session.ID)
	if session.ID == f.errAt {
		if f.err != nil {
			return nil, f.err
		}
		return nil, errors.New("/private/source failure")
	}
	return io.NopCloser(strings.NewReader(f.data[session.ID])), nil
}

func testScope() source.Scope {
	return source.Scope{
		Key: "scope-key", Type: source.ScopeProject, Label: "campus",
		Sessions: []source.Session{
			{ID: "codex:2", Product: "codex", FormatVersion: "v2", AdapterVersion: "1", Capabilities: []source.Capability{"tool"}},
			{ID: "claude-code:1", Product: "claude-code", FormatVersion: "v1", AdapterVersion: "2", Capabilities: []source.Capability{"message"}},
		},
	}
}

func TestBuildScopeStreamsInScopeOrderAndCanReopen(t *testing.T) {
	opener := &fakeSessionOpener{data: map[string]string{
		"codex:2":       `{"z":2,"a":"two"}` + "\n",
		"claude-code:1": `{"message":"one"}` + "\n",
	}}
	exporter := NewStreamExporter(opener, Client{Name: "kuai", Version: "1", Platform: "test"}, Limits{})
	exporter.now = func() time.Time { return time.Unix(0, 0) }
	artifact, err := exporter.BuildScope(context.Background(), testScope())
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Remove()
	if strings.Join(opener.opens, ",") != "codex:2,claude-code:1" {
		t.Fatalf("open order=%v", opener.opens)
	}
	first := readArtifact(t, artifact)
	second := readArtifact(t, artifact)
	if first != second || strings.Index(first, "codex:2") > strings.Index(first, "claude-code:1") {
		t.Fatalf("unstable artifact: %s", first)
	}
	if artifact.SchemaVersion != 2 || artifact.SessionCount != 2 || artifact.Bytes != int64(len(first)) || len(artifact.Digest) != 64 {
		t.Fatalf("metadata=%+v", artifact)
	}
	want, err := CanonicalBytes(Package{
		SchemaVersion: 2,
		Client:        Client{Name: "kuai", Version: "1", Platform: "test"},
		Scope:         Scope{Type: "project", Key: "scope-key", Label: "campus"},
		Sessions: []Session{
			{ID: "codex:2", Source: Source{Product: "codex", FormatVersion: "v2", AdapterVersion: "1", Capabilities: []string{"tool"}}, Events: []map[string]any{{"z": json.Number("2"), "a": "two"}}},
			{ID: "claude-code:1", Source: Source{Product: "claude-code", FormatVersion: "v1", AdapterVersion: "2", Capabilities: []string{"message"}}, Events: []map[string]any{{"message": "one"}}},
		},
		CreatedAt: time.Unix(0, 0),
	})
	if err != nil || first != string(want) {
		t.Fatalf("stream differs from canonical bytes: err=%v\nstream=%s\nwant=%s", err, first, want)
	}
}

func TestBuildScopeCountsEachRemovedFieldOnce(t *testing.T) {
	scope := testScope()
	scope.Sessions = scope.Sessions[:1]
	opener := &fakeSessionOpener{data: map[string]string{
		"codex:2": `{"cwd":"/Users/alice/project","message":"keep"}` + "\n",
	}}
	artifact, err := NewStreamExporter(opener, Client{}, Limits{}).BuildScope(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Remove()
	var wire struct {
		Redaction struct {
			RemovedFields int `json:"removed_fields"`
		} `json:"redaction"`
	}
	if err := json.Unmarshal([]byte(readArtifact(t, artifact)), &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Redaction.RemovedFields != 1 {
		t.Fatalf("removed_fields=%d want 1", wire.Redaction.RemovedFields)
	}
}

func TestBuildScopeOmitsCanonicalReadResultAcrossJSONLLines(t *testing.T) {
	scope := testScope()
	scope.Sessions = scope.Sessions[:1]
	opener := &fakeSessionOpener{data: map[string]string{
		"codex:2": strings.Join([]string{
			`{"type":"tool_use","call_id":"read-1","tool":{"name":"Read","arguments":{"path":"/private/source.txt"}}}`,
			`{"type":"tool_result","call_id":"read-1","content":"COMPLETE PRIVATE FILE"}`,
		}, "\n") + "\n",
	}}

	artifact, err := NewStreamExporter(opener, Client{}, Limits{}).BuildScope(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Remove()
	body := readArtifact(t, artifact)
	if strings.Contains(body, "COMPLETE PRIVATE FILE") || !strings.Contains(body, omittedFileContent) {
		t.Fatalf("canonical Read result leaked across JSONL lines: %s", body)
	}
}

func TestBuildScopeFiltersCanonicalEventsAcrossProducts(t *testing.T) {
	scope := source.Scope{
		Key: "selected-project", Type: source.ScopeProject, Label: "selected",
		Sessions: []source.Session{
			{ID: "codex:selected", Product: "codex", Capabilities: []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}},
			{ID: "qoder:selected", Product: "qoder-cli", Capabilities: []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}},
		},
	}
	opener := &fakeSessionOpener{data: map[string]string{
		"codex:selected": strings.Join([]string{
			`{"type":"message","role":"user","text":"open /Users/alice/private.go and call 13812345678. token sk-proj-abcdefghijklmnopqrstuvwxyz"}`,
			`{"type":"thinking","text":"nested","payload":{"items":[{"text":"C:\\Users\\alice\\secret.txt"}]}}`,
			`{"type":"tool_use","call_id":"read-1","tool":{"name":"Read","arguments":{"path":"/Users/alice/private.go"}}}`,
			`{"type":"tool_result","call_id":"read-1","content":"PRIVATE FILE BODY"}`,
		}, "\n") + "\n",
		"qoder:selected": strings.Join([]string{
			`{"type":"tool_use","call_id":"write-1","tool":{"name":"apply_patch"}}`,
			`{"type":"tool_result","call_id":"write-1","content":"diff --git a/a b/a"}`,
		}, "\n") + "\n",
	}}

	artifact, err := NewStreamExporter(opener, Client{}, Limits{}).BuildScope(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Remove()
	body := readArtifact(t, artifact)
	for _, secret := range []string{
		"/Users/alice/private.go", `C:\\Users\\alice\\secret.txt`, "13812345678",
		"sk-proj-abcdefghijklmnopqrstuvwxyz", "PRIVATE FILE BODY",
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("export leaked %q: %s", secret, body)
		}
	}
	for _, retained := range []string{`"type":"message"`, `"type":"thinking"`, `"type":"tool_use"`, `"type":"tool_result"`, "diff --git a/a b/a"} {
		if !strings.Contains(body, retained) {
			t.Fatalf("export lost canonical semantic value %q: %s", retained, body)
		}
	}
	if !reflect.DeepEqual(opener.opens, []string{"codex:selected", "qoder:selected"}) {
		t.Fatalf("opened sessions=%v", opener.opens)
	}
}

func TestBuildScopeFailureReturnsNoArtifactAndCleansTemporaryFile(t *testing.T) {
	tempDir := t.TempDir()
	opener := &fakeSessionOpener{
		data:  map[string]string{"codex:2": "{}\n"},
		errAt: "claude-code:1",
	}
	exporter := NewStreamExporter(opener, Client{}, Limits{})
	exporter.tempDir = tempDir
	artifact, err := exporter.BuildScope(context.Background(), testScope())
	if err == nil || artifact != nil || strings.Contains(err.Error(), "private") {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
	entries, readErr := os.ReadDir(tempDir)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("temporary files=%v err=%v", entries, readErr)
	}
}

func TestBuildScopeFailureDestroysPartialPackageWhenRemovalIsBlocked(t *testing.T) {
	tempDir := t.TempDir()
	opener := &fakeSessionOpener{
		data:  map[string]string{"codex:2": `{"type":"message","text":"partial private value"}` + "\n"},
		errAt: "claude-code:1",
	}
	exporter := NewStreamExporter(opener, Client{}, Limits{})
	exporter.tempDir = tempDir
	exporter.remove = func(string) error { return errors.New("simulated sharing violation") }
	artifact, err := exporter.BuildScope(context.Background(), testScope())
	if err == nil || artifact != nil {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
	entries, readErr := os.ReadDir(tempDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		info, statErr := entry.Info()
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Size() != 0 {
			t.Fatalf("failed export retained %d bytes in %q", info.Size(), entry.Name())
		}
	}
}

func TestBuildScopeHonorsCancellationAndDualLimits(t *testing.T) {
	tests := []struct {
		name   string
		data   string
		limits Limits
	}{
		{"line", `{"long":"123456789"}` + "\n", Limits{MaxLineBytes: 8, MaxSessionBytes: 100, MaxPackageBytes: 1000}},
		{"session", `{"long":"123456789"}` + "\n", Limits{MaxLineBytes: 100, MaxSessionBytes: 8, MaxPackageBytes: 1000}},
		{"package", "{}\n", Limits{MaxLineBytes: 100, MaxSessionBytes: 100, MaxPackageBytes: 8}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope := testScope()
			scope.Sessions = scope.Sessions[:1]
			artifact, err := NewStreamExporter(&fakeSessionOpener{data: map[string]string{"codex:2": tt.data}}, Client{}, tt.limits).
				BuildScope(context.Background(), scope)
			if err == nil || artifact != nil {
				t.Fatalf("artifact=%v err=%v", artifact, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	opener := &fakeSessionOpener{}
	artifact, err := NewStreamExporter(opener, Client{}, Limits{}).BuildScope(ctx, testScope())
	if !errors.Is(err, context.Canceled) || artifact != nil || len(opener.opens) != 0 {
		t.Fatalf("artifact=%v err=%v opens=%v", artifact, err, opener.opens)
	}
}

func TestBuildScopePropagatesContextErrorsFromOpen(t *testing.T) {
	for _, contextErr := range []error{context.Canceled, context.DeadlineExceeded} {
		scope := testScope()
		scope.Sessions = scope.Sessions[:1]
		opener := &fakeSessionOpener{errAt: "codex:2", err: contextErr}
		artifact, err := NewStreamExporter(opener, Client{}, Limits{}).BuildScope(context.Background(), scope)
		if artifact != nil || !errors.Is(err, contextErr) {
			t.Fatalf("contextErr=%v artifact=%v err=%v", contextErr, artifact, err)
		}
	}
}

type cancelingSessionOpener struct {
	cancel func()
	err    error
}

func (o cancelingSessionOpener) Open(context.Context, source.Session) (io.ReadCloser, error) {
	return io.NopCloser(&cancelingReader{cancel: o.cancel, err: o.err}), nil
}

type cancelingReader struct {
	cancel func()
	read   bool
	err    error
}

func (r *cancelingReader) Read(buffer []byte) (int, error) {
	if !r.read {
		r.read = true
		n := copy(buffer, "{}\n")
		r.cancel()
		return n, nil
	}
	return 0, r.err
}

func TestBuildScopePropagatesCancellationAndTimeoutDuringRead(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() (context.Context, func(), error)
	}{
		{"canceled", func() (context.Context, func(), error) {
			ctx, cancel := context.WithCancel(context.Background())
			return ctx, cancel, context.Canceled
		}},
		{"deadline", func() (context.Context, func(), error) {
			return context.Background(), func() {}, context.DeadlineExceeded
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel, want := tt.ctx()
			defer cancel()
			scope := testScope()
			scope.Sessions = scope.Sessions[:1]
			artifact, err := NewStreamExporter(cancelingSessionOpener{
				cancel: cancel,
				err:    want,
			}, Client{}, Limits{}).BuildScope(ctx, scope)
			if artifact != nil || !errors.Is(err, want) {
				t.Fatalf("artifact=%v err=%v want=%v", artifact, err, want)
			}
		})
	}
}

type contextBlockingOpener struct{}

func (contextBlockingOpener) Open(ctx context.Context, _ source.Session) (io.ReadCloser, error) {
	return io.NopCloser(contextBlockingReader{ctx: ctx}), nil
}

type contextBlockingReader struct{ ctx context.Context }

func (r contextBlockingReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func TestBuildScopePropagatesActualDeadlineDuringRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	scope := testScope()
	scope.Sessions = scope.Sessions[:1]
	artifact, err := NewStreamExporter(contextBlockingOpener{}, Client{}, Limits{}).BuildScope(ctx, scope)
	if artifact != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("artifact=%v err=%v", artifact, err)
	}
}

type trackingOpener struct {
	line, active, maxActive int
}

func (o *trackingOpener) Open(context.Context, source.Session) (io.ReadCloser, error) {
	o.active++
	if o.active > o.maxActive {
		o.maxActive = o.active
	}
	body := `{"message":"` + strings.Repeat("x", o.line) + `"}` + "\n"
	return &trackedReadCloser{
		Reader: strings.NewReader(body),
		close:  func() { o.active-- },
	}, nil
}

type trackedReadCloser struct {
	io.Reader
	close func()
}

func (r *trackedReadCloser) Close() error {
	r.close()
	return nil
}

func TestBuildScopeLargePackageKeepsOnlyOneSessionStreamOpen(t *testing.T) {
	scope := source.Scope{Key: "large", Type: source.ScopeSessionCollection, Label: "large"}
	for index := 0; index < 80; index++ {
		scope.Sessions = append(scope.Sessions, source.Session{
			ID: "codex:" + strconv.Itoa(index), Product: "codex",
		})
	}
	opener := &trackingOpener{line: 64 << 10}
	artifact, err := NewStreamExporter(opener, Client{}, Limits{}).BuildScope(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Remove()
	if opener.maxActive != 1 || opener.active != 0 {
		t.Fatalf("stream concurrency max=%d active=%d", opener.maxActive, opener.active)
	}
	if artifact.Bytes < 5<<20 {
		t.Fatalf("artifact unexpectedly small: %d", artifact.Bytes)
	}
}

func TestArtifactConcurrentOpenCallsReturnIndependentReaders(t *testing.T) {
	artifact, err := NewStreamExporter(&fakeSessionOpener{data: map[string]string{
		"codex:2":       "{}\n",
		"claude-code:1": "{}\n",
	}}, Client{}, Limits{}).BuildScope(context.Background(), testScope())
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Remove()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reader, openErr := artifact.Open()
			if openErr != nil {
				errs <- openErr
				return
			}
			_, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil {
				errs <- readErr
			} else if closeErr != nil {
				errs <- closeErr
			}
		}()
	}
	wg.Wait()
	close(errs)
	for concurrentErr := range errs {
		t.Fatal(concurrentErr)
	}
}

func TestArtifactOpenAndRemoveAreCoordinated(t *testing.T) {
	artifact, err := NewStreamExporter(&fakeSessionOpener{data: map[string]string{
		"codex:2":       "{}\n",
		"claude-code:1": "{}\n",
	}}, Client{}, Limits{}).BuildScope(context.Background(), testScope())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := artifact.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Remove(); err != nil {
		_ = reader.Close()
		t.Fatal(err)
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("reader acquired before Remove became invalid: %v", err)
	}
	_ = reader.Close()
	if _, err := artifact.Open(); err == nil || strings.Contains(err.Error(), ".kuai-upload") {
		t.Fatalf("post-remove Open err=%v", err)
	}
}

func TestArtifactConcurrentOpenWaitsForRemoveState(t *testing.T) {
	artifact, err := NewStreamExporter(&fakeSessionOpener{data: map[string]string{
		"codex:2":       "{}\n",
		"claude-code:1": "{}\n",
	}}, Client{}, Limits{}).BuildScope(context.Background(), testScope())
	if err != nil {
		t.Fatal(err)
	}
	realRemove := artifact.remove
	removeEntered := make(chan struct{})
	allowRemove := make(chan struct{})
	artifact.remove = func(path string) error {
		close(removeEntered)
		<-allowRemove
		return realRemove(path)
	}
	removeDone := make(chan error, 1)
	go func() { removeDone <- artifact.Remove() }()
	<-removeEntered
	openDone := make(chan error, 1)
	go func() {
		reader, openErr := artifact.Open()
		if reader != nil {
			_ = reader.Close()
		}
		openDone <- openErr
	}()
	close(allowRemove)
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-openDone; err == nil || strings.Contains(err.Error(), ".kuai-upload") {
		t.Fatalf("Open racing with successful Remove err=%v", err)
	}
}

func TestArtifactConcurrentRemoveIsIdempotent(t *testing.T) {
	artifact, err := NewStreamExporter(&fakeSessionOpener{data: map[string]string{
		"codex:2":       "{}\n",
		"claude-code:1": "{}\n",
	}}, Client{}, Limits{}).BuildScope(context.Background(), testScope())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if removeErr := artifact.Remove(); removeErr != nil {
				errs <- removeErr
			}
		}()
	}
	wg.Wait()
	close(errs)
	for removeErr := range errs {
		t.Fatal(removeErr)
	}
}

func TestArtifactFailedRemoveKeepsPathForRetryWithoutLeakingIt(t *testing.T) {
	artifact, err := NewStreamExporter(&fakeSessionOpener{data: map[string]string{
		"codex:2":       "{}\n",
		"claude-code:1": "{}\n",
	}}, Client{}, Limits{}).BuildScope(context.Background(), testScope())
	if err != nil {
		t.Fatal(err)
	}
	realRemove := artifact.remove
	var calls atomic.Int32
	artifact.remove = func(path string) error {
		if calls.Add(1) == 1 {
			return errors.New("busy: " + path)
		}
		return realRemove(path)
	}
	if err := artifact.Remove(); err == nil || strings.Contains(err.Error(), ".kuai-upload") {
		t.Fatalf("first Remove err=%v", err)
	}
	reader, err := artifact.Open()
	if err != nil {
		t.Fatalf("failed Remove discarded path: %v", err)
	}
	_ = reader.Close()
	if err := artifact.Remove(); err != nil {
		t.Fatalf("retry Remove: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("remove calls=%d", calls.Load())
	}
}

func readArtifact(t *testing.T, artifact *Artifact) string {
	t.Helper()
	reader, err := artifact.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
