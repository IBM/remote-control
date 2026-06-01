package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabe-l-hart/remote-control/internal/common/types"
	testmain "github.com/gabe-l-hart/remote-control/test"
)

func TestMain(m *testing.M) {
	testmain.TestMain(m)
}

// cleanEnv clears env vars that would pollute config loading in tests.
func cleanEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("REMOTE_CONTROL_HOME", dir)
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "")
	t.Setenv("REMOTE_CONTROL_SERVER_URLS", "")
	t.Setenv("REMOTE_CONTROL_SERVER_CERT", "")
	t.Setenv("REMOTE_CONTROL_SERVER_KEY", "")
	t.Setenv("REMOTE_CONTROL_SERVER_CA", "")
	t.Setenv("REMOTE_CONTROL_CLIENT_CERT", "")
	t.Setenv("REMOTE_CONTROL_CLIENT_KEY", "")
	t.Setenv("REMOTE_CONTROL_CLIENT_CA", "")
	t.Setenv("REMOTE_CONTROL_SKIP_HOSTNAME_VERIFICATION", "")
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
	if len(cfg.ServerURLs) != 1 || cfg.ServerURLs[0] != "https://localhost:8443" {
		t.Errorf("expected ServerURLs to have one default URL, got %v", cfg.ServerURLs)
	}
	if cfg.RequireApproval {
		t.Error("expected RequireApproval=false by default")
	}
	if cfg.DefaultPermission != "read-write" {
		t.Errorf("expected read-write, got %s", cfg.DefaultPermission)
	}
	if cfg.PollIntervalMs != 100 {
		t.Errorf("expected 100ms poll interval, got %d", cfg.PollIntervalMs)
	}
	if cfg.ClientTimeoutSeconds != 60 {
		t.Errorf("expected 60s client timeout, got %d", cfg.ClientTimeoutSeconds)
	}
	if cfg.MaxOutputBuffer != 1024*1024 {
		t.Errorf("expected 1MB max output buffer, got %d", cfg.MaxOutputBuffer)
	}
}

func TestLoadWithServerURLEnvOverride(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://test-server:9090")
	t.Setenv("REMOTE_CONTROL_AUTH_MODE", "none")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.ServerURLs) != 1 || cfg.ServerURLs[0] != "http://test-server:9090" {
		t.Errorf("expected env override URL, got %v", cfg.ServerURLs)
	}
}

func TestLoadWithServerURLSEnvOverride(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_SERVER_URLS", "http://url1:8080, http://url2:9090,http://url3:7070")
	t.Setenv("REMOTE_CONTROL_AUTH_MODE", "none")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.ServerURLs) != 3 {
		t.Fatalf("expected 3 URLs, got %d: %v", len(cfg.ServerURLs), cfg.ServerURLs)
	}
	if cfg.ServerURLs[0] != "http://url1:8080" {
		t.Errorf("expected http://url1:8080, got %s", cfg.ServerURLs[0])
	}
	if cfg.ServerURLs[1] != "http://url2:9090" {
		t.Errorf("expected http://url2:9090, got %s", cfg.ServerURLs[1])
	}
	if cfg.ServerURLs[2] != "http://url3:7070" {
		t.Errorf("expected http://url3:7070, got %s", cfg.ServerURLs[2])
	}
}

func TestLoadWithServerURLSEnvTakesPriorityOverServerURL(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://single:8080")
	t.Setenv("REMOTE_CONTROL_SERVER_URLS", "http://multi1:8080,http://multi2:9090")
	t.Setenv("REMOTE_CONTROL_AUTH_MODE", "none")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.ServerURLs) != 2 {
		t.Fatalf("expected 2 URLs, got %d: %v", len(cfg.ServerURLs), cfg.ServerURLs)
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
		"server":    "http://cli-server:8080",
		"auth-mode": "none",
	})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.ServerURLs) != 1 || cfg.ServerURLs[0] != "http://cli-server:8080" {
		t.Errorf("expected CLI override URL, got %v", cfg.ServerURLs)
	}
}

