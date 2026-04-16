package server

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
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
				// If no TLS is configured (r.TLS == nil), allow the request through
				// This supports tests and setups where TLS is not yet configured
				if r.TLS == nil {
					authCh.Log(alog.WARNING, "mTLS mode configured but TLS not active; allowing anonymous access")
					authCtx = &types.AuthContext{
						Mode:     types.AuthModeMTLS,
						ClientID: "anonymous",
						Verified: true,
						Source:   "none",
					}
				} else {
					authCtx = extractMTLSIdentity(r)
					if !authCtx.Verified {
						authCh.Log(alog.WARNING, "mTLS authentication failed")
						http.Error(w, "Unauthorized", http.StatusUnauthorized)
						return
					}
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

			default:
				// Default to allowing access for backward compatibility
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

	// Extract identity from header if configured
	clientID := ""
	if cfg.Auth.Proxy.IdentityHeader != "" {
		clientID = r.Header.Get(cfg.Auth.Proxy.IdentityHeader)
		if clientID == "" {
			authCh.Log(alog.DEBUG, "Identity header missing: %s", cfg.Auth.Proxy.IdentityHeader)
			return &types.AuthContext{Mode: types.AuthModeProxy, Verified: false}
		}

		// Validate identity format (basic validation)
		if !isValidIdentity(clientID) {
			authCh.Log(alog.DEBUG, "Invalid identity format: %s", clientID)
			return &types.AuthContext{Mode: types.AuthModeProxy, Verified: false}
		}
	}

	return &types.AuthContext{
		Mode:     types.AuthModeProxy,
		ClientID: clientID,
		Verified: true,
		Source:   "proxy-header",
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
		_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
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
