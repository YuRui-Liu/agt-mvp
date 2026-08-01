// Package catalog describes source support without probing session content.
package catalog

import (
	"os"
	"path/filepath"
	"sort"
)

type Status string

const (
	Ready               Status = "ready"
	DetectedUnsupported Status = "detected_unsupported"
)

type Definition struct {
	Product     string   `json:"product"`
	DisplayName string   `json:"displayName"`
	EnvVar      string   `json:"envVar,omitempty"`
	DefaultDirs []string `json:"defaultDirs,omitempty"`
	Supported   bool     `json:"supported"`
	Enabled     bool     `json:"enabled"`
	Detected    bool     `json:"detected"`
	Dirs        []string `json:"dirs"`
	Status      Status   `json:"status"`
}

var products = []Definition{
	{Product: "claude-code", DisplayName: "Claude Code", Supported: true, Enabled: true, Status: Ready},
	{Product: "codex", DisplayName: "Codex", Supported: true, Enabled: true, Status: Ready},
	{Product: "cursor", DisplayName: "Cursor", Supported: true, Enabled: true, Status: Ready},
	{Product: "opencode", DisplayName: "OpenCode", Supported: true, Enabled: true, Status: Ready},
	{Product: "vscode-copilot", DisplayName: "VS Code Copilot", Supported: true, Enabled: true, Status: Ready},
	{Product: "codeflicker", DisplayName: "CodeFlicker", Supported: true, Enabled: true, Status: Ready},
	{Product: "myflicker", DisplayName: "MyFlicker", Supported: true, Enabled: true, Status: Ready},
	{Product: "openclaw", DisplayName: "OpenClaw", EnvVar: "OPENCLAW_DIR", DefaultDirs: []string{".openclaw/agents"}, Supported: true, Enabled: true, Status: Ready},
	{Product: "hermes-agent", DisplayName: "Hermes Agent", EnvVar: "HERMES_SESSIONS_DIR", DefaultDirs: []string{".hermes/sessions"}, Supported: true, Enabled: true, Status: Ready},
	{Product: "workbuddy", DisplayName: "WorkBuddy", EnvVar: "WORKBUDDY_PROJECTS_DIR", DefaultDirs: []string{".workbuddy/projects"}, Supported: true, Enabled: true, Status: Ready},
	{Product: "trae", DisplayName: "TRAE", Status: DetectedUnsupported},
	{Product: "trae-work", DisplayName: "TRAE Work", Status: DetectedUnsupported},
	{Product: "kimi-cli", DisplayName: "Kimi CLI", EnvVar: "KIMI_DIR", DefaultDirs: []string{".kimi/sessions"}, Supported: true, Enabled: true, Status: Ready},
	{Product: "kimi-work", DisplayName: "Kimi Work", Status: DetectedUnsupported},
	{Product: "kimi-code", DisplayName: "Kimi Code", Status: DetectedUnsupported},
	{Product: "qwen-code", DisplayName: "Qwen Code", EnvVar: "QWEN_PROJECTS_DIR", DefaultDirs: []string{".qwen/projects"}, Supported: true, Enabled: true, Status: Ready},
	{Product: "tongyi-lingma", DisplayName: "通义灵码", Status: DetectedUnsupported},
	{Product: "qoder", DisplayName: "Qoder", Status: DetectedUnsupported},
	{Product: "qoder-work", DisplayName: "QoderWork", Status: DetectedUnsupported},
	{Product: "codebuddy", DisplayName: "CodeBuddy", Status: DetectedUnsupported},
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