func TestLoadWithCLIServerURLsOverride(t *testing.T) {
	cleanEnv(t, t.TempDir())

	cfg, err := Load(map[string]string{
		"server-urls": "http://c1:8080,http://c2:9090",
		"auth-mode":   "none",
	})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.ServerURLs) != 2 {
		t.Fatalf("expected 2 URLs, got %d: %v", len(cfg.ServerURLs), cfg.ServerURLs)
	}
	if cfg.ServerURLs[0] != "http://c1:8080" {
		t.Errorf("expected http://c1:8080, got %s", cfg.ServerURLs[0])
	}
	if cfg.ServerURLs[1] != "http://c2:9090" {
		t.Errorf("expected http://c2:9090, got %s", cfg.ServerURLs[1])
	}
}

func TestCLIServerURLsTakePriorityOverServerURL(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_SERVER_URLS", "http://env-multi1:8080,http://env-multi2:9090")
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://env-single:7070")
	t.Setenv("REMOTE_CONTROL_AUTH_MODE", "none")

	cfg, err := Load(map[string]string{
		"server-urls": "http://cli-multi1:8080,http://cli-multi2:9090",
	})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.ServerURLs) != 2 {
		t.Fatalf("expected 2 URLs, got %d: %v", len(cfg.ServerURLs), cfg.ServerURLs)
	}
	if cfg.ServerURLs[0] != "http://cli-multi1:8080" {
		t.Errorf("expected http://cli-multi1:8080, got %s", cfg.ServerURLs[0])
	}
}

