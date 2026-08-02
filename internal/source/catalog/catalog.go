// Package catalog describes source support without probing session content.
package catalog

import (
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

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
	{Product: "qoder-ide", DisplayName: "Qoder IDE", DefaultDirs: DefaultSharedClientRoots("Qoder"), Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesOnly},
	{Product: "qwen-code", DisplayName: "Qwen Code", EnvVar: "QWEN_PROJECTS_DIR", DefaultDirs: []string{".qwen/projects"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "tongyi-lingma-cli", DisplayName: "通义灵码 CLI", DefaultDirs: DefaultSharedClientRoots("Lingma"), Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesOnly},
	{Product: "tongyi-lingma-ide", DisplayName: "通义灵码 IDE", DefaultDirs: DefaultSharedClientRoots("Lingma"), Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesOnly},
	{Product: "vscode-copilot", DisplayName: "GitHub Copilot for VS Code", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "workbuddy", DisplayName: "WorkBuddy", EnvVar: "WORKBUDDY_PROJECTS_DIR", DefaultDirs: []string{".workbuddy-ai/projects", ".workbuddy/projects"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: messagesAndTools},
	{Product: "trae", DisplayName: "TRAE", Status: DetectedUnsupported, Verification: source.VerificationExport, Reason: "official_export_required"},
	{Product: "trae-work", DisplayName: "TRAE Work", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"},
	{Product: "kimi-work", DisplayName: "Kimi Work", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
	{Product: "qoder-work", DisplayName: "QoderWork", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"},
	{Product: "codebuddy-ide", DisplayName: "CodeBuddy IDE", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_transcript_body"},
	{Product: "kiro", DisplayName: "Kiro", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
}

// DefaultSharedClientRoots returns only absolute, clean platform roots. Invalid
// APPDATA/XDG_CONFIG_HOME values are ignored instead of being passed to an
// adapter as a configuration error.
func DefaultSharedClientRoots(product string) []string {
	home, _ := os.UserHomeDir()
	return sharedClientRoots(runtime.GOOS, product, home, os.Getenv("APPDATA"), os.Getenv("XDG_CONFIG_HOME"))
}

func sharedClientRoots(goos, product, home, appData, xdg string) []string {
	if product != "Lingma" && product != "Qoder" {
		return []string{}
	}
	switch goos {
	case "darwin":
		if !cleanAbsolutePath(goos, home) {
			return []string{}
		}
		return []string{joinPlatformPath(goos, home, "Library", "Application Support", product, "SharedClientCache")}
	case "windows":
		base := appData
		if !cleanAbsolutePath(goos, base) {
			if !cleanAbsolutePath(goos, home) {
				return []string{}
			}
			base = joinPlatformPath(goos, home, "AppData", "Roaming")
		}
		return []string{joinPlatformPath(goos, base, product, "SharedClientCache")}
	default:
		base := xdg
		if !cleanAbsolutePath(goos, base) {
			if !cleanAbsolutePath(goos, home) {
				return []string{}
			}
			base = joinPlatformPath(goos, home, ".config")
		}
		return []string{joinPlatformPath(goos, base, product, "SharedClientCache")}
	}
}

func cleanAbsolutePath(goos, value string) bool {
	if value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if goos != "windows" {
		return path.IsAbs(value) && path.Clean(value) == value
	}
	normalized := strings.ReplaceAll(value, `\`, "/")
	driveAbsolute := len(normalized) >= 3 && ((normalized[0] >= 'A' && normalized[0] <= 'Z') ||
		(normalized[0] >= 'a' && normalized[0] <= 'z')) && normalized[1] == ':' && normalized[2] == '/'
	if driveAbsolute {
		return path.Clean(normalized) == normalized
	}
	if !strings.HasPrefix(normalized, "//") {
		return false
	}
	tail := strings.TrimPrefix(normalized, "//")
	return len(strings.Split(tail, "/")) >= 2 && path.Clean(tail) == tail
}

func joinPlatformPath(goos string, parts ...string) string {
	if goos == "windows" {
		if len(parts) == 0 {
			return ""
		}
		joined := strings.TrimRight(strings.ReplaceAll(parts[0], `\`, "/"), "/")
		for _, part := range parts[1:] {
			joined += "/" + strings.Trim(strings.ReplaceAll(part, `\`, "/"), "/")
		}
		return strings.ReplaceAll(joined, "/", `\`)
	}
	return path.Join(parts...)
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
		state, code = normalizeStateCode(runtimeStatus.State, runtimeStatus.Code)
	} else if definition.Supported && definition.Enabled && definition.Status == source.SourceReady {
		state = source.SourceNotFound
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

func normalizeStateCode(state source.SourceState, code string) (source.SourceState, string) {
	switch state {
	case source.SourceReady, source.SourceNotFound, source.SourceDetectedUnsupported:
		return state, ""
	case source.SourceFormatUnsupported:
		return state, "format_unsupported"
	case source.SourceExportRequired:
		return state, "export_required"
	case source.SourceReadError:
		switch code {
		case "invalid_product", "duplicate_product", "invalid_session", "read_failed":
			return state, code
		default:
			return state, "read_failed"
		}
	default:
		return source.SourceReadError, "read_failed"
	}
}

// CountSessions returns per-product counts with saturating increments.
func CountSessions(sessions []source.Session) map[string]int {
	counts := make(map[string]int)
	for _, session := range sessions {
		counts[session.Product] = saturatingIncrement(counts[session.Product])
	}
	return counts
}

func saturatingIncrement(value int) int {
	maximum := int(^uint(0) >> 1)
	if value >= maximum {
		return maximum
	}
	if value < 0 {
		return 1
	}
	return value + 1
}
