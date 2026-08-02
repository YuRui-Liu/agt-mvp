package catalog

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

func TestResolveFailsClosedWithoutRuntimeAndNormalizesStateCodes(t *testing.T) {
	definition := Definition{Product: "codex", DisplayName: "Codex", Supported: true, Enabled: true,
		Status: source.SourceReady, Verification: source.VerificationMachine,
		Capabilities: []source.Capability{source.CapabilityMessages}}
	tests := []struct {
		name       string
		status     *source.SourceStatus
		state      source.SourceState
		code       string
		count      int
		selectable bool
	}{
		{name: "missing runtime with sessions", state: source.SourceNotFound, count: 3},
		{name: "ready strips code", status: &source.SourceStatus{State: source.SourceReady, Code: "read_failed"}, state: source.SourceReady, count: 1, selectable: true},
		{name: "not found strips code", status: &source.SourceStatus{State: source.SourceNotFound, Code: "invalid_session"}, state: source.SourceNotFound},
		{name: "detected unsupported strips code", status: &source.SourceStatus{State: source.SourceDetectedUnsupported, Code: "read_failed"}, state: source.SourceDetectedUnsupported},
		{name: "format normalizes empty", status: &source.SourceStatus{State: source.SourceFormatUnsupported}, state: source.SourceFormatUnsupported, code: "format_unsupported"},
		{name: "format normalizes mismatch", status: &source.SourceStatus{State: source.SourceFormatUnsupported, Code: "export_required"}, state: source.SourceFormatUnsupported, code: "format_unsupported"},
		{name: "export normalizes empty", status: &source.SourceStatus{State: source.SourceExportRequired}, state: source.SourceExportRequired, code: "export_required"},
		{name: "export normalizes mismatch", status: &source.SourceStatus{State: source.SourceExportRequired, Code: "format_unsupported"}, state: source.SourceExportRequired, code: "export_required"},
		{name: "read error retains public code", status: &source.SourceStatus{State: source.SourceReadError, Code: "duplicate_product", Error: "/private/error"}, state: source.SourceReadError, code: "duplicate_product"},
		{name: "read error normalizes empty", status: &source.SourceStatus{State: source.SourceReadError}, state: source.SourceReadError, code: "read_failed"},
		{name: "read error normalizes mismatch", status: &source.SourceStatus{State: source.SourceReadError, Code: "export_required"}, state: source.SourceReadError, code: "read_failed"},
		{name: "unknown state fails closed", status: &source.SourceStatus{State: "private-state", Code: "/private/error", Error: "secret"}, state: source.SourceReadError, code: "read_failed"},
		{name: "negative count is zero", status: &source.SourceStatus{State: source.SourceReady}, state: source.SourceReady, count: -1},
		{name: "max count remains safe", status: &source.SourceStatus{State: source.SourceReady}, state: source.SourceReady, count: math.MaxInt, selectable: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(definition, tt.status, tt.count)
			wantCount := tt.count
			if wantCount < 0 {
				wantCount = 0
			}
			if got.State != tt.state || got.Code != tt.code || got.Selectable != tt.selectable || got.SessionCount != wantCount {
				t.Fatalf("resolved=%#v", got)
			}
			if strings.Contains(got.Code, "private") || strings.Contains(got.Code, "secret") {
				t.Fatalf("resolved code leaked error: %#v", got)
			}
		})
	}

	unsupported := Definition{Product: "trae", Status: source.SourceDetectedUnsupported,
		Verification: source.VerificationExport, Reason: "official_export_required", Capabilities: []source.Capability{}}
	if got := Resolve(unsupported, nil, 0); got.State != source.SourceDetectedUnsupported || got.Selectable {
		t.Fatalf("catalog-only unsupported did not retain status: %#v", got)
	}
	resolved := Resolve(definition, &source.SourceStatus{State: source.SourceReady}, 1)
	resolved.Capabilities[0] = source.CapabilityTools
	if definition.Capabilities[0] != source.CapabilityMessages {
		t.Fatal("Resolve aliased definition capabilities")
	}
}

