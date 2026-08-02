package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/YuRui-Liu/agt-mvp/internal/service"
	"github.com/YuRui-Liu/agt-mvp/internal/source"
	"github.com/YuRui-Liu/agt-mvp/internal/source/aider"
	"github.com/YuRui-Liu/agt-mvp/internal/source/catalog"
	"github.com/YuRui-Liu/agt-mvp/internal/source/claude"
	"github.com/YuRui-Liu/agt-mvp/internal/source/cline"
	"github.com/YuRui-Liu/agt-mvp/internal/source/codebuddycli"
	"github.com/YuRui-Liu/agt-mvp/internal/source/codeflicker"
	"github.com/YuRui-Liu/agt-mvp/internal/source/codex"
	"github.com/YuRui-Liu/agt-mvp/internal/source/copilot"
	"github.com/YuRui-Liu/agt-mvp/internal/source/copilotcli"
	"github.com/YuRui-Liu/agt-mvp/internal/source/cursor"
	"github.com/YuRui-Liu/agt-mvp/internal/source/gemini"
	"github.com/YuRui-Liu/agt-mvp/internal/source/hermes"
	"github.com/YuRui-Liu/agt-mvp/internal/source/kimi"
	"github.com/YuRui-Liu/agt-mvp/internal/source/kimicode"
	"github.com/YuRui-Liu/agt-mvp/internal/source/lingma"
	"github.com/YuRui-Liu/agt-mvp/internal/source/myflicker"
	"github.com/YuRui-Liu/agt-mvp/internal/source/openclaw"
	"github.com/YuRui-Liu/agt-mvp/internal/source/opencode"
	"github.com/YuRui-Liu/agt-mvp/internal/source/qoder"
	"github.com/YuRui-Liu/agt-mvp/internal/source/qwen"
	"github.com/YuRui-Liu/agt-mvp/internal/source/workbuddy"
	"github.com/YuRui-Liu/agt-mvp/internal/upload"
	"github.com/YuRui-Liu/agt-mvp/internal/webapp"
)

const shutdownTimeout = 5 * time.Second

var version = "dev"

// stateLock is retained for the platform lock implementation's focused tests.
// The in-memory local UI no longer persists task state during startup.
type stateLock interface{ Close() error }

type dependencies struct {
	loadConfig    func([]string) (cliConfig, error)
	newRegistry   func(map[string][]string) *source.Registry
	newCatalog    func(map[string][]string) []catalog.Definition
	randomBytes   func(int) ([]byte, error)
	newServer     func(string, *webapp.App) (*http.Server, net.Listener, error)
	openBrowser   func(string) error
	waitForSignal func(context.Context) error
	shutdown      func(context.Context, *http.Server) error
}

