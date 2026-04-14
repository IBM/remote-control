package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// LoggingConfig holds the key configuration elements for logging
type LoggingConfig struct {
	DefaultLevel string `json:"default_level"`
	Filters      string `json:"filters"`
	Json         bool   `json:"json"`
}

// TLSBundle holds TLS certificate configuration for one side of a connection.
type TLSBundle struct {
	CertFile      string `json:"cert_file"`
	KeyFile       string `json:"key_file"`
	TrustedCAFile string `json:"trusted_ca_file"`
}

// Config holds the full remote-control configuration.
type Config struct {
	// ConfigDir is determined from REMOTE_CONTROL_HOME; not persisted.
	ConfigDir string `json:"-"`

	ServerURL string    `json:"server_url"`
	ServerTLS TLSBundle `json:"server_tls"`
	ClientTLS TLSBundle `json:"client_tls"`

	RequireApproval      bool             `json:"require_approval"`
	DefaultPermission    types.Permission `json:"default_permission"`
	PollIntervalMs       int              `json:"poll_interval_ms"`
	ClientTimeoutSeconds int              `json:"client_timeout_seconds"`
	MaxOutputBuffer      int              `json:"max_output_buffer"`

	// WebSocket configuration
	EnableWebSocket        bool   `json:"enable_websocket"`
	WebSocketPath          string `json:"websocket_path"`
	WebSocketPingInterval  int    `json:"websocket_ping_interval_seconds"`
	WebSocketPongTimeout   int    `json:"websocket_pong_timeout_seconds"`
	WSFailureThreshold     int    `json:"ws_failure_threshold"`
	WSFailureWindow        int    `json:"ws_failure_window_seconds"`
	WSUpgradeCheckInterval int    `json:"ws_upgrade_check_interval_seconds"`
	WSReconnectDelay       int    `json:"ws_reconnect_delay"`
	WSMaxReconnectDelay    int    `json:"ws_max_reconnect_delay"`

	// WebSocket recovery configuration
	WebSocketReconnectIntervalSeconds int `json:"websocket_reconnect_interval_seconds"`
	WebSocketReconnectTimeoutSeconds  int `json:"websocket_reconnect_timeout_seconds"`
	WebSocketMaxQueueLength           int `json:"websocket_max_queue_length"`

	Log LoggingConfig `json:"log"`
}

func Defaults() *Config {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".remote-control")
	if h := os.Getenv("REMOTE_CONTROL_HOME"); h != "" {
		configDir = expandTilde(h)
	}
	return &Config{
		ConfigDir:                         configDir,
		ServerURL:                         "https://localhost:8443",
		RequireApproval:                   false,
		DefaultPermission:                 types.PermissionReadWrite,
		PollIntervalMs:                    100,
		ClientTimeoutSeconds:              60,
		MaxOutputBuffer:                   1024 * 1024,
		EnableWebSocket:                   true,
		WSFailureThreshold:                3,
		WSFailureWindow:                   60,
		WSUpgradeCheckInterval:            10,
		WSReconnectDelay:                  1,
		WSMaxReconnectDelay:               30,
		WebSocketReconnectIntervalSeconds: 5,
		WebSocketReconnectTimeoutSeconds:  10,
		WebSocketMaxQueueLength:           100,
		Log: LoggingConfig{
			DefaultLevel: "info",
			Filters:      "",
			Json:         false,
		},
	}
}

func expandTilde(path string) string {
	if path == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}

func expandTildePaths(cfg *Config) {
	cfg.ConfigDir = expandTilde(cfg.ConfigDir)
	cfg.ServerTLS.CertFile = expandTilde(cfg.ServerTLS.CertFile)
	cfg.ServerTLS.KeyFile = expandTilde(cfg.ServerTLS.KeyFile)
	cfg.ServerTLS.TrustedCAFile = expandTilde(cfg.ServerTLS.TrustedCAFile)
	cfg.ClientTLS.CertFile = expandTilde(cfg.ClientTLS.CertFile)
	cfg.ClientTLS.KeyFile = expandTilde(cfg.ClientTLS.KeyFile)
	cfg.ClientTLS.TrustedCAFile = expandTilde(cfg.ClientTLS.TrustedCAFile)
}

