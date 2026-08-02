package webapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/service"
	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/catalog"
	"github.com/YuRui-Liu/agt-mvp/internal/upload"
)

type testAdapter struct{}

func (testAdapter) Product() string { return "codex" }
func (testAdapter) Capabilities() []source.Capability {
	return []source.Capability{"message", "tool"}
}

type changingAdapter struct {
	mu    sync.Mutex
	scans int
}

type stateAdapter struct {
	product string
	state   source.SourceState
	error   string
	session bool
}

func (a stateAdapter) Product() string { return a.product }
func (a stateAdapter) Capabilities() []source.Capability {
	return []source.Capability{source.CapabilityMessages}
}
func (a stateAdapter) Discover(context.Context) ([]source.Session, error) {
	switch a.state {
	case source.SourceFormatUnsupported, source.SourceExportRequired:
		return nil, source.NewDiscoveryError(a.state, errors.New(a.error))
	case source.SourceReadError:
		return nil, errors.New(a.error)
	}
	if !a.session {
		return nil, nil
	}
	return []source.Session{{ID: "private-session-id", Scope: source.ScopeRef{
		Type: source.ScopeProject, Root: "/private/project", Label: "project",
	}}}, nil
}
func (stateAdapter) Open(context.Context, source.Session) (io.ReadCloser, error) {
	return nil, io.EOF
}

func (a *changingAdapter) Product() string                   { return "codex" }
func (a *changingAdapter) Capabilities() []source.Capability { return []source.Capability{"message"} }
func (a *changingAdapter) Discover(context.Context) ([]source.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.scans++
	if a.scans > 1 {
		return nil, errors.New("source became unavailable")
	}
	return []source.Session{{ID: "one", Scope: source.ScopeRef{Type: source.ScopeProject, Root: "/safe/project", Label: "project"}}}, nil
}
func (a *changingAdapter) Open(context.Context, source.Session) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(`{"type":"message","role":"user","text":"hello"}` + "\n")), nil
}

type retryClient struct {
	service.Client
	mu                                                sync.Mutex
	createCalls, uploadCalls, completeCalls, getCalls int
	failUpload, failComplete, zeroCreatedAt           bool
}

func (c *retryClient) CreateUpload(ctx context.Context, auth service.AuthSession, metadata service.UploadMetadata) (service.UploadTarget, error) {
	c.mu.Lock()
	c.createCalls++
	c.mu.Unlock()
	return c.Client.CreateUpload(ctx, auth, metadata)
}
func (c *retryClient) Upload(ctx context.Context, auth service.AuthSession, target service.UploadTarget, body io.Reader) error {
	c.mu.Lock()
	c.uploadCalls++
	fail := c.failUpload
	c.failUpload = false
	c.mu.Unlock()
	if fail {
		return service.ErrRetryable
	}
	return c.Client.Upload(ctx, auth, target, body)
}
func (c *retryClient) CompleteUpload(ctx context.Context, auth service.AuthSession, id string, digest service.Digest) (service.Task, error) {
	c.mu.Lock()
	c.completeCalls++
	fail := c.failComplete
	c.failComplete = false
	c.mu.Unlock()
	if fail {
		return service.Task{}, service.ErrRetryable
	}
	task, err := c.Client.CompleteUpload(ctx, auth, id, digest)
	if c.zeroCreatedAt {
		task.CreatedAt = time.Time{}
	}
	return task, err
}
func (c *retryClient) GetTask(ctx context.Context, auth service.AuthSession, id string) (service.Task, error) {
	c.mu.Lock()
	c.getCalls++
	c.mu.Unlock()
	task, err := c.Client.GetTask(ctx, auth, id)
	if c.zeroCreatedAt {
		task.CreatedAt = time.Time{}
	}
	return task, err
}
func (testAdapter) Discover(context.Context) ([]source.Session, error) {
	return []source.Session{
		{ID: "private-session-one", Scope: source.ScopeRef{Type: source.ScopeProject, Root: "/Users/alice/work/atlas", Label: "atlas"}, StartedAt: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC), Usage: map[string]int64{"bytes": 128}},
		{ID: "private-session-two", Scope: source.ScopeRef{Type: source.ScopeProject, Root: "/Users/alice/work/atlas", Label: "atlas"}, StartedAt: time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC), EndedAt: time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC), Usage: map[string]int64{"bytes": 256}},
	}, nil
}
func (testAdapter) Open(_ context.Context, session source.Session) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(`{"type":"message","role":"user","text":"hello"}` + "\n")), nil
}