func productionDependencies() dependencies {
	return dependencies{
		loadConfig: loadCLIConfig,
		newRegistry: func(roots map[string][]string) *source.Registry {
			return source.NewRegistry(
				aider.New(roots["aider"]...), claude.New(roots["claude-code"]...),
				cline.New(roots["cline"]...), codebuddycli.New(roots["codebuddy-cli"]...),
				codeflicker.New(roots["codeflicker"]...), codex.New(roots["codex"]...),
				copilotcli.New(roots["copilot-cli"]...), cursor.New(roots["cursor"]...),
				gemini.New(roots["gemini-cli"]...), hermes.New(roots["hermes-agent"]...),
				kimi.New(roots["kimi-cli"]...), kimicode.New(roots["kimi-code"]...),
				myflicker.New(roots["myflicker"]...), openclaw.New(roots["openclaw"]...),
				opencode.New(roots["opencode"]...), qoder.NewCLI(roots["qoder-cli"]...),
				qoder.NewIDE(roots["qoder-ide"]...), qwen.New(roots["qwen-code"]...),
				lingma.NewCLI(roots["tongyi-lingma-cli"]...), lingma.NewIDE(roots["tongyi-lingma-ide"]...),
				copilot.New(roots["vscode-copilot"]...), workbuddy.New(roots["workbuddy"]...),
			)
		},
		newCatalog: catalog.Detect,
		randomBytes: func(size int) ([]byte, error) {
			value := make([]byte, size)
			_, err := rand.Read(value)
			return value, err
		},
		newServer:     webapp.NewServer,
		openBrowser:   openPlatformBrowser,
		waitForSignal: waitForTermination,
		shutdown: func(ctx context.Context, server *http.Server) error {
			return server.Shutdown(ctx)
		},
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	command := "start"
	if len(args) > 0 {
		switch args[0] {
		case "--version", "version":
			if len(args) != 1 {
				fmt.Fprintln(stderr, "kuai: version does not accept arguments")
				return 2
			}
			fmt.Fprintf(stdout, "kuai %s\n", version)
			return 0
		case "-h", "--help", "help":
			if len(args) != 1 {
				fmt.Fprintln(stderr, "kuai: help does not accept arguments")
				return 2
			}
			printHelp(stdout)
			return 0
		case "start", "scan", "status":
			command, args = args[0], args[1:]
		}
	}
	if command == "status" {
		if len(args) != 0 {
			fmt.Fprintln(stderr, "kuai: status does not accept options")
			return 2
		}
		_ = json.NewEncoder(stdout).Encode(map[string]string{
			"type": "local-diagnostic", "version": version, "service_mode": "mock",
			"remote_task": "unavailable", "next": "kuai start",
		})
		return 0
	}

	cfg, err := deps.loadConfig(args)
	if err != nil {
		fmt.Fprintln(stderr, "kuai: invalid configuration")
		return 2
	}
	registry := deps.newRegistry(cfg.SourceRoots)
	if registry == nil {
		fmt.Fprintln(stderr, "kuai: source registry is unavailable")
		return 1
	}
	if command == "scan" {
		result, err := registry.Scan(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "kuai: session discovery failed")
			return 1
		}
		scopeSecret, err := deps.randomBytes(32)
		if err != nil {
			fmt.Fprintln(stderr, "kuai: secure scan failed")
			return 1
		}
		output, err := safeScanOutput(result, deps.newCatalog(cfg.SourceRoots), scopeSecret)
		if err != nil {
			fmt.Fprintln(stderr, "kuai: scan result could not be prepared")
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(output); err != nil {
			fmt.Fprintln(stderr, "kuai: scan result could not be written")
			return 1
		}
		return 0
	}

	scopeSecret, err := deps.randomBytes(32)
	if err != nil {
		fmt.Fprintln(stderr, "kuai: secure startup failed")
		return 1
	}
	tokenBytes, err := deps.randomBytes(32)
	if err != nil {
		fmt.Fprintln(stderr, "kuai: secure startup failed")
		return 1
	}
	launchToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	serviceClient, serviceHost, err := configuredService(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "kuai: service configuration is unavailable")
		return 1
	}
	app := &webapp.App{
		LaunchToken: launchToken,
		Registry:    registry, ScopeSecret: scopeSecret,
		Catalog: deps.newCatalog(cfg.SourceRoots),
		Exporter: upload.NewStreamExporter(registry, upload.Client{
			Name: "kuai", Version: version, Platform: runtime.GOOS + "-" + runtime.GOARCH,
		}, upload.Limits{}),
		Service: serviceClient, ServiceMode: cfg.ServiceMode, ServiceHost: serviceHost,
	}
	server, listener, err := deps.newServer(cfg.BindAddress, app)
	if err != nil {
		fmt.Fprintln(stderr, "kuai: local server could not start")
		return 1
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	startURL := "http://" + listener.Addr().String() + "/?token=" + url.QueryEscape(launchToken)
	if err := json.NewEncoder(stdout).Encode(map[string]string{"type": "server-started", "url": startURL}); err != nil {
		_ = server.Close()
		fmt.Fprintln(stderr, "kuai: startup event could not be written")
		return 1
	}
	if !cfg.NoBrowser {
		if err := deps.openBrowser(startURL); err != nil {
			fmt.Fprintln(stderr, "kuai: browser could not be opened; copy the URL from stdout")
		}
	}
	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	signalDone := make(chan error, 1)
	go func() { signalDone <- deps.waitForSignal(waitCtx) }()
	select {
	case err := <-serveDone:
		cancelWait()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "kuai: local server stopped unexpectedly")
			return 1
		}
		return 0
	case err := <-signalDone:
		if err != nil && ctx.Err() == nil {
			fmt.Fprintln(stderr, "kuai: signal wait failed")
			_ = server.Close()
			return 1
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := deps.shutdown(shutdownCtx, server); err != nil {
		_ = server.Close()
		fmt.Fprintln(stderr, "kuai: local server shutdown was forced")
		return 1
	}
	if err := <-serveDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "kuai: local server stopped unexpectedly")
		return 1
	}
	return 0
}

