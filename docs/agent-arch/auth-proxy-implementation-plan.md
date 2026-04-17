# Authentication Proxy Implementation Plan

## Overview

This document provides a detailed implementation plan for adding authentication proxy support to remote-control. The implementation will be done in phases to ensure stability and maintain backward compatibility.

## Prerequisites

- Go 1.21+
- Existing remote-control codebase
- Understanding of current mTLS implementation
- Test environment for validation

## Phase 1: Core Infrastructure (Week 1)

### 1.1 Add Authentication Types and Constants

**File:** `internal/common/types.go`

**Changes:**
```go
// AuthMode defines the authentication mode for the server
type AuthMode string

const (
    AuthModeMTLS  AuthMode = "mtls"
    AuthModeProxy AuthMode = "proxy"
    AuthModeNone  AuthMode = "none"
)

// AuthContext holds authentication information for a request
type AuthContext struct {
    Mode       AuthMode
    ClientID   string
    Verified   bool
    Source     string // "mtls", "proxy-header", "none"
    Metadata   map[string]string
}

// Context key for auth context
type contextKey string

const AuthContextKey contextKey = "auth_context"
```

**Testing:**
- Unit tests for type validation
- Ensure constants are properly defined

### 1.2 Update Configuration Schema

**File:** `internal/common/config/config.go`

**Changes:**
```go
// AuthConfig holds authentication configuration
type AuthConfig struct {
    Mode  AuthMode    `json:"mode"`
    Proxy ProxyConfig `json:"proxy"`
}

// ProxyConfig holds proxy authentication configuration
type ProxyConfig struct {
    IdentityHeader string   `json:"identity_header"`
    TrustedHeaders []string `json:"trusted_headers"`
    RequireTLS     bool     `json:"require_tls"`
    AllowedSources []string `json:"allowed_sources"`
}

// Update Config struct
type Config struct {
    // ... existing fields ...
    Auth AuthConfig `json:"auth"`
}

// Update Defaults()
func Defaults() *Config {
    // ... existing defaults ...
    cfg.Auth = AuthConfig{
        Mode: AuthModeMTLS, // Default to current behavior
        Proxy: ProxyConfig{
            IdentityHeader: "X-Authenticated-User",
            TrustedHeaders: []string{"X-Forwarded-For", "X-Real-IP"},
            RequireTLS:     true,
            AllowedSources: []string{},
        },
    }
    return cfg
}
```

**Environment Variables:**
```go
func applyEnvOverrides(cfg *Config) error {
    // ... existing overrides ...
    if v := os.Getenv("REMOTE_CONTROL_AUTH_MODE"); v != "" {
        cfg.Auth.Mode = AuthMode(v)
    }
    if v := os.Getenv("REMOTE_CONTROL_AUTH_PROXY_IDENTITY_HEADER"); v != "" {
        cfg.Auth.Proxy.IdentityHeader = v
    }
    if v := os.Getenv("REMOTE_CONTROL_AUTH_PROXY_REQUIRE_TLS"); v != "" {
        if val, err := strToBool(v); err == nil {
            cfg.Auth.Proxy.RequireTLS = val
        }
    }
    return nil
}
```

**Testing:**
- Unit tests for config loading
- Test environment variable overrides
- Test default values
- Test JSON marshaling/unmarshaling

### 1.3 Update TLS Configuration

**File:** `internal/common/tlsconfig/config.go`

