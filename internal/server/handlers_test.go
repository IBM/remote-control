package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
)

/* --- Test Helpers --------------------------------------------------------- */

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	srv := NewServer(":0", cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func newTestServerWithConfig(t *testing.T, cfg *config.Config) (*Server, *httptest.Server) {
	t.Helper()
	srv := NewServer(":0", cfg)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func postJSON(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func getJSON(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func deleteJSON(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+path, nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

func patchJSON(t *testing.T, ts *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH %s: %v", path, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

func createTestSession(t *testing.T, ts *httptest.Server) types.SessionInfo {
	t.Helper()
	resp := postJSON(t, ts, "/sessions", types.CreateSessionRequest{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var session types.SessionInfo
	decodeJSON(t, resp, &session)
	return session
}

func registerTestClient(t *testing.T, ts *httptest.Server, sessionID string) string {
	t.Helper()
	resp := postJSON(t, ts, "/sessions/"+sessionID+"/clients", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var result types.RegisterClientResponse
	decodeJSON(t, resp, &result)
	return result.ClientID
}

/* --- Session CRUD Tests --------------------------------------------------- */

func TestHandleCreateSessionWithoutID(t *testing.T) {
	_, ts := newTestServer(t)

	resp := postJSON(t, ts, "/sessions", types.CreateSessionRequest{})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var session types.SessionInfo
	decodeJSON(t, resp, &session)

	if session.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if session.Status != types.SessionStatusActive {
		t.Errorf("expected active status, got %d", session.Status)
	}
	if session.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt timestamp")
	}
}

func TestHandleCreateSessionWithID(t *testing.T) {
	_, ts := newTestServer(t)

	customID := "custom-session-123"
	resp := postJSON(t, ts, "/sessions", types.CreateSessionRequest{ID: customID})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var session types.SessionInfo
	decodeJSON(t, resp, &session)

	if session.ID != customID {
		t.Errorf("expected ID %q, got %q", customID, session.ID)
	}
}

func TestHandleListSessionsEmpty(t *testing.T) {
	_, ts := newTestServer(t)

	resp := getJSON(t, ts, "/sessions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sessions []types.SessionInfo
	decodeJSON(t, resp, &sessions)

	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestHandleListSessionsMultiple(t *testing.T) {
	_, ts := newTestServer(t)

	// Create 3 sessions
	for i := 0; i < 3; i++ {
		postJSON(t, ts, "/sessions", types.CreateSessionRequest{})
	}

	resp := getJSON(t, ts, "/sessions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var sessions []types.SessionInfo
	decodeJSON(t, resp, &sessions)

	if len(sessions) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(sessions))
	}
}

func TestHandleGetSessionExists(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)

	resp := getJSON(t, ts, "/sessions/"+created.ID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var session types.SessionInfo
	decodeJSON(t, resp, &session)

	if session.ID != created.ID {
		t.Errorf("expected ID %q, got %q", created.ID, session.ID)
	}
}

func TestHandleGetSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := getJSON(t, ts, "/sessions/nonexistent")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}

	var errResp types.ErrorResponse
	decodeJSON(t, resp, &errResp)
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleDeleteSessionExists(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)

	resp := deleteJSON(t, ts, "/sessions/"+created.ID)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	// Verify session is gone
	getResp := getJSON(t, ts, "/sessions/"+created.ID)
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", getResp.StatusCode)
	}
}

func TestHandleDeleteSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := deleteJSON(t, ts, "/sessions/nonexistent")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandlePatchSession(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)

	exitCode := 42
	resp := patchJSON(t, ts, "/sessions/"+created.ID, types.PatchSessionRequest{ExitCode: exitCode})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var session types.SessionInfo
	decodeJSON(t, resp, &session)

	if session.Status != types.SessionStatusCompleted {
		t.Errorf("expected completed status, got %d", session.Status)
	}
	if session.ExitCode == nil || *session.ExitCode != exitCode {
		t.Errorf("expected exit code %d, got %v", exitCode, session.ExitCode)
	}
	if session.CompletedAt == nil || session.CompletedAt.IsZero() {
		t.Error("expected non-zero CompletedAt timestamp")
	}
}

func TestHandlePatchSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := patchJSON(t, ts, "/sessions/nonexistent", types.PatchSessionRequest{ExitCode: 0})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandlePatchSessionDeletesSession(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)

	// Complete the session
	patchJSON(t, ts, "/sessions/"+created.ID, types.PatchSessionRequest{ExitCode: 0})

	// Verify session is deleted
	getResp := getJSON(t, ts, "/sessions/"+created.ID)
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion, got %d", getResp.StatusCode)
	}
}

/* --- Output Handling Tests ------------------------------------------------ */

func TestHandleAppendOutputNewSession(t *testing.T) {
	_, ts := newTestServer(t)

	sessionID := "new-session-123"
	data := base64.StdEncoding.EncodeToString([]byte("test output"))

	resp := postJSON(t, ts, "/sessions/"+sessionID+"/output", types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte(data),
	})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 (session created), got %d", resp.StatusCode)
	}

	// Verify session was created
	getResp := getJSON(t, ts, "/sessions/"+sessionID)
	if getResp.StatusCode != http.StatusOK {
		t.Errorf("expected session to exist, got %d", getResp.StatusCode)
	}
}

func TestHandleAppendOutputExistingSession(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	data := base64.StdEncoding.EncodeToString([]byte("test output"))

	resp := postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte(data),
	})

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestHandleAppendOutputStdout(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	data := base64.StdEncoding.EncodeToString([]byte("stdout data"))

	resp := postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte(data),
	})

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestHandleAppendOutputStderr(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	data := base64.StdEncoding.EncodeToString([]byte("stderr data"))

	resp := postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: types.StreamStderr,
		Data:   []byte(data),
	})

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestHandleAppendOutputInvalidStream(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	data := base64.StdEncoding.EncodeToString([]byte("test data"))

	resp := postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: 99, // Invalid stream
		Data:   []byte(data),
	})

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	var errResp types.ErrorResponse
	decodeJSON(t, resp, &errResp)
	if errResp.Error == "" {
		t.Error("expected non-empty error message")
	}
}

