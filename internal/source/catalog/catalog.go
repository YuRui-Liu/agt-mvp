// Package catalog describes source support without probing session content.
package catalog

import (
	"os"
	"path/filepath"
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

var verifiedCapabilities = []source.Capability{source.CapabilityMessages, source.CapabilityTools}

var products = []Definition{
	{Product: "claude-code", DisplayName: "Claude Code", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "codex", DisplayName: "Codex", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "cursor", DisplayName: "Cursor", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "opencode", DisplayName: "OpenCode", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "vscode-copilot", DisplayName: "VS Code Copilot", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "codeflicker", DisplayName: "CodeFlicker", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "myflicker", DisplayName: "MyFlicker", Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "openclaw", DisplayName: "OpenClaw", EnvVar: "OPENCLAW_DIR", DefaultDirs: []string{".openclaw/agents"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "hermes-agent", DisplayName: "Hermes Agent", EnvVar: "HERMES_SESSIONS_DIR", DefaultDirs: []string{".hermes/sessions"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "workbuddy", DisplayName: "WorkBuddy", EnvVar: "WORKBUDDY_PROJECTS_DIR", DefaultDirs: []string{".workbuddy/projects"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "trae", DisplayName: "TRAE", Status: DetectedUnsupported, Verification: source.VerificationExport, Reason: "official_export_required"},
	{Product: "trae-work", DisplayName: "TRAE Work", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"},
	{Product: "kimi-cli", DisplayName: "Kimi CLI", EnvVar: "KIMI_DIR", DefaultDirs: []string{".kimi/sessions"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "kimi-work", DisplayName: "Kimi Work", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
	{Product: "kimi-code", DisplayName: "Kimi Code", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
	{Product: "qwen-code", DisplayName: "Qwen Code", EnvVar: "QWEN_PROJECTS_DIR", DefaultDirs: []string{".qwen/projects"}, Supported: true, Enabled: true, Status: Ready, Verification: source.VerificationMachine, Capabilities: verifiedCapabilities},
	{Product: "tongyi-lingma", DisplayName: "通义灵码", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
	{Product: "qoder", DisplayName: "Qoder", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
	{Product: "qoder-work", DisplayName: "QoderWork", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_distinct_local_format"},
	{Product: "codebuddy", DisplayName: "CodeBuddy", Status: DetectedUnsupported, Verification: source.VerificationUnsupported, Reason: "no_verified_session_schema"},
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
			info, err := os.Stat(root)
			if err == nil && info.IsDir() {
				out[i].Dirs = append(out[i].Dirs, root)
				out[i].Detected = true
			}
		}
		sort.Strings(out[i].Dirs)
	}
	return out
}