**Changes:**
```go
// BuildServerTLSConfig constructs TLS config based on auth mode
func BuildServerTLSConfig(serverCertFile, serverKeyFile, clientCAFile string, authMode types.AuthMode) (*tls.Config, error) {
    cert, err := tls.LoadX509KeyPair(serverCertFile, serverKeyFile)
    if err != nil {
        return nil, fmt.Errorf("load server cert/key: %w", err)
    }

    var clientAuth tls.ClientAuthType
    var clientCAs *x509.CertPool

    switch authMode {
    case types.AuthModeMTLS:
        // Require and verify client certificates
        clientCA, err := loadCertPool(clientCAFile)
        if err != nil {
            return nil, fmt.Errorf("load client CA: %w", err)
        }
        clientAuth = tls.RequireAndVerifyClientCert
        clientCAs = clientCA

    case types.AuthModeProxy, types.AuthModeNone:
        // No client certificate required
        clientAuth = tls.NoClientCert
        clientCAs = nil
    }

    return &tls.Config{
        MinVersion:   tls.VersionTLS13,
        Certificates: []tls.Certificate{cert},
        ClientCAs:    clientCAs,
        ClientAuth:   clientAuth,
    }, nil
}

// BuildClientTLSConfig constructs TLS config for clients based on auth mode
func BuildClientTLSConfig(clientCertFile, clientKeyFile, serverCAFile string, authMode types.AuthMode) (*tls.Config, error) {
    serverCA, err := loadCertPool(serverCAFile)
    if err != nil {
        return nil, fmt.Errorf("load server CA: %w", err)
    }

    config := &tls.Config{
        MinVersion: tls.VersionTLS13,
        RootCAs:    serverCA,
    }

    // Only load client cert in mTLS mode
    if authMode == types.AuthModeMTLS {
        cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
        if err != nil {
            return nil, fmt.Errorf("load client cert/key: %w", err)
        }
        config.Certificates = []tls.Certificate{cert}
    }

    return config, nil
}
```

**Testing:**
- Unit tests for each auth mode
- Test certificate loading in mTLS mode
- Test no client cert in proxy mode
- Test error handling

### 1.4 Create Authentication Middleware

**File:** `internal/server/auth_middleware.go` (NEW)

