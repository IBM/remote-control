package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

/* --- Client identity bound to authenticated principal ------------------- */

// approveAuth has the host approve the given client with the given permission.
func approveAuth(t *testing.T, srv *Server, sessionID, clientID string, perm types.Permission) {
	t.Helper()
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sessionID+"/clients/"+clientID+"/approve",
		types.ApproveClientRequest{Permission: perm},
		types.HostClientID, types.AuthModeMTLS)
	if w.Code != http.StatusNoContent {
		t.Fatalf("approve client: expected 204, got %d", w.Code)
	}
}

func stdinBody() types.StdinEntry {
	return types.StdinEntry{Data: []byte("id\n")}
}

func TestStdinAsHostBlockedForNonHostIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)

	// Attacker with a valid but unapproved identity claims to be the host.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/stdin?client_id=host", stdinBody(), "attacker", types.AuthModeMTLS)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for non-host submitting stdin as host, got %d", w.Code)
	}

	// Nothing may have reached the host's stdin queue.
	s, err := srv.store.Get(sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if queued := s.PeekClientQueue(types.HostClientID, types.WSMessageStdin); len(queued) != 0 {
		t.Errorf("expected empty host stdin queue, got %d entries", len(queued))
	}
}

func TestStdinAsHostAllowedForHostIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)

	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/stdin?client_id=host", stdinBody(), types.HostClientID, types.AuthModeMTLS)
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201 for host submitting stdin, got %d", w.Code)
	}
}

func TestStdinWithAnotherIdentitysClientForbidden(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	reg := registerClientAuth(t, srv, sess.ID, "alice", types.AuthModeMTLS)
	approveAuth(t, srv, sess.ID, reg.ClientID, types.PermissionReadWrite)

	// Mallory knows alice's client ID and tries to use it.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/stdin?client_id="+reg.ClientID, stdinBody(), "mallory", types.AuthModeMTLS)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for stdin with another identity's client, got %d", w.Code)
	}

	// Alice can use her own client.
	w = serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/stdin?client_id="+reg.ClientID, stdinBody(), "alice", types.AuthModeMTLS)
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201 for stdin from owning identity, got %d", w.Code)
	}
}

func TestStdinAsHostAllowedInNoneMode(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, "anonymous", types.AuthModeNone)

	// In AuthModeNone, the host identity is not restricted.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/stdin?client_id=host", stdinBody(), "anonymous", types.AuthModeNone)
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201 in none-mode, got %d", w.Code)
	}
}

func TestPollAckAsHostBlockedForNonHostIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)

	for _, op := range []string{"poll", "ack"} {
		target := fmt.Sprintf("/sessions/%s/%d/%s?client_id=host", sess.ID, types.WSMessageStdin, op)
		if w := serveWithAuth(t, srv, http.MethodGet, target, nil, "attacker", types.AuthModeMTLS); w.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403 for non-host acting as host, got %d", op, w.Code)
		}
		if w := serveWithAuth(t, srv, http.MethodGet, target, nil, types.HostClientID, types.AuthModeMTLS); w.Code != http.StatusOK {
			t.Errorf("%s: expected 200 for host, got %d", op, w.Code)
		}
	}
}

func TestPollAckWithAnotherIdentitysClientForbidden(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	reg := registerClientAuth(t, srv, sess.ID, "alice", types.AuthModeMTLS)
	approveAuth(t, srv, sess.ID, reg.ClientID, types.PermissionReadOnly)

	for _, op := range []string{"poll", "ack"} {
		target := fmt.Sprintf("/sessions/%s/%d/%s?client_id=%s", sess.ID, types.WSMessageOutput, op, reg.ClientID)
		if w := serveWithAuth(t, srv, http.MethodGet, target, nil, "mallory", types.AuthModeMTLS); w.Code != http.StatusForbidden {
			t.Errorf("%s: expected 403 for another identity's client, got %d", op, w.Code)
		}
		if w := serveWithAuth(t, srv, http.MethodGet, target, nil, "alice", types.AuthModeMTLS); w.Code != http.StatusOK {
			t.Errorf("%s: expected 200 for owning identity, got %d", op, w.Code)
		}
	}
}

func TestPollRequiresApproval(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	reg := registerClientAuth(t, srv, sess.ID, "alice", types.AuthModeMTLS)
	target := fmt.Sprintf("/sessions/%s/%d/poll?client_id=%s", sess.ID, types.WSMessageOutput, reg.ClientID)

	if w := serveWithAuth(t, srv, http.MethodGet, target, nil, "alice", types.AuthModeMTLS); w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for pending client poll, got %d", w.Code)
	}
	approveAuth(t, srv, sess.ID, reg.ClientID, types.PermissionReadOnly)
	if w := serveWithAuth(t, srv, http.MethodGet, target, nil, "alice", types.AuthModeMTLS); w.Code != http.StatusOK {
		t.Errorf("expected 200 for approved client poll, got %d", w.Code)
	}
}

func TestRegisterReuseBlockedForAnotherIdentity(t *testing.T) {
	srv := newAuthTestServer(t)
	sess := createSessionAuth(t, srv, types.HostClientID, types.AuthModeMTLS)
	alice := registerClientAuth(t, srv, sess.ID, "alice", types.AuthModeMTLS)
	approveAuth(t, srv, sess.ID, alice.ClientID, types.PermissionReadWrite)

	// Mallory tries to take over alice's record by supplying its ID.
	w := serveWithAuth(t, srv, http.MethodPost,
		"/sessions/"+sess.ID+"/clients?client_id="+alice.ClientID, nil, "mallory", types.AuthModeMTLS)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var reg types.RegisterClientResponse
	if err := json.NewDecoder(w.Body).Decode(&reg); err != nil {
		t.Fatalf("decode register response: %v", err)
	}
	if reg.ClientID == alice.ClientID {
		t.Error("expected a fresh client ID, got alice's")
	}
	if reg.Status != types.ApprovalPending {
		t.Errorf("expected new record to be pending, got %s", reg.Status)
	}

	// Alice's record is untouched.
	s, err := srv.store.Get(sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if c := s.GetClient(alice.ClientID); c == nil || c.Owner() != "alice" || c.Info.Approval != types.ApprovalApproved {
		t.Error("expected alice's record to keep its owner and approval")
	}
}
