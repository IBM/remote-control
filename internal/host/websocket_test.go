//go:build !race

package host

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	ws "github.com/gabe-l-hart/remote-control/internal/common/websocket"
	"github.com/gabe-l-hart/remote-control/internal/common/types"
	gowebsocket "github.com/gorilla/websocket"
)

// startServerThatPushesMessages starts a WebSocket server. When a client
// connects at /ws/{id}, the server waits briefly then sends a Stdin message
// to that client. Returns the server URL and a cleanup function.
func startServerThatPushesMessages(t *testing.T) (string, func()) {
	t.Helper()

	var upgrader = gowebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	var wg sync.WaitGroup

	httpServer := http.NewServeMux()
	httpServer.HandleFunc("/ws/{id}", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()

			// Wait for the pipe to be ready (readPump to start)
			time.Sleep(100 * time.Millisecond)

			// Send a stdin message to the client
			entries := []types.StdinEntry{{Data: []byte("pushed from server")}}
			entryBytes, _ := json.Marshal(entries)
			msg := types.WSMessage{
				Type:    types.WSMessageStdin,
				Message: entryBytes,
			}
			data, _ := json.Marshal(msg)
			_ = conn.WriteMessage(gowebsocket.TextMessage, data)
		}()
	})

	listener := newLocalListener(t)
	srv := &http.Server{Handler: httpServer}
	go srv.Serve(listener)

	return "ws://" + listener.Addr().String(), func() {
		srv.Close()
		wg.Wait()
		listener.Close()
	}
}

// TestNewWebSocketHostWithFallbackStartsPipe is the critical regression test for
// the bug where NewWebSocketHostWithFallback created a WebSocketPipe but never
// called pipe.Start(), leaving the read/write pump goroutines uninitialized and
// preventing any messages from flowing.
func TestNewWebSocketHostWithFallbackStartsPipe(t *testing.T) {
	wsURL, stop := startServerThatPushesMessages(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsConfig := &ws.WebSocketConfig{
		ReconnectInterval: time.Millisecond,
		ReconnectTimeout:  time.Second,
		MaxQueueLength:    100,
	}

	wh, err := NewWebSocketHostWithFallback(ctx, []string{wsURL}, nil, nil, "test-session", "test-client", wsConfig)
	if err != nil {
		t.Fatalf("failed to create WebSocketHost: %v", err)
	}

	// Set up a handler that should fire if the pipe is started
	receivedStdin := make(chan types.StdinEntry, 1)
	wh.OnStdin(func(entry types.StdinEntry) {
		select {
		case receivedStdin <- entry:
		default:
		}
	})

	// Wait for the message pushed by the server
	select {
	case entry := <-receivedStdin:
		if string(entry.Data) != "pushed from server" {
			t.Errorf("expected 'pushed from server', got %q", string(entry.Data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message — pipe.Start was likely not called")
	}

	// Close and wait for server goroutine to finish to avoid races
	wh.Close()
	time.Sleep(50 * time.Millisecond)
}

func newLocalListener(t *testing.T) *net.TCPListener {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return l
}
