package catalog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

func TestDefinitionsExposeVerificationMetadata(t *testing.T) {
	supported := map[string]bool{
		"claude-code": true, "codex": true, "cursor": true, "opencode": true,
		"vscode-copilot": true, "codeflicker": true, "myflicker": true,
		"openclaw": true, "hermes-agent": true, "workbuddy": true,
		"kimi-cli": true, "qwen-code": true,
	}
	reasons := map[string]string{
		"trae": "official_export_required", "trae-work": "no_distinct_local_format",
		"kimi-work": "no_verified_session_schema", "kimi-code": "no_verified_session_schema",
		"tongyi-lingma": "no_verified_session_schema", "qoder": "no_verified_session_schema",
		"qoder-work": "no_distinct_local_format", "codebuddy": "no_verified_session_schema",
	}

	for _, definition := range Definitions() {
		if supported[definition.Product] {
			if definition.Status != source.SourceReady || definition.Verification != source.VerificationMachine || definition.Reason != "" {
				t.Errorf("supported %s metadata = %#v", definition.Product, definition)
			}
			wantCapabilities := []source.Capability{source.CapabilityMessages, source.CapabilityTools}
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
	if err := os.WriteFile(filepath.Join(root, "secret.jsonl"), []byte("not-json"), 0o000); err != nil {
		t.Fatal(err)
	}
	got := Detect(map[string][]string{"trae": {root}})
	d := find(got, "trae")
	if d == nil || d.Enabled || !d.Detected || len(d.Dirs) != 1 || d.Status != DetectedUnsupported {
		t.Fatalf("definition=%#v", d)
	}
}

func TestExplicitRootRejectsMissingRelativeAndUncleanPaths(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "trae")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	got := Detect(map[string][]string{"trae": {
		"relative/path",
		filepath.Join(base, "missing"),
		existing + string(os.PathSeparator) + ".",
	}})
	d := find(got, "trae")
	if d == nil || d.Detected || len(d.Dirs) != 0 {
		t.Fatalf("unsafe roots accepted: %#v", d)
	}
}

func TestDefinitionsUseRegistryProductNames(t *testing.T) {
	for _, product := range []string{"claude-code", "vscode-copilot"} {
		d := find(Definitions(), product)
		if d == nil || !d.Supported || !d.Enabled {
			t.Fatalf("%s=%#v", product, d)
		}
	}
	for _, obsolete := range []string{"claude", "copilot"} {
		if d := find(Definitions(), obsolete); d != nil {
			t.Fatalf("obsolete product definition=%#v", d)
		}
	}
}

func TestSupportedLongTailHasOnlyVerifiedRoots(t *testing.T) {
	want := map[string]string{"openclaw": "OPENCLAW_DIR", "hermes-agent": "HERMES_SESSIONS_DIR", "workbuddy": "WORKBUDDY_PROJECTS_DIR", "kimi-cli": "KIMI_DIR", "qwen-code": "QWEN_PROJECTS_DIR"}
	for product, env := range want {
		d := find(Definitions(), product)
		if d == nil || !d.Supported || !d.Enabled || d.Status != Ready || d.EnvVar != env || len(d.DefaultDirs) != 1 {
			t.Fatalf("%s=%#v", product, d)
		}
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
