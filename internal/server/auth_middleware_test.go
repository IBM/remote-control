package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
)

// Test authMiddleware helper functions

func TestIsValidIdentity(t *testing.T) {
	tests := []struct {
		name     string
		identity string
		want     bool
	}{
		{"empty", "", false},
		{"valid user", "user@example.com", true},
		{"valid API key", "api-key-12345", true},
		{"too long", strings.Repeat("a", 257), false},
		{"exactly 256", strings.Repeat("a", 256), true},
		{"control char", "user\x00name", false},
		{"newline", "user\nname", false},
		{"valid with special", "user_name-123", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidIdentity(tt.identity); got != tt.want {
				t.Errorf("isValidIdentity(%q) = %v, want %v", tt.identity, got, tt.want)
			}
		})
	}
}

func TestIsAllowedSource(t *testing.T) {
	tests := []struct {
		name           string
		remoteAddr     string
		allowedSources []string
		want           bool
	}{
		{"empty allowed", "192.168.1.1", []string{}, false},
		{"single match", "192.168.1.1", []string{"192.168.1.0/24"}, true},
		{"no match", "10.0.0.1", []string{"192.168.1.0/24"}, false},
		{"multiple ranges match", "10.1.1.1", []string{"192.168.1.0/24", "10.0.0.0/8"}, true},
		{"multiple ranges no match", "172.16.0.1", []string{"192.168.1.0/24", "10.0.0.0/8"}, false},
		{"invalid CIDR ignored", "192.168.1.1", []string{"invalid", "192.168.1.0/24"}, true},
		{"localhost", "127.0.0.1", []string{"127.0.0.1/32"}, true},
		{"IPv6 localhost", "::1", []string{"::1/128"}, true},
		{"port parsing", "192.168.1.1:12345", []string{"192.168.1.0/24"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAllowedSource(tt.remoteAddr, tt.allowedSources); got != tt.want {
				t.Errorf("isAllowedSource(%q, %v) = %v, want %v", tt.remoteAddr, tt.allowedSources, got, tt.want)
			}
		})
	}
}

// Test auth context extraction

func TestExtractMTLSIdentityNoTLS(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	ctx := extractMTLSIdentity(req)

	if ctx.Mode != types.AuthModeMTLS {
		t.Errorf("expected mode %s, got %s", types.AuthModeMTLS, ctx.Mode)
	}
	if ctx.Verified {
		t.Error("expected unverified when no TLS")
	}
}

func TestExtractMTLSIdentityEmptyCertChain(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.TLS = &tls.ConnectionState{} // TLS active but no certs

	ctx := extractMTLSIdentity(req)

	if ctx.Verified {
		t.Error("expected unverified when no certificates")
	}
}

func TestExtractProxyIdentityNoTLSRequired(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode: types.AuthModeProxy,
			Proxy: config.ProxyConfig{
				RequireTLS:     false,
				IdentityHeader: "X-Authenticated-User",
				AllowedSources: []string{},
			},
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Authenticated-User", "test-user")

	ctx := extractProxyIdentity(req, cfg)

	if !ctx.Verified {
		t.Error("expected verified with valid header and TLS not required")
	}
	if ctx.ClientID != "test-user" {
		t.Errorf("expected clientID test-user, got %s", ctx.ClientID)
	}
}

func TestExtractProxyIdentityTLSRequired(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode: types.AuthModeProxy,
			Proxy: config.ProxyConfig{
				RequireTLS:     true,
				IdentityHeader: "X-Authenticated-User",
				AllowedSources: []string{},
			},
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Authenticated-User", "test-user")

	ctx := extractProxyIdentity(req, cfg)

	if ctx.Verified {
		t.Error("expected unverified when TLS required but not present")
	}
}

func TestExtractProxyIdentityMissingHeader(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode: types.AuthModeProxy,
			Proxy: config.ProxyConfig{
				RequireTLS:     false,
				IdentityHeader: "X-Authenticated-User",
				AllowedSources: []string{},
			},
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	// No identity header set

	ctx := extractProxyIdentity(req, cfg)

	if ctx.Verified {
		t.Error("expected unverified when identity header missing")
	}
}

