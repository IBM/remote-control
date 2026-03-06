package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestFullSessionLifecycle(t *testing.T) {
	serverURL := testServer(t)

	// 1. Create a session.
	createBody, _ := json.Marshal(map[string]any{"command": []string{"bash"}})
	resp, err := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	var session struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()

	if session.ID == "" {
		t.Fatal("expected session ID")
	}
	if session.Status != "active" {
		t.Errorf("expected active, got %q", session.Status)
	}

	// 2. Append output chunks.
	now := time.Now()
	for i, stream := range []string{"stdout", "stderr"} {
		data := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s chunk %d", stream, i)))
		body, _ := json.Marshal(map[string]string{
			"stream":    stream,
			"data":      data,
			"timestamp": now.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
		})
		resp, _ = http.Post(serverURL+"/sessions/"+session.ID+"/output", "application/json", bytes.NewReader(body))
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("append output failed with status %d", resp.StatusCode)
		}
		resp.Body.Close()
	}

	// 3. Register a client.
	clientResp, err := http.Post(serverURL+"/sessions/"+session.ID+"/clients", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	var clientRespData struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(clientResp.Body).Decode(&clientRespData)
	clientResp.Body.Close()
	clientID := clientRespData.ClientID
	if clientID == "" {
		t.Fatal("expected non-empty client_id")
	}

	// 4. Poll output with retry to handle async processing (with client_id).
	var poll struct {
		Chunks []struct {
			Stream string `json:"stream"`
			Data   string `json:"data"`
		} `json:"chunks"`
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pollResp, err := http.Get(serverURL + "/sessions/" + session.ID + "/output?stdout_offset=0&stderr_offset=0")
		if err != nil {
			t.Fatalf("poll output: %v", err)
		}
		json.NewDecoder(pollResp.Body).Decode(&poll)
		pollResp.Body.Close()

		if len(poll.Chunks) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(poll.Chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(poll.Chunks))
	}
	if poll.Chunks[0].Stream != "stdout" {
		t.Errorf("expected first chunk stdout, got %q", poll.Chunks[0].Stream)
	}

	// 4. Complete the session.
	patchBody, _ := json.Marshal(map[string]int{"exit_code": 0})
	req, _ := http.NewRequest(http.MethodPatch, serverURL+"/sessions/"+session.ID, bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	patchResp, _ := http.DefaultClient.Do(req)
	var patchResult struct {
		Status string `json:"status"`
	}
	json.NewDecoder(patchResp.Body).Decode(&patchResult)
	patchResp.Body.Close()

	if patchResult.Status != "completed" {
		t.Errorf("expected completed status in PATCH response, got %q", patchResult.Status)
	}

	// 5. Verify session is deleted from memory after completion.
	getResp, _ := http.Get(serverURL + "/sessions/" + session.ID)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion (session deleted), got %d", getResp.StatusCode)
	}
}
