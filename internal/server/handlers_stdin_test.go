package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func makeSession(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	return created.ID
}

func enqueueStdin(t *testing.T, ts *httptest.Server, sid, data string) StdinResponse {
	t.Helper()
	encoded := base64.StdEncoding.EncodeToString([]byte(data))
	resp := postJSON(t, ts, "/sessions/"+sid+"/stdin?client_id=test", StdinRequest{Data: encoded})
	var entry StdinResponse
	decodeJSON(t, resp, &entry)
	return entry
}

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

func TestStdinAck(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)
	entry := enqueueStdin(t, ts, sid, "ls -la\n")

	resp := postJSON(t, ts, "/sessions/"+sid+"/stdin/ack", AckStdinRequest{ID: entry.ID})
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestStdinAckNotFound(t *testing.T) {
	_, ts := newTestServer(t)
	sid := makeSession(t, ts)

	resp := postJSON(t, ts, "/sessions/"+sid+"/stdin/ack", AckStdinRequest{ID: 999})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	resp.Body.Close()
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
