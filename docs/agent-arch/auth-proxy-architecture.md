# Authentication Proxy Architecture

## Overview

This document describes the design for a flexible authentication system that supports both mutual TLS (mTLS) and authentication proxy patterns. The goal is to enable deployment on platforms like IBM Code Engine where mTLS may not be supported, while maintaining backward compatibility with existing mTLS deployments.

## Problem Statement

The current implementation uses mutual TLS (mTLS) for client authentication, where:
- Server requires and verifies client certificates
- Clients present certificates for authentication
- All traffic is encrypted via TLS

However, some hosting platforms (like IBM Code Engine) don't support mTLS. Instead, they use authentication proxies that:
- Sit in front of the application
- Handle authentication (e.g., IBM Cloud IAM API Keys)
- Forward authenticated requests with identity headers
- Present normal HTTP/HTTPS traffic to the application

## Design Goals

1. **Flexibility**: Support multiple authentication modes (mTLS, proxy, none)
2. **Backward Compatibility**: Existing mTLS deployments continue to work unchanged
3. **Generic Design**: Work with various auth proxy implementations (IBM IAM, OAuth2, custom)
4. **Security**: Maintain security guarantees appropriate to each mode
5. **Simplicity**: Minimal configuration changes for users

## Architecture

### Authentication Modes

The system will support three authentication modes:

#### 1. mTLS Mode (Current, Default)
- Server requires client certificates
- Client identity derived from certificate CN/SAN
- Full end-to-end encryption
- **Use case**: Direct deployments, VPN environments, high-security scenarios

#### 2. Proxy Mode (New)
- Server accepts requests without client certificates
- Client identity extracted from HTTP headers
- Auth proxy handles authentication upstream
- **Use case**: IBM Code Engine, Kubernetes with auth sidecars, API gateways

#### 3. None Mode (New, Development Only)
- No authentication required
- All clients auto-approved
- **Use case**: Local development, testing, trusted networks

### Configuration Schema

```json
{
  "auth": {
    "mode": "mtls|proxy|none",
    "proxy": {
      "identity_header": "X-Authenticated-User",
      "trusted_headers": ["X-Forwarded-For", "X-Real-IP"],
      "require_tls": true,
      "allowed_sources": ["10.0.0.0/8", "172.16.0.0/12"]
    }
  }
}
```

### Component Changes

#### 1. Server TLS Configuration (`internal/common/tlsconfig/config.go`)

**Current:**
```go
ClientAuth: tls.RequireAndVerifyClientCert
```

**New:**
```go
func BuildServerTLSConfig(serverCertFile, serverKeyFile, clientCAFile string, authMode AuthMode) (*tls.Config, error) {
    // ... existing cert loading ...
    
    var clientAuth tls.ClientAuthType
    var clientCAs *x509.CertPool
    
    switch authMode {
    case AuthModeMTLS:
        clientAuth = tls.RequireAndVerifyClientCert
        clientCAs = loadedClientCA
    case AuthModeProxy, AuthModeNone:
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
```

#### 2. Authentication Middleware (`internal/server/auth_middleware.go` - New)

```go
type AuthContext struct {
    Mode       AuthMode
    ClientID   string
    Verified   bool
    Source     string // "mtls", "proxy-header", "none"
    Metadata   map[string]string
}

func authMiddleware(cfg *config.Config) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx := r.Context()
            
            switch cfg.Auth.Mode {
            case AuthModeMTLS:
                authCtx := extractMTLSIdentity(r)
                ctx = context.WithValue(ctx, authContextKey, authCtx)
                
            case AuthModeProxy:
                authCtx := extractProxyIdentity(r, cfg)
                if !authCtx.Verified {
                    http.Error(w, "Unauthorized", http.StatusUnauthorized)
                    return
                }
                ctx = context.WithValue(ctx, authContextKey, authCtx)
                
            case AuthModeNone:
                authCtx := &AuthContext{
                    Mode:     AuthModeNone,
                    ClientID: "anonymous",
                    Verified: true,
                    Source:   "none",
                }
                ctx = context.WithValue(ctx, authContextKey, authCtx)
            }
            
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func extractMTLSIdentity(r *http.Request) *AuthContext {
    if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
        return &AuthContext{
            Mode:     AuthModeMTLS,
            Verified: false,
        }
    }
    
    cert := r.TLS.PeerCertificates[0]
    return &AuthContext{
        Mode:     AuthModeMTLS,
        ClientID: cert.Subject.CommonName,
        Verified: true,
        Source:   "mtls",
        Metadata: map[string]string{
            "serial": cert.SerialNumber.String(),
        },
    }
}

func extractProxyIdentity(r *http.Request, cfg *config.Config) *AuthContext {
    // Validate request comes from trusted source
    if cfg.Auth.Proxy.RequireTLS && r.TLS == nil {
        return &AuthContext{Mode: AuthModeProxy, Verified: false}
    }
    
    // Check source IP if configured
    if len(cfg.Auth.Proxy.AllowedSources) > 0 {
        if !isAllowedSource(r.RemoteAddr, cfg.Auth.Proxy.AllowedSources) {
            return &AuthContext{Mode: AuthModeProxy, Verified: false}
        }
    }
    
    // Extract identity from configured header
    clientID := r.Header.Get(cfg.Auth.Proxy.IdentityHeader)
    if clientID == "" {
        return &AuthContext{Mode: AuthModeProxy, Verified: false}
    }
    
    metadata := make(map[string]string)
    for _, header := range cfg.Auth.Proxy.TrustedHeaders {
        if value := r.Header.Get(header); value != "" {
            metadata[header] = value
        }
    }
    
    return &AuthContext{
        Mode:     AuthModeProxy,
        ClientID: clientID,
        Verified: true,
        Source:   "proxy-header",
        Metadata: metadata,
    }
}
```

