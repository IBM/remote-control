package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/session"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store, _ := session.NewStore("memory", session.StoreOptions{})
	cfg := &config.Config{RequireApproval: false}
	srv := NewServer(":0", store, cfg)
	ts := httptest.NewServer(srv.mux)
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

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

func TestCreateAndGetSession(t *testing.T) {
	_, ts := newTestServer(t)

	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
	var created SessionResponse
	decodeJSON(t, resp, &created)
	if created.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if created.Status != "active" {
		t.Errorf("expected active status, got %q", created.Status)
	}

	// GET
	resp2 := getJSON(t, ts, "/sessions/"+created.ID)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
	var got SessionResponse
	decodeJSON(t, resp2, &got)
	if got.ID != created.ID {
		t.Errorf("IDs don't match: %s vs %s", got.ID, created.ID)
	}
}

func TestListSessions(t *testing.T) {
	_, ts := newTestServer(t)
	postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"sh"}})

	resp := getJSON(t, ts, "/sessions")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var sessions []SessionResponse
	decodeJSON(t, resp, &sessions)
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestDeleteSession(t *testing.T) {
	_, ts := newTestServer(t)
	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/sessions/"+created.ID, nil)
	delResp, _ := ts.Client().Do(req)
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", delResp.StatusCode)
	}

	// Getting it now should 404.
	getResp := getJSON(t, ts, "/sessions/"+created.ID)
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", getResp.StatusCode)
	}
}

func TestPatchSession(t *testing.T) {
	_, ts := newTestServer(t)
	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)

	patchResp := postJSON(t, ts, "/sessions/"+created.ID, nil)
	_ = patchResp

	// Use PATCH properly.
	data, _ := json.Marshal(PatchSessionRequest{ExitCode: 0})
	req, _ := http.NewRequest(http.MethodPatch, ts.URL+"/sessions/"+created.ID, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	patchResp2, _ := ts.Client().Do(req)
	if patchResp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", patchResp2.StatusCode)
	}
	var patched SessionResponse
	decodeJSON(t, patchResp2, &patched)
	if patched.Status != "completed" {
		t.Errorf("expected completed, got %q", patched.Status)
	}
}

func TestAppendAndPollOutput(t *testing.T) {
	_, ts := newTestServer(t)
	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	now := time.Now()

	// Register a client.
	clientResp := postJSON(t, ts, "/sessions/"+sid+"/clients", nil)
	var clientRespData struct {
		ClientID string `json:"client_id"`
	}
	decodeJSON(t, clientResp, &clientRespData)
	clientID := clientRespData.ClientID
	if clientID == "" {
		t.Fatalf("expected non-empty client_id")
	}

	// Append stdout.
	stdoutData := base64.StdEncoding.EncodeToString([]byte("hello stdout"))
	resp1 := postJSON(t, ts, "/sessions/"+sid+"/output", AppendOutputRequest{
		Stream:    "stdout",
		Data:      stdoutData,
		Timestamp: now.Format(time.RFC3339Nano),
	})
	if resp1.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp1.StatusCode)
	}

	// Append stderr.
	stderrData := base64.StdEncoding.EncodeToString([]byte("hello stderr"))
	resp2 := postJSON(t, ts, "/sessions/"+sid+"/output", AppendOutputRequest{
		Stream:    "stderr",
		Data:      stderrData,
		Timestamp: now.Add(time.Millisecond).Format(time.RFC3339Nano),
	})
	if resp2.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp2.StatusCode)
	}

	// Poll output with retry to handle async processing (with client_id).
	var poll PollOutputResponse
	for i := 0; i < 20; i++ {
		pollResp := getJSON(t, ts, "/sessions/"+sid+"/output?client_id="+clientID+"&stdout_offset=0&stderr_offset=0")
		if pollResp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", pollResp.StatusCode)
		}
		decodeJSON(t, pollResp, &poll)
		if len(poll.Chunks) >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if len(poll.Chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d after retries", len(poll.Chunks))
	}

	// First chunk should be stdout (earlier timestamp).
	if poll.Chunks[0].Stream != "stdout" {
		t.Errorf("expected first chunk to be stdout, got %q", poll.Chunks[0].Stream)
	}
	if poll.Chunks[1].Stream != "stderr" {
		t.Errorf("expected second chunk to be stderr, got %q", poll.Chunks[1].Stream)
	}

	// Verify next offsets.
	if poll.NextOffsets["stdout"] != 12 {
		t.Errorf("expected stdout next offset 12, got %d", poll.NextOffsets["stdout"])
	}
	if poll.NextOffsets["stderr"] != 12 {
		t.Errorf("expected stderr next offset 12, got %d", poll.NextOffsets["stderr"])
	}

	// Poll again with updated offsets and client_id — should get no chunks.
	pollResp2 := getJSON(t, ts, "/sessions/"+sid+"/output?client_id="+clientID+"&stdout_offset=12&stderr_offset=12")
	var poll2 PollOutputResponse
	decodeJSON(t, pollResp2, &poll2)
	if len(poll2.Chunks) != 0 {
		t.Errorf("expected no chunks on second poll, got %d", len(poll2.Chunks))
	}
}

func TestStdinEnqueueAndPeek(t *testing.T) {
	_, ts := newTestServer(t)
	resp := postJSON(t, ts, "/sessions", CreateSessionRequest{Command: []string{"bash"}})
	var created SessionResponse
	decodeJSON(t, resp, &created)
	sid := created.ID

	// Enqueue stdin.
	stdinData := base64.StdEncoding.EncodeToString([]byte("ls -la\n"))
	enqResp := postJSON(t, ts, "/sessions/"+sid+"/stdin?client_id=test-client", StdinRequest{
		Data: stdinData,
	})
	if enqResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", enqResp.StatusCode)
	}
	var entry StdinResponse
	decodeJSON(t, enqResp, &entry)
	if entry.ID == "" {
		t.Error("expected non-empty entry ID")
	}
	if entry.Status != "pending" {
		t.Errorf("expected pending status, got %q", entry.Status)
	}

	// Peek.
	peekResp := getJSON(t, ts, "/sessions/"+sid+"/stdin")
	if peekResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", peekResp.StatusCode)
	}
	var peeked StdinResponse
	decodeJSON(t, peekResp, &peeked)
	if peeked.ID != entry.ID {
		t.Errorf("peek returned wrong ID: %s vs %s", peeked.ID, entry.ID)
	}
}
