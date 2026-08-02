package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/catalog"
	"github.com/YuRui-Liu/agt-mvp/internal/webapp"
)

type emptyAdapter struct{}

func (emptyAdapter) Product() string                                    { return "test" }
func (emptyAdapter) Capabilities() []source.Capability                  { return nil }
func (emptyAdapter) Discover(context.Context) ([]source.Session, error) { return nil, nil }
func (emptyAdapter) Open(context.Context, source.Session) (io.ReadCloser, error) {
	return nil, io.EOF
}

type privateAdapter struct {
	root   string
	opaque string
}

func (privateAdapter) Product() string                   { return "private-source" }
func (privateAdapter) Capabilities() []source.Capability { return []source.Capability{"message"} }
func (a privateAdapter) Discover(context.Context) ([]source.Session, error) {
	return []source.Session{{
		ID: "private-session-id", Product: "private-source", OpaqueRef: a.opaque,
		Scope:        source.ScopeRef{Type: source.ScopeProject, Root: a.root, Label: filepath.Base(a.root)},
		Capabilities: []source.Capability{"message"}, MessageCount: 3,
	}}, nil
}
func (privateAdapter) Open(context.Context, source.Session) (io.ReadCloser, error) {
	return nil, io.EOF
}

type fakeListener struct {
	closed chan struct{}
	once   sync.Once
}

func (l *fakeListener) Accept() (net.Conn, error) {
	<-l.closed
	return nil, net.ErrClosed
}
func (l *fakeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}
func (l *fakeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43123}
}

func commandDependencies(calls *[]string) dependencies {
	return dependencies{
		loadConfig: func([]string) (cliConfig, error) {
			*calls = append(*calls, "config")
			return cliConfig{}, nil
		},
		newRegistry: func(map[string][]string) *source.Registry {
			*calls = append(*calls, "registry")
			return source.NewRegistry(emptyAdapter{})
		},
		newCatalog: func(map[string][]string) []catalog.Definition {
			return []catalog.Definition{{Product: "test", Supported: true, Enabled: true, Status: catalog.Ready}}
		},
		randomBytes: func(size int) ([]byte, error) {
			return bytes.Repeat([]byte{1}, size), nil
		},
	}
}

func TestVersionDoesNotInitializeRuntime(t *testing.T) {
	var calls []string
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &stdout, &stderr, commandDependencies(&calls)); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "kuai dev\n" || len(calls) != 0 {
		t.Fatalf("stdout=%q calls=%v", stdout.String(), calls)
	}
}

func TestHelpAndVersionRejectExtraArguments(t *testing.T) {
	for _, args := range [][]string{{"help", "extra"}, {"--version", "extra"}, {"version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), args, &stdout, &stderr, commandDependencies(new([]string))); code != 2 {
			t.Fatalf("args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestLoadCLIConfigAcceptsOnlyAbsoluteExplicitSourceRoots(t *testing.T) {
	root := t.TempDir()
	cfg, err := loadCLIConfig([]string{"--source-root", "trae=" + root})
	if err != nil || len(cfg.SourceRoots["trae"]) != 1 || cfg.SourceRoots["trae"][0] != root {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
	for _, value := range []string{"trae=relative", "unknown=" + root, "trae=" + root + "/."} {
		if _, err := loadCLIConfig([]string{"--source-root", value}); err == nil {
			t.Fatalf("unsafe source root accepted: %q", value)
		}
	}
}

func TestHelpListsSingleBinaryCommandsWithoutInitializingRuntime(t *testing.T) {
	var calls []string
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"help"}, &stdout, &stderr, commandDependencies(&calls)); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, command := range []string{"kuai start", "kuai scan", "kuai status"} {
		if !strings.Contains(stdout.String(), command) {
			t.Fatalf("help missing %q: %s", command, stdout.String())
		}
	}
	if len(calls) != 0 {
		t.Fatalf("help initialized runtime: %v", calls)
	}
}

func TestScanUsesEmbeddedRegistryAndWritesJSON(t *testing.T) {
	var calls []string
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"scan"}, &stdout, &stderr, commandDependencies(&calls)); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got := strings.Join(calls, ","); got != "config,registry" {
		t.Fatalf("calls=%s", got)
	}
	if !strings.Contains(stdout.String(), `"product":"test"`) ||
		!strings.Contains(stdout.String(), `"state":"not_found"`) {
		t.Fatalf("scan output=%q", stdout.String())
	}
}

