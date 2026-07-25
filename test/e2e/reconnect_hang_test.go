package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/IBM/remote-control/internal/client"
	"github.com/IBM/remote-control/internal/common/types"
	ws "github.com/IBM/remote-control/internal/common/websocket"
)

// appendOutput posts a chunk of host output to the session, exactly as a real
// host process streaming a PTY does.
func appendOutput(t *testing.T, serverURL, sessionID, data string) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"stream": types.StreamStdout,
		"data":   base64.StdEncoding.EncodeToString([]byte(data)),
	})
	resp, err := http.Post(serverURL+"/sessions/"+sessionID+"/output", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("append output: %v", err)
	}
	resp.Body.Close()
}

// TestReconnectDoesNotDegradeConnectSpeed is a regression test for a
// production hang reported against a real PTY-backed host session: a client
// reconnecting (each reconnect is a fresh process, so it always registers
// with a new clientID) took progressively longer to establish a connection
// the more times it reconnected, eventually hanging altogether.
//
// Root cause: every connect calls Session.RegisterClient twice - once over
// HTTP with no socket yet, then again over the WebSocket upgrade - and the
// server used to always mint a brand-new client record on both calls instead
// of recognizing they were the same connect attempt. That left one
// permanently-unreachable "zombie" record behind per connect. A host
// streaming continuous output (like a high-refresh-rate terminal UI) fans
// out to every client on every write, including the zombies, whose
// undeliverable retry queues grew without bound - and that fan-out held the
// session lock for its entire duration, blocking any new connect attempt
// trying to acquire the same lock.
//
// This test drives a busy host in the background and repeatedly connects a
// fresh client (new clientID each time, mirroring separate reconnects),
// asserting that connect latency does not grow as reconnects accumulate.
func TestReconnectDoesNotDegradeConnectSpeed(t *testing.T) {
	serverURL := testServer(t)
	sessionID := createSession(t, serverURL)

	// Simulate a busy host - e.g. opencode running in a PTY - streaming
	// output continuously for the duration of the test.
	stopHost := make(chan struct{})
	hostDone := make(chan struct{})
	go func() {
		defer close(hostDone)
		i := 0
		for {
			select {
			case <-stopHost:
				return
			default:
			}
			appendOutput(t, serverURL, sessionID, fmt.Sprintf("line %d: some terminal output\n", i))
			i++
		}
	}()
	t.Cleanup(func() {
		close(stopHost)
		<-hostDone
	})

	// Let the host build up a backlog before the first connect, matching the
	// reported "waiting longer before connecting" trigger.
	time.Sleep(300 * time.Millisecond)

	const numReconnects = 15
	latencies := make([]time.Duration, 0, numReconnects)
	for i := range numReconnects {
		start := time.Now()

		// Each reconnect is a brand-new client process in the real world, so
		// it always registers over HTTP with a fresh (empty) clientID first.
		clientID := registerClient(t, serverURL, sessionID)

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		conn := client.NewWebSocketConnection(
			ws.DeriveWebSocketURL(serverURL), nil, clientID, sessionID,
			&ws.WebSocketConfig{
				ReconnectInterval: 1 * time.Second,
				ReconnectTimeout:  2 * time.Second,
				MaxQueueLength:    100,
			},
		)
		err := conn.Connect(ctx)
		cancel()
		latency := time.Since(start)
		if err != nil {
			t.Fatalf("reconnect #%d failed after %v: %v", i, latency, err)
		}
		conn.Close()

		latencies = append(latencies, latency)
		t.Logf("reconnect #%d: %v", i, latency)

		time.Sleep(100 * time.Millisecond)
	}

	first, last := latencies[0], latencies[len(latencies)-1]
	t.Logf("first connect: %v, last connect (after %d reconnects): %v", first, numReconnects, last)

	// Before the fix, each reconnect stacks another permanently-failing
	// zombie client onto the session, and every subsequent connect measured
	// ~80-90ms (vs. sub-millisecond once the zombies are gone) because
	// registering/approving the new client contends for Session.mu with the
	// host's output fan-out, which now has to pay for every zombie's
	// undeliverable retry queue on every write. Use a generous absolute
	// ceiling well above normal fixed-code noise but far below that plateau.
	const maxAcceptableLatency = 25 * time.Millisecond
	for i, latency := range latencies {
		if latency > maxAcceptableLatency {
			t.Errorf(
				"reconnect #%d took %v (> %v) - looks like zombie client records are accumulating and slowing down connections",
				i, latency, maxAcceptableLatency,
			)
		}
	}
}
