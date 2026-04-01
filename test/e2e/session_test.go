package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestFullSessionLifecycle(t *testing.T) {
	serverURL := testServer(t)

	// 1. Create a session.
	createBody, _ := json.Marshal(map[string]any{})
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

	// 2. Register a client.
	clientResp, err := http.Post(serverURL+"/sessions/"+session.ID+"/clients", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	var clientRespData struct {
		ClientID string `json:"client_id"`
		Status   string `json:"status"`
	}
	json.NewDecoder(clientResp.Body).Decode(&clientRespData)
	clientResp.Body.Close()
	clientID := clientRespData.ClientID
	if clientID == "" {
		t.Fatal("expected non-empty client_id")
	}

	// 3. Append output chunks.
	now := time.Now()
	for i, stream := range []string{"stdout", "stderr"} {
		data := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s chunk %d", stream, i)))
		body, _ := json.Marshal(map[string]string{
			"stream":    stream,
			"data":      data,
			"timestamp": now.Add(time.Duration(i) * time.Millisecond).Format(time.RFC3339Nano),
		})
		_, err = http.Post(serverURL+"/sessions/"+session.ID+"/output", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("append output: %v", err)
		}
	}

	// 4. Poll output using message type 10 (WSMessageOutput).
	var poll struct {
		Chunks []struct {
			Stream string `json:"stream"`
			Data   string `json:"data"`
		} `json:"elements"`
	}

	mType := 10
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pollResp, err := http.Get(serverURL + "/sessions/" + session.ID + "/" + strconv.Itoa(mType) + "/poll?client_id=" + clientID)
		if err != nil {
			t.Fatalf("poll output: %v", err)
		}
		if pollResp.StatusCode != http.StatusOK {
			pollResp.Body.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		json.NewDecoder(pollResp.Body).Decode(&poll)
		pollResp.Body.Close()

		if len(poll.Chunks) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(poll.Chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %+v", len(poll.Chunks), poll.Chunks)
	}
	if poll.Chunks[0].Stream != "stdout" {
		t.Errorf("expected first chunk stdout, got %q", poll.Chunks[0].Stream)
	}

	// 5. Complete the session.
	patchBody, _ := json.Marshal(map[string]int{"exit_code": 0})
	req, _ := http.NewRequest(http.MethodPatch, serverURL+"/sessions/"+session.ID, bytes.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("patch session: %v", err)
	}
	var patchResult struct {
		Status string `json:"status"`
	}
	json.NewDecoder(patchResp.Body).Decode(&patchResult)
	patchResp.Body.Close()

	if patchResult.Status != "completed" {
		t.Errorf("expected completed status in PATCH response, got %q", patchResult.Status)
	}

	// 6. Verify session is deleted from memory after completion.
	getResp, err := http.Get(serverURL + "/sessions/" + session.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion (session deleted), got %d", getResp.StatusCode)
	}
}