func TestSharedClientRootsRejectUnsafeEnvironmentAndUsePlatformFallbacks(t *testing.T) {
	home := filepath.Join(string(os.PathSeparator), "safe", "home")
	tests := []struct {
		name, goos, appData, xdg string
		want                     string
	}{
		{name: "darwin", goos: "darwin", want: filepath.Join(home, "Library", "Application Support", "Lingma", "SharedClientCache")},
		{name: "windows valid appdata", goos: "windows", appData: `C:\Users\student\AppData\Roaming`, want: `C:\Users\student\AppData\Roaming\Qoder\SharedClientCache`},
		{name: "windows valid UNC appdata", goos: "windows", appData: `\\server\students\AppData`, want: `\\server\students\AppData\Qoder\SharedClientCache`},
		{name: "windows empty falls back", goos: "windows", want: `C:\Users\student\AppData\Roaming\Qoder\SharedClientCache`},
		{name: "windows relative falls back", goos: "windows", appData: `relative\AppData`, want: `C:\Users\student\AppData\Roaming\Qoder\SharedClientCache`},
		{name: "windows unclean falls back", goos: "windows", appData: `C:\Users\student\..\other`, want: `C:\Users\student\AppData\Roaming\Qoder\SharedClientCache`},
		{name: "linux valid xdg", goos: "linux", xdg: "/safe/xdg", want: "/safe/xdg/Lingma/SharedClientCache"},
		{name: "linux empty falls back", goos: "linux", want: filepath.Join(home, ".config", "Lingma", "SharedClientCache")},
		{name: "linux relative falls back", goos: "linux", xdg: "relative/xdg", want: filepath.Join(home, ".config", "Lingma", "SharedClientCache")},
		{name: "linux unclean falls back", goos: "linux", xdg: "/safe/../unsafe", want: filepath.Join(home, ".config", "Lingma", "SharedClientCache")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testHome := home
			product := "Lingma"
			if tt.goos == "windows" {
				testHome = `C:\Users\student`
				product = "Qoder"
			}
			got := sharedClientRoots(tt.goos, product, testHome, tt.appData, tt.xdg)
			if !reflect.DeepEqual(got, []string{tt.want}) {
				t.Fatalf("roots=%#v want=%q", got, tt.want)
			}
		})
	}
	if got := sharedClientRoots("linux", "Lingma", "relative/home", "", "relative/xdg"); len(got) != 0 {
		t.Fatalf("unsafe home produced roots: %#v", got)
	}
}

func TestSaturatingSessionCountDoesNotOverflow(t *testing.T) {
	if got := saturatingIncrement(math.MaxInt); got != math.MaxInt {
		t.Fatalf("overflowed count to %d", got)
	}
}

func TestDefinitionsExposeVerificationMetadata(t *testing.T) {
	supported := map[string]bool{
		"aider": true, "claude-code": true, "cline": true, "codebuddy-cli": true,
		"codeflicker": true, "codex": true, "copilot-cli": true, "cursor": true,
		"gemini-cli": true, "hermes-agent": true, "kimi-cli": true, "kimi-code": true,
		"myflicker": true, "openclaw": true, "opencode": true, "qoder-cli": true,
		"qoder-ide": true, "qwen-code": true, "tongyi-lingma-cli": true,
		"tongyi-lingma-ide": true, "vscode-copilot": true, "workbuddy": true,
	}
	reasons := map[string]string{
		"trae": "official_export_required", "trae-work": "no_distinct_local_format",
		"kimi-work": "no_verified_session_schema", "qoder-work": "no_distinct_local_format",
		"kiro": "no_verified_session_schema", "codebuddy-ide": "no_verified_transcript_body",
	}
	messagesOnly := map[string]bool{"aider": true, "tongyi-lingma-cli": true, "tongyi-lingma-ide": true, "qoder-ide": true}
	reasoningOnly := map[string]bool{"qoder-cli": true}
	toolReasoning := map[string]bool{"cline": true, "codebuddy-cli": true, "copilot-cli": true, "gemini-cli": true, "kimi-code": true}
	fixtureVerified := map[string]bool{"gemini-cli": true, "copilot-cli": true}

	for _, definition := range Definitions() {
		if supported[definition.Product] {
			wantVerification := source.VerificationMachine
			if fixtureVerified[definition.Product] {
				wantVerification = source.VerificationFixture
			}
			if definition.Status != source.SourceReady || definition.Verification != wantVerification || definition.Reason != "" {
				t.Errorf("supported %s metadata = %#v", definition.Product, definition)
			}
			wantCapabilities := []source.Capability{source.CapabilityMessages, source.CapabilityTools}
			switch {
			case messagesOnly[definition.Product]:
				wantCapabilities = []source.Capability{source.CapabilityMessages}
			case reasoningOnly[definition.Product]:
				wantCapabilities = []source.Capability{source.CapabilityMessages, source.CapabilityReasoning}
			case toolReasoning[definition.Product]:
				wantCapabilities = []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}
			}
			if !reflect.DeepEqual(definition.Capabilities, wantCapabilities) {
				t.Errorf("%s capabilities = %#v, want %#v", definition.Product, definition.Capabilities, wantCapabilities)
			}
			continue
		}

		wantVerification := source.VerificationUnsupported
		if definition.Product == "trae" {
			wantVerification = source.VerificationExport
		}
		if definition.Status != source.SourceDetectedUnsupported || definition.Verification != wantVerification || definition.Reason != reasons[definition.Product] {
			t.Errorf("unsupported %s metadata = %#v", definition.Product, definition)
		}
		if definition.Capabilities == nil || len(definition.Capabilities) != 0 {
			t.Errorf("unsupported %s capabilities = %#v, want an empty list", definition.Product, definition.Capabilities)
		}
	}
}

