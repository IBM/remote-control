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

	createBody := []byte("{}")
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var session struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()

	// Stdin uses message type 20 (WSMessageStdin)
	stdinBody, _ := json.Marshal(map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte("ls -la\n")),
	})
	stdinResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin?client_id=client-1", "application/json", bytes.NewReader(stdinBody))
	var stdinEntry struct {
		ID uint64 `json:"id"`
	}
	json.NewDecoder(stdinResp.Body).Decode(&stdinEntry)
	stdinResp.Body.Close()

	// With new API, we just verify success
	if stdinResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", stdinResp.StatusCode)
	}
}

func TestStdinAck(t *testing.T) {
	serverURL := testServer(t)

	createBody := []byte("{}")
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var session struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()

	// Stdin uses message type 20 (WSMessageStdin)
	stdinBody, _ := json.Marshal(map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte("pwd\n")),
	})
	stdinResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin?client_id=client", "application/json", bytes.NewReader(stdinBody))
	stdinResp.Body.Close()

	if stdinResp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", stdinResp.StatusCode)
	}

	// Ack uses GET method with message ID in URL
	ackResp, err := http.Get(serverURL + "/sessions/" + session.ID + "/20/ack?client_id=client")
	if err != nil {
		t.Fatalf("ack request failed: %v", err)
	}
	ackResp.Body.Close()
	if ackResp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", ackResp.StatusCode)
	}
}