**Implementation:**
```go
package server

import (
    "context"
    "net"
    "net/http"
    "strings"

    "github.com/IBM/alchemy-logging/src/go/alog"
    "github.com/gabe-l-hart/remote-control/internal/common/types"
    "github.com/gabe-l-hart/remote-control/internal/common/config"
)

var authCh = alog.UseChannel("AUTH")

// authMiddleware creates authentication middleware based on config
func authMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx := r.Context()
            var authCtx *types.AuthContext

            switch cfg.Auth.Mode {
            case types.AuthModeMTLS:
                authCtx = extractMTLSIdentity(r)
                if !authCtx.Verified {
                    authCh.Log(alog.WARNING, "mTLS authentication failed")
                    http.Error(w, "Unauthorized", http.StatusUnauthorized)
                    return
                }

            case types.AuthModeProxy:
                authCtx = extractProxyIdentity(r, cfg)
                if !authCtx.Verified {
                    authCh.Log(alog.WARNING, "Proxy authentication failed")
                    http.Error(w, "Unauthorized", http.StatusUnauthorized)
                    return
                }

            case types.AuthModeNone:
                authCtx = &types.AuthContext{
                    Mode:     types.AuthModeNone,
                    ClientID: "anonymous",
                    Verified: true,
                    Source:   "none",
                }
            }

            authCh.Log(alog.DEBUG, "Authenticated: %s (source: %s)", authCtx.ClientID, authCtx.Source)
            ctx = context.WithValue(ctx, types.AuthContextKey, authCtx)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

// extractMTLSIdentity extracts client identity from TLS certificate
func extractMTLSIdentity(r *http.Request) *types.AuthContext {
    if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
        return &types.AuthContext{
            Mode:     types.AuthModeMTLS,
            Verified: false,
        }
    }

    cert := r.TLS.PeerCertificates[0]
    return &types.AuthContext{
        Mode:     types.AuthModeMTLS,
        ClientID: cert.Subject.CommonName,
        Verified: true,
        Source:   "mtls",
        Metadata: map[string]string{
            "serial":     cert.SerialNumber.String(),
            "not_before": cert.NotBefore.String(),
            "not_after":  cert.NotAfter.String(),
        },
    }
}

// extractProxyIdentity extracts client identity from proxy headers
func extractProxyIdentity(r *http.Request, cfg *config.Config) *types.AuthContext {
    // Validate TLS if required
    if cfg.Auth.Proxy.RequireTLS && r.TLS == nil {
        authCh.Log(alog.DEBUG, "TLS required but not present")
        return &types.AuthContext{Mode: types.AuthModeProxy, Verified: false}
    }

    // Validate source IP if configured
    if len(cfg.Auth.Proxy.AllowedSources) > 0 {
        if !isAllowedSource(r.RemoteAddr, cfg.Auth.Proxy.AllowedSources) {
            authCh.Log(alog.DEBUG, "Source IP not allowed: %s", r.RemoteAddr)
            return &types.AuthContext{Mode: types.AuthModeProxy, Verified: false}
        }
    }

    // Extract identity from header
    clientID := r.Header.Get(cfg.Auth.Proxy.IdentityHeader)
    if clientID == "" {
        authCh.Log(alog.DEBUG, "Identity header missing: %s", cfg.Auth.Proxy.IdentityHeader)
        return &types.AuthContext{Mode: types.AuthModeProxy, Verified: false}
    }

    // Validate identity format (basic validation)
    if !isValidIdentity(clientID) {
        authCh.Log(alog.DEBUG, "Invalid identity format: %s", clientID)
        return &types.AuthContext{Mode: types.AuthModeProxy, Verified: false}
    }

    // Collect trusted headers
    metadata := make(map[string]string)
    for _, header := range cfg.Auth.Proxy.TrustedHeaders {
        if value := r.Header.Get(header); value != "" {
            metadata[header] = value
        }
    }

    return &types.AuthContext{
        Mode:     types.AuthModeProxy,
        ClientID: clientID,
        Verified: true,
        Source:   "proxy-header",
        Metadata: metadata,
    }
}

// isAllowedSource checks if the remote address is in allowed CIDR ranges
func isAllowedSource(remoteAddr string, allowedSources []string) bool {
    host, _, err := net.SplitHostPort(remoteAddr)
    if err != nil {
        host = remoteAddr
    }

    ip := net.ParseIP(host)
    if ip == nil {
        return false
    }

    for _, cidr := range allowedSources {
        _, network, err := net.ParseCIDR(cidr)
        if err != nil {
            continue
        }
        if network.Contains(ip) {
            return true
        }
    }

    return false
}

// isValidIdentity performs basic validation on identity string
func isValidIdentity(identity string) bool {
    // Basic validation: non-empty, reasonable length, no control characters
    if len(identity) == 0 || len(identity) > 256 {
        return false
    }
    for _, r := range identity {
        if r < 32 || r == 127 {
            return false
        }
    }
    return true
}

// GetAuthContext retrieves auth context from request context
func GetAuthContext(r *http.Request) *types.AuthContext {
    if ctx := r.Context().Value(types.AuthContextKey); ctx != nil {
        if authCtx, ok := ctx.(*types.AuthContext); ok {
            return authCtx
        }
    }
    return nil
}
```

**Testing:**
- Unit tests for each extraction function
- Test source IP validation
- Test identity validation
- Test header extraction
- Test TLS requirement enforcement

## Phase 2: Server Integration (Week 2)

### 2.1 Update Server Initialization

**File:** `cmd/server.go`

**Changes:**
```go
func runServer(cmd *cobra.Command, args []string) error {
    cfg, err := config.Load(cliOverrides())
    if err != nil {
        return fmt.Errorf("load config: %w", err)
    }

    // Log authentication mode
    ch.Log(alog.INFO, "[remote-control] Authentication mode: %s", cfg.Auth.Mode)

    // Warn on expiring certs (only in mTLS mode)
    if cfg.Auth.Mode == types.AuthModeMTLS {
        tlsconfig.CheckCertExpiry("server cert", cfg.ServerTLS.CertFile)
        tlsconfig.CheckCertExpiry("server CA", cfg.ServerTLS.TrustedCAFile)
        tlsconfig.CheckCertExpiry("client CA", cfg.ClientTLS.TrustedCAFile)
    }

    srv := server.NewServer(serverAddr, cfg)
    ch.Log(alog.INFO, "[remote-control] server listening on %s", serverAddr)

    // ... rest of function ...

    // Build TLS config with auth mode
    if cfg.ServerTLS.CertFile != "" && cfg.ServerTLS.KeyFile != "" {
        tlsCfg, err := tlsconfig.BuildServerTLSConfig(
            cfg.ServerTLS.CertFile,
            cfg.ServerTLS.KeyFile,
            cfg.ServerTLS.TrustedCAFile,
            cfg.Auth.Mode,
        )
        if err != nil {
            return fmt.Errorf("build TLS config: %w", err)
        }
        if err := srv.ListenAndServeTLS(tlsCfg); err != nil && err != http.ErrServerClosed {
            return err
        }
    } else {
        if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            return err
        }
    }
    return nil
}
```

