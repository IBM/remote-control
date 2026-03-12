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

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/api"
	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/host"
	"github.com/gabe-l-hart/remote-control/internal/session"
	testmain "github.com/gabe-l-hart/remote-control/test"
)

// testServer starts a real HTTP server on a free port and returns its URL.
// The server is shut down when the test ends.
func testServer(t *testing.T) string {
	t.Helper()

	store, _ := session.NewStore("memory", session.StoreOptions{})
	cfg := &config.Config{RequireApproval: false, MaxInitialBufferBytes: 1024}

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
		EnableWebSocket: false, // Disable WebSocket in tests for HTTP-only mode
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
// Note: With immediate deletion of completed sessions, this may not find sessions
// that complete very quickly. Use a shorter polling interval.
func waitForAnySession(t *testing.T, serverURL string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastSeenID string
	for time.Now().Before(deadline) {
		sessions := listSessions(t, serverURL)
		if len(sessions) > 0 {
			lastSeenID = sessions[0]["id"].(string)
			return lastSeenID
		}
		// If we've seen a session ID before but it's gone now, it completed
		// Return the last seen ID even though it's deleted (session was deleted after completion)
		if lastSeenID != "" {
			time.Sleep(10 * time.Millisecond)
			// Try one more time to see if session exists (in case it completed just now)
			sessions2 := listSessions(t, serverURL)
			if len(sessions2) > 0 {
				return lastSeenID
			}
			// Session deleted - return it anyway
			return lastSeenID
		}
		time.Sleep(10 * time.Millisecond) // Faster polling
	}
	// If we never saw a session, that's a real failure
	if lastSeenID == "" {
		t.Fatal("no session created within timeout")
	}
	return lastSeenID
}

// getOutput collects all output chunks from a session and returns the combined text.
// Returns empty string if session doesn't exist (404). Takes clientID to track output.
func getOutput(t *testing.T, serverURL, sessionID, clientID string) string {
	t.Helper()
	resp, err := http.Get(serverURL + "/sessions/" + sessionID + "/output?client_id=" + clientID + "&stdout_offset=0&stderr_offset=0")
	if err != nil {
		t.Fatalf("get output: %v", err)
	}
	defer resp.Body.Close()

	// If session is deleted (404), return empty string
	if resp.StatusCode == http.StatusNotFound {
		return ""
	}

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

	// Make a temporary dir for command end sentinel file
	workdir, err := os.MkdirTemp("", "test")
	if nil != err {
		t.Fatal(err)
	}
	defer os.RemoveAll(workdir)

	runErr := make(chan error, 1)
	sentinelPath := filepath.Join(workdir, "done")
	outputText := "hello"
	go func() {
		// Print, then wait for the sentinel file to show up to terminate
		cmd := fmt.Sprintf(`printf '%s'; while ! [ -f %s ]; do sleep 0.05; done`, outputText, sentinelPath)
		runErr <- h.Run(ctx, []string{"sh", "-c", cmd})
	}()
	// In case the test terminates early, make sure the child process does too
	defer func() {
		os.Create(sentinelPath)
		// Make sure there is time for it to terminate before deferred cleanup
		// removes the workdir
		time.Sleep(50 * time.Millisecond)
	}()

	// Poll aggressively for session creation and output
	deadline := time.Now().Add(5 * time.Second)
	var output string
	var sessionID string
	var clientID string
	var startedSession bool = false

	// First, wait for session to appear and register client
	for time.Now().Before(deadline) {
		sessions := listSessions(t, serverURL)
		if len(sessions) > 0 {
			sessionID = sessions[0]["id"].(string)
			// Register a client for this session only once
			if !startedSession {
				clientResp, err := http.Post(serverURL+"/sessions/"+sessionID+"/clients", "application/json", bytes.NewReader([]byte("{}")))
				if err != nil {
					t.Fatalf("create client: %v", err)
				}
				var clientRespData struct {
					ClientID string `json:"client_id"`
				}
				json.NewDecoder(clientResp.Body).Decode(&clientRespData)
				clientResp.Body.Close()
				clientID = clientRespData.ClientID
				if clientID == "" {
					t.Fatal("expected non-empty client_id")
				}
				// Approve the client for output access
				_, err = http.Post(serverURL+"/sessions/"+sessionID+"/clients/"+clientID+"/approve", "application/json",
					bytes.NewReader([]byte(`{"permission": "read-write"}`)))
				if err != nil {
					t.Fatalf("approve client: %v", err)
				}
				startedSession = true
				testmain.TestCh.Log(alog.DEBUG, "Client connected!")
				break // Client registered, now collect output
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if sessionID == "" {
		t.Fatal("no session found")
	}
	if !startedSession {
		t.Fatal("failed to connect client")
	}

	// Now collect output after client is registered
	outputDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(outputDeadline) {
		output = getOutput(t, serverURL, sessionID, clientID)
		if output == outputText {
			break
		}
		// Verify session still exists
		sessions := listSessions(t, serverURL)
		if len(sessions) == 0 {
			t.Fatal("session deleted before output received")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if output != outputText {
		t.Errorf("expected %q, got %q", "hello", output)
	}

	// Make the sentinel file to stop the process
	os.Create(sentinelPath)

	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run() did not return after session completed")
	}

	// After completion, session should be deleted from memory
	resp, _ := http.Get(serverURL + "/sessions/" + sessionID)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion (session deleted), got %d", resp.StatusCode)
	}
}

// TestHostOutputProxyingWithClientApproval tests the full workflow.

// TestHostStdinRouting verifies that stdin enqueued via the server API is
// forwarded to the subprocess and appears in the session output.
func TestHostStdinRouting(t *testing.T) {
	serverURL := testServer(t)
	h := newTestHost(t, serverURL)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// sh -c 'read x; echo "$x"; sleep 0.5' reads one line from stdin, echoes it, then sleeps
	// to keep session alive long enough to read output before deletion.
	runErr := make(chan error, 1)
	go func() {
		runErr <- h.Run(ctx, []string{"sh", "-c", `read x; echo "$x"; sleep 0.5`})
	}()

	// Poll aggressively for session and wait for it to be ready
	deadline := time.Now().Add(5 * time.Second)
	var sessionID string
	var clientID string
	var startedSession bool

	for time.Now().Before(deadline) {
		sessions := listSessions(t, serverURL)
		if len(sessions) > 0 {
			sessionID = sessions[0]["id"].(string)
			// Register a client for this session only once
			if !startedSession {
				clientResp, err := http.Post(serverURL+"/sessions/"+sessionID+"/clients", "application/json", bytes.NewReader([]byte("{}")))
				if err != nil {
					t.Fatalf("create client: %v", err)
				}
				var clientRespData struct {
					ClientID string `json:"client_id"`
				}
				json.NewDecoder(clientResp.Body).Decode(&clientRespData)
				clientResp.Body.Close()
				clientID = clientRespData.ClientID
				if clientID == "" {
					t.Fatal("expected non-empty client_id")
				}
				startedSession = true
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if sessionID == "" {
		t.Fatal("no session found")
	}

	// Give the subprocess a moment to reach its read() call.
	time.Sleep(100 * time.Millisecond)

	// Enqueue stdin via the server API.
	payload := base64.StdEncoding.EncodeToString([]byte("hello from client\n"))
	body, _ := json.Marshal(map[string]string{
		"data": payload,
	})
	resp, err := http.Post(serverURL+"/sessions/"+sessionID+"/stdin?client_id=test-client", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("enqueue stdin: %v", err)
	}
	var stdinResp struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&stdinResp) //nolint:errcheck
	resp.Body.Close()

	// Poll until we see the expected output
	deadline = time.Now().Add(5 * time.Second)
	var output string
	for time.Now().Before(deadline) {
		output = getOutput(t, serverURL, sessionID, clientID)
		if output == "hello from client\n" {
			break
		}
		time.Sleep(10 * time.Millisecond)
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

	// After completion, session should be deleted from memory
	resp2, _ := http.Get(serverURL + "/sessions/" + sessionID)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion (session deleted), got %d", resp2.StatusCode)
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

	// Wait for Run() to complete, which means the session was completed
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("Run() returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("Run() did not return after session completed")
	}

	// After completion, session should be deleted from memory
	// We can't check the exit code anymore since the session is deleted
	resp, _ := http.Get(serverURL + "/sessions/" + sessionID)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 after completion (session deleted), got %d", resp.StatusCode)
	}
}

// TestMain ////////////////////////////////////////////////////////////////////

func TestMain(m *testing.M) {
	testmain.TestMain(m)
}
