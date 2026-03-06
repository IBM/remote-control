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

	// Create a session.
	createBody, _ := json.Marshal(map[string]any{"command": []string{"bash"}})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var session struct{ ID string `json:"id"` }
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()

	// Client submits stdin (pending).
	stdinBody, _ := json.Marshal(map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte("ls -la\n")),
	})
	stdinResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin?client_id=client-1", "application/json", bytes.NewReader(stdinBody))
	var stdinEntry struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	json.NewDecoder(stdinResp.Body).Decode(&stdinEntry)
	stdinResp.Body.Close()

	if stdinEntry.Status != "pending" {
		t.Fatalf("expected pending, got %q", stdinEntry.Status)
	}

	// Host rejects all pending.
	rejectAllResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin/reject-all", "application/json", nil)
	var rejected struct {
		RejectedIDs []string `json:"rejected_ids"`
	}
	json.NewDecoder(rejectAllResp.Body).Decode(&rejected)
	rejectAllResp.Body.Close()

	if len(rejected.RejectedIDs) != 1 || rejected.RejectedIDs[0] != stdinEntry.ID {
		t.Errorf("expected [%s] rejected, got %v", stdinEntry.ID, rejected.RejectedIDs)
	}

	// Verify status is rejected.
	statusResp, _ := http.Get(serverURL + "/sessions/" + session.ID + "/stdin/" + stdinEntry.ID + "/status")
	var statusResult struct {
		Status string `json:"status"`
	}
	json.NewDecoder(statusResp.Body).Decode(&statusResult)
	statusResp.Body.Close()

	if statusResult.Status != "rejected" {
		t.Errorf("expected rejected, got %q", statusResult.Status)
	}
}

func TestStdinAccept(t *testing.T) {
	serverURL := testServer(t)

	createBody, _ := json.Marshal(map[string]any{"command": []string{"bash"}})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var session struct{ ID string `json:"id"` }
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()

	// Enqueue.
	stdinBody, _ := json.Marshal(map[string]string{
		"data": base64.StdEncoding.EncodeToString([]byte("pwd\n")),
	})
	stdinResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin?client_id=client", "application/json", bytes.NewReader(stdinBody))
	var stdinEntry struct{ ID string `json:"id"` }
	json.NewDecoder(stdinResp.Body).Decode(&stdinEntry)
	stdinResp.Body.Close()

	// Verify status before accepting
	statusResp, _ := http.Get(serverURL + "/sessions/" + session.ID + "/stdin/" + stdinEntry.ID + "/status")
	var beforeResult struct{ Status string `json:"status"` }
	json.NewDecoder(statusResp.Body).Decode(&beforeResult)
	statusResp.Body.Close()
	if beforeResult.Status != "pending" {
		t.Errorf("expected pending before accept, got %q", beforeResult.Status)
	}

	// Accept.
	acceptResp, _ := http.Post(serverURL+"/sessions/"+session.ID+"/stdin/"+stdinEntry.ID+"/accept", "application/json", nil)
	acceptResp.Body.Close()
	if acceptResp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", acceptResp.StatusCode)
	}

	// After accepting, the stdin entry is purged from memory, so we can't verify the status
	// The accept operation itself succeeding (204) is sufficient verification
}
