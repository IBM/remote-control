package e2e

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/gabe-l-hart/remote-control/internal/api"
	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/session"
)

// approvalServer starts a server with RequireApproval=true for approval tests.
func approvalServer(t *testing.T) string {
	t.Helper()
	store, _ := session.NewStore("memory", session.StoreOptions{})
	cfg := &config.Config{RequireApproval: true, DefaultPermission: "read-write"}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	srv := api.NewServer(addr, store, cfg)
	go func() {
		hs := &http.Server{Handler: srv.Handler()}
		hs.Serve(ln) //nolint:errcheck
	}()
	t.Cleanup(func() { ln.Close() })
	return "http://" + addr
}

func TestClientApprovalFlow(t *testing.T) {
	serverURL := approvalServer(t)

	// Create a session.
	createBody, _ := json.Marshal(map[string]any{"command": []string{"bash"}})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var sess struct{ ID string `json:"id"` }
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	// Register a client — should be pending.
	regBody, _ := json.Marshal(map[string]string{"client_id": "test-client"})
	regResp, _ := http.Post(serverURL+"/sessions/"+sess.ID+"/clients", "application/json", bytes.NewReader(regBody))
	var regResult struct {
		ClientID string `json:"client_id"`
		Status   string `json:"status"`
	}
	json.NewDecoder(regResp.Body).Decode(&regResult)
	regResp.Body.Close()
	if regResult.Status != "pending" {
		t.Fatalf("expected pending, got %q", regResult.Status)
	}

	// List pending clients.
	listResp, _ := http.Get(serverURL + "/sessions/" + sess.ID + "/clients?status=pending")
	var clients []struct {
		ClientID string `json:"client_id"`
		Approval string `json:"approval"`
	}
	json.NewDecoder(listResp.Body).Decode(&clients)
	listResp.Body.Close()
	if len(clients) != 1 {
		t.Fatalf("expected 1 pending client, got %d", len(clients))
	}

	// Approve the client using the ID that the server assigned.
	approveBody, _ := json.Marshal(map[string]string{"permission": "read-write"})
	approveResp, _ := http.Post(serverURL+"/sessions/"+sess.ID+"/clients/"+clients[0].ClientID+"/approve",
		"application/json", bytes.NewReader(approveBody))
	approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", approveResp.StatusCode)
	}

	// Pending list should now be empty.
	listResp2, _ := http.Get(serverURL + "/sessions/" + sess.ID + "/clients?status=pending")
	var clients2 []struct{ ClientID string `json:"client_id"` }
	json.NewDecoder(listResp2.Body).Decode(&clients2)
	listResp2.Body.Close()
	if len(clients2) != 0 {
		t.Errorf("expected 0 pending clients after approval, got %d: %+v", len(clients2), clients2)
	}
}

func TestClientDenyFlow(t *testing.T) {
	serverURL := approvalServer(t)

	createBody, _ := json.Marshal(map[string]any{"command": []string{"bash"}})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var sess struct{ ID string `json:"id"` }
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	regBody, _ := json.Marshal(map[string]string{"client_id": "deny-client"})
	regResp, _ := http.Post(serverURL+"/sessions/"+sess.ID+"/clients", "application/json", bytes.NewReader(regBody))
	var regResult struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(regResp.Body).Decode(&regResult)
	regResp.Body.Close()

	denyResp, _ := http.Post(serverURL+"/sessions/"+sess.ID+"/clients/"+regResult.ClientID+"/deny", "application/json", nil)
	denyResp.Body.Close()
	if denyResp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", denyResp.StatusCode)
	}
}
