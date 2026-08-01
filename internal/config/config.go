package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBindAddress       = "127.0.0.1:0"
	defaultMockScenario      = "success"
	defaultServiceMode       = "mock"
	defaultAnalysisAsyncTime = 30 * time.Second
)

var validMockScenarios = map[string]struct{}{
	"success":        {},
	"otp_error":      {},
	"upload_error":   {},
	"analysis_error": {},
	"slow":           {},
	"ticket_error":   {},
}

type Config struct {
	BindAddress        string
	DataDir            string
	StatePath          string
	NoBrowser          bool
	DebugSessionUpload bool
	SkipSync           bool
	ServiceMode        string
	ServiceURL         string
	MockScenario       string
	AnalysisAsyncAfter time.Duration
	AllowNetwork       bool
}

func Load(args []string) (Config, error) {
	dataDir := os.Getenv("KUAI_DATA_DIR")
	if dataDir == "" {
		userConfigDir, err := os.UserConfigDir()
		if err != nil {
			return Config{}, fmt.Errorf("find user config directory: %w", err)
		}
		dataDir = filepath.Join(userConfigDir, "kuai")
	}

	noBrowser, err := environmentBool("KUAI_NO_BROWSER")
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		BindAddress:        defaultBindAddress,
		DataDir:            dataDir,
		StatePath:          filepath.Join(dataDir, "state.json"),
		NoBrowser:          noBrowser,
		ServiceMode:        defaultServiceMode,
		MockScenario:       environmentDefault("KUAI_MOCK_SCENARIO", defaultMockScenario),
		AnalysisAsyncAfter: defaultAnalysisAsyncTime,
		AllowNetwork:       false,
	}

	if err := parseArgs(args, &cfg); err != nil {
		return Config{}, err
	}
	if _, ok := validMockScenarios[cfg.MockScenario]; !ok {
		return Config{}, fmt.Errorf("unknown mock scenario: %s", cfg.MockScenario)
	}
	switch cfg.ServiceMode {
	case "mock":
		if cfg.ServiceURL != "" {
			return Config{}, fmt.Errorf("--service-url requires --service-mode=http")
		}
		cfg.AllowNetwork = false
	case "http":
		if err := validateServiceURL(cfg.ServiceURL); err != nil {
			return Config{}, err
		}
		cfg.AllowNetwork = true
	default:
		return Config{}, fmt.Errorf("unknown service mode: %s", cfg.ServiceMode)
	}

	return cfg, nil
}

func parseArgs(args []string, cfg *Config) error {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch arg {
		case "--no-browser":
			cfg.NoBrowser = true
		case "--debug-session-upload":
			cfg.DebugSessionUpload = true
		case "--skip-sync":
			cfg.SkipSync = true
		case "--service-mode":
			index++
			if index >= len(args) || args[index] == "" {
				return fmt.Errorf("missing value for %s", arg)
			}
			cfg.ServiceMode = args[index]
		case "--service-url":
			index++
			if index >= len(args) || args[index] == "" {
				return fmt.Errorf("missing value for %s", arg)
			}
			cfg.ServiceURL = args[index]
		default:
			if value, ok := strings.CutPrefix(arg, "--service-mode="); ok {
				if value == "" {
					return fmt.Errorf("missing value for --service-mode")
				}
				cfg.ServiceMode = value
				continue
			}
			if value, ok := strings.CutPrefix(arg, "--service-url="); ok {
				if value == "" {
					return fmt.Errorf("missing value for --service-url")
				}
				cfg.ServiceURL = value
				continue
			}
			return fmt.Errorf("unknown argument: %s", arg)
		}
	}
	return nil
}

func validateServiceURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Hostname() == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("service URL must be an HTTPS origin")
	}
	if port := parsed.Port(); port != "" {
		value, parseErr := strconv.Atoi(port)
		if parseErr != nil || value < 1 || value > 65535 {
			return fmt.Errorf("service URL must be an HTTPS origin")
		}
	} else if strings.HasSuffix(parsed.Host, ":") {
		return fmt.Errorf("service URL must be an HTTPS origin")
	}
	return nil
}

func environmentBool(name string) (bool, error) {
	value := os.Getenv(name)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}

func environmentDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
