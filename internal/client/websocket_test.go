//go:build !race

package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IBM/remote-control/internal/common/types"
	ws "github.com/IBM/remote-control/internal/common/websocket"
	gowebsocket "github.com/gorilla/websocket"
)

// serverConn stores the current server-side WebSocket connection atomically.
type serverConn struct {
	v atomic.Value // holds *gowebsocket.Conn
}

func (s *serverConn) Store(c *gowebsocket.Conn) { s.v.Store(c) }
func (s *serverConn) Load() *gowebsocket.Conn {
	v := s.v.Load()
	if v == nil {
		return nil
	}
	return v.(*gowebsocket.Conn)
}

// startServerThatPushesMessages starts a WebSocket server. When a client
// connects at /ws/{id}, the server stores the connection, waits briefly,
// then sends a Stdin message to that client. Returns the server URL and cleanup.
func startServerThatPushesMessages(t *testing.T) (string, *serverConn, func()) {
	t.Helper()

	var upgrader = gowebsocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}

	var serverConn serverConn
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

			// Store the connection so the test can send messages
			serverConn.Store(conn)

			// Wait for the pipe to be ready (readPump to start)
			time.Sleep(100 * time.Millisecond)

			// Send an output message to the client (client's OnOutput handler expects []OutputChunk)
			chunk := types.OutputChunk{Stream: types.StreamStdout, Data: []byte("pushed from server")}
			chunkBytes, _ := json.Marshal([]types.OutputChunk{chunk})
			msg := types.WSMessage{
				Type:    types.WSMessageOutput,
				Message: chunkBytes,
			}
			data, _ := json.Marshal(msg)
			_ = conn.WriteMessage(gowebsocket.TextMessage, data)
		}()
	})

	listener := newLocalListener(t)
	srv := &http.Server{Handler: httpServer}
	go srv.Serve(listener)

	return "ws://" + listener.Addr().String(), &serverConn, func() {
		srv.Close()
		wg.Wait()
		listener.Close()
	}
}

// TestNewWebSocketConnectionWithFallbackStartsPipe is the critical regression test for
// the bug where NewWebSocketConnectionWithFallback created a WebSocketPipe but never
// called pipe.Start(), leaving the read/write pump goroutines uninitialized and
// preventing any messages from flowing.
func TestNewWebSocketConnectionWithFallbackStartsPipe(t *testing.T) {
	wsURL, _, stop := startServerThatPushesMessages(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsConfig := &ws.WebSocketConfig{
		ReconnectInterval: time.Millisecond,
		ReconnectTimeout:  time.Second,
		MaxQueueLength:    100,
	}

	conn, err := NewWebSocketConnectionWithFallback(ctx, []string{wsURL}, "test-client", "test-session", nil, wsConfig)
	if err != nil {
		t.Fatalf("failed to create WebSocketConnection: %v", err)
	}

	// Set up a handler that should fire if the pipe is started
	receivedOutput := make(chan types.OutputChunk, 1)
	conn.OnOutput(func(chunk types.OutputChunk) {
		select {
		case receivedOutput <- chunk:
		default:
		}
	})

	// Wait for the message pushed by the server
	select {
	case chunk := <-receivedOutput:
		if string(chunk.Data) != "pushed from server" {
			t.Errorf("expected 'pushed from server', got %q", string(chunk.Data))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message — pipe.Start was likely not called")
	}

	// Close and wait for server goroutine to finish to avoid races
	conn.Close()
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