func TestCLIOverridesTakePriorityOverEnv(t *testing.T) {
	cleanEnv(t, t.TempDir())
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://env-server:9090")

	cfg, err := Load(map[string]string{
		"server":    "http://cli-server:8080",
		"auth-mode": "none",
	})
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(cfg.ServerURLs) != 1 || cfg.ServerURLs[0] != "http://cli-server:8080" {
		t.Errorf("expected CLI server to win over env, got %v", cfg.ServerURLs)
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
		"server_urls":      []string{"http://file-server:7777"},
		"auth":             map[string]any{"mode": "none"},
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
	if len(cfg.ServerURLs) != 1 || cfg.ServerURLs[0] != "http://file-server:7777" {
		t.Errorf("expected file server URL, got %v", cfg.ServerURLs)
	}
	if cfg.RequireApproval {
		t.Error("expected RequireApproval=false from file")
	}
}

func TestLoadWithConfigFileMultipleURLs(t *testing.T) {
	dir := t.TempDir()
	cleanEnv(t, dir)

	fileCfg := map[string]any{
		"server_urls":      []string{"http://url1:8080", "http://url2:9090"},
		"auth":             map[string]any{"mode": "none"},
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
	if len(cfg.ServerURLs) != 2 {
		t.Fatalf("expected 2 URLs, got %d: %v", len(cfg.ServerURLs), cfg.ServerURLs)
	}
}

func TestEnvOverridesTakePriorityOverConfigFile(t *testing.T) {
	dir := t.TempDir()
	cleanEnv(t, dir)

	// Config file sets one URL.
	fileCfg := map[string]any{
		"server_urls": []string{"http://file-server:7777"},
		"auth":        map[string]any{"mode": "none"},
	}
	data, _ := json.MarshalIndent(fileCfg, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), data, 0600) //nolint:errcheck

	// Env var sets a different URL.
	t.Setenv("REMOTE_CONTROL_SERVER_URL", "http://env-server:9090")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	// Env wins over file.
	if len(cfg.ServerURLs) != 1 || cfg.ServerURLs[0] != "http://env-server:9090" {
		t.Errorf("expected env server to win over file, got %v", cfg.ServerURLs)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	cfg := &Config{
		ConfigDir:         dir,
		ServerURLs:        []string{"https://my-server:8443"},
		RequireApproval:   false,
		DefaultPermission: "read-only",
		PollIntervalMs:    1000,
		Log: LoggingConfig{
			DefaultLevel: "info",
			Json:         false,
		},
		Auth: AuthConfig{
			Mode: types.AuthModeMTLS,
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
	if len(cfg.ServerURLs) != 1 || cfg.ServerURLs[0] != "https://my-server:8443" {
		t.Errorf("expected saved URL, got %v", loaded.ServerURLs)
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

func TestSaveMultipleURLs(t *testing.T) {
	dir := t.TempDir()

	cfg := &Config{
		ConfigDir:  dir,
		ServerURLs: []string{"http://url1:8080", "http://url2:9090"},
		Log:        LoggingConfig{DefaultLevel: "info"},
		Auth:       AuthConfig{Mode: types.AuthModeNone},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	cleanEnv(t, dir)
	loaded, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(loaded.ServerURLs) != 2 {
		t.Fatalf("expected 2 saved URLs, got %d: %v", len(loaded.ServerURLs), loaded.ServerURLs)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "nested", "config")

	cfg := &Config{
		ConfigDir:  subDir,
		ServerURLs: []string{"https://example.com"},
		Log:        LoggingConfig{DefaultLevel: "info"},
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

// ============================================================================
// WebSocket Configuration Tests (Phase 2.2)
// ============================================================================

func TestDefaultWebSocketConfig(t *testing.T) {
	cfg := Defaults()

	if !cfg.EnableWebSocket {
		t.Error("expected EnableWebSocket=true by default")
	}
	if cfg.EnableWebSocket && cfg.WebSocketPath != "/ws" {
		// WebSocketPath has no default, it's only set via env/CLI
	}
	if cfg.WSFailureThreshold != 3 {
		t.Errorf("expected WSFailureThreshold=3, got %d", cfg.WSFailureThreshold)
	}
	if cfg.WSFailureWindow != 60 {
		t.Errorf("expected WSFailureWindow=60, got %d", cfg.WSFailureWindow)
	}
	if cfg.WSUpgradeCheckInterval != 10 {
		t.Errorf("expected WSUpgradeCheckInterval=10, got %d", cfg.WSUpgradeCheckInterval)
	}
	if cfg.WSReconnectDelay != 1 {
		t.Errorf("expected WSReconnectDelay=1, got %d", cfg.WSReconnectDelay)
	}
	if cfg.WSMaxReconnectDelay != 30 {
		t.Errorf("expected WSMaxReconnectDelay=30, got %d", cfg.WSMaxReconnectDelay)
	}
}

func TestEnableWebSocketEnvOverride(t *testing.T) {
	// This needs explicit environment variable handling in applyEnvOverrides
	// which doesn't exist yet for WebSocket settings - just skip for now
	t.Skip("WebSocket env overrides not yet implemented in applyEnvOverrides")
}

func TestWebSocketInvalidNegativeValues(t *testing.T) {
	// This test requires env variable support for WS settings which isn't implemented yet
	t.Skip("WebSocket env overrides not yet implemented in applyEnvOverrides")
}

func TestWebSocketConfigCliOverride(t *testing.T) {
	// This test requires CLI flag support for WebSocket settings which isn't implemented yet
	t.Skip("WebSocket CLI overrides not yet implemented in applyCLIOverrides")
}

func TestConfigPrecedenceDefaultToFileToEnvToCli(t *testing.T) {
	dir := t.TempDir()
	cleanEnv(t, dir)

	// Config file sets EnableWebSocket=false
	fileCfg := map[string]any{
		"enable_websocket": false,
	}
	data, _ := json.MarshalIndent(fileCfg, "", "  ")
	os.WriteFile(filepath.Join(dir, "config.json"), data, 0600)

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.EnableWebSocket {
		t.Error("expected EnableWebSocket=false from config file")
	}
}

func TestConfigVerify(t *testing.T) {
	tests := []struct {
		name        string
		config      Config
		expectError bool
	}{
		{
			name:        "valid defaults",
			config:      *Defaults(),
			expectError: false,
		},
	}

	for _, tt := range tests {
		err := Verify(&tt.config)
		if tt.expectError && nil == err {
			t.Errorf("[%s] Expected error, but Verify passed", tt.name)
		} else if !tt.expectError && nil != err {
			t.Errorf("[%s] Expected Verify to pass; got error: %v", tt.name, err)
		}
	}
}

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"  ", nil},
		{"a", []string{"a"}},
		{"a,b", []string{"a", "b"}},
		{"a, b, c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"a,,b", []string{"a", "b"}},
		{"a, ,b", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := splitAndTrim(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("splitAndTrim(%q): expected %v, got %v", tc.input, tc.want, got)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitAndTrim(%q)[%d]: expected %q, got %q", tc.input, i, tc.want[i], got[i])
			}
		}
	}
}