#### 3. Client Configuration (`internal/client/client.go`)

Clients need to know which auth mode to use:

```go
type Client struct {
    cfg       *config.Config
    api       *types.APIClient
    clientID  string
    tlsConfig *tls.Config
    authMode  AuthMode
}

func buildHTTPClient(cfg *config.Config) (*http.Client, *tls.Config) {
    switch cfg.Auth.Mode {
    case AuthModeMTLS:
        // Current behavior - load client cert
        tlsCfg, err := tlsconfig.BuildClientTLSConfig(...)
        return &http.Client{
            Transport: &http.Transport{TLSClientConfig: tlsCfg},
        }, tlsCfg
        
    case AuthModeProxy:
        // No client cert, but verify server cert
        tlsCfg := &tls.Config{
            MinVersion: tls.VersionTLS13,
            RootCAs:    loadServerCA(cfg.ClientTLS.TrustedCAFile),
        }
        return &http.Client{
            Transport: &http.Transport{TLSClientConfig: tlsCfg},
        }, tlsCfg
        
    case AuthModeNone:
        // Plain HTTP or HTTPS without verification
        return &http.Client{Timeout: 30 * time.Second}, nil
    }
}
```

### IBM Code Engine Integration

#### Deployment Pattern

```
┌─────────────────────────────────────────────────────┐
│ IBM Code Engine                                     │
│                                                     │
│  ┌──────────────────────────────────────────────┐  │
│  │ IAM Auth Proxy (Code Engine Managed)         │  │
│  │ - Validates IBM Cloud IAM API Keys           │  │
│  │ - Adds X-Authenticated-User header           │  │
│  │ - Forwards to application                    │  │
│  └──────────────────┬───────────────────────────┘  │
│                     │                               │
│  ┌──────────────────▼───────────────────────────┐  │
│  │ remote-control server                        │  │
│  │ - auth.mode = "proxy"                        │  │
│  │ - auth.proxy.identity_header =               │  │
│  │   "X-Authenticated-User"                     │  │
│  │ - No client cert validation                  │  │
│  └──────────────────────────────────────────────┘  │
│                                                     │
└─────────────────────────────────────────────────────┘
                      ▲
                      │ HTTPS + IAM API Key
                      │
              ┌───────┴────────┐
              │ remote-control │
              │ client/host    │
              └────────────────┘
```

#### Configuration Example

**Server (in Code Engine):**
```json
{
  "auth": {
    "mode": "proxy",
    "proxy": {
      "identity_header": "X-Authenticated-User",
      "trusted_headers": ["X-Forwarded-For", "X-Request-ID"],
      "require_tls": true
    }
  },
  "server_url": "https://remote-control.example.com",
  "require_approval": false,
  "default_permission": "read-write"
}
```

**Client:**
```json
{
  "auth": {
    "mode": "proxy"
  },
  "server_url": "https://remote-control.example.com"
}
```

**Environment Variables (for IBM Cloud IAM):**
```bash
# Client sets IAM API Key for authentication
export IBMCLOUD_API_KEY="your-api-key-here"

# Client uses standard HTTP client with IAM token
# (handled by IBM Cloud SDK or custom token provider)
```

### Security Considerations

#### Proxy Mode Security

1. **TLS Requirement**: By default, require TLS even in proxy mode to prevent header injection
2. **Source Validation**: Optionally restrict requests to known proxy IP ranges
3. **Header Validation**: Validate identity header format and content
4. **Audit Logging**: Log all authentication attempts and failures

#### Trust Boundary

In proxy mode, the trust boundary shifts:
- **mTLS mode**: Trust established at TLS layer (cryptographic)
- **Proxy mode**: Trust established at application layer (header-based)
- **Critical**: Proxy mode assumes the auth proxy is trusted and cannot be bypassed

#### Deployment Recommendations

**Proxy Mode Should Only Be Used When:**
1. The auth proxy is managed by the platform (e.g., Code Engine)
2. Direct access to the application is prevented (network isolation)
3. The proxy validates credentials cryptographically (e.g., IAM tokens)
4. TLS is enforced between client and proxy

