package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// cleanEnv clears env vars that would pollute config loading in tests.
func cleanEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("REMOTE_CONTROL_HOME", dir)
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "")
	t.Setenv("REMOTE_CONTROL_SERVER_CERT", "")
	t.Setenv("REMOTE_CONTROL_SERVER_KEY", "")
	t.Setenv("REMOTE_CONTROL_SERVER_CA", "")
	t.Setenv("REMOTE_CONTROL_CLIENT_CERT", "")
	t.Setenv("REMOTE_CONTROL_CLIENT_KEY", "")
	t.Setenv("REMOTE_CONTROL_CLIENT_CA", "")
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_FILTERS", "")
	t.Setenv("LOG_JSON", "")
}

func TestDefaults(t *testing.T) {
	cleanEnv(t, t.TempDir())

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ServerURL != "https://localhost:8443" {
		t.Errorf("expected default server URL, got %s", cfg.ServerURL)
	}
	if !cfg.RequireApproval {
		t.Error("expected RequireApproval=true by default")
	}
	if cfg.DefaultPermission != "read-write" {
		t.Errorf("expected read-write, got %s", cfg.DefaultPermission)
	}
	if cfg.PollIntervalMs != 500 {
		t.Errorf("expected 500ms poll interval, got %d", cfg.PollIntervalMs)
	}
}

func TestLoadWithServerURLEnvOverride(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://test-server:9090")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ServerURL != "http://test-server:9090" {
		t.Errorf("expected env override URL, got %s", cfg.ServerURL)
	}
}

func TestLoadWithTLSEnvOverrides(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_CLIENT_CERT", "/tmp/client.crt")
	t.Setenv("REMOTE_CONTROL_CLIENT_KEY", "/tmp/client.key")
	t.Setenv("REMOTE_CONTROL_CLIENT_CA", "/tmp/ca.crt")
	t.Setenv("REMOTE_CONTROL_SERVER_CERT", "/tmp/server.crt")
	t.Setenv("REMOTE_CONTROL_SERVER_KEY", "/tmp/server.key")
	t.Setenv("REMOTE_CONTROL_SERVER_CA", "/tmp/server-ca.crt")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ClientTLS.CertFile != "/tmp/client.crt" {
		t.Errorf("expected /tmp/client.crt, got %s", cfg.ClientTLS.CertFile)
	}
	if cfg.ClientTLS.KeyFile != "/tmp/client.key" {
		t.Errorf("expected /tmp/client.key, got %s", cfg.ClientTLS.KeyFile)
	}
	if cfg.ClientTLS.TrustedCAFile != "/tmp/ca.crt" {
		t.Errorf("expected /tmp/ca.crt, got %s", cfg.ClientTLS.TrustedCAFile)
	}
	if cfg.ServerTLS.CertFile != "/tmp/server.crt" {
		t.Errorf("expected /tmp/server.crt, got %s", cfg.ServerTLS.CertFile)
	}
}

func TestLoadWithCLIOverride(t *testing.T) {
	cleanEnv(t, t.TempDir())

	cfg, err := Load(map[string]string{
		"server": "http://cli-server:8080",
	})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ServerURL != "http://cli-server:8080" {
		t.Errorf("expected CLI override URL, got %s", cfg.ServerURL)
	}
}

func TestCLIOverridesTakePriorityOverEnv(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://env-server:9090")

	cfg, err := Load(map[string]string{
		"server": "http://cli-server:8080",
	})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ServerURL != "http://cli-server:8080" {
		t.Errorf("expected CLI server to win over env, got %s", cfg.ServerURL)
	}
}

func TestCLIOverridesTLSPaths(t *testing.T) {
	cleanEnv(t, t.TempDir())

	cfg, err := Load(map[string]string{
		"client-cert": "/cli/client.crt",
		"client-key":  "/cli/client.key",
		"client-ca":   "/cli/ca.crt",
		"server-cert": "/cli/server.crt",
		"server-key":  "/cli/server.key",
		"server-ca":   "/cli/server-ca.crt",
	})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ClientTLS.CertFile != "/cli/client.crt" {
		t.Errorf("expected /cli/client.crt, got %s", cfg.ClientTLS.CertFile)
	}
	if cfg.ServerTLS.CertFile != "/cli/server.crt" {
		t.Errorf("expected /cli/server.crt, got %s", cfg.ServerTLS.CertFile)
	}
}

