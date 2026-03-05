package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/session"
)

// Server wraps an http.Server and holds the session store.
type Server struct {
	httpServer    *http.Server
	mux           *http.ServeMux
	store         session.Store
	cfg           *config.Config
	clientTimeout time.Duration
}

// NewServer creates a new Server bound to addr, using the given Store.
func NewServer(addr string, store session.Store, cfg *config.Config) *Server {
	mux := http.NewServeMux()
	s := &Server{
		mux:           mux,
		store:         store,
		cfg:           cfg,
		clientTimeout: time.Duration(cfg.ClientTimeoutSeconds) * time.Second,
	}
	s.registerRoutes()

	handler := loggingMiddleware(recoveryMiddleware(mux))
	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// ListenAndServe starts the HTTP server. Blocks until the server stops.
func (s *Server) ListenAndServe() error {
	return s.httpServer.ListenAndServe()
}

// ListenAndServeTLS starts the HTTPS server with the given TLS config.
func (s *Server) ListenAndServeTLS(tlsCfg *tls.Config) error {
	s.httpServer.TLSConfig = tlsCfg
	return s.httpServer.ListenAndServeTLS("", "")
}

// Shutdown gracefully stops the server with the given context.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// Addr returns the server's bound address.
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// Handler returns the HTTP handler for use with custom listeners (e.g., in tests).
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// clientIdentity extracts a client ID and common name from the request.
// In Phase 6+ this uses TLS peer cert CN. Before TLS, falls back to a header.
func (s *Server) clientIdentity(r *http.Request) (clientID, commonName string) {
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cn := r.TLS.PeerCertificates[0].Subject.CommonName
		return cn, cn
	}
	// Pre-TLS fallback: use a header or generate a synthetic ID.
	clientID = r.Header.Get("X-Client-ID")
	if clientID == "" {
		clientID = fmt.Sprintf("anon-%s", r.RemoteAddr)
	}
	return clientID, clientID
}

// checkClientApproved verifies that the requesting client is approved.
// Returns (approved, readWrite). On false, the handler should return 403.
func (s *Server) checkClientApproval(sess *session.Session, clientID string, needWrite bool) bool {
	if !s.cfg.RequireApproval {
		return true
	}
	rec, err := sess.GetClient(clientID)
	if err != nil {
		return false
	}
	if rec.Approval != session.ApprovalApproved {
		return false
	}
	if needWrite && rec.Permission == session.PermissionReadOnly {
		return false
	}
	return true
}
