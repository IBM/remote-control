package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

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

	RequireApproval   bool   `json:"require_approval"`
	DefaultPermission string `json:"default_permission"`
	PollIntervalMs    int    `json:"poll_interval_ms"`
}

func defaults() *Config {
	home, _ := os.UserHomeDir()
	configDir := filepath.Join(home, ".remote-control")
	return &Config{
		ConfigDir:         configDir,
		ServerURL:         "https://localhost:8443",
		RequireApproval:   true,
		DefaultPermission: "read-write",
		PollIntervalMs:    500,
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

func applyEnvOverrides(cfg *Config) {
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

// Load loads the configuration applying the full priority chain:
//  1. Defaults
//  2. REMOTE_CONTROL_HOME (sets ConfigDir)
//  3. config.json from ConfigDir
//  4. Individual env overrides
//  5. CLI flag overrides
func Load(cliOverrides map[string]string) (*Config, error) {
	cfg := defaults()

	if h := os.Getenv("REMOTE_CONTROL_HOME"); h != "" {
		cfg.ConfigDir = expandTilde(h)
	}

	if err := readConfigFile(cfg); err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	applyEnvOverrides(cfg)
	applyCLIOverrides(cfg, cliOverrides)
	expandTildePaths(cfg)

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