func TestDefinitionsHaveUniqueProducts(t *testing.T) {
	want := map[string]struct{}{
		"aider": {}, "claude-code": {}, "cline": {}, "codebuddy-cli": {},
		"codeflicker": {}, "codex": {}, "copilot-cli": {}, "cursor": {},
		"gemini-cli": {}, "hermes-agent": {}, "kimi-code": {}, "myflicker": {},
		"openclaw": {}, "opencode": {}, "qoder-cli": {}, "qoder-ide": {},
		"tongyi-lingma-cli": {}, "tongyi-lingma-ide": {}, "vscode-copilot": {}, "workbuddy": {},
		"trae": {}, "trae-work": {}, "kimi-cli": {}, "kimi-work": {},
		"qwen-code": {}, "qoder-work": {}, "codebuddy-ide": {}, "kiro": {},
	}
	seen := make(map[string]struct{})
	for _, definition := range Definitions() {
		if definition.Product == "" {
			t.Fatal("catalog contains an empty product identifier")
		}
		if _, duplicate := seen[definition.Product]; duplicate {
			t.Fatalf("duplicate product definition: %q", definition.Product)
		}
		seen[definition.Product] = struct{}{}
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("catalog product set=%v, want %v", seen, want)
	}
}

func TestUnsupportedProductsExposeNoScannerConfiguration(t *testing.T) {
	definitions := Definitions()
	for i := range definitions {
		definition := definitions[i]
		if definition.Status != DetectedUnsupported {
			continue
		}
		if definition.Supported || definition.Enabled || definition.Detected ||
			definition.EnvVar != "" || len(definition.DefaultDirs) != 0 || len(definition.Dirs) != 0 ||
			len(definition.Capabilities) != 0 {
			t.Fatalf("unsupported product exposes scanner configuration: %#v", definition)
		}
	}
}

func TestTRAERequiresOfficialExport(t *testing.T) {
	definition := find(Definitions(), "trae")
	if definition == nil {
		t.Fatal("missing TRAE definition")
	}
	if definition.Status != DetectedUnsupported ||
		definition.Verification != source.VerificationExport ||
		definition.Reason != "official_export_required" {
		t.Fatalf("TRAE metadata=%#v", definition)
	}
}

func TestKiroNeverAdvertisesReady(t *testing.T) {
	definition := find(Definitions(), "kiro")
	if definition == nil {
		t.Fatal("missing Kiro definition")
	}
	if definition.Status == Ready || definition.Supported || definition.Enabled ||
		definition.Verification != source.VerificationUnsupported ||
		definition.Reason != "no_verified_session_schema" {
		t.Fatalf("Kiro metadata=%#v", definition)
	}
}