func TestExtractProxyIdentityInvalidIdentity(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode: types.AuthModeProxy,
			Proxy: config.ProxyConfig{
				RequireTLS:     false,
				IdentityHeader: "X-Authenticated-User",
				AllowedSources: []string{},
			},
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Authenticated-User", "invalid\x00user")

	ctx := extractProxyIdentity(req, cfg)

	if ctx.Verified {
		t.Error("expected unverified with invalid identity format")
	}
}

func TestExtractProxyIdentitySourceIPNotAllowed(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode: types.AuthModeProxy,
			Proxy: config.ProxyConfig{
				RequireTLS:     false,
				IdentityHeader: "X-Authenticated-User",
				AllowedSources: []string{"10.0.0.0/8"},
			},
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("X-Authenticated-User", "test-user")

	ctx := extractProxyIdentity(req, cfg)

	if ctx.Verified {
		t.Error("expected unverified when source IP not in allowed list")
	}
}

func TestExtractProxyIdentitySourceIPOk(t *testing.T) {
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Mode: types.AuthModeProxy,
			Proxy: config.ProxyConfig{
				RequireTLS:     false,
				IdentityHeader: "X-Authenticated-User",
				AllowedSources: []string{"192.168.0.0/16"},
			},
		},
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("X-Authenticated-User", "test-user")

	ctx := extractProxyIdentity(req, cfg)

	if !ctx.Verified {
		t.Error("expected verified when source IP is allowed")
	}
}

func TestAuthContextMiddlewareIntegration(t *testing.T) {
	tests := []struct {
		name            string
		authMode        types.AuthMode
		configModifiers []func(*config.Config)
		reqModifiers    []func(*http.Request)
		wantStatus      int
		wantAuth        bool
	}{
		{
			name:       "none mode allows all",
			authMode:   types.AuthModeNone,
			wantStatus: http.StatusOK,
			wantAuth:   true,
		},
		{
			name:     "proxy mode with valid header",
			authMode: types.AuthModeProxy,
			configModifiers: []func(*config.Config){
				func(c *config.Config) {
					c.Auth.Proxy.RequireTLS = false
				},
			},
			reqModifiers: []func(*http.Request){
				func(r *http.Request) {
					r.Header.Set("X-Authenticated-User", "test-user")
				},
			},
			wantStatus: http.StatusOK,
			wantAuth:   true,
		},
		{
			name:     "proxy mode without header fails",
			authMode: types.AuthModeProxy,
			configModifiers: []func(*config.Config){
				func(c *config.Config) {
					c.Auth.Proxy.RequireTLS = false
				},
			},
			wantStatus: http.StatusUnauthorized,
			wantAuth:   false,
		},
		{
			name:     "proxy mode with invalid identity fails",
			authMode: types.AuthModeProxy,
			configModifiers: []func(*config.Config){
				func(c *config.Config) {
					c.Auth.Proxy.RequireTLS = false
				},
			},
			reqModifiers: []func(*http.Request){
				func(r *http.Request) {
					r.Header.Set("X-Authenticated-User", "invalid\x00user")
				},
			},
			wantStatus: http.StatusUnauthorized,
			wantAuth:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Auth: config.AuthConfig{
					Mode: tt.authMode,
					Proxy: config.ProxyConfig{
						IdentityHeader: "X-Authenticated-User",
						RequireTLS:     false,
						AllowedSources: []string{},
					},
				},
			}

			for _, mod := range tt.configModifiers {
				mod(cfg)
			}

			req := httptest.NewRequest("GET", "/", nil)
			for _, mod := range tt.reqModifiers {
				mod(req)
			}

			w := httptest.NewRecorder()

			middleware := authMiddleware(cfg)
			handler := middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authCtx := GetAuthContext(r)
				if authCtx == nil || !authCtx.Verified {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))

			handler.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, w.Code)
			}
		})
	}
}