**Never Use Proxy Mode When:**
1. The application is directly exposed to the internet
2. The proxy can be bypassed
3. Header values can be spoofed by clients

### Migration Path

#### From mTLS to Proxy Mode

1. **Deploy new server with proxy mode** in parallel
2. **Update clients gradually** to use new endpoint
3. **Decommission old mTLS server** when all clients migrated

#### Configuration Migration

**Before (mTLS):**
```json
{
  "server_url": "https://localhost:8443",
  "server_tls": {
    "cert_file": "~/.remote-control/certs/server-cert.pem",
    "key_file": "~/.remote-control/certs/server-key.pem",
    "trusted_ca_file": "~/.remote-control/certs/client-ca.pem"
  },
  "client_tls": {
    "cert_file": "~/.remote-control/certs/client-cert.pem",
    "key_file": "~/.remote-control/certs/client-key.pem",
    "trusted_ca_file": "~/.remote-control/certs/server-ca.pem"
  }
}
```

**After (Proxy):**
```json
{
  "auth": {
    "mode": "proxy",
    "proxy": {
      "identity_header": "X-Authenticated-User"
    }
  },
  "server_url": "https://remote-control.example.com",
  "server_tls": {
    "trusted_ca_file": "~/.remote-control/certs/server-ca.pem"
  }
}
```

### Testing Strategy

#### Unit Tests
- Auth middleware with different modes
- Identity extraction from headers
- Source IP validation
- TLS requirement enforcement

#### Integration Tests
- End-to-end with mock auth proxy
- Header injection prevention
- Mode switching
- Backward compatibility with mTLS

#### Security Tests
- Header spoofing attempts
- Bypass attempts
- Invalid identity formats
- Missing required headers

## Implementation Phases

### Phase 1: Core Infrastructure
1. Add `AuthMode` type and configuration schema
2. Implement auth middleware framework
3. Update TLS configuration to support optional client certs
4. Add auth context to request handling

### Phase 2: Proxy Mode Implementation
1. Implement proxy identity extraction
2. Add source validation
3. Add header validation
4. Update handlers to use auth context

### Phase 3: Client Updates
1. Update client to support auth modes
2. Add proxy mode HTTP client configuration
3. Update documentation

### Phase 4: Testing & Documentation
1. Comprehensive test suite
2. Security testing
3. Migration guide
4. IBM Code Engine deployment guide

## Configuration Reference

### Server Configuration

```json
{
  "auth": {
    "mode": "mtls|proxy|none",
    "proxy": {
      "identity_header": "X-Authenticated-User",
      "trusted_headers": ["X-Forwarded-For", "X-Real-IP"],
      "require_tls": true,
      "allowed_sources": ["10.0.0.0/8"]
    }
  }
}
```

### Environment Variables

```bash
# Auth mode
REMOTE_CONTROL_AUTH_MODE=proxy

# Proxy configuration
REMOTE_CONTROL_AUTH_PROXY_IDENTITY_HEADER=X-Authenticated-User
REMOTE_CONTROL_AUTH_PROXY_REQUIRE_TLS=true
REMOTE_CONTROL_AUTH_PROXY_ALLOWED_SOURCES=10.0.0.0/8,172.16.0.0/12
```

### CLI Flags

```bash
# Server
remote-control server --auth-mode=proxy \
  --auth-proxy-identity-header=X-Authenticated-User

# Client
remote-control connect --auth-mode=proxy
```

## Backward Compatibility

### Default Behavior
- If no `auth.mode` is specified, default to `mtls` (current behavior)
- Existing configurations continue to work without changes
- New deployments can opt into proxy mode explicitly

### Deprecation Path
- mTLS remains fully supported indefinitely
- No plans to deprecate mTLS mode
- Both modes are first-class citizens

## Future Enhancements

### Potential Additions
1. **OAuth2/OIDC Support**: Direct OAuth2 token validation
2. **JWT Validation**: Validate JWT tokens in headers
3. **Multi-tenant Support**: Namespace isolation based on identity
4. **Rate Limiting**: Per-identity rate limits
5. **Audit Logging**: Comprehensive auth event logging

### IBM Cloud Specific
1. **IAM Token Validation**: Direct validation of IBM Cloud IAM tokens
2. **Service ID Support**: Automatic service-to-service auth
3. **Resource Groups**: Map identities to resource groups
4. **Activity Tracker**: Integration with IBM Cloud Activity Tracker

## Conclusion

This architecture provides a flexible, secure authentication system that:
- Maintains backward compatibility with mTLS
- Enables deployment on platforms like IBM Code Engine
- Supports various auth proxy implementations
- Provides clear security boundaries and trust models
- Offers a straightforward migration path

The design prioritizes security while providing the flexibility needed for modern cloud deployments.
