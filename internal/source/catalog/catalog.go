// Package catalog describes source support without probing session content.
package catalog

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/YuRui-Liu/agt-mvp/internal/source"
)

type Status = source.SourceState

const (
	Ready               = source.SourceReady
	DetectedUnsupported = source.SourceDetectedUnsupported
)

type Definition struct {
	Product      string              `json:"product"`
	DisplayName  string              `json:"displayName"`
	EnvVar       string              `json:"envVar,omitempty"`
	DefaultDirs  []string            `json:"defaultDirs,omitempty"`
	Supported    bool                `json:"supported"`
	Enabled      bool                `json:"enabled"`
	Detected     bool                `json:"detected"`
	Dirs         []string            `json:"dirs"`
	Status       source.SourceState  `json:"status"`
	Verification source.Verification `json:"verification"`
	Capabilities []source.Capability `json:"capabilities"`
	Reason       string              `json:"reason,omitempty"`
}

// ResolvedSource combines immutable support metadata with one scan's runtime
// status. It deliberately excludes roots, opaque references, and adapter error
// text so CLI and HTTP callers share the same privacy boundary.
type ResolvedSource struct {
	Product      string
	DisplayName  string
	State        source.SourceState
	Code         string
	Supported    bool
	Enabled      bool
	Detected     bool
	Selectable   bool
	SessionCount int
	Verification source.Verification
	Capabilities []source.Capability
	Reason       string
}

var messagesAndTools = []source.Capability{source.CapabilityMessages, source.CapabilityTools}
var messagesOnly = []source.Capability{source.CapabilityMessages}
var messagesAndReasoning = []source.Capability{source.CapabilityMessages, source.CapabilityReasoning}
var messagesToolsAndReasoning = []source.Capability{source.CapabilityMessages, source.CapabilityTools, source.CapabilityReasoning}

