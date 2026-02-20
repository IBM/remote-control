package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/session"
)

// newApprovalTestServer creates a Server with RequireApproval=true.
func newApprovalTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, _ := session.NewStore("memory", session.StoreOptions{})
	cfg := &config.Config{RequireApproval: true, DefaultPermission: "read-write"}
	srv := NewServer(":0", store, cfg)
	ts := httptest.NewServer(srv.mux)
	t.Cleanup(ts.Close)
	return srv, ts
}

// doWithClientID makes an HTTP request with a custom X-Client-ID header.
func doWithClientID(t *testing.T, ts *httptest.Server, method, path, clientID string, body any) *http.Response {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		bodyReader = bytes.NewReader(data)
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, ts.URL+path, bodyReader)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if clientID != "" {
		req.Header.Set("X-Client-ID", clientID)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func TestRegisterClientAutoApproved(t *testing.T) {
	// RequireApproval=false (default test server): clients are auto-approved.
	_, ts := newTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)

	regResp := doWithClientID(t, ts, http.MethodPost, "/sessions/"+created.ID+"/clients", "test-client", nil)
	if regResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", regResp.StatusCode)
	}
	var result map[string]string
	decodeJSON(t, regResp, &result)
	if result["status"] != "approved" {
		t.Errorf("expected auto-approved status, got %s", result["status"])
	}
	if result["client_id"] != "test-client" {
		t.Errorf("expected client_id=test-client, got %s", result["client_id"])
	}
}

func TestRegisterClientPending(t *testing.T) {
	// RequireApproval=true: clients start pending.
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)

	regResp := doWithClientID(t, ts, http.MethodPost, "/sessions/"+created.ID+"/clients", "pending-client", nil)
	if regResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", regResp.StatusCode)
	}
	var result map[string]string
	decodeJSON(t, regResp, &result)
	if result["status"] != "pending" {
		t.Errorf("expected pending status, got %s", result["status"])
	}
}

func TestRegisterClientSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	regResp := doWithClientID(t, ts, http.MethodPost, "/sessions/nonexistent/clients", "client-1", nil)
	if regResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", regResp.StatusCode)
	}
	regResp.Body.Close()
}

func TestListAllClients(t *testing.T) {
	_, ts := newTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Register two clients.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-1", nil).Body.Close()
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-2", nil).Body.Close()

	listResp := getJSON(t, ts, "/sessions/"+sid+"/clients")
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}
	var clients []map[string]any
	decodeJSON(t, listResp, &clients)
	if len(clients) != 2 {
		t.Errorf("expected 2 clients, got %d", len(clients))
	}
}

func TestListPendingClientsFilter(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Register two pending clients.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-1", nil).Body.Close()
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-2", nil).Body.Close()

	// Approve one.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients/client-1/approve",
		"", map[string]string{"permission": "read-write"}).Body.Close()

	// List pending — should only see client-2.
	listResp := getJSON(t, ts, "/sessions/"+sid+"/clients?status=pending")
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", listResp.StatusCode)
	}
	var pending []map[string]any
	decodeJSON(t, listResp, &pending)
	if len(pending) != 1 {
		t.Errorf("expected 1 pending client, got %d", len(pending))
	}
}

func TestListClientsSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := getJSON(t, ts, "/sessions/nonexistent/clients")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestApproveClient(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Register a pending client.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-1", nil).Body.Close()

	// Approve with read-write permission.
	approveResp := doWithClientID(t, ts, http.MethodPost,
		"/sessions/"+sid+"/clients/client-1/approve", "",
		map[string]string{"permission": "read-write"})
	if approveResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", approveResp.StatusCode)
	}
	approveResp.Body.Close()

	// Verify in client list.
	listResp := getJSON(t, ts, "/sessions/"+sid+"/clients")
	var clients []map[string]any
	decodeJSON(t, listResp, &clients)
	found := false
	for _, c := range clients {
		if c["client_id"] == "client-1" && c["approval"] == "approved" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected client-1 to be approved in client list, got %+v", clients)
	}
}

func TestApproveClientDefaultPermission(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-1", nil).Body.Close()

	// Approve without specifying permission (should default to read-write).
	approveResp := doWithClientID(t, ts, http.MethodPost,
		"/sessions/"+sid+"/clients/client-1/approve", "", nil)
	if approveResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", approveResp.StatusCode)
	}
	approveResp.Body.Close()
}