func readConfigFile(cfg *Config) error {
	path := filepath.Join(cfg.ConfigDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, cfg)
}

func strToBool(s string) (bool, error) {
	s = strings.ToLower(strings.TrimSpace(s))

	switch s {
	case "true", "1":
		return true, nil
	case "false", "0", "":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value: %s", s)
	}
}

func applyEnvOverrides(cfg *Config) error {
	if v := os.Getenv("REMOTE_CONTROL_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("REMOTE_CONTROL_SERVER_CERT"); v != "" {
		cfg.ServerTLS.CertFile = v
	}
	if v := os.Getenv("REMOTE_CONTROL_SERVER_KEY"); v != "" {
		cfg.ServerTLS.KeyFile = v
	}
	if v := os.Getenv("REMOTE_CONTROL_SERVER_CA"); v != "" {
		cfg.ServerTLS.TrustedCAFile = v
	}
	if v := os.Getenv("REMOTE_CONTROL_CLIENT_CERT"); v != "" {
		cfg.ClientTLS.CertFile = v
	}
	if v := os.Getenv("REMOTE_CONTROL_CLIENT_KEY"); v != "" {
		cfg.ClientTLS.KeyFile = v
	}
	if v := os.Getenv("REMOTE_CONTROL_CLIENT_CA"); v != "" {
		cfg.ClientTLS.TrustedCAFile = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.DefaultLevel = v
	}
	if v := os.Getenv("LOG_FILTERS"); v != "" {
		cfg.Log.Filters = v
	}
	if v := os.Getenv("LOG_JSON"); v != "" {
		if val, err := strToBool(v); nil != err {
			return err
		} else {
			cfg.Log.Json = val
		}
	}
	return nil
}

func applyCLIOverrides(cfg *Config, overrides map[string]string) {
	if v, ok := overrides["server"]; ok {
		cfg.ServerURL = v
	}
	if v, ok := overrides["server-cert"]; ok {
		cfg.ServerTLS.CertFile = v
	}
	if v, ok := overrides["server-key"]; ok {
		cfg.ServerTLS.KeyFile = v
	}
	if v, ok := overrides["server-ca"]; ok {
		cfg.ServerTLS.TrustedCAFile = v
	}
	if v, ok := overrides["client-cert"]; ok {
		cfg.ClientTLS.CertFile = v
	}
	if v, ok := overrides["client-key"]; ok {
		cfg.ClientTLS.KeyFile = v
	}
	if v, ok := overrides["client-ca"]; ok {
		cfg.ClientTLS.TrustedCAFile = v
	}
}

func configureLogging(cfg *Config) error {
	if chanMap, err := alog.ParseChannelFilter(cfg.Log.Filters); nil != err {
		return err
	} else if level, err := alog.LevelFromString(cfg.Log.DefaultLevel); nil != err {
		return err
	} else {
		alog.Config(level, chanMap)
		alog.EnableGID()
		if cfg.Log.Json {
			alog.UseJSONLogFormatter()
		}
	}
	return nil
}

// Load loads the configuration applying the full priority chain:
//  1. Defaults (REMOTE_CONTROL_HOME sets ConfigDir)
//  2. config.json from ConfigDir
//  3. Individual env overrides
//  4. CLI flag overrides
func Load(cliOverrides map[string]string) (*Config, error) {
	cfg := Defaults()

	if err := readConfigFile(cfg); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := applyEnvOverrides(cfg); nil != err {
		return nil, err
	}
	applyCLIOverrides(cfg, cliOverrides)
	expandTildePaths(cfg)
	if err := configureLogging(cfg); nil != err {
		return nil, err
	}

	return cfg, nil
}

// Save writes the config to ConfigDir/config.json.
func Save(cfg *Config) error {
	if err := os.MkdirAll(cfg.ConfigDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(cfg.ConfigDir, "config.json")
	return os.WriteFile(path, data, 0600)
}