type cliScanScope struct {
	Key          string              `json:"key"`
	Type         source.ScopeType    `json:"type"`
	Label        string              `json:"label"`
	SessionCount int                 `json:"session_count"`
	Products     []string            `json:"products"`
	Capabilities []source.Capability `json:"capabilities"`
	StartedAt    time.Time           `json:"started_at,omitempty"`
	EndedAt      time.Time           `json:"ended_at,omitempty"`
	Status       string              `json:"status"`
	Selectable   bool                `json:"selectable"`
}

type cliScanSource struct {
	Product      string              `json:"product"`
	DisplayName  string              `json:"display_name"`
	State        string              `json:"state"`
	Status       string              `json:"status"`
	Code         string              `json:"code,omitempty"`
	Supported    bool                `json:"supported"`
	Enabled      bool                `json:"enabled"`
	Selectable   bool                `json:"selectable"`
	Detected     bool                `json:"detected"`
	SessionCount int                 `json:"session_count"`
	Verification source.Verification `json:"verification"`
	Capabilities []source.Capability `json:"capabilities"`
	Reason       string              `json:"reason,omitempty"`
}

type cliScanOutput struct {
	Scopes  []cliScanScope  `json:"scopes"`
	Sources []cliScanSource `json:"sources"`
}

func safeScanOutput(scan source.ScanResult, definitions []catalog.Definition, secret []byte) (cliScanOutput, error) {
	grouped, err := source.GroupScopes(scan.Sessions, secret)
	if err != nil {
		return cliScanOutput{}, err
	}
	output := cliScanOutput{Scopes: make([]cliScanScope, 0, len(grouped)), Sources: make([]cliScanSource, 0, len(definitions))}
	for _, scope := range grouped {
		capabilitySet := map[source.Capability]struct{}{}
		var started, ended time.Time
		selectable := len(scope.Sessions) > 0
		for _, session := range scope.Sessions {
			if scan.Sources[session.Product].State != source.SourceReady {
				selectable = false
			}
			for _, capability := range session.Capabilities {
				capabilitySet[capability] = struct{}{}
			}
			if started.IsZero() || (!session.StartedAt.IsZero() && session.StartedAt.Before(started)) {
				started = session.StartedAt
			}
			if session.EndedAt.After(ended) {
				ended = session.EndedAt
			}
		}
		capabilities := make([]source.Capability, 0, len(capabilitySet))
		for capability := range capabilitySet {
			capabilities = append(capabilities, capability)
		}
		sort.Slice(capabilities, func(i, j int) bool { return capabilities[i] < capabilities[j] })
		status := "ready"
		if !selectable {
			status = "detected_unsupported"
		}
		output.Scopes = append(output.Scopes, cliScanScope{
			Key: scope.Key, Type: scope.Type, Label: scope.Label, SessionCount: scope.SessionCount,
			Products: scope.Products, Capabilities: capabilities, StartedAt: started, EndedAt: ended,
			Status: status, Selectable: selectable,
		})
	}
	sessionCounts := make(map[string]int, len(definitions))
	for _, session := range scan.Sessions {
		sessionCounts[session.Product]++
	}
	for _, definition := range definitions {
		var runtimeStatus *source.SourceStatus
		if status, exists := scan.Sources[definition.Product]; exists {
			statusCopy := status
			runtimeStatus = &statusCopy
		}
		resolved := catalog.Resolve(definition, runtimeStatus, sessionCounts[definition.Product])
		output.Sources = append(output.Sources, cliScanSource{
			Product: resolved.Product, DisplayName: resolved.DisplayName,
			State: string(resolved.State), Status: string(resolved.State), Code: resolved.Code,
			Supported: resolved.Supported, Enabled: resolved.Enabled, Selectable: resolved.Selectable,
			Detected: resolved.Detected, SessionCount: resolved.SessionCount,
			Verification: resolved.Verification, Capabilities: resolved.Capabilities, Reason: resolved.Reason,
		})
	}
	return output, nil
}

