package e2e

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/server"
)

// approvalServer starts a server with RequireApproval=true for approval tests.
func approvalServer(t *testing.T) string {
	t.Helper()
	cfg := &config.Config{RequireApproval: true, DefaultPermission: "read-write", MaxOutputBuffer: 1024}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	srv := server.NewServer(addr, cfg)
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
	createBody, _ := json.Marshal(map[string]any{})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var sess struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	// Register a client - should be pending.
	regBody, _ := json.Marshal(map[string]any{})
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

	// Approve the client using the ID that the server assigned.
	approveBody, _ := json.Marshal(map[string]string{"permission": "read-write"})
	approveResp, _ := http.Post(serverURL+"/sessions/"+sess.ID+"/clients/"+regResult.ClientID+"/approve",
		"application/json", bytes.NewReader(approveBody))
	approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", approveResp.StatusCode)
	}
}

func TestClientDenyFlow(t *testing.T) {
	serverURL := approvalServer(t)

	createBody, _ := json.Marshal(map[string]any{})
	resp, _ := http.Post(serverURL+"/sessions", "application/json", bytes.NewReader(createBody))
	var sess struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&sess)
	resp.Body.Close()

	regBody, _ := json.Marshal(map[string]any{})
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
