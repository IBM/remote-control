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
	"testing"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/api"
	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/host"
	"github.com/gabe-l-hart/remote-control/internal/session"
)

// testServer starts a real HTTP server on a free port and returns its URL.
// The server is shut down when the test ends.
func testServer(t *testing.T) string {
	t.Helper()

	store, _ := session.NewStore("memory", session.StoreOptions{})
	cfg := &config.Config{RequireApproval: false}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	srv := api.NewServer(addr, store, cfg)
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

// newTestHost creates a Host configured against the given server URL.
func newTestHost(t *testing.T, serverURL string) *host.Host {
	t.Helper()
	cfg := &config.Config{
		ServerURL:       serverURL,
		RequireApproval: false,
	}
	return host.NewHost(cfg)
}

// listSessions returns all sessions from the server.
func listSessions(t *testing.T, serverURL string) []map[string]any {
	t.Helper()
	resp, err := http.Get(serverURL + "/sessions")
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	defer resp.Body.Close()
	var result []map[string]any
	json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
	return result
}

// waitForSession polls until the session reaches wantStatus or the deadline passes.
func waitForSession(t *testing.T, serverURL, sessionID, wantStatus string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(serverURL + "/sessions/" + sessionID)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		var result map[string]any
		json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
		resp.Body.Close()
		if result["status"] == wantStatus {
			return result
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach status %q within %v", sessionID, wantStatus, timeout)
	return nil
}

// waitForAnySession polls until at least one session exists and returns its ID.
func waitForAnySession(t *testing.T, serverURL string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		sessions := listSessions(t, serverURL)
		if len(sessions) > 0 {
			return sessions[0]["id"].(string)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no session created within timeout")
	return ""
}

// getOutput collects all output chunks from a session and returns the combined text.
func getOutput(t *testing.T, serverURL, sessionID string) string {
	t.Helper()
	resp, err := http.Get(serverURL + "/sessions/" + sessionID + "/output?stdout_offset=0&stderr_offset=0")
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	defer resp.Body.Close()
	var result struct {
		Chunks []struct {
			Data string `json:"data"`
		} `json:"chunks"`
	}
	json.NewDecoder(resp.Body).Decode(&result) //nolint:errcheck
	var combined string
	for _, chunk := range result.Chunks {
		b, _ := base64.StdEncoding.DecodeString(chunk.Data)
		combined += string(b)
	}
	return combined
}

// TestHostOutputProxying verifies that subprocess stdout is proxied to the server.
func TestHostOutputProxying(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runErr := make(chan error, 1)
	go func() {
		runErr <- h.Run(ctx, []string{"sh", "-c", `printf 'hello\nworld\n'`})
	}()

	sessionID := waitForAnySession(t, serverURL, 5*time.Second)
	waitForSession(t, serverURL, sessionID, "completed", 5*time.Second)

	output := getOutput(t, serverURL, sessionID)
	if output != "hello\nworld\n" {
		t.Errorf("expected %q, got %q", "hello\nworld\n", output)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run() did not return after session completed")
	}
}

// TestHostStdinRouting verifies that stdin enqueued via the server API is
// forwarded to the subprocess and appears in the session output.
func TestHostStdinRouting(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// sh -c 'read x; echo "$x"' reads one line from stdin then exits.
	// Using sh avoids stdio buffering issues that cat would have with pipe output.
	runErr := make(chan error, 1)
	go func() {
		runErr <- h.Run(ctx, []string{"sh", "-c", `read x; echo "$x"`})
	}()

	sessionID := waitForAnySession(t, serverURL, 5*time.Second)

	// Give the subprocess a moment to reach its read() call.
	time.Sleep(100 * time.Millisecond)

	// Enqueue stdin via the server API.
	payload := base64.StdEncoding.EncodeToString([]byte("hello from client\n"))
	body, _ := json.Marshal(map[string]string{
		"source": "test-client",
		"data":   payload,
	})
	resp, err := http.Post(serverURL+"/sessions/"+sessionID+"/stdin", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("enqueue stdin: %v", err)
	}
	var stdinResp struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&stdinResp) //nolint:errcheck
	resp.Body.Close()

	// sh exits after reading one line, so the session should complete naturally.
	waitForSession(t, serverURL, sessionID, "completed", 5*time.Second)

	// echo "$x" outputs the line read from stdin.
	output := getOutput(t, serverURL, sessionID)
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
}

// TestHostCleanShutdown verifies that cancelling the context causes Run() to
// return within the WaitDelay grace period (≤5s).
func TestHostCleanShutdown(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithCancel(context.Background())

	runErr := make(chan error, 1)
	go func() {
		runErr <- h.Run(ctx, []string{"sleep", "60"})
	}()

	// Wait for the session to appear before cancelling.
	waitForAnySession(t, serverURL, 5*time.Second)

	// Let the subprocess run briefly, then cancel.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-runErr:
		// Context cancellation may surface as nil or a context error; both are fine.
		if err != nil && err != context.Canceled {
			t.Logf("Run() returned: %v (acceptable non-nil error on shutdown)", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Run() did not return within 5s after context cancellation")
	}
}

// TestHostSessionCompleted verifies that the session is marked completed on the
// server with the correct exit code after the subprocess exits.
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
	result := waitForSession(t, serverURL, sessionID, "completed", 5*time.Second)

	exitCode, ok := result["exit_code"]
	if !ok {
		t.Fatal("expected exit_code in session response")
	}
	// JSON numbers decode as float64.
	if code, ok := exitCode.(float64); !ok || int(code) != 42 {
		t.Errorf("expected exit code 42, got %v", exitCode)
	}

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run() did not return after session completed")
	}
}