func TestProductionDependenciesExposeCompleteCatalog(t *testing.T) {
	deps := productionDependencies()
	definitions := deps.newCatalog(nil)
	want := map[string]struct{}{
		"aider": {}, "claude-code": {}, "cline": {}, "codebuddy-cli": {}, "codeflicker": {},
		"codex": {}, "copilot-cli": {}, "cursor": {}, "gemini-cli": {}, "hermes-agent": {},
		"kimi-cli": {}, "kimi-code": {}, "myflicker": {}, "openclaw": {}, "opencode": {},
		"qoder-cli": {}, "qoder-ide": {}, "qwen-code": {}, "tongyi-lingma-cli": {},
		"tongyi-lingma-ide": {}, "vscode-copilot": {}, "workbuddy": {},
	}
	ready := map[string]struct{}{}
	for _, definition := range definitions {
		if definition.Supported && definition.Enabled && definition.Status == catalog.Ready {
			ready[definition.Product] = struct{}{}
		}
	}
	if !reflect.DeepEqual(ready, want) {
		t.Fatalf("ready sources=%v want=%v", ready, want)
	}
	roots := make(map[string][]string, len(want))
	for product := range want {
		roots[product] = []string{t.TempDir()}
	}
	// CodeFlicker has a stable two-position constructor contract:
	// projects directory followed by the composer SQLite file.
	roots["codeflicker"] = []string{t.TempDir(), filepath.Join(t.TempDir(), "composer_data.sqlite")}
	registry := deps.newRegistry(roots)
	if registry == nil {
		t.Fatal("production registry is nil")
	}
	scan, err := registry.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]struct{}, len(scan.Sources))
	for product, status := range scan.Sources {
		if status.Code == "duplicate_product" {
			t.Fatalf("duplicate registry product %q", product)
		}
		got[product] = struct{}{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registry sources=%v want=%v", got, want)
	}
}