func TestCatalogResultsDoNotShareMutableMetadata(t *testing.T) {
	tests := []struct {
		name string
		load func() []Definition
	}{
		{name: "Definitions", load: Definitions},
		{name: "Detect", load: func() []Definition { return Detect(nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := tt.load()
			codex := find(first, "codex")
			openclaw := find(first, "openclaw")
			if codex == nil || len(codex.Capabilities) == 0 || openclaw == nil || len(openclaw.DefaultDirs) == 0 {
				t.Fatalf("missing mutable metadata: codex=%#v openclaw=%#v", codex, openclaw)
			}
			codex.Capabilities[0] = "mutated"
			openclaw.DefaultDirs[0] = "mutated"

			second := tt.load()
			if got := find(second, "codex").Capabilities[0]; got != source.CapabilityMessages {
				t.Fatalf("capabilities polluted across calls: %q", got)
			}
			if got := find(second, "openclaw").DefaultDirs[0]; got != ".openclaw/agents" {
				t.Fatalf("default dirs polluted across calls: %q", got)
			}
		})
	}
}

func TestUnknownProductsNeverGuessDirectories(t *testing.T) {
	for _, d := range Definitions() {
		if d.Supported {
			continue
		}
		if d.Enabled || len(d.Dirs) != 0 || d.Status != DetectedUnsupported {
			t.Fatalf("%s: %#v", d.Product, d)
		}
	}
}

func TestExplicitRootOnlyChecksDirectoryExistence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "trae")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "state.vscdb"), []byte("SQLCipher lure: must not be opened"), 0o000); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "snapshots"), 0o000); err != nil {
		t.Fatal(err)
	}
	got := Detect(map[string][]string{"trae": {root}})
	d := find(got, "trae")
	if d == nil || d.Enabled || !d.Detected || len(d.Dirs) != 1 || d.Status != DetectedUnsupported ||
		d.Verification != source.VerificationExport || d.Reason != "official_export_required" {
		t.Fatalf("definition=%#v", d)
	}
}

func TestTRAEIsNotDetectedWithoutExplicitConfiguration(t *testing.T) {
	definition := find(Detect(nil), "trae")
	if definition == nil || definition.Detected || len(definition.Dirs) != 0 {
		t.Fatalf("TRAE must not probe default or home paths: %#v", definition)
	}
}

func TestExplicitRootRejectsMissingRelativeAndUncleanPaths(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "trae")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	regularFile := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(regularFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Detect(map[string][]string{"trae": {
		"",
		"relative/path",
		filepath.Join(base, "missing"),
		existing + string(os.PathSeparator) + ".",
		regularFile,
	}})
	d := find(got, "trae")
	if d == nil || d.Detected || len(d.Dirs) != 0 {
		t.Fatalf("unsafe roots accepted: %#v", d)
	}
}

func TestExplicitRootRejectsDirectorySymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks on Windows may require additional privileges")
	}
	base := t.TempDir()
	target := filepath.Join(base, "target")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	definition := find(Detect(map[string][]string{"trae": {link}}), "trae")
	if definition == nil || definition.Detected || len(definition.Dirs) != 0 {
		t.Fatalf("symlink root accepted: %#v", definition)
	}
}

func TestDefinitionsUseRegistryProductNames(t *testing.T) {
	for _, product := range []string{"claude-code", "vscode-copilot", "copilot-cli", "kimi-cli", "kimi-code", "tongyi-lingma-cli", "tongyi-lingma-ide", "qoder-cli", "qoder-ide"} {
		d := find(Definitions(), product)
		if d == nil || !d.Supported || !d.Enabled {
			t.Fatalf("%s=%#v", product, d)
		}
	}
	for _, obsolete := range []string{"claude", "copilot", "tongyi-lingma", "qoder", "codebuddy"} {
		if d := find(Definitions(), obsolete); d != nil {
			t.Fatalf("obsolete product definition=%#v", d)
		}
	}
}

func TestSupportedLongTailHasOnlyVerifiedRoots(t *testing.T) {
	want := map[string]string{"openclaw": "OPENCLAW_DIR", "hermes-agent": "HERMES_SESSIONS_DIR", "workbuddy": "WORKBUDDY_PROJECTS_DIR", "kimi-cli": "KIMI_DIR", "qwen-code": "QWEN_PROJECTS_DIR", "copilot-cli": "COPILOT_DIR", "gemini-cli": "GEMINI_DIR"}
	for product, env := range want {
		d := find(Definitions(), product)
		wantDirCount := 1
		if product == "workbuddy" {
			wantDirCount = 2
		}
		if d == nil || !d.Supported || !d.Enabled || d.Status != Ready || d.EnvVar != env || len(d.DefaultDirs) != wantDirCount {
			t.Fatalf("%s=%#v", product, d)
		}
	}
	workbuddy := find(Definitions(), "workbuddy")
	if !reflect.DeepEqual(workbuddy.DefaultDirs, []string{".workbuddy-ai/projects", ".workbuddy/projects"}) {
		t.Fatalf("workbuddy defaults=%#v", workbuddy.DefaultDirs)
	}
}

func find(ds []Definition, product string) *Definition {
	for i := range ds {
		if ds[i].Product == product {
			return &ds[i]
		}
	}
	return nil
}
