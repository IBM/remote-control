package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
)

func TestStdinConflictResolution(t *testing.T) {
	serverURL := testServer(t)

	createBody, _ := json.Marshal(map[string]any{})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var session struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()

	stdinBody, _ := json.Marshal(map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte("ls -la\n")),
	})
	stdinResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin?client_id=client-1", "application/json", bytes.NewReader(stdinBody))
	var stdinEntry struct {
		ID uint64 `json:"id"`
	}
	json.NewDecoder(stdinResp.Body).Decode(&stdinEntry)
	stdinResp.Body.Close()

	if stdinEntry.ID == 0 {
		t.Fatalf("expected non-zero id")
	}
}

func TestStdinAck(t *testing.T) {
	serverURL := testServer(t)

	createBody, _ := json.Marshal(map[string]any{})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var session struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()

	stdinBody, _ := json.Marshal(map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte("pwd\n")),
	})
	stdinResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin?client_id=client", "application/json", bytes.NewReader(stdinBody))
	var stdinEntry struct {
		ID uint64 `json:"id"`
	}
	json.NewDecoder(stdinResp.Body).Decode(&stdinEntry)
	stdinResp.Body.Close()

	if stdinEntry.ID == 0 {
		t.Fatalf("expected non-zero id")
	}

	ackBody, _ := json.Marshal(map[string]uint64{"id": stdinEntry.ID})
	acceptResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin/ack", "application/json", bytes.NewReader(ackBody))
	acceptResp.Body.Close()
	if acceptResp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", acceptResp.StatusCode)
	}
}
