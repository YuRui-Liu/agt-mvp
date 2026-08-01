package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestRegistryClassifiesSourceStates(t *testing.T) {
	registry := NewRegistry(
		testAdapter{product: "missing"},
		testAdapter{product: "unsupported", err: NewDiscoveryError(SourceFormatUnsupported, errors.New("private transcript: /Users/alice/secret"))},
		testAdapter{product: "export", err: NewDiscoveryError(SourceExportRequired, errors.New("private export: /Users/alice/archive"))},
		testAdapter{product: "broken", err: errors.New("private read: /Users/alice/session.jsonl")},
		testAdapter{product: "healthy", sessions: []Session{{ID: "ok"}}},
	)

	result, err := registry.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]SourceStatus{
		"missing":     {State: SourceNotFound},
		"unsupported": {State: SourceFormatUnsupported, Code: "format_unsupported", Error: "format_unsupported"},
		"export":      {State: SourceExportRequired, Code: "export_required", Error: "export_required"},
		"broken":      {State: SourceReadError, Code: "read_failed", Error: "read_failed"},
		"healthy":     {State: SourceReady},
	}
	for product, expected := range want {
		if got := result.Sources[product]; got != expected {
			t.Errorf("%s status = %#v, want %#v", product, got, expected)
		}
	}

	encoded, err := json.Marshal(result.Sources)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"error"`) || strings.Contains(string(encoded), `"Error"`) {
		t.Fatalf("compatibility Error field was serialized: %s", encoded)
	}
	for _, private := range []string{"alice", "transcript", "session.jsonl", "/Users"} {
		if strings.Contains(string(encoded), private) {
			t.Fatalf("source status leaked private error text %q: %s", private, encoded)
		}
	}
}

func TestClassifyDiscoveryErrorVariants(t *testing.T) {
	var typedNil *DiscoveryError
	tests := []struct {
		name string
		err  error
		want SourceStatus
	}{
		{
			name: "wrapped declared state",
			err:  fmt.Errorf("adapter wrapper: %w", NewDiscoveryError(SourceFormatUnsupported, errors.New("private path"))),
			want: sourceErrorStatus(SourceFormatUnsupported, "format_unsupported"),
		},
		{
			name: "typed nil declaration",
			err:  typedNil,
			want: sourceErrorStatus(SourceReadError, "read_failed"),
		},
		{
			name: "invalid declared state",
			err:  NewDiscoveryError(SourceReady, errors.New("private path")),
			want: sourceErrorStatus(SourceReadError, "read_failed"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyDiscoveryError(tt.err); got != tt.want {
				t.Fatalf("status = %#v, want %#v", got, tt.want)
			}
		})
	}
}

type testAdapter struct {
	product  string
	sessions []Session
	err      error
	discover func(context.Context) ([]Session, error)
	calls    *int
	caps     []Capability
	capsFunc func() []Capability
	open     func(context.Context, Session) (io.ReadCloser, error)
}

func (a testAdapter) Product() string { return a.product }
func (a testAdapter) Capabilities() []Capability {
	if a.capsFunc != nil {
		return a.capsFunc()
	}
	return a.caps
}
func (a testAdapter) Open(ctx context.Context, session Session) (io.ReadCloser, error) {
	if a.open != nil {
		return a.open(ctx, session)
	}
	return io.NopCloser(strings.NewReader("")), nil
}
func (a testAdapter) Discover(ctx context.Context) ([]Session, error) {
	if a.calls != nil {
		*a.calls++
	}
	if a.discover != nil {
		return a.discover(ctx)
	}
	return a.sessions, a.err
}

func TestRegistryScanIsolatesAdapterFailures(t *testing.T) {
	registry := NewRegistry(
		testAdapter{product: "claude", err: errors.New("private transcript: /Users/alice/secret")},
		testAdapter{product: "codex", sessions: []Session{{ID: "ok", Product: "codex"}}},
	)

	result, err := registry.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessions, statuses := result.Sessions, result.Sources

	if len(sessions) != 1 || sessions[0].ID != "ok" {
		t.Fatalf("sessions = %#v, want the healthy adapter session", sessions)
	}
	if got := statuses["codex"].State; got != SourceReady {
		t.Fatalf("codex state = %q, want %q", got, SourceReady)
	}
	failed := statuses["claude"]
	if failed.State != SourceReadError || failed.Code != "read_failed" || failed.Error != failed.Code {
		t.Fatalf("claude status = %#v, want safe failed status", failed)
	}
	if strings.Contains(failed.Code, "alice") || strings.Contains(failed.Code, "transcript") {
		t.Fatalf("failure leaked adapter error: %q", failed.Code)
	}
}

func TestRegistryScanSortsAndHandlesDuplicateSessionIDs(t *testing.T) {
	registry := NewRegistry(
		testAdapter{product: "zeta", sessions: []Session{{ID: "same"}, {ID: "b"}, {ID: "same"}}},
		testAdapter{product: "alpha", sessions: []Session{{ID: "same"}, {ID: "a"}}},
	)

	result, err := registry.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sessions := result.Sessions

	got := make([]string, 0, len(sessions))
	for _, session := range sessions {
		got = append(got, session.Product+":"+session.ID)
	}
	want := []string{"alpha:a", "alpha:same", "zeta:b", "zeta:same"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
}

func TestRegistryScanReturnsPreCanceledContextWithoutDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0

	result, err := NewRegistry(testAdapter{product: "codex", calls: &calls}).Scan(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(result.Sessions) != 0 || result.Sources != nil {
		t.Fatalf("result = %#v, want unusable zero result", result)
	}
	if calls != 0 {
		t.Fatalf("Discover calls = %d, want 0", calls)
	}
}

func TestRegistryScanStopsWhenAdapterCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	laterCalls := 0
	registry := NewRegistry(
		testAdapter{product: "healthy", sessions: []Session{{ID: "partial"}}},
		testAdapter{product: "first", discover: func(context.Context) ([]Session, error) {
			cancel()
			return nil, context.Canceled
		}},
		testAdapter{product: "later", calls: &laterCalls},
	)

	result, err := registry.Scan(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(result.Sessions) != 0 || result.Sources != nil {
		t.Fatalf("result = %#v, want unusable zero result", result)
	}
	if laterCalls != 0 {
		t.Fatalf("later adapter calls = %d, want 0", laterCalls)
	}
}

func TestRegistryScanIsolatesAdapterContextErrorWhenParentIsActive(t *testing.T) {
	for _, adapterErr := range []error{
		fmt.Errorf("adapter child: %w", context.Canceled),
		fmt.Errorf("adapter child: %w", context.DeadlineExceeded),
	} {
		registry := NewRegistry(
			testAdapter{product: "broken", err: adapterErr},
			testAdapter{product: "healthy", sessions: []Session{{ID: "ok"}}},
		)

		result, err := registry.Scan(context.Background())

		if err != nil {
			t.Fatalf("Scan returned adapter-local context error: %v", err)
		}
		if got := result.Sources["broken"]; got.State != SourceReadError || got.Code != "read_failed" || got.Error != got.Code {
			t.Fatalf("status = %#v, want read_error/read_failed", got)
		}
		if len(result.Sessions) != 1 || result.Sessions[0].Product != "healthy" {
			t.Fatalf("sessions = %#v, want healthy adapter result", result.Sessions)
		}
	}
}

func TestRegistryScanReturnsZeroResultWhenParentCancelsDuringAdapterMetadata(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result, err := NewRegistry(testAdapter{
		product:  "codex",
		sessions: []Session{{ID: "partial"}},
		capsFunc: func() []Capability {
			cancel()
			return nil
		},
	}).Scan(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(result.Sessions) != 0 || result.Sources != nil {
		t.Fatalf("result = %#v, want unusable zero result", result)
	}
}

func TestRegistryScanRejectsEmptySessionID(t *testing.T) {
	result, err := NewRegistry(testAdapter{
		product:  "codex",
		sessions: []Session{{ID: ""}, {ID: "otherwise-valid"}},
	}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 0 {
		t.Fatalf("sessions = %#v, want none from invalid product", result.Sessions)
	}
	if got := result.Sources["codex"]; got.State != SourceReadError || got.Code != "invalid_session" || got.Error != got.Code {
		t.Fatalf("status = %#v, want read_error/invalid_session", got)
	}
}

func TestRegistryScanRejectsConflictingDuplicateSessionID(t *testing.T) {
	result, err := NewRegistry(testAdapter{
		product: "codex",
		sessions: []Session{
			{ID: "same", OpaqueRef: "/private/one"},
			{ID: "same", OpaqueRef: "/private/two"},
		},
	}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 0 {
		t.Fatalf("sessions = %#v, want fail-closed product", result.Sessions)
	}
	if got := result.Sources["codex"]; got.State != SourceReadError || got.Code != "invalid_session" || got.Error != got.Code {
		t.Fatalf("status = %#v, want read_error/invalid_session", got)
	}
}

func TestRegistryScanDeduplicatesIdenticalSessions(t *testing.T) {
	session := Session{ID: "same", OpaqueRef: "/private/same", Scope: ScopeRef{Type: ScopeProject, Root: "/work/demo"}}
	result, err := NewRegistry(testAdapter{
		product:  "codex",
		sessions: []Session{session, session},
	}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %#v, want one identical session", result.Sessions)
	}
	if got := result.Sources["codex"].State; got != SourceReady {
		t.Fatalf("state = %q, want ready", got)
	}
}

func TestRegistryIgnoresNilAdapter(t *testing.T) {
	result, err := NewRegistry(nil, testAdapter{
		product: "codex", sessions: []Session{{ID: "ok"}},
	}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %#v, want healthy adapter result", result.Sessions)
	}
}

func TestRegistryRejectsDuplicateProductWithoutScanningIt(t *testing.T) {
	firstCalls, secondCalls := 0, 0
	result, err := NewRegistry(
		testAdapter{product: "codex", calls: &firstCalls, sessions: []Session{{ID: "one"}}},
		testAdapter{product: "codex", calls: &secondCalls, sessions: []Session{{ID: "two"}}},
		testAdapter{product: "claude", sessions: []Session{{ID: "healthy"}}},
	).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if firstCalls != 0 || secondCalls != 0 {
		t.Fatalf("duplicate product adapters were scanned: %d, %d", firstCalls, secondCalls)
	}
	if got := result.Sources["codex"]; got.State != SourceReadError || got.Code != "duplicate_product" || got.Error != got.Code {
		t.Fatalf("status = %#v, want read_error/duplicate_product", got)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].Product != "claude" {
		t.Fatalf("sessions = %#v, want only non-duplicate product", result.Sessions)
	}
}

type dynamicProductAdapter struct {
	productCalls int
}

func (a *dynamicProductAdapter) Product() string {
	a.productCalls++
	if a.productCalls == 1 {
		return "stable"
	}
	return "changed"
}
func (*dynamicProductAdapter) Capabilities() []Capability { return nil }
func (*dynamicProductAdapter) Discover(context.Context) ([]Session, error) {
	return []Session{{ID: "ok"}}, nil
}
func (*dynamicProductAdapter) Open(context.Context, Session) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}

func TestRegistryScanSnapshotsProductExactlyOnce(t *testing.T) {
	adapter := &dynamicProductAdapter{}
	result, err := NewRegistry(adapter).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if adapter.productCalls != 1 {
		t.Fatalf("Product calls = %d, want 1", adapter.productCalls)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].Product != "stable" {
		t.Fatalf("sessions = %#v, want frozen product snapshot", result.Sessions)
	}
}

func TestRegistryScanRejectsEmptyAndInvalidProducts(t *testing.T) {
	tests := []struct {
		product string
		key     string
	}{
		{product: "   ", key: ""},
		{product: " Bad_Product ", key: "Bad_Product"},
		{product: "-leading", key: "-leading"},
	}
	for _, tt := range tests {
		t.Run(tt.product, func(t *testing.T) {
			calls := 0
			result, err := NewRegistry(testAdapter{product: tt.product, calls: &calls}).Scan(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatalf("Discover calls = %d, want 0", calls)
			}
			if got := result.Sources[tt.key]; got.State != SourceReadError || got.Code != "invalid_product" || got.Error != got.Code {
				t.Fatalf("status = %#v, want read_error/invalid_product", got)
			}
		})
	}
}

func TestRegistryScanAcceptsProductStartingWithDigit(t *testing.T) {
	result, err := NewRegistry(testAdapter{
		product: "9agent", sessions: []Session{{ID: "ok"}},
	}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].Product != "9agent" {
		t.Fatalf("sessions = %#v, want valid numeric-leading product", result.Sessions)
	}
}

func TestRegistryScanCopiesSessionCapabilities(t *testing.T) {
	capabilities := []Capability{"messages"}
	result, err := NewRegistry(testAdapter{
		product: "codex", sessions: []Session{{ID: "ok", Capabilities: capabilities}},
	}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	capabilities[0] = "mutated"

	if got := result.Sessions[0].Capabilities[0]; got != "messages" {
		t.Fatalf("result capability = %q, want independent copy", got)
	}
}

func TestRegistryPreservesSessionQualityCounts(t *testing.T) {
	registry := NewRegistry(testAdapter{product: "codex", sessions: []Session{{ID: "one", MalformedCount: 3}}})
	result, err := registry.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Sessions[0].MalformedCount; got != 3 {
		t.Fatalf("MalformedCount=%d", got)
	}
}

func TestRegistryOpenRoutesByExactProductAndHidesAdapterErrors(t *testing.T) {
	var opened Session
	registry := NewRegistry(
		testAdapter{product: "codex", open: func(_ context.Context, session Session) (io.ReadCloser, error) {
			opened = session
			return io.NopCloser(strings.NewReader("ok")), nil
		}},
		testAdapter{product: "claude", open: func(context.Context, Session) (io.ReadCloser, error) {
			return nil, errors.New("/Users/alice/private")
		}},
	)
	reader, err := registry.Open(context.Background(), Session{ID: "one", Product: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(reader)
	_ = reader.Close()
	if string(body) != "ok" || opened.ID != "one" {
		t.Fatalf("body=%q opened=%+v", body, opened)
	}
	if _, err := registry.Open(context.Background(), Session{ID: "two", Product: "claude"}); err == nil || strings.Contains(err.Error(), "alice") {
		t.Fatalf("unsafe adapter error=%v", err)
	}
	if _, err := registry.Open(context.Background(), Session{Product: "unknown"}); err == nil {
		t.Fatal("expected unknown product error")
	}
}

func TestRegistryOpenHonorsCanceledContextWithoutCallingAdapter(t *testing.T) {
	calls := 0
	registry := NewRegistry(testAdapter{product: "codex", open: func(context.Context, Session) (io.ReadCloser, error) {
		calls++
		return io.NopCloser(strings.NewReader("")), nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := registry.Open(ctx, Session{Product: "codex"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("calls=%d", calls)
	}
}

type nilSliceAdapter []Session

func (nilSliceAdapter) Product() string            { panic("typed nil adapter must be ignored") }
func (nilSliceAdapter) Capabilities() []Capability { return nil }
func (nilSliceAdapter) Discover(context.Context) ([]Session, error) {
	panic("typed nil adapter must be ignored")
}
func (nilSliceAdapter) Open(context.Context, Session) (io.ReadCloser, error) {
	panic("typed nil adapter must be ignored")
}

func TestRegistryIgnoresTypedNilSliceAdapter(t *testing.T) {
	var adapter nilSliceAdapter
	result, err := NewRegistry(adapter, testAdapter{
		product: "codex", sessions: []Session{{ID: "ok"}},
	}).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %#v, want healthy adapter result", result.Sessions)
	}
}
