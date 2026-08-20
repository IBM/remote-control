package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IBM/remote-control/internal/common/config"
	"github.com/IBM/remote-control/internal/common/types"
)

// withAuthContext injects an AuthContext into the request, mimicking what
// authMiddleware does in production.
func withAuthContext(r *http.Request, clientID string, mode types.AuthMode) *http.Request {
	authCtx := &types.AuthContext{
		Mode:     mode,
		ClientID: clientID,
		Verified: true,
		Source:   "test",
	}
	return r.WithContext(context.WithValue(r.Context(), types.AuthContextKey, authCtx))
}

// serveWithAuth executes a handler request directly (no TCP round-trip) so the
// injected AuthContext survives to the handler.
func serveWithAuth(t *testing.T, srv *Server, method, target string, body any, clientID string, mode types.AuthMode) *httptest.ResponseRecorder {
	t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req = withAuthContext(req, clientID, mode)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	return w
}

func newAuthTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		RequireApproval: true,
		MaxOutputBuffer: 1024 * 1024,
	}
	return NewServer(":0", cfg)
}

func createSessionAuth(t *testing.T, srv *Server, callerID string, mode types.AuthMode) types.SessionInfo {
	t.Helper()
	w := serveWithAuth(t, srv, http.MethodPost, "/sessions",
		types.CreateSessionRequest{Name: "test"}, callerID, mode)
	if w.Code != http.StatusCreated {
		t.Fatalf("create session: expected 201, got %d", w.Code)
	}
	var sess types.SessionInfo
	if err := json.NewDecoder(w.Body).Decode(&sess); err != nil {
		t.Fatalf("decode session: %v", err)
	}
	return sess
}

func registerClientAuth(t *testing.T, srv *Server, sessionID, callerID string, mode types.AuthMode) types.RegisterClientResponse {
	t.Helper()
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sessionID+"/clients", nil, callerID, mode)
	if w.Code != http.StatusOK {
		t.Fatalf("register client: expected 200, got %d", w.Code)
	}
	var reg types.RegisterClientResponse
	if err := json.NewDecoder(w.Body).Decode(&reg); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	return reg
}

/* --- Sub-Task 1: Host impersonation via HTTP registration ---------------- */

func TestRegisterAsHostBlockedForNonHostIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)

	// Attacker with a valid but non-host identity tries to register as host.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients?client_id=host", nil, "attacker", types.AuthModeMTLS)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-host claiming host slot, got %d", w.Code)
	}
}

func TestRegisterAsHostAllowedForHostIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)

	// Caller whose identity IS "host" should be allowed to register as host.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients?client_id=host", nil, types.HostClientID, types.AuthModeMTLS)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for host claiming host slot, got %d", w.Code)
	}
}

func TestRegisterAsNonHostAllowedForAnyIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)

	// Any authenticated caller can register as a regular (non-host) client.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients", nil, "some-client", types.AuthModeMTLS)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for non-host client registration, got %d", w.Code)
	}
}

func TestRegisterAsHostAllowedInNoneMode(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, "anonymous", types.AuthModeNone)

	// In AuthModeNone, host-slot registration is not restricted.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients?client_id=host", nil, "anonymous", types.AuthModeNone)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 in none-mode, got %d", w.Code)
	}
}

/* --- Sub-Task 2: Approve/deny restricted to host identity --------------- */

func TestApproveClientByNonHostForbidden(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	reg := registerClientAuth(t, srv, sess.ID, "client-a", types.AuthModeMTLS)

	// A non-host tries to approve the pending client.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients/"+reg.ClientID+"/approve",
		types.ApproveClientRequest{Permission: types.PermissionReadWrite},
		"client-a", types.AuthModeMTLS)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-host approving client, got %d", w.Code)
	}
}

func TestApproveClientByHostAllowed(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	reg := registerClientAuth(t, srv, sess.ID, "client-b", types.AuthModeMTLS)

	// The host approves the pending client.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients/"+reg.ClientID+"/approve",
		types.ApproveClientRequest{Permission: types.PermissionReadWrite},
		types.HostClientID, types.AuthModeMTLS)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for host approving client, got %d", w.Code)
	}
}

func TestDenyClientByNonHostForbidden(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	reg := registerClientAuth(t, srv, sess.ID, "client-c", types.AuthModeMTLS)

	// A non-host tries to deny the pending client.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients/"+reg.ClientID+"/deny",
		nil, "client-c", types.AuthModeMTLS)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-host denying client, got %d", w.Code)
	}
}

func TestDenyClientByHostAllowed(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	reg := registerClientAuth(t, srv, sess.ID, "client-d", types.AuthModeMTLS)

	// The host denies the pending client.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients/"+reg.ClientID+"/deny",
		nil, types.HostClientID, types.AuthModeMTLS)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 for host denying client, got %d", w.Code)
	}
}

func TestApproveClientAllowedInNoneMode(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, "anonymous", types.AuthModeNone)
	reg := registerClientAuth(t, srv, sess.ID, "anonymous", types.AuthModeNone)

	// In AuthModeNone, any caller may approve.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients/"+reg.ClientID+"/approve",
		types.ApproveClientRequest{Permission: types.PermissionReadWrite},
		"anonymous", types.AuthModeNone)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204 in none-mode, got %d", w.Code)
	}
}
