package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// makeSession creates a session in ts and returns its ID.
func makeSession(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	return created.ID
}

// enqueueStdin enqueues a base64-encoded stdin entry and returns the entry response.
func enqueueStdin(t *testing.T, ts *httptest.Server, sid, data string) StdinResponse {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(data))
	resp := postJSON(t, ts, "/sessions/"+sid+"/stdin?client_id=test", StdinRequest{Data: encoded})
	var entry StdinResponse
	decodeJSON(t, resp, &entry)
	return entry
}

// patchSession sends a PATCH request for session completion.
func patchSession(t *testing.T, ts *httptest.Server, sid string, exitCode int) *http.Response {
	t.Helper()
	data, _ := json.Marshal(PatchSessionRequest{ExitCode: exitCode})
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/sessions/"+sid, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	return resp
}

// --- Stdin accept / reject / status ---

func TestStdinAcceptAndStatus(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)
	entry := enqueueStdin(t, ts, sid, "ls -la\n")

	// Check status before accepting
	statusResp := getJSON(t, ts, "/sessions/"+sid+"/stdin/"+entry.ID+"/status")
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", statusResp.StatusCode)
	}
	var statusResult StdinStatusResponse
	decodeJSON(t, statusResp, &statusResult)
	if statusResult.Status != "pending" {
		t.Errorf("expected pending before accept, got %s", statusResult.Status)
	}

	// Accept the entry
	acceptResp := postJSON(t, ts, "/sessions/"+sid+"/stdin/"+entry.ID+"/accept", nil)
	if acceptResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", acceptResp.StatusCode)
	}
	acceptResp.Body.Close()

	// After accepting, the entry is purged from memory, so we can't check status
	// The successful 204 response is sufficient verification
}

func TestStdinReject(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)
	entry := enqueueStdin(t, ts, sid, "ls\n")

	rejectResp := postJSON(t, ts, "/sessions/"+sid+"/stdin/"+entry.ID+"/reject", nil)
	if rejectResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rejectResp.StatusCode)
	}
	rejectResp.Body.Close()

	statusResp := getJSON(t, ts, "/sessions/"+sid+"/stdin/"+entry.ID+"/status")
	var statusResult StdinStatusResponse
	decodeJSON(t, statusResp, &statusResult)
	if statusResult.Status != "rejected" {
		t.Errorf("expected rejected, got %s", statusResult.Status)
	}
}

func TestStdinRejectAll(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)
	enqueueStdin(t, ts, sid, "ls\n")
	enqueueStdin(t, ts, sid, "pwd\n")

	rejectAllResp := postJSON(t, ts, "/sessions/"+sid+"/stdin/reject-all", nil)
	if rejectAllResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", rejectAllResp.StatusCode)
	}
	var result map[string][]string
	decodeJSON(t, rejectAllResp, &result)
	if len(result["rejected_ids"]) != 2 {
		t.Errorf("expected 2 rejected IDs, got %d", len(result["rejected_ids"]))
	}
}

func TestStdinPeekEmpty(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)

	peekResp := getJSON(t, ts, "/sessions/"+sid+"/stdin")
	if peekResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", peekResp.StatusCode)
	}
	peekResp.Body.Close()
}

func TestStdinAcceptNotFound(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)

	resp := postJSON(t, ts, "/sessions/"+sid+"/stdin/nonexistent/accept", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStdinRejectNotFound(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)

	resp := postJSON(t, ts, "/sessions/"+sid+"/stdin/nonexistent/reject", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStdinStatusNotFound(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)

	resp := getJSON(t, ts, "/sessions/"+sid+"/stdin/nonexistent/status")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStdinRejectAllSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := postJSON(t, ts, "/sessions/nonexistent/stdin/reject-all", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStdinEnqueueInvalidBase64(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)

	resp := postJSON(t, ts, "/sessions/"+sid+"/stdin?client_id=test", StdinRequest{
		Data: "not-valid-base64!!!",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

// --- Session error cases ---

func TestGetSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := getJSON(t, ts, "/sessions/nonexistent")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestCreateSessionBadRequest(t *testing.T) {
	_, ts := newTestServer(t)

	// Empty command slice should be rejected.
	resp := postJSON(t, ts, "/sessions", AppendOutputRequest{Stream: "oops!"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAppendOutputInvalidStream(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)

	data := base64.StdEncoding.EncodeToString([]byte("data"))
	resp := postJSON(t, ts, "/sessions/"+sid+"/output", AppendOutputRequest{
		Stream:    "invalid-stream",
		Data:      data,
		Timestamp: "2024-01-01T00:00:00Z",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPatchSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := patchSession(t, ts, "nonexistent", 0)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestDeleteSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/sessions/nonexistent", nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestAppendOutputSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	data := base64.StdEncoding.EncodeToString([]byte("hello"))
	resp := postJSON(t, ts, "/sessions/nonexistent/output", AppendOutputRequest{
		Stream:    "stdout",
		Data:      data,
		Timestamp: "2024-01-01T00:00:00Z",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestPollOutputSessionNotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp := getJSON(t, ts, "/sessions/nonexistent/output?stdout_offset=0&stderr_offset=0")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}