### 2.2 Update Server Middleware Chain

**File:** `internal/server/server.go`

**Changes:**
```go
func NewServer(addr string, cfg *config.Config) *Server {
    mux := http.NewServeMux()
    s := &Server{
        mux:           mux,
        store:         session.NewStore(int(cfg.MaxOutputBuffer)),
        cfg:           cfg,
        clientTimeout: time.Duration(cfg.ClientTimeoutSeconds) * time.Second,
        upgrader:      &websocket.Upgrader{},
    }
    s.registerRoutes()

    // Build middleware chain with auth
    handler := loggingMiddleware(
        authMiddleware(cfg)(
            recoveryMiddleware(mux),
        ),
    )

    s.httpServer = &http.Server{
        Addr:              addr,
        Handler:           handler,
        ReadHeaderTimeout: 10 * time.Second,
        WriteTimeout:      60 * time.Second,
        IdleTimeout:       120 * time.Second,
    }
    return s
}
```

### 2.3 Update Handlers to Use Auth Context

**File:** `internal/server/handlers.go`

**Changes:**
```go
// Update handlers to extract client ID from auth context
func (s *Server) handleRegisterClient(id string, clientID string, conn *websocket.Conn) (int, interface{}) {
    // ... existing code ...

    // If no clientID provided, try to get from auth context
    if clientID == "" {
        if authCtx := GetAuthContext(r); authCtx != nil {
            clientID = authCtx.ClientID
        }
    }

    // ... rest of function ...
}

// Similar updates for other handlers that need client identity
```

**Testing:**
- Integration tests with different auth modes
- Test auth context propagation
- Test handler behavior with each mode

## Phase 3: Client Updates (Week 3)

### 3.1 Update Client Configuration

**File:** `internal/client/client.go`

**Changes:**
```go
func buildHTTPClient(cfg *config.Config) (*http.Client, *tls.Config) {
    switch cfg.Auth.Mode {
    case types.AuthModeMTLS:
        // Current behavior - load client cert
        if cfg.ClientTLS.CertFile == "" || cfg.ClientTLS.KeyFile == "" || cfg.ClientTLS.TrustedCAFile == "" {
            ch.Log(alog.WARNING, "[remote-control] mTLS mode but certs not configured")
            return &http.Client{Timeout: 30 * time.Second}, nil
        }
        tlsCfg, err := tlsconfig.BuildClientTLSConfig(
            cfg.ClientTLS.CertFile,
            cfg.ClientTLS.KeyFile,
            cfg.ClientTLS.TrustedCAFile,
            types.AuthModeMTLS,
        )
        if err != nil {
            ch.Log(alog.WARNING, "[remote-control] TLS config error: %v", err)
            return &http.Client{Timeout: 30 * time.Second}, nil
        }
        return &http.Client{
            Timeout:   30 * time.Second,
            Transport: &http.Transport{TLSClientConfig: tlsCfg},
        }, tlsCfg

    case types.AuthModeProxy:
        // Proxy mode - verify server cert but no client cert
        if cfg.ClientTLS.TrustedCAFile == "" {
            ch.Log(alog.WARNING, "[remote-control] proxy mode but server CA not configured")
            return &http.Client{Timeout: 30 * time.Second}, nil
        }
        tlsCfg, err := tlsconfig.BuildClientTLSConfig(
            "", "", // No client cert
            cfg.ClientTLS.TrustedCAFile,
            types.AuthModeProxy,
        )
        if err != nil {
            ch.Log(alog.WARNING, "[remote-control] TLS config error: %v", err)
            return &http.Client{Timeout: 30 * time.Second}, nil
        }
        return &http.Client{
            Timeout:   30 * time.Second,
            Transport: &http.Transport{TLSClientConfig: tlsCfg},
        }, tlsCfg

    case types.AuthModeNone:
        // No authentication
        return &http.Client{Timeout: 30 * time.Second}, nil
    }

    // Default to mTLS behavior
    return buildHTTPClient(&config.Config{Auth: config.AuthConfig{Mode: types.AuthModeMTLS}})
}
```

