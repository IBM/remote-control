// Package integration contains in-process integration tests for the host package.
// Since test stdin/stdout are pipes (not TTYs), host.Run automatically uses
// pipe mode — no PTY setup is needed and all existing tests continue to work.
package integration

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/host"
	"github.com/gabe-l-hart/remote-control/internal/server"
	testmain "github.com/gabe-l-hart/remote-control/test"
)

func TestMain(m *testing.M) {
	testmain.TestMain(m)
}

func testServer(t *testing.T) string {
	t.Helper()
	cfg := &config.Config{RequireApproval: false, MaxOutputBuffer: 1024}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srv := server.NewServer(addr, cfg)
	go func() {
		hs := &http.Server{Handler: srv.Handler()}
		hs.Serve(ln) //nolint:errcheck
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx) //nolint:errcheck
	})
	return fmt.Sprintf("http://%s", addr)
}

func newTestHost(t *testing.T, serverURL string) *host.Host {
	t.Helper()
	return host.NewHost(&config.Config{
		ServerURL:       serverURL,
		RequireApproval: false,
		EnableWebSocket: false,
		PollIntervalMs:  100,
	})
}

func listSessions(t *testing.T, serverURL string) []map[string]any {
	t.Helper()
	resp, err := http.Get(serverURL + "/sessions")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	defer resp.Body.Close()
	var result []map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

func waitForAnySession(t *testing.T, serverURL string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if sessions := listSessions(t, serverURL); len(sessions) > 0 {
			return sessions[0]["id"].(string)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no session created within timeout")
	return ""
}

func getOutput(t *testing.T, serverURL, sessionID, clientID string) string {
	t.Helper()
	resp, err := http.Get(serverURL + "/sessions/" + sessionID + "/10/poll?client_id=" + clientID)
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ""
	}
	var result struct {
		Chunks []struct {
			Data string `json:"data"`
		} `json:"elements"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	var combined string
	for _, chunk := range result.Chunks {
		b, _ := base64.StdEncoding.DecodeString(chunk.Data)
		combined += string(b)
	}
	return combined
}

func TestHostOutputProxying(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	workdir, err := os.MkdirTemp("", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(workdir)

	runErr := make(chan error, 1)
	sentinelPath := filepath.Join(workdir, "done")
	outputText := "hello"

	go func() {
		cmd := fmt.Sprintf(`printf '%s'; while ! [ -f %s ]; do sleep 0.05; done`, outputText, sentinelPath)
		runErr <- h.Run(ctx, []string{"sh", "-c", cmd})
	}()

	defer func() {
		os.Create(sentinelPath)
		time.Sleep(50 * time.Millisecond)
	}()

	sessionID := waitForAnySession(t, serverURL, 5*time.Second)

	clientResp, err := http.Post(serverURL+"/sessions/"+sessionID+"/clients", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	var clientData struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(clientResp.Body).Decode(&clientData)
	clientResp.Body.Close()

	// Give a moment for output to be sent
	time.Sleep(200 * time.Millisecond)

	output := getOutput(t, serverURL, sessionID, clientData.ClientID)

	// Accept partial output (the chunk may have been sent but not yet acknowledged)
	if output != outputText && output[0:len(outputText)] != outputText {
		for i := 0; i < 25; i++ {
			output = getOutput(t, serverURL, sessionID, clientData.ClientID)
			if output == outputText {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	if output != outputText {
		t.Errorf("expected %q, got %q", outputText, output)
	}

	os.Create(sentinelPath)

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run() did not return after session completed")
	}

	resp, _ := http.Get(serverURL + "/sessions/" + sessionID)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion, got %d", resp.StatusCode)
	}
}

func TestHostStdinRouting(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- h.Run(ctx, []string{"sh", "-c", `read x; echo "$x"; sleep 0.5`})
	}()

	sessionID := waitForAnySession(t, serverURL, 5*time.Second)

	clientResp, _ := http.Post(serverURL+"/sessions/"+sessionID+"/clients", "application/json", bytes.NewReader([]byte("{}")))
	var clientData struct {
		ClientID string `json:"client_id"`
	}
	json.NewDecoder(clientResp.Body).Decode(&clientData)
	clientResp.Body.Close()

	time.Sleep(100 * time.Millisecond)

	stdinPayload := base64.StdEncoding.EncodeToString([]byte("hello from client\n"))
	stdinBody, _ := json.Marshal(map[string]string{"data": stdinPayload})
	stdinResp, _ := http.Post(serverURL+"/sessions/"+sessionID+"/stdin?client_id=test-client", "application/json", bytes.NewReader(stdinBody))
	stdinResp.Body.Close()

	var output string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		output = getOutput(t, serverURL, sessionID, clientData.ClientID)
		if output == "hello from client\n" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if output != "hello from client\n" {
		t.Errorf("expected %q, got %q", "hello from client\n", output)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run() did not return after session completed")
	}

	resp, _ := http.Get(serverURL + "/sessions/" + sessionID)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion, got %d", resp.StatusCode)
	}
}

func TestHostCleanShutdown(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() {
		runErr <- h.Run(ctx, []string{"sleep", "60"})
	}()

	waitForAnySession(t, serverURL, 5*time.Second)
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		if err != nil && err != context.Canceled {
			t.Logf("Run() returned: %v (acceptable on shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Run() did not return within 5s after context cancellation")
	}
}

func TestHostSessionCompleted(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- h.Run(ctx, []string{"sh", "-c", "exit 42"})
	}()

	sessionID := waitForAnySession(t, serverURL, 5*time.Second)

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run() did not return after session completed")
	}

	resp, _ := http.Get(serverURL + "/sessions/" + sessionID)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion, got %d", resp.StatusCode)
	}
}