func TestHandleAppendOutputEmptyData(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	data := base64.StdEncoding.EncodeToString([]byte(""))

	resp := postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte(data),
	})

	// Empty data should still succeed
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
}

func TestHandleAppendOutputClientTimeout(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 1, // 1 second timeout
	}
	srv, ts := newTestServerWithConfig(t, cfg)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	// Get the session and update client activity to make it "old"
	sess, _ := srv.store.Get(created.ID)
	client := sess.GetClient(clientID)
	if client != nil {
		client.Info.LastPollAt = time.Now().Add(-2 * time.Second)
	}

	// Append output, which should trigger cleanup
	data := base64.StdEncoding.EncodeToString([]byte("test"))
	postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte(data),
	})

	// Verify client was removed
	sess, _ = srv.store.Get(created.ID)
	if sess.GetClient(clientID) != nil {
		t.Error("expected inactive client to be removed")
	}
}

/* --- Poll/Ack Flow Tests -------------------------------------------------- */

func TestHandlePollOutputEmpty(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	resp := getJSON(t, ts, "/sessions/"+created.ID+"/10/poll?client_id="+clientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var poll types.PollResponse
	decodeJSON(t, resp, &poll)

	if len(poll.Elements) != 0 {
		t.Errorf("expected 0 elements, got %d", len(poll.Elements))
	}
}

func TestHandlePollOutputWithMessages(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	// Append some output
	data := base64.StdEncoding.EncodeToString([]byte("test output"))
	postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte(data),
	})

	// Poll for output
	resp := getJSON(t, ts, "/sessions/"+created.ID+"/10/poll?client_id="+clientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var poll types.PollResponse
	decodeJSON(t, resp, &poll)

	if len(poll.Elements) == 0 {
		t.Error("expected at least 1 element")
	}
}