type cliConfig struct {
	BindAddress        string
	NoBrowser          bool
	ServiceMode        string
	ServiceURL         string
	MockScenario       string
	AnalysisAsyncAfter time.Duration
	SourceRoots        map[string][]string
}

func loadCLIConfig(args []string) (cliConfig, error) {
	cfg := cliConfig{
		BindAddress: "127.0.0.1:0", ServiceMode: "mock",
		MockScenario: "success", AnalysisAsyncAfter: 30 * time.Second,
	}
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--no-browser":
			cfg.NoBrowser = true
		case "--service-mode":
			index++
			if index >= len(args) {
				return cliConfig{}, errors.New("missing service mode")
			}
			cfg.ServiceMode = args[index]
		case "--service-url":
			index++
			if index >= len(args) {
				return cliConfig{}, errors.New("missing service URL")
			}
			cfg.ServiceURL = args[index]
		case "--source-root":
			index++
			if index >= len(args) {
				return cliConfig{}, errors.New("missing source root")
			}
			product, root, ok := strings.Cut(args[index], "=")
			if !ok || !validCatalogProduct(product) || root == "" || !filepath.IsAbs(root) ||
				filepath.Clean(root) != root {
				return cliConfig{}, errors.New("invalid source root")
			}
			if cfg.SourceRoots == nil {
				cfg.SourceRoots = map[string][]string{}
			}
			cfg.SourceRoots[product] = append(cfg.SourceRoots[product], root)
		default:
			return cliConfig{}, errors.New("unknown argument")
		}
	}
	if cfg.ServiceMode != "mock" && cfg.ServiceMode != "http" {
		return cliConfig{}, errors.New("invalid service mode")
	}
	if (cfg.ServiceMode == "mock" && cfg.ServiceURL != "") ||
		(cfg.ServiceMode == "http" && cfg.ServiceURL == "") {
		return cliConfig{}, errors.New("invalid service configuration")
	}
	return cfg, nil
}

func validCatalogProduct(product string) bool {
	for _, definition := range catalog.Definitions() {
		if definition.Product == product {
			return true
		}
	}
	return false
}

func configuredService(cfg cliConfig) (service.Client, string, error) {
	if cfg.ServiceMode == "http" {
		client, err := service.NewHTTPClient(cfg.ServiceURL, nil)
		if err != nil {
			return nil, "", err
		}
		parsed, _ := url.Parse(cfg.ServiceURL)
		return client, parsed.Hostname(), nil
	}
	return service.NewMockClient(service.MockOptions{
		Scenario: cfg.MockScenario, AnalysisDelay: cfg.AnalysisAsyncAfter,
	}), "", nil
}

func printHelp(output io.Writer) {
	fmt.Fprint(output, `Usage:
  kuai start [options]  Scan local sessions and open the local assessment UI
  kuai scan [options]   Discover available local sessions
  kuai status           Show local CLI diagnostics and the UI recovery step
  kuai version          Print the installed version

Options:
  --source-root product=/absolute/path  Check an explicit source directory
`)
}

func openPlatformBrowser(target string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("open", target)
	case "linux":
		command = exec.Command("xdg-open", target)
	case "windows":
		command = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return errors.New("browser opening unsupported")
	}
	return command.Run()
}

func waitForTermination(ctx context.Context) error {
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-signalCtx.Done()
	return nil
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, productionDependencies()))
}