### 3.2 Update Host Configuration

**File:** `internal/host/host.go`

**Changes:**
```go
// Similar updates to buildHTTPClient and buildTLSConfig
func buildHTTPClient(cfg *config.Config) *http.Client {
    switch cfg.Auth.Mode {
    case types.AuthModeMTLS:
        // ... mTLS implementation ...
    case types.AuthModeProxy:
        // ... proxy implementation ...
    case types.AuthModeNone:
        // ... none implementation ...
    }
}

func buildTLSConfig(cfg *config.Config) *tls.Config {
    switch cfg.Auth.Mode {
    case types.AuthModeMTLS:
        // ... mTLS implementation ...
    case types.AuthModeProxy:
        // ... proxy implementation ...
    case types.AuthModeNone:
        return nil
    }
}
```

## Phase 4: Testing (Week 4)

### 4.1 Unit Tests

**Files to create:**
- `internal/server/auth_middleware_test.go`
- `internal/common/tlsconfig/auth_test.go`
- `internal/common/config/auth_config_test.go`

**Test coverage:**
- Auth mode validation
- Identity extraction (mTLS, proxy, none)
- Source IP validation
- Header validation
- TLS requirement enforcement
- Configuration loading and overrides

### 4.2 Integration Tests

**File:** `test/integration/auth_test.go` (NEW)

**Test scenarios:**
```go
func TestAuthModes(t *testing.T) {
    t.Run("mTLS mode", func(t *testing.T) {
        // Test with valid client cert
        // Test with invalid client cert
        // Test with no client cert
    })

    t.Run("proxy mode", func(t *testing.T) {
        // Test with valid identity header
        // Test with missing identity header
        // Test with invalid identity header
        // Test source IP validation
        // Test TLS requirement
    })

    t.Run("none mode", func(t *testing.T) {
        // Test anonymous access
        // Test all operations work
    })
}
```

### 4.3 End-to-End Tests

**File:** `test/e2e/auth_e2e_test.go` (NEW)

**Test scenarios:**
- Full session lifecycle in each auth mode
- Client registration and approval
- WebSocket connections
- Mode switching
- Migration scenarios

### 4.4 Security Tests

**File:** `test/security/auth_security_test.go` (NEW)

**Test scenarios:**
- Header injection attempts
- Bypass attempts
- Invalid identity formats
- Source IP spoofing
- TLS downgrade attempts

## Phase 5: Documentation (Week 5)

### 5.1 Update README

**File:** `README.md`

**Additions:**
- Authentication modes section
- Configuration examples for each mode
- IBM Code Engine deployment guide
- Migration guide from mTLS to proxy

### 5.2 Create Deployment Guides

**Files to create:**
- `docs/deployment/ibm-code-engine.md`
- `docs/deployment/kubernetes-auth-proxy.md`
- `docs/deployment/migration-guide.md`

### 5.3 Update Configuration Documentation

**File:** `docs/configuration.md` (NEW)

**Content:**
- Complete configuration reference
- Environment variables
- CLI flags
- Examples for each auth mode

## Implementation Checklist

