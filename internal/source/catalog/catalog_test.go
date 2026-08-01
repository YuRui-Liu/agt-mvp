package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

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