func TestLoadWithConfigFile(t *testing.T) {
	dir := t.TempDir()
	cleanEnv(t, dir)

	fileCfg := map[string]any{
		"server_url":       "http://file-server:7777",
		"require_approval": false,
	}
	data, _ := json.MarshalIndent(fileCfg, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.ServerURL != "http://file-server:7777" {
		t.Errorf("expected file server URL, got %s", cfg.ServerURL)
	}
	if cfg.RequireApproval {
		t.Error("expected RequireApproval=false from file")
	}
}

func TestEnvOverridesTakePriorityOverConfigFile(t *testing.T) {
	dir := t.TempDir()
	cleanEnv(t, dir)

	// Config file sets one URL.
	fileCfg := map[string]any{"server_url": "http://file-server:7777"}
	data, _ := json.MarshalIndent(fileCfg, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), data, 0600) //nolint:errcheck

	// Env var sets a different URL.
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://env-server:9090")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	// Env wins over file.
	if cfg.ServerURL != "http://env-server:9090" {
		t.Errorf("expected env server to win over file, got %s", cfg.ServerURL)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	cfg := &Config{
		ConfigDir:         dir,
		ServerURL:         "https://my-server:8443",
		RequireApproval:   false,
		DefaultPermission: "read-only",
		PollIntervalMs:    1000,
		Log: LoggingConfig{
			DefaultLevel: "info",
			Json:         false,
		},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Verify the file was created.
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("config.json not created: %v", err)
	}

	// Load it back.
	cleanEnv(t, dir)
	loaded, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded.ServerURL != "https://my-server:8443" {
		t.Errorf("expected saved URL, got %s", loaded.ServerURL)
	}
	if loaded.RequireApproval {
		t.Error("expected RequireApproval=false after load")
	}
	if loaded.DefaultPermission != "read-only" {
		t.Errorf("expected read-only, got %s", loaded.DefaultPermission)
	}
	if loaded.PollIntervalMs != 1000 {
		t.Errorf("expected 1000ms, got %d", loaded.PollIntervalMs)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "nested", "config")

	cfg := &Config{
		ConfigDir: subDir,
		ServerURL: "https://example.com",
		Log:       LoggingConfig{DefaultLevel: "info"},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(subDir, "config.json")); err != nil {
		t.Fatalf("config.json not created in nested dir: %v", err)
	}
}

func TestLoadInvalidBoolEnv(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("LOG_JSON", "not-a-bool")

	_, err := Load(nil)
	if err == nil {
		t.Error("expected error for invalid bool env var")
	}
}

func TestExpandTilde(t *testing.T) {
	home, _ := os.UserHomeDir()

	cases := []struct {
		input string
		check func(string) bool
	}{
		{"~", func(s string) bool { return s == home }},
		{"~/foo/bar", func(s string) bool { return s == home+"/foo/bar" }},
		{"/absolute/path", func(s string) bool { return s == "/absolute/path" }},
		{"relative/path", func(s string) bool { return s == "relative/path" }},
	}
	for _, tc := range cases {
		got := expandTilde(tc.input)
		if !tc.check(got) {
			t.Errorf("expandTilde(%q) = %q (home=%q)", tc.input, got, home)
		}
	}
}

func TestStrToBool(t *testing.T) {
	cases := []struct {
		input string
		want  bool
		isErr bool
	}{
		{"true", true, false},
		{"True", true, false},
		{"TRUE", true, false},
		{"1", true, false},
		{"false", false, false},
		{"False", false, false},
		{"FALSE", false, false},
		{"0", false, false},
		{"", false, false},
		{"  ", false, false},
		{"yes", false, true},
		{"no", false, true},
		{"not-a-bool", false, true},
	}
	for _, tc := range cases {
		got, err := strToBool(tc.input)
		if tc.isErr {
			if err == nil {
				t.Errorf("strToBool(%q): expected error, got nil", tc.input)
			}
		} else {
			if err != nil {
				t.Errorf("strToBool(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("strToBool(%q): expected %v, got %v", tc.input, tc.want, got)
			}
		}
	}
}