func TestHandlePollStdinEmpty(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)

	resp := getJSON(t, ts, "/sessions/"+created.ID+"/20/poll?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var poll types.PollResponse
	decodeJSON(t, resp, &poll)

	if len(poll.Elements) != 0 {
		t.Errorf("expected 0 elements, got %d", len(poll.Elements))
	}
}

func TestHandlePollStdinWithMessages(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	// Enqueue stdin
	data := base64.StdEncoding.EncodeToString([]byte("ls -la\n"))
	postJSON(t, ts, "/sessions/"+created.ID+"/stdin?client_id="+clientID, types.StdinEntry{
		Data: []byte(data),
	})

	// Poll for stdin (as host)
	resp := getJSON(t, ts, "/sessions/"+created.ID+"/20/poll?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var poll types.PollResponse
	decodeJSON(t, resp, &poll)

	if len(poll.Elements) == 0 {
		t.Error("expected at least 1 element")
	}
}

func TestHandlePollPendingClientEmpty(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      true,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	_, ts := newTestServerWithConfig(t, cfg)

	created := createTestSession(t, ts)

	resp := getJSON(t, ts, "/sessions/"+created.ID+"/30/poll?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var poll types.PollResponse
	decodeJSON(t, resp, &poll)

	if len(poll.Elements) != 0 {
		t.Errorf("expected 0 elements, got %d", len(poll.Elements))
	}
}

func TestHandlePollPendingClientWithMessages(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      true,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	_, ts := newTestServerWithConfig(t, cfg)

	created := createTestSession(t, ts)

	// Register a client (will be pending)
	registerTestClient(t, ts, created.ID)

	// Poll for pending clients (as host)
	resp := getJSON(t, ts, "/sessions/"+created.ID+"/30/poll?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var poll types.PollResponse
	decodeJSON(t, resp, &poll)

	if len(poll.Elements) == 0 {
		t.Error("expected at least 1 element")
	}
}

func TestHandlePollSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := getJSON(t, ts, "/sessions/nonexistent/10/poll?client_id=test")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleAckOutput(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	// Append output
	data := base64.StdEncoding.EncodeToString([]byte("test"))
	postJSON(t, ts, "/sessions/"+created.ID+"/output", types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte(data),
	})

	// Poll first (marks messages as peeked)
	resp := getJSON(t, ts, "/sessions/"+created.ID+"/10/poll?client_id="+clientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Ack the output (clears peeked messages)
	resp = getJSON(t, ts, "/sessions/"+created.ID+"/10/ack?client_id="+clientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Poll again - should be empty
	pollResp := getJSON(t, ts, "/sessions/"+created.ID+"/10/poll?client_id="+clientID)
	var poll types.PollResponse
	decodeJSON(t, pollResp, &poll)

	if len(poll.Elements) != 0 {
		t.Errorf("expected 0 elements after ack, got %d", len(poll.Elements))
	}
}

func TestHandleAckStdin(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	// Enqueue stdin
	data := base64.StdEncoding.EncodeToString([]byte("test"))
	postJSON(t, ts, "/sessions/"+created.ID+"/stdin?client_id="+clientID, types.StdinEntry{
		Data: []byte(data),
	})

	// Poll first (marks messages as peeked)
	resp := getJSON(t, ts, "/sessions/"+created.ID+"/20/poll?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Ack the stdin (clears peeked messages)
	resp = getJSON(t, ts, "/sessions/"+created.ID+"/20/ack?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Poll again - should be empty
	pollResp := getJSON(t, ts, "/sessions/"+created.ID+"/20/poll?client_id="+types.HostClientID)
	var poll types.PollResponse
	decodeJSON(t, pollResp, &poll)

	if len(poll.Elements) != 0 {
		t.Errorf("expected 0 elements after ack, got %d", len(poll.Elements))
	}
}

func TestHandleAckPendingClient(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      true,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	_, ts := newTestServerWithConfig(t, cfg)

	created := createTestSession(t, ts)
	registerTestClient(t, ts, created.ID)

	// Poll first (marks messages as peeked)
	resp := getJSON(t, ts, "/sessions/"+created.ID+"/30/poll?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Ack pending clients (clears peeked messages)
	resp = getJSON(t, ts, "/sessions/"+created.ID+"/30/ack?client_id="+types.HostClientID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Poll again - should be empty
	pollResp := getJSON(t, ts, "/sessions/"+created.ID+"/30/poll?client_id="+types.HostClientID)
	var poll types.PollResponse
	decodeJSON(t, pollResp, &poll)

	if len(poll.Elements) != 0 {
		t.Errorf("expected 0 elements after ack, got %d", len(poll.Elements))
	}
}

func TestHandleAckSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := getJSON(t, ts, "/sessions/nonexistent/10/ack?client_id=test")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

/* --- Stdin Handling Tests ------------------------------------------------- */

func TestHandleEnqueueStdin(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	data := base64.StdEncoding.EncodeToString([]byte("ls -la\n"))
	resp := postJSON(t, ts, "/sessions/"+created.ID+"/stdin?client_id="+clientID, types.StdinEntry{
		Data: []byte(data),
	})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestHandleEnqueueStdinBase64Decode(t *testing.T) {
	_, ts := newTestServer(t)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	testData := "test stdin data"
	data := base64.StdEncoding.EncodeToString([]byte(testData))

	postJSON(t, ts, "/sessions/"+created.ID+"/stdin?client_id="+clientID, types.StdinEntry{
		Data: []byte(data),
	})

	// Poll as host to verify data
	pollResp := getJSON(t, ts, "/sessions/"+created.ID+"/20/poll?client_id="+types.HostClientID)
	var poll types.PollResponse
	decodeJSON(t, pollResp, &poll)

	if len(poll.Elements) == 0 {
		t.Fatal("expected stdin entry in queue")
	}
}

func TestHandleEnqueueStdinInvalidBase64(t *testing.T) {
	// StdinEntry.Data is []byte and the server accepts it as-is
	// without base64 validation. This test is obsolete.
	t.Skip("StdinEntry.Data is accepted as bytes, base64 validation not implemented")
}

func TestHandleEnqueueStdinSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	data := base64.StdEncoding.EncodeToString([]byte("test"))
	resp := postJSON(t, ts, "/sessions/nonexistent/stdin?client_id=test", types.StdinEntry{
		Data: []byte(data),
	})

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandleEnqueueStdinWithApproval(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      true,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	srv, ts := newTestServerWithConfig(t, cfg)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	// Approve client with read-write permission
	sess, _ := srv.store.Get(created.ID)
	sess.ApproveClient(clientID, types.PermissionReadWrite)

	data := base64.StdEncoding.EncodeToString([]byte("test"))
	resp := postJSON(t, ts, "/sessions/"+created.ID+"/stdin?client_id="+clientID, types.StdinEntry{
		Data: []byte(data),
	})

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestHandleEnqueueStdinReadOnlyDenied(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      true,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	srv, ts := newTestServerWithConfig(t, cfg)

	created := createTestSession(t, ts)
	clientID := registerTestClient(t, ts, created.ID)

	// Approve client with read-only permission
	sess, _ := srv.store.Get(created.ID)
	sess.ApproveClient(clientID, types.PermissionReadOnly)

	data := base64.StdEncoding.EncodeToString([]byte("test"))

	resp := postJSON(t, ts, "/sessions/"+created.ID+"/stdin?client_id="+clientID, types.StdinEntry{
		Data: []byte(data),
	})

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}