var products = []Definition{
	{Product: "aider", DisplayName: "aider", DefaultDirs: []string{"."}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesOnly},
	{Product: "claude-code", DisplayName: "Claude Code", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "cline", DisplayName: "Cline", DefaultDirs: []string{".cline/data/sessions"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesToolsAndReasoning},
	{Product: "codebuddy-cli", DisplayName: "CodeBuddy CLI", DefaultDirs: []string{".codebuddy/projects"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesToolsAndReasoning},
	{Product: "codeflicker", DisplayName: "CodeFlicker", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "codex", DisplayName: "Codex", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "copilot-cli", DisplayName: "GitHub Copilot CLI", EnvVar: "COPILOT_DIR", DefaultDirs: []string{".copilot"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationFixture, Capabilities: messagesToolsAndReasoning},
	{Product: "cursor", DisplayName: "Cursor", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "gemini-cli", DisplayName: "Gemini CLI", EnvVar: "GEMINI_DIR", DefaultDirs: []string{".gemini"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationFixture, Capabilities: messagesToolsAndReasoning},
	{Product: "hermes-agent", DisplayName: "Hermes Agent", EnvVar: "HERMES_SESSIONS_DIR", DefaultDirs: []string{".hermes/sessions"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "kimi-cli", DisplayName: "Kimi CLI", EnvVar: "KIMI_DIR", DefaultDirs: []string{".kimi/sessions"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "kimi-code", DisplayName: "Kimi Code", DefaultDirs: []string{".kimi-code"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesToolsAndReasoning},
	{Product: "myflicker", DisplayName: "MyFlicker", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "openclaw", DisplayName: "OpenClaw", EnvVar: "OPENCLAW_DIR", DefaultDirs: []string{".openclaw/agents"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "opencode", DisplayName: "OpenCode", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "qoder-cli", DisplayName: "Qoder CLI", DefaultDirs: []string{".qoder"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndReasoning},
	{Product: "qoder-ide", DisplayName: "Qoder IDE", DefaultDirs: sharedClientDefaultDirs("Qoder"), Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesOnly},
	{Product: "qwen-code", DisplayName: "Qwen Code", EnvVar: "QWEN_PROJECTS_DIR", DefaultDirs: []string{".qwen/projects"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "tongyi-lingma-cli", DisplayName: "通义灵码 CLI", DefaultDirs: sharedClientDefaultDirs("Lingma"), Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesOnly},
	{Product: "tongyi-lingma-ide", DisplayName: "通义灵码 IDE", DefaultDirs: sharedClientDefaultDirs("Lingma"), Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesOnly},
	{Product: "vscode-copilot", DisplayName: "GitHub Copilot for VS Code", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "workbuddy", DisplayName: "WorkBuddy", EnvVar: "WORKBUDDY_PROJECTS_DIR", DefaultDirs: []string{".workbuddy-ai/projects", ".workbuddy/projects"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "trae", DisplayName: "TRAE", Status: DetectedUnsupported, Verification: source.VerificationExport, Reason: "official_export_required"},
	{Product: "trae-work", DisplayName: "TRAE Work", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"},
	{Product: "kimi-work", DisplayName: "Kimi Work", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
	{Product: "qoder-work", DisplayName: "QoderWork", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"},
	{Product: "codebuddy-ide", DisplayName: "CodeBuddy IDE", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_transcript_body"},
	{Product: "kiro", DisplayName: "Kiro", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
}

func sharedClientDefaultDirs(product string) []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{filepath.Join("Library", "Application Support", product, "SharedClientCache")}
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return []string{filepath.Join(appData, product, "SharedClientCache")}
		}
		return []string{}
	default:
		if config := os.Getenv("XDG_CONFIG_HOME"); config != "" {
			return []string{filepath.Join(config, product, "SharedClientCache")}
		}
		return []string{filepath.Join(".config", product, "SharedClientCache")}
	}
}

func Definitions() []Definition { return Detect(nil) }

// Detect checks only explicitly configured roots. It never derives defaults,
// walks directories, expands globs, or opens files.
func Detect(configured map[string][]string) []Definition {
	out := make([]Definition, len(products))
	copy(out, products)
	for i := range out {
		out[i].Dirs = nil
		out[i].DefaultDirs = append([]string(nil), out[i].DefaultDirs...)
		out[i].Capabilities = append([]source.Capability{}, out[i].Capabilities...)
		roots, explicitlyConfigured := configured[out[i].Product]
		if !explicitlyConfigured {
			continue
		}
		for _, root := range roots {
			if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
				continue
			}
			info, err := os.Lstat(root)
			if err == nil && info.IsDir() {
				out[i].Dirs = append(out[i].Dirs, root)
				out[i].Detected = true
			}
		}
		sort.Strings(out[i].Dirs)
	}
	return out
}

// Resolve merges a definition with one runtime scan status. Runtime state
// always wins; metadata remains catalog-owned. Error codes are restricted to
// the Registry's public enum and never carry adapter text.
func Resolve(definition Definition, runtimeStatus *source.SourceStatus, sessionCount int) ResolvedSource {
	if sessionCount < 0 {
		sessionCount = 0
	}
	state := definition.Status
	code := ""
	if runtimeStatus != nil {
		state = safeState(runtimeStatus.State)
		code = safeCode(runtimeStatus.Code, state)
	}
	return ResolvedSource{
		Product: definition.Product, DisplayName: definition.DisplayName,
		State: state, Code: code, Supported: definition.Supported, Enabled: definition.Enabled,
		Detected:     definition.Detected || sessionCount > 0,
		Selectable:   definition.Supported && definition.Enabled && state == source.SourceReady && sessionCount > 0,
		SessionCount: sessionCount, Verification: definition.Verification,
		Capabilities: append([]source.Capability{}, definition.Capabilities...), Reason: definition.Reason,
	}
}

func safeState(state source.SourceState) source.SourceState {
	switch state {
	case source.SourceReady, source.SourceNotFound, source.SourceExportRequired,
		source.SourceFormatUnsupported, source.SourceReadError, source.SourceDetectedUnsupported:
		return state
	default:
		return source.SourceReadError
	}
}

func safeCode(code string, state source.SourceState) string {
	switch code {
	case "", "invalid_product", "duplicate_product", "invalid_session", "format_unsupported", "export_required", "read_failed":
		return code
	}
	switch state {
	case source.SourceFormatUnsupported:
		return "format_unsupported"
	case source.SourceExportRequired:
		return "export_required"
	case source.SourceReadError:
		return "read_failed"
	default:
		return ""
	}
}