func newTestApp() *App {
	registry := source.NewRegistry(testAdapter{})
	return &App{
		LaunchToken: "launch-token",
		Registry:    registry,
		ScopeSecret: bytes.Repeat([]byte{3}, 32),
		Exporter:    upload.NewStreamExporter(registry, upload.Client{Name: "kuai", Version: "test", Platform: "test"}, upload.Limits{}),
		Service:     service.NewMockClient(service.MockOptions{}),
		ServiceMode: "mock",
	}
}

func call(t *testing.T, handler http.Handler, method, path, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("X-Kuai-Token", "launch-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestScopeAPIAndCompleteServiceFlow(t *testing.T) {
	handler := Handler(newTestApp())
	response := call(t, handler, http.MethodGet, "/api/scopes", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("scopes status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.Bytes()
	for _, secret := range [][]byte{[]byte("/Users/"), []byte("private-session"), []byte("alice")} {
		if bytes.Contains(body, secret) {
			t.Fatalf("scope response leaked %q: %s", secret, body)
		}
	}
	var scopes struct {
		Scopes []struct {
			Key, Type, Label     string
			Agents, Capabilities []string
			SessionCount         int  `json:"session_count"`
			Selectable           bool `json:"selectable"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(body, &scopes); err != nil || len(scopes.Scopes) != 1 {
		t.Fatalf("scopes=%s err=%v", body, err)
	}
	scope := scopes.Scopes[0]
	if scope.Type != "project" || scope.Label != "atlas" || scope.SessionCount != 2 || !scope.Selectable ||
		len(scope.Agents) != 1 || len(scope.Capabilities) != 2 {
		t.Fatalf("scope=%+v", scope)
	}

	response = call(t, handler, http.MethodPost, "/api/prepare", `{"scope_key":"`+scope.Key+`"}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("prepare status=%d body=%s", response.Code, response.Body.String())
	}
	var prepared struct {
		ID              string `json:"preparation_id"`
		SessionCount    int    `json:"session_count"`
		SessionProgress []struct {
			Status string `json:"status"`
		} `json:"session_progress"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &prepared); err != nil || prepared.ID == "" ||
		prepared.SessionCount != 2 || len(prepared.SessionProgress) != 2 {
		t.Fatalf("prepared=%s err=%v", response.Body.String(), err)
	}
	for _, progress := range prepared.SessionProgress {
		if progress.Status != "exported" {
			t.Fatalf("progress=%+v", progress)
		}
	}

	if got := call(t, handler, http.MethodPost, "/api/auth/request-code", `{"phone":"13800138000"}`, ""); got.Code != http.StatusNoContent {
		t.Fatalf("request OTP status=%d body=%s", got.Code, got.Body.String())
	}
	response = call(t, handler, http.MethodPost, "/api/auth/verify", `{"phone":"13800138000","code":"246810"}`, "")
	var auth struct {
		Bearer string `json:"bearer"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &auth) != nil || auth.Bearer == "" {
		t.Fatalf("verify status=%d body=%s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("13800138000")) || bytes.Contains(response.Body.Bytes(), []byte("246810")) {
		t.Fatalf("auth response leaked phone or OTP: %s", response.Body.String())
	}

	withoutConsent := call(t, handler, http.MethodPost, "/api/tasks",
		`{"preparation_id":"`+prepared.ID+`","idempotency_key":"retry-key-123456"}`, auth.Bearer)
	if withoutConsent.Code != http.StatusBadRequest {
		t.Fatalf("task passed consent gate: %d %s", withoutConsent.Code, withoutConsent.Body.String())
	}
	if got := call(t, handler, http.MethodPost, "/api/consent", `{"version":"kuai-consent-v1"}`, auth.Bearer); got.Code != http.StatusNoContent {
		t.Fatalf("consent status=%d body=%s", got.Code, got.Body.String())
	}
	response = call(t, handler, http.MethodPost, "/api/tasks",
		`{"preparation_id":"`+prepared.ID+`","idempotency_key":"retry-key-123456"}`, auth.Bearer)
	if response.Code != http.StatusOK {
		t.Fatalf("task status=%d body=%s", response.Code, response.Body.String())
	}
	var receipt struct {
		ReceiptID   string    `json:"receipt_id"`
		SubmittedAt time.Time `json:"submitted_at"`
		Status      string    `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("receipt=%s err=%v", response.Body.String(), err)
	}
	if !strings.HasPrefix(receipt.ReceiptID, "KW-") || len(receipt.ReceiptID) != len("KW-")+32 {
		t.Fatalf("receipt id is not a 128-bit opaque value: %q", receipt.ReceiptID)
	}
	if receipt.SubmittedAt.IsZero() || receipt.Status != "submitted" {
		t.Fatalf("receipt=%#v", receipt)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 3 || fields["receipt_id"] == nil || fields["submitted_at"] == nil || fields["status"] == nil {
		t.Fatalf("task response must be a minimal receipt DTO, got %s", response.Body.String())
	}
	for _, forbidden := range []string{"id", "kuai_id", "analysis", "poster_url", "created_at", "updated_at", "scope", "scope_path"} {
		if _, ok := fields[forbidden]; ok {
			t.Fatalf("task response leaked %q: %s", forbidden, response.Body.String())
		}
	}
	for _, path := range []string{"/api/tasks/latest", "/api/tasks/internal-task", "/api/tasks/internal-task/poster"} {
		response = call(t, handler, http.MethodGet, path, "", auth.Bearer)
		if response.Code != http.StatusNotFound {
			t.Fatalf("submission-only route %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestScopesIncludeDisabledUnsupportedSourceDiagnostics(t *testing.T) {
	app := newTestApp()
	root := t.TempDir()
	app.Catalog = catalog.Detect(map[string][]string{"trae": {root}})
	response := call(t, Handler(app), http.MethodGet, "/api/scopes", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Scopes  []scopeView  `json:"scopes"`
		Sources []sourceView `json:"sources"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, scope := range result.Scopes {
		if len(scope.Agents) == 1 && scope.Agents[0] == "trae" {
			found = true
		}
	}
	if found {
		t.Fatalf("unsupported source was exposed as a fake assessment scope: %s", response.Body.String())
	}
	var trae *sourceView
	for i := range result.Sources {
		if result.Sources[i].Product == "trae" {
			trae = &result.Sources[i]
			break
		}
	}
	if trae == nil || trae.State != source.SourceDetectedUnsupported || trae.Status != source.SourceDetectedUnsupported ||
		trae.Selectable || !trae.Detected || trae.Verification != source.VerificationExport ||
		trae.Reason != "official_export_required" || trae.Capabilities == nil {
		t.Fatalf("TRAE source metadata=%#v", trae)
	}
	if strings.Contains(response.Body.String(), root) || strings.Contains(response.Body.String(), `"dirs"`) {
		t.Fatalf("source diagnostics exposed filesystem roots: %s", response.Body.String())
	}
}

func TestScopesMergeRuntimeSourceStateAndExposeOnlySafeMetadata(t *testing.T) {
	states := []source.SourceState{source.SourceReady, source.SourceNotFound, source.SourceFormatUnsupported,
		source.SourceReadError, source.SourceExportRequired}
	for _, runtimeState := range states {
		t.Run(string(runtimeState), func(t *testing.T) {
			adapter := stateAdapter{product: "codex", state: runtimeState, error: "private /Users/alice/session-id"}
			if runtimeState == source.SourceReady {
				adapter.session = true
			}
			registry := source.NewRegistry(adapter)
			app := newTestApp()
			app.Registry = registry
			app.Exporter = upload.NewStreamExporter(registry, upload.Client{Name: "kuai", Version: "test", Platform: "test"}, upload.Limits{})
			app.Catalog = []catalog.Definition{{
				Product: "codex", DisplayName: "Codex", Supported: true, Enabled: true,
				Status: source.SourceReady, Verification: source.VerificationMachine,
				Capabilities: []source.Capability{source.CapabilityMessages, source.CapabilityTools},
			}}
			response := call(t, Handler(app), http.MethodGet, "/api/scopes", "", "")
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var result struct {
				Scopes  []scopeView  `json:"scopes"`
				Sources []sourceView `json:"sources"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Sources) != 1 {
				t.Fatalf("result=%s err=%v", response.Body.String(), err)
			}
			got := result.Sources[0]
			wantCount := 0
			if runtimeState == source.SourceReady {
				wantCount = 1
			}
			if got.Product != "codex" || got.DisplayName != "Codex" || got.State != runtimeState ||
				got.Status != runtimeState || got.Verification != source.VerificationMachine ||
				!reflect.DeepEqual(got.Capabilities, []source.Capability{source.CapabilityMessages, source.CapabilityTools}) ||
				got.SessionCount != wantCount || got.Selectable != (runtimeState == source.SourceReady) ||
				got.Detected != (wantCount > 0) {
				t.Fatalf("source=%#v", got)
			}
			if runtimeState == source.SourceReady && len(result.Scopes) != 1 {
				t.Fatalf("ready session scope missing: %#v", result.Scopes)
			}
			if runtimeState != source.SourceReady && len(result.Scopes) != 0 {
				t.Fatalf("non-ready source created scope: %#v", result.Scopes)
			}
			for _, secret := range []string{"/Users/alice", "private-session-id", "private /"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatalf("response leaked %q: %s", secret, response.Body.String())
				}
			}
		})
	}
}

func TestScopesFailClosedWhenCatalogReadySourceHasNoRuntimeStatus(t *testing.T) {
	registry := source.NewRegistry(stateAdapter{product: "other-source", state: source.SourceReady, session: true})
	app := newTestApp()
	app.Registry = registry
	app.Exporter = upload.NewStreamExporter(registry, upload.Client{Name: "kuai", Version: "test", Platform: "test"}, upload.Limits{})
	app.Catalog = []catalog.Definition{{
		Product: "codex", DisplayName: "Codex", Supported: true, Enabled: true,
		Status: source.SourceReady, Verification: source.VerificationMachine,
		Capabilities: []source.Capability{source.CapabilityMessages},
	}}
	response := call(t, Handler(app), http.MethodGet, "/api/scopes", "", "")
	var result struct {
		Sources []sourceView `json:"sources"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Sources) != 1 {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	if got := result.Sources[0]; got.State != source.SourceNotFound || got.Selectable || got.SessionCount != 0 {
		t.Fatalf("catalog source failed open: %#v", got)
	}
}

func TestBootstrapContainsModeButNoSecrets(t *testing.T) {
	response := call(t, Handler(newTestApp()), http.MethodGet, "/", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("index status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"service_mode":"mock"`) {
		t.Fatalf("bootstrap missing service mode: %s", body)
	}
	for _, forbidden := range []string{"launch-token", "13800138000", `"bearer"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("bootstrap leaked %q", forbidden)
		}
	}
}

func TestMockApplicationClosure(t *testing.T) {
	handler := Handler(newTestApp())
	page := call(t, handler, http.MethodGet, "/application", "", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "模拟职位投递") {
		t.Fatalf("application page status=%d body=%s", page.Code, page.Body.String())
	}
	response := call(t, handler, http.MethodPost, "/api/applications",
		`{"name":"Candidate","email":"candidate@example.com","position":"Engineer"}`, "")
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"status":"accepted"`) {
		t.Fatalf("application status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPrepareRejectsScopeWhenSourceIsNoLongerReady(t *testing.T) {
	adapter := &changingAdapter{}
	registry := source.NewRegistry(adapter)
	app := newTestApp()
	app.Registry = registry
	app.Exporter = upload.NewStreamExporter(registry, upload.Client{Name: "kuai", Version: "test", Platform: "test"}, upload.Limits{})
	handler := Handler(app)
	response := call(t, handler, http.MethodGet, "/api/scopes", "", "")
	var result struct {
		Scopes []scopeView `json:"scopes"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &result) != nil || len(result.Scopes) != 1 {
		t.Fatalf("scope response=%d %s", response.Code, response.Body.String())
	}
	response = call(t, handler, http.MethodPost, "/api/prepare", `{"scope_key":"`+result.Scopes[0].Key+`"}`, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("prepare accepted stale non-ready source: %d %s", response.Code, response.Body.String())
	}
}

func TestUploadRetryReusesIdempotencyAndTargetAcrossFailures(t *testing.T) {
	base := service.NewMockClient(service.MockOptions{})
	client := &retryClient{Client: base, failUpload: true, zeroCreatedAt: true}
	app := newTestApp()
	app.Service = client
	handler := Handler(app)

	scopes := call(t, handler, http.MethodGet, "/api/scopes", "", "")
	var scopeResult struct {
		Scopes []scopeView `json:"scopes"`
	}
	_ = json.Unmarshal(scopes.Body.Bytes(), &scopeResult)
	preparedResponse := call(t, handler, http.MethodPost, "/api/prepare", `{"scope_key":"`+scopeResult.Scopes[0].Key+`"}`, "")
	var prepared struct {
		ID string `json:"preparation_id"`
	}
	_ = json.Unmarshal(preparedResponse.Body.Bytes(), &prepared)
	_ = call(t, handler, http.MethodPost, "/api/auth/request-code", `{"phone":"13800138000"}`, "")
	verified := call(t, handler, http.MethodPost, "/api/auth/verify", `{"phone":"13800138000","code":"246810"}`, "")
	var auth service.AuthSession
	_ = json.Unmarshal(verified.Body.Bytes(), &auth)
	_ = call(t, handler, http.MethodPost, "/api/consent", `{"version":"kuai-consent-v1"}`, auth.Bearer)
	payload := `{"preparation_id":"` + prepared.ID + `","idempotency_key":"persistent-key-123"}`

	first := call(t, handler, http.MethodPost, "/api/tasks", payload, auth.Bearer)
	if first.Code != http.StatusServiceUnavailable {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	client.mu.Lock()
	creates, uploads := client.createCalls, client.uploadCalls
	client.failComplete = true
	client.mu.Unlock()
	if creates != 1 || uploads != 1 {
		t.Fatalf("after upload failure create=%d upload=%d", creates, uploads)
	}

	second := call(t, handler, http.MethodPost, "/api/tasks", payload, auth.Bearer)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	client.mu.Lock()
	creates, uploads, completes := client.createCalls, client.uploadCalls, client.completeCalls
	client.mu.Unlock()
	if creates != 1 || uploads != 2 || completes != 1 {
		t.Fatalf("after complete failure create=%d upload=%d complete=%d", creates, uploads, completes)
	}

	third := call(t, handler, http.MethodPost, "/api/tasks", payload, auth.Bearer)
	if third.Code != http.StatusOK {
		t.Fatalf("third status=%d body=%s", third.Code, third.Body.String())
	}
	client.mu.Lock()
	creates, uploads, completes = client.createCalls, client.uploadCalls, client.completeCalls
	client.mu.Unlock()
	if creates != 1 || uploads != 2 || completes != 2 {
		t.Fatalf("retry recreated/reuploaded create=%d upload=%d complete=%d", creates, uploads, completes)
	}
	replayed := call(t, handler, http.MethodPost, "/api/tasks", payload, auth.Bearer)
	if replayed.Code != http.StatusOK || replayed.Body.String() != third.Body.String() {
		t.Fatalf("receipt replay changed: first=%s replay=%s", third.Body.String(), replayed.Body.String())
	}
	client.mu.Lock()
	creates, uploads, completes, gets := client.createCalls, client.uploadCalls, client.completeCalls, client.getCalls
	client.mu.Unlock()
	if creates != 1 || uploads != 2 || completes != 2 || gets != 0 {
		t.Fatalf("receipt replay called service: create=%d upload=%d complete=%d get=%d", creates, uploads, completes, gets)
	}
}

func TestPreparationExpiryRemovesArtifactAndRejectsTask(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	app := newTestApp()
	app.Clock = func() time.Time { return time.Unix(0, clock.Load()) }
	handler := Handler(app)
	prepared, auth := prepareAndAuthenticate(t, handler, "13800138000")
	h := handler.(*appHandler)
	auth.ExpiresAt = now.Add(time.Hour)
	h.mu.Lock()
	h.auth[auth.Bearer] = auth
	entry := h.prepared[prepared]
	h.mu.Unlock()
	_ = call(t, handler, http.MethodPost, "/api/consent", `{"version":"kuai-consent-v1"}`, auth.Bearer)
	now = now.Add(16 * time.Minute)
	clock.Store(now.UnixNano())
	deadline := time.Now().Add(time.Second)
	for {
		reader, err := entry.artifact.Open()
		if err != nil {
			break
		}
		_ = reader.Close()
		if time.Now().After(deadline) {
			t.Fatal("background cleanup did not remove expired artifact")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := call(t, handler, http.MethodPost, "/api/tasks",
		`{"preparation_id":"`+prepared+`","idempotency_key":"expiry-key"}`, auth.Bearer); got.Code != http.StatusBadRequest {
		t.Fatalf("expired task status=%d body=%s", got.Code, got.Body.String())
	}
	fresh, _ := prepareAndAuthenticate(t, handler, "13700137000")
	h.mu.Lock()
	freshEntry := h.prepared[fresh]
	h.mu.Unlock()
	app.Close()
	if reader, err := freshEntry.artifact.Open(); err == nil {
		_ = reader.Close()
		t.Fatal("App.Close retained artifact")
	}
}

func TestPreparationIsBoundToFirstAuthenticatedSubject(t *testing.T) {
	app := newTestApp()
	handler := Handler(app)
	prepared, first := prepareAndAuthenticate(t, handler, "13800138000")
	_, second := prepareAndAuthenticate(t, handler, "13900139000")
	_ = call(t, handler, http.MethodPost, "/api/consent", `{"version":"kuai-consent-v1"}`, first.Bearer)
	_ = call(t, handler, http.MethodPost, "/api/consent", `{"version":"kuai-consent-v1"}`, second.Bearer)
	payload := `{"preparation_id":"` + prepared + `","idempotency_key":"owner-key"}`
	app.Service = &retryClient{Client: app.Service, failUpload: true}
	if got := call(t, handler, http.MethodPost, "/api/tasks", payload, first.Bearer); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("first claim status=%d body=%s", got.Code, got.Body.String())
	}
	if got := call(t, handler, http.MethodPost, "/api/tasks", payload, second.Bearer); got.Code != http.StatusConflict {
		t.Fatalf("second subject stole preparation: %d %s", got.Code, got.Body.String())
	}
	if got := call(t, handler, http.MethodPost, "/api/tasks", payload, first.Bearer); got.Code != http.StatusOK {
		t.Fatalf("original subject could not retry: %d %s", got.Code, got.Body.String())
	}
}

func prepareAndAuthenticate(t *testing.T, handler http.Handler, phone string) (string, service.AuthSession) {
	t.Helper()
	scopes := call(t, handler, http.MethodGet, "/api/scopes", "", "")
	var found struct {
		Scopes []scopeView `json:"scopes"`
	}
	_ = json.Unmarshal(scopes.Body.Bytes(), &found)
	response := call(t, handler, http.MethodPost, "/api/prepare", `{"scope_key":"`+found.Scopes[0].Key+`"}`, "")
	var prepared struct {
		ID string `json:"preparation_id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &prepared)
	_ = call(t, handler, http.MethodPost, "/api/auth/request-code", `{"phone":"`+phone+`"}`, "")
	verified := call(t, handler, http.MethodPost, "/api/auth/verify", `{"phone":"`+phone+`","code":"246810"}`, "")
	var auth service.AuthSession
	_ = json.Unmarshal(verified.Body.Bytes(), &auth)
	return prepared.ID, auth
}

func TestExpiredAuthIsReleased(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	nowFunc := func() time.Time { return time.Unix(0, clock.Load()) }
	mock := service.NewMockClient(service.MockOptions{Now: nowFunc})
	app := newTestApp()
	app.Clock, app.Service = nowFunc, mock
	handler := Handler(app)
	_, auth := prepareAndAuthenticate(t, handler, "13800138000")
	h := handler.(*appHandler)
	now = auth.ExpiresAt.Add(time.Second)
	clock.Store(now.UnixNano())
	app.CleanupExpired()
	if got := call(t, handler, http.MethodPost, "/api/consent", `{"version":"kuai-consent-v1"}`, auth.Bearer); got.Code != http.StatusUnauthorized {
		t.Fatalf("expired bearer status=%d body=%s", got.Code, got.Body.String())
	}
	h.mu.Lock()
	_, authExists := h.auth[auth.Bearer]
	h.mu.Unlock()
	if authExists {
		t.Fatal("expired bearer retained")
	}
}

func TestAuthCapacityRejectsAndDoesNotRetainNewSecret(t *testing.T) {
	app := newTestApp()
	h := Handler(app).(*appHandler)
	expiry := time.Now().Add(time.Hour)
	h.mu.Lock()
	for index := 0; index < 1024; index++ {
		bearer := fmt.Sprintf("bearer-%d", index)
		h.auth[bearer] = service.AuthSession{Bearer: bearer, ExpiresAt: expiry,
			Identity: service.Identity{SubjectID: fmt.Sprintf("subject-%d", index)}}
	}
	h.mu.Unlock()
	_ = call(t, h, http.MethodPost, "/api/auth/request-code", `{"phone":"13800138000"}`, "")
	response := call(t, h, http.MethodPost, "/api/auth/verify", `{"phone":"13800138000","code":"246810"}`, "")
	h.mu.Lock()
	authCount := len(h.auth)
	h.mu.Unlock()
	if response.Code != http.StatusServiceUnavailable || authCount != 1024 {
		t.Fatalf("capacity status=%d auth=%d body=%s", response.Code, authCount, response.Body.String())
	}
}

func TestCleanupRetainsBusyArtifactAndRetriesRemoval(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	var attempts atomic.Int64
	app := newTestApp()
	app.Clock = func() time.Time { return time.Unix(0, clock.Load()) }
	app.RemoveArtifact = func(artifact *upload.Artifact) error {
		if attempts.Add(1) == 1 {
			return errors.New("windows sharing violation")
		}
		return artifact.Remove()
	}
	h := Handler(app).(*appHandler)
	prepared, _ := prepareAndAuthenticate(t, h, "13800138000")
	h.mu.Lock()
	entry := h.prepared[prepared]
	h.mu.Unlock()
	now = now.Add(16 * time.Minute)
	clock.Store(now.UnixNano())
	app.CleanupExpired()
	h.mu.Lock()
	_, retained := h.prepared[prepared]
	h.mu.Unlock()
	if !retained {
		t.Fatal("busy artifact entry was orphaned")
	}
	reader, err := entry.artifact.Open()
	if err != nil {
		t.Fatalf("busy artifact removed: %v", err)
	}
	_ = reader.Close()
	app.CleanupExpired()
	h.mu.Lock()
	_, retained = h.prepared[prepared]
	h.mu.Unlock()
	if retained {
		t.Fatal("artifact was not removed on later cleanup")
	}
	if reader, err := entry.artifact.Open(); err == nil {
		_ = reader.Close()
		t.Fatal("removed artifact remained readable")
	}
}

func TestCleanupDefersRemovalWhileClaimIsActive(t *testing.T) {
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(now.UnixNano())
	var attempts atomic.Int64
	app := newTestApp()
	app.Clock = func() time.Time { return time.Unix(0, clock.Load()) }
	app.RemoveArtifact = func(artifact *upload.Artifact) error { attempts.Add(1); return artifact.Remove() }
	h := Handler(app).(*appHandler)
	prepared, _ := prepareAndAuthenticate(t, h, "13800138000")
	h.mu.Lock()
	entry := h.prepared[prepared]
	entry.claimed = true
	h.mu.Unlock()
	now = now.Add(16 * time.Minute)
	clock.Store(now.UnixNano())
	app.CleanupExpired()
	if attempts.Load() != 0 {
		t.Fatal("cleanup removed artifact during active claim")
	}
	h.mu.Lock()
	if !entry.pendingCleanup {
		t.Fatal("active claim was not marked for cleanup")
	}
	entry.claimed = false
	h.finishPendingCleanupLocked(prepared, entry)
	_, retained := h.prepared[prepared]
	h.mu.Unlock()
	if retained || attempts.Load() != 1 {
		t.Fatalf("pending cleanup retained=%v attempts=%d", retained, attempts.Load())
	}
}