func TestApproveClientNotFound(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	approveResp := doWithClientID(t, ts, http.MethodPost,
		"/sessions/"+sid+"/clients/nonexistent/approve", "",
		map[string]string{"permission": "read-write"})
	if approveResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", approveResp.StatusCode)
	}
	approveResp.Body.Close()
}

func TestApproveClientSessionNotFound(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	approveResp := doWithClientID(t, ts, http.MethodPost,
		"/sessions/nonexistent/clients/client-1/approve", "",
		map[string]string{"permission": "read-write"})
	if approveResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", approveResp.StatusCode)
	}
	approveResp.Body.Close()
}

func TestDenyClient(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Register a pending client.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-1", nil).Body.Close()

	// Deny.
	denyResp := doWithClientID(t, ts, http.MethodPost,
		"/sessions/"+sid+"/clients/client-1/deny", "", nil)
	if denyResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", denyResp.StatusCode)
	}
	denyResp.Body.Close()

	// Verify in client list.
	listResp := getJSON(t, ts, "/sessions/"+sid+"/clients")
	var clients []map[string]any
	decodeJSON(t, listResp, &clients)
	found := false
	for _, c := range clients {
		if c["client_id"] == "client-1" && c["approval"] == "denied" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected client-1 to be denied, got %+v", clients)
	}
}

func TestDenyClientNotFound(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	denyResp := doWithClientID(t, ts, http.MethodPost,
		"/sessions/"+sid+"/clients/nonexistent/deny", "", nil)
	if denyResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", denyResp.StatusCode)
	}
	denyResp.Body.Close()
}

func TestDenyClientSessionNotFound(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	denyResp := doWithClientID(t, ts, http.MethodPost,
		"/sessions/nonexistent/clients/client-1/deny", "", nil)
	if denyResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", denyResp.StatusCode)
	}
	denyResp.Body.Close()
}

// --- checkClientApproval coverage ---

func TestPollOutputForbiddenWhenNotApproved(t *testing.T) {
	// With RequireApproval=true, polling without being approved gets 403.
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Register but don't approve.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-1", nil).Body.Close()

	// Poll output without approval — should be 403.
	pollReq, _ := http.NewRequest(http.MethodGet,
		ts.URL+"/sessions/"+sid+"/output?stdout_offset=0&stderr_offset=0", nil)
	pollReq.Header.Set("X-Client-ID", "client-1")
	pollResp, err := ts.Client().Do(pollReq)
	if err != nil {
		t.Fatalf("GET output: %v", err)
	}
	if pollResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", pollResp.StatusCode)
	}
	pollResp.Body.Close()
}

func TestPollOutputAllowedWhenApproved(t *testing.T) {
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Register and approve.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-1", nil).Body.Close()
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients/client-1/approve",
		"", map[string]string{"permission": "read-write"}).Body.Close()

	// Poll output — should succeed.
	pollReq, _ := http.NewRequest(http.MethodGet,
		ts.URL+"/sessions/"+sid+"/output?stdout_offset=0&stderr_offset=0", nil)
	pollReq.Header.Set("X-Client-ID", "client-1")
	pollResp, err := ts.Client().Do(pollReq)
	if err != nil {
		t.Fatalf("GET output: %v", err)
	}
	if pollResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", pollResp.StatusCode)
	}
	pollResp.Body.Close()
}

func TestEnqueueStdinForbiddenWhenReadOnly(t *testing.T) {
	// Read-only clients cannot enqueue stdin.
	_, ts := newApprovalTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Register and approve as read-only.
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients", "client-ro", nil).Body.Close()
	doWithClientID(t, ts, http.MethodPost, "/sessions/"+sid+"/clients/client-ro/approve",
		"", map[string]string{"permission": "read-only"}).Body.Close()

	// Enqueue stdin with read-only client — should be 403.
	import64 := "bHMgLWxhCg==" // base64("ls -la\n")
	enqReq, _ := http.NewRequest(http.MethodPost,
		ts.URL+"/sessions/"+sid+"/stdin",
		bytes.NewReader(func() []byte { b, _ := json.Marshal(StdinRequest{Source: "client-ro", Data: import64}); return b }()))
	enqReq.Header.Set("Content-Type", "application/json")
	enqReq.Header.Set("X-Client-ID", "client-ro")
	enqResp, err := ts.Client().Do(enqReq)
	if err != nil {
		t.Fatalf("POST stdin: %v", err)
	}
	if enqResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", enqResp.StatusCode)
	}
	enqResp.Body.Close()
}