### Phase 1: Core Infrastructure
- [ ] Add authentication types to `internal/common/types.go`
- [ ] Update configuration schema in `internal/common/config/config.go`
- [ ] Add environment variable overrides
- [ ] Update TLS configuration in `internal/common/tlsconfig/config.go`
- [ ] Create authentication middleware in `internal/server/auth_middleware.go`
- [ ] Write unit tests for all new code
- [ ] Verify backward compatibility

### Phase 2: Server Integration
- [ ] Update server initialization in `cmd/server.go`
- [ ] Update middleware chain in `internal/server/server.go`
- [ ] Update handlers to use auth context
- [ ] Add CLI flags for auth configuration
- [ ] Write integration tests
- [ ] Test with existing mTLS setup

### Phase 3: Client Updates
- [ ] Update client HTTP client builder
- [ ] Update host HTTP client builder
- [ ] Update WebSocket connection logic
- [ ] Add client-side auth mode handling
- [ ] Write client integration tests
- [ ] Test client with different server modes

### Phase 4: Testing
- [ ] Complete unit test coverage (>80%)
- [ ] Write integration tests for all modes
- [ ] Write end-to-end tests
- [ ] Write security tests
- [ ] Performance testing
- [ ] Load testing with different modes

### Phase 5: Documentation
- [ ] Update README with auth modes
- [ ] Create IBM Code Engine deployment guide
- [ ] Create Kubernetes auth proxy guide
- [ ] Create migration guide
- [ ] Create configuration reference
- [ ] Add examples for each mode
- [ ] Update API documentation

## Rollout Strategy

### Development Environment
1. Implement and test in development
2. Run full test suite
3. Manual testing with all modes
4. Security review

### Staging Environment
1. Deploy with mTLS mode (current behavior)
2. Verify no regressions
3. Test proxy mode with mock auth proxy
4. Test mode switching

### Production Rollout
1. **Week 1**: Release with mTLS as default (no breaking changes)
2. **Week 2**: Beta test proxy mode with select users
3. **Week 3**: General availability for proxy mode
4. **Week 4**: Documentation and examples published
5. **Week 5**: Support and feedback collection

## Risk Mitigation

### Backward Compatibility
- Default to mTLS mode if not specified
- Existing configurations work without changes
- No breaking changes to API
- Comprehensive testing of mTLS mode

### Security
- Thorough security review of proxy mode
- Clear documentation of trust boundaries
- Validation of all inputs
- Audit logging of auth events

### Performance
- Minimal overhead in auth middleware
- Efficient header parsing
- No impact on existing mTLS performance
- Load testing with different modes

## Success Criteria

### Functional
- [ ] All three auth modes work correctly
- [ ] Backward compatibility maintained
- [ ] No regressions in existing functionality
- [ ] WebSocket support in all modes
- [ ] Client approval workflow works in all modes

### Quality
- [ ] >80% test coverage
- [ ] All tests passing
- [ ] No security vulnerabilities
- [ ] Performance within 5% of baseline
- [ ] Documentation complete and accurate

### Deployment
- [ ] Successfully deployed to IBM Code Engine
- [ ] Migration guide validated
- [ ] Examples tested and working
- [ ] Support documentation available

## Timeline Summary

| Phase | Duration | Key Deliverables |
|-------|----------|------------------|
| Phase 1: Core Infrastructure | Week 1 | Auth types, config, middleware |
| Phase 2: Server Integration | Week 2 | Server updates, handler changes |
| Phase 3: Client Updates | Week 3 | Client and host updates |
| Phase 4: Testing | Week 4 | Complete test suite |
| Phase 5: Documentation | Week 5 | Guides and examples |
| **Total** | **5 weeks** | Production-ready auth proxy support |

## Post-Implementation

### Monitoring
- Track auth mode usage
- Monitor auth failures
- Performance metrics per mode
- Error rates and patterns

### Maintenance
- Regular security reviews
- Update documentation as needed
- Address user feedback
- Performance optimization

### Future Enhancements
- OAuth2/OIDC support
- JWT validation
- Multi-tenant support
- Enhanced audit logging
- IBM Cloud IAM integration