func TestProductionRegistryKeepsCodeFlickerRootsPositionalAndProductsIsolated(t *testing.T) {
	projects := t.TempDir()
	projectDir := filepath.Join(projects, "campus")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := strings.Join([]string{
		`{"type":"config","config":{"cwd":"/workspace/campus"}}`,
		`{"type":"message","role":"user","content":"hello"}`,
		`{"type":"message","role":"assistant","content":"hi"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(projectDir, "codeflicker-session.jsonl"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	isolatedProducts := []string{
		"aider", "claude-code", "cline", "codebuddy-cli", "codeflicker", "codex", "copilot-cli",
		"cursor", "gemini-cli", "hermes-agent", "kimi-cli", "kimi-code", "myflicker", "openclaw",
		"opencode", "qoder-cli", "qoder-ide", "qwen-code", "tongyi-lingma-cli",
		"tongyi-lingma-ide", "vscode-copilot", "workbuddy",
	}
	roots := make(map[string][]string, len(isolatedProducts))
	for _, product := range isolatedProducts {
		roots[product] = []string{t.TempDir()}
	}
	roots["codeflicker"] = []string{projects, filepath.Join(t.TempDir(), "composer_data.sqlite")}
	scan, err := productionDependencies().newRegistry(roots).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Sources["codeflicker"].State != source.SourceReady {
		t.Fatalf("CodeFlicker did not receive its projects root: %#v", scan.Sources["codeflicker"])
	}
	for _, product := range []string{"myflicker", "copilot-cli", "vscode-copilot", "kimi-cli", "kimi-code", "qoder-cli", "qoder-ide", "tongyi-lingma-cli", "tongyi-lingma-ide"} {
		if scan.Sources[product].State == source.SourceReady {
			t.Fatalf("%s consumed another product's root", product)
		}
	}
}

func TestSafeScanOutputMergesEveryRuntimeStateWithoutLeakingErrors(t *testing.T) {
	definitions := []catalog.Definition{
		{Product: "ready-one", DisplayName: "Ready", Supported: true, Enabled: true, Status: source.SourceReady,
			Verification: source.VerificationMachine, Capabilities: []source.Capability{source.CapabilityMessages}},
		{Product: "export-one", DisplayName: "Export", Status: source.SourceDetectedUnsupported,
			Verification: source.VerificationExport, Reason: "official_export_required", Capabilities: []source.Capability{}},
	}
	session := source.Session{ID: "private-session-id", Product: "ready-one", OpaqueRef: "/private/opaque",
		Scope:        source.ScopeRef{Type: source.ScopeProject, Root: "/private/project", Label: "project"},
		Capabilities: []source.Capability{source.CapabilityMessages}}
	states := []source.SourceState{source.SourceReady, source.SourceNotFound, source.SourceFormatUnsupported,
		source.SourceReadError, source.SourceExportRequired, source.SourceDetectedUnsupported}
	for _, state := range states {
		t.Run(string(state), func(t *testing.T) {
			scan := source.ScanResult{Sources: map[string]source.SourceStatus{
				"ready-one":  {State: state, Code: "filesystem /private/error secret"},
				"export-one": {State: source.SourceExportRequired, Code: "raw product failure"},
			}}
			if state == source.SourceReady {
				scan.Sessions = []source.Session{session}
			}
			output, err := safeScanOutput(scan, definitions, bytes.Repeat([]byte{7}, 32))
			if err != nil {
				t.Fatal(err)
			}
			if got := output.Sources[0]; got.State != string(state) || got.Status != string(state) ||
				got.Selectable != (state == source.SourceReady) || got.SessionCount != len(scan.Sessions) ||
				got.Verification != source.VerificationMachine || !reflect.DeepEqual(got.Capabilities, []source.Capability{source.CapabilityMessages}) {
				t.Fatalf("source=%#v", got)
			}
			encoded, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"/private", "private-session-id", "raw product failure", "filesystem"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("safe output leaked %q: %s", secret, encoded)
				}
			}
		})
	}
}

func TestScanReportsExplicitUnsupportedRootWithoutReadingSessions(t *testing.T) {
	var calls []string
	deps := commandDependencies(&calls)
	deps.loadConfig = func(args []string) (cliConfig, error) {
		calls = append(calls, "config")
		return loadCLIConfig(args)
	}
	deps.newCatalog = catalog.Detect
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"scan", "--source-root", "trae=" + root}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, expected := range []string{`"product":"trae"`, `"detected":true`, `"state":"detected_unsupported"`} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("scan missing %q: %s", expected, stdout.String())
		}
	}
}

func TestScanOutputNeverExposesPrivateSessionOrCatalogPaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "safe-label")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	opaque := "opaque-private-reference"
	var calls []string
	deps := commandDependencies(&calls)
	deps.loadConfig = func([]string) (cliConfig, error) {
		return cliConfig{SourceRoots: map[string][]string{"trae": {root}}}, nil
	}
	deps.newRegistry = func(map[string][]string) *source.Registry {
		return source.NewRegistry(privateAdapter{root: root, opaque: opaque})
	}
	deps.newCatalog = catalog.Detect
	deps.randomBytes = func(size int) ([]byte, error) {
		return bytes.Repeat([]byte{9}, size), nil
	}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"scan"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	output := stdout.String()
	for _, secret := range []string{root, filepath.Dir(root), opaque, "private-session-id", `"dirs"`, `"defaultDirs"`, `"envVar"`} {
		if strings.Contains(output, secret) {
			t.Fatalf("scan leaked %q: %s", secret, output)
		}
	}
	for _, expected := range []string{`"label":"safe-label"`, `"session_count":1`,
		`"status":"ready"`, `"product":"trae"`, `"detected":true`, `"selectable":false`} {
		if !strings.Contains(output, expected) {
			t.Fatalf("scan missing %q: %s", expected, output)
		}
	}
}

func TestStatusDoesNotInitializeRuntime(t *testing.T) {
	var calls []string
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &stdout, &stderr, commandDependencies(&calls)); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, expected := range []string{`"type":"local-diagnostic"`, `"version":"dev"`, `"service_mode":"mock"`,
		`"remote_task":"unavailable"`, `"next":"kuai start"`} {
		if !strings.Contains(stdout.String(), expected) {
			t.Fatalf("status missing %q: %s", expected, stdout.String())
		}
	}
	if len(calls) != 0 {
		t.Fatalf("stdout=%q calls=%v", stdout.String(), calls)
	}
}

func TestStartUsesEmbeddedComponentsAndTerminates(t *testing.T) {
	var calls []string
	deps := commandDependencies(&calls)
	deps.loadConfig = func([]string) (cliConfig, error) {
		calls = append(calls, "config")
		return cliConfig{BindAddress: "127.0.0.1:0", NoBrowser: true, ServiceMode: "mock"}, nil
	}
	randomCalls := 0
	deps.randomBytes = func(size int) ([]byte, error) {
		randomCalls++
		return bytes.Repeat([]byte{byte(randomCalls)}, size), nil
	}
	deps.newServer = func(address string, app *webapp.App) (*http.Server, net.Listener, error) {
		if address != "127.0.0.1:0" || app.Registry == nil || app.Exporter == nil ||
			app.Service == nil || len(app.ScopeSecret) != 32 || app.LaunchToken == "" {
			t.Fatal("start did not inject the embedded application")
		}
		return &http.Server{Handler: webapp.Handler(app)}, &fakeListener{closed: make(chan struct{})}, nil
	}
	deps.waitForSignal = func(context.Context) error { return nil }
	deps.shutdown = func(ctx context.Context, server *http.Server) error {
		return server.Shutdown(ctx)
	}
	deps.openBrowser = func(string) error {
		t.Fatal("browser opened despite --no-browser")
		return nil
	}
	var stdout, stderr bytes.Buffer

	if code := run(context.Background(), []string{"start", "--no-browser"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if randomCalls != 2 {
		t.Fatalf("random calls=%d, want independent scope secret and launch token", randomCalls)
	}
	if !strings.Contains(stdout.String(), `"type":"server-started"`) ||
		!strings.Contains(stdout.String(), "http://127.0.0.1:43123/?token=") {
		t.Fatalf("startup event=%q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
}
