package config

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func clearKUAIEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"KUAI_DATA_DIR",
		"KUAI_NO_BROWSER",
		"KUAI_MOCK_SCENARIO",
		"KUAI_SERVICE_URL",
	} {
		t.Setenv(name, "")
	}
}

func TestLoadEnablesHTTPOnlyWithExplicitModeAndHTTPSURL(t *testing.T) {
	clearKUAIEnvironment(t)
	cfg, err := Load([]string{"--service-mode=http", "--service-url=https://hr-b.example"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceMode != "http" || cfg.ServiceURL != "https://hr-b.example" || !cfg.AllowNetwork {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoadServiceURLEnvironmentDoesNotSwitchMode(t *testing.T) {
	clearKUAIEnvironment(t)
	t.Setenv("KUAI_SERVICE_URL", "https://hr-b.example")
	cfg, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServiceMode != "mock" || cfg.AllowNetwork || cfg.ServiceURL != "" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoadRejectsIncompleteOrUnsafeHTTPMode(t *testing.T) {
	clearKUAIEnvironment(t)
	for _, args := range [][]string{
		{"--service-mode=http"},
		{"--service-mode=http", "--service-url=http://example.com"},
		{"--service-mode=http", "--service-url=https://example.com:0"},
		{"--service-mode=http", "--service-url=https://example.com:65536"},
		{"--service-mode=http", "--service-url=https://example.com:abc"},
		{"--service-mode=mock", "--service-url=https://example.com"},
		{"--service-mode=other"},
	} {
		if _, err := Load(args); err == nil {
			t.Errorf("Load(%q) error = nil", args)
		}
	}
}

func TestLoadUsesPrivateLocalMockDefaults(t *testing.T) {
	clearKUAIEnvironment(t)
	t.Setenv("KUAI_DATA_DIR", t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load(nil) error = %v", err)
	}

	if cfg.BindAddress != "127.0.0.1:0" {
		t.Errorf("BindAddress = %q, want %q", cfg.BindAddress, "127.0.0.1:0")
	}
	if cfg.AnalysisAsyncAfter != 30*time.Second {
		t.Errorf("AnalysisAsyncAfter = %v, want %v", cfg.AnalysisAsyncAfter, 30*time.Second)
	}
	if cfg.ServiceMode != "mock" {
		t.Errorf("ServiceMode = %q, want %q", cfg.ServiceMode, "mock")
	}
	if cfg.AllowNetwork {
		t.Error("AllowNetwork = true, want false")
	}
	if got := filepath.Base(cfg.StatePath); got != "state.json" {
		t.Errorf("StatePath basename = %q, want %q", got, "state.json")
	}
	if want := filepath.Join(cfg.DataDir, "state.json"); cfg.StatePath != want {
		t.Errorf("StatePath = %q, want %q", cfg.StatePath, want)
	}
}

func TestLoadRejectsUnknownMockScenario(t *testing.T) {
	clearKUAIEnvironment(t)
	t.Setenv("KUAI_MOCK_SCENARIO", "invented")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("Load(nil) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unknown mock scenario: invented") {
		t.Fatalf("Load(nil) error = %q, want unknown mock scenario error", err)
	}
}

func TestLoadParsesSafeLocalArguments(t *testing.T) {
	clearKUAIEnvironment(t)
	cfg, err := Load([]string{
		"--no-browser",
		"--debug-session-upload",
		"--skip-sync",
	})
	if err != nil {
		t.Fatalf("Load(args) error = %v", err)
	}

	if !cfg.NoBrowser {
		t.Error("NoBrowser = false, want true")
	}
	if !cfg.DebugSessionUpload {
		t.Error("DebugSessionUpload = false, want true")
	}
	if !cfg.SkipSync {
		t.Error("SkipSync = false, want true")
	}
}

func TestLoadUsesSupportedEnvironment(t *testing.T) {
	clearKUAIEnvironment(t)
	t.Setenv("KUAI_NO_BROWSER", "true")
	t.Setenv("KUAI_MOCK_SCENARIO", "slow")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load(nil) error = %v", err)
	}

	if !cfg.NoBrowser {
		t.Error("NoBrowser = false, want true")
	}
	if cfg.MockScenario != "slow" {
		t.Errorf("MockScenario = %q, want %q", cfg.MockScenario, "slow")
	}
}

func TestLoadRejectsUnknownArgument(t *testing.T) {
	clearKUAIEnvironment(t)

	_, err := Load([]string{"--unknown"})
	if err == nil {
		t.Fatal("Load(args) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "unknown argument: --unknown") {
		t.Fatalf("Load(args) error = %q, want unknown argument error", err)
	}
}

func TestLoadRejectsInvalidNoBrowserEnvironment(t *testing.T) {
	clearKUAIEnvironment(t)
	t.Setenv("KUAI_NO_BROWSER", "sometimes")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("Load(nil) error = nil, want error")
	}
	if !strings.Contains(err.Error(), "KUAI_NO_BROWSER must be a boolean") {
		t.Fatalf("Load(nil) error = %q, want invalid boolean error", err)
	}
}
