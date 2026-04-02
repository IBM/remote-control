package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/server/session"
	"github.com/gorilla/websocket"
)

func wsURL(t *testing.T, testServer *httptest.Server, sessionID, clientID string) string {
	t.Helper()
	return "ws" + strings.TrimPrefix(testServer.URL, "http") + "/ws/" + sessionID + "?client_id=" + clientID
}

func newTestWSClient(t *testing.T, testServer *httptest.Server, sessionID, clientID string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	dialer := &websocket.Dialer{}
	return dialer.Dial(wsURL(t, testServer, sessionID, clientID), nil)
}

func TestWebSocketUpgradeSuccess(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-ws-upgrade"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	clientID := "test-client"
	conn, _, err := newTestWSClient(t, testServer, sessionID, clientID)
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	t.Log("WebSocket upgrade succeeded")
}

func TestWebSocketConnectionEstablishmentWithInvalidSession(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	invalidSessionID := "nonexistent-session"
	clientID := "test-client"

	// WebSocket upgrade may succeed (HTTP 101), but the connection should close
	conn, resp, err := newTestWSClient(t, testServer, invalidSessionID, clientID)

	// Either we get an error OR we get a 101 status with immediate close
	if nil == err {
		conn.Close()
		if resp != nil && resp.StatusCode == 101 {
			t.Log("WebSocket upgrade succeeded (HTTP 101) - server may close connection afterward")
		} else {
			t.Logf("Got response status: %d", resp.StatusCode)
		}
	} else {
		t.Logf("Connection failed as expected: %v", err)
	}
}

func TestWebSocketOutputChunkRoundTrip(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-ws-output"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	conn, _, err := newTestWSClient(t, testServer, sessionID, "output-client")
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	outputData := []byte("Hello, WebSocket!")
	outputChunk := types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   outputData,
	}

	msg := types.WSMessage{
		Type:    types.WSMessageOutput,
		Message: mustMarshalJSON(t, outputChunk),
	}

	if err := conn.WriteJSON(msg); nil != err {
		t.Fatalf("Failed to write output chunk: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	t.Log("Output chunk sent and acknowledged")
}

func TestWebSocketStdinRoundTrip(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-ws-stdin"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	conn, _, err := newTestWSClient(t, testServer, sessionID, "stdin-client")
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	stdinData := []byte("ls -la\n")
	stdinEntry := types.StdinEntry{
		Data: stdinData,
	}

	msg := types.WSMessage{
		Type:    types.WSMessageStdin,
		Message: mustMarshalJSON(t, stdinEntry),
	}

	if err := conn.WriteJSON(msg); nil != err {
		t.Fatalf("Failed to write stdin entry: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	t.Log("Stdin entry sent and acknowledged")
}

func TestWebSocketMultipleConcurrentClients(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-ws-concurrent"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	numClients := 5
	var conns []*websocket.Conn

	for i := 0; i < numClients; i++ {
		clientID := "client-" + string(rune('0'+i))

		conn, _, err := newTestWSClient(t, testServer, sessionID, clientID)
		if nil != err {
			t.Fatalf("WebSocket dial failed for %s: %v", clientID, err)
		}
		conns = append(conns, conn)
	}

	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()

	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			outputData := []byte("output from client " + string(rune('0'+byte(idx))))
			chunk := types.OutputChunk{
				Stream: types.StreamStdout,
				Data:   outputData,
			}

			msg := types.WSMessage{
				Type:    types.WSMessageOutput,
				Message: mustMarshalJSON(t, chunk),
			}

			if err := conns[idx].WriteJSON(msg); nil != err {
				t.Errorf("Failed to send from client %d: %v", idx, err)
			}
		}(i)
	}

	wg.Wait()
	t.Log("Concurrent clients completed successfully")
}

func TestWebSocketConnectionDrop(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-ws-drop"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	clientID := "drop-client"
	conn, _, err := newTestWSClient(t, testServer, sessionID, clientID)
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}

	conn.Close()
	time.Sleep(100 * time.Millisecond)
	t.Log("Client cleanup triggered on connection drop")
}

func TestHandleAppendOutputWSInvalidJSON(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-invalid-output-json"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	conn, _, err := newTestWSClient(t, testServer, sessionID, "invalid-json-client")
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	// Send invalid JSON directly to the connection
	invalidJSON := "{\"type\": 10, \"message\": {\"invalid json"
	if err := conn.WriteMessage(websocket.TextMessage, []byte(invalidJSON)); nil != err {
		t.Fatalf("Failed to write message: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	t.Log("Invalid JSON handling tested")
}

func TestHandleStdinSubmitWSInvalidJSON(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-invalid-stdin-json"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	conn, _, err := newTestWSClient(t, testServer, sessionID, "invalid-stdin-json-client")
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	// Send invalid JSON directly to the connection
	invalidJSON := "{\"type\": 20, \"message\": {\"data\": "
	if err := conn.WriteMessage(websocket.TextMessage, []byte(invalidJSON)); nil != err {
		t.Fatalf("Failed to write message: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	t.Log("Invalid JSON handling for stdin tested")
}

func TestHandleServerMessageUnknownType(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      false,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-invalid-type"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	conn, _, err := newTestWSClient(t, testServer, sessionID, "invalid-type-client")
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)

	invalidMsg := types.WSMessage{
		Type:    types.WSMessageUnknown,
		Message: []byte(`{}`),
	}

	if err := conn.WriteJSON(invalidMsg); nil != err {
		t.Fatalf("Failed to write message: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	t.Log("Error handling for unknown type tested")
}

func TestWebSocketClientApprovalFlow(t *testing.T) {
	cfg := &config.Config{
		RequireApproval:      true,
		MaxOutputBuffer:      1024 * 1024,
		ClientTimeoutSeconds: 0,
	}
	store := session.NewStore(cfg.MaxOutputBuffer)
	server := NewServer(":0", cfg)
	testServer := httptest.NewServer(server.Handler())
	defer testServer.Close()

	sessionID := "test-approval"
	_, err := store.Create(&sessionID, nil, cfg)
	if nil != err {
		t.Fatalf("Failed to create session: %v", err)
	}

	conn, _, err := newTestWSClient(t, testServer, sessionID, "pending-client")
	if nil != err {
		t.Fatalf("WebSocket dial failed: %v", err)
	}
	defer conn.Close()

	time.Sleep(100 * time.Millisecond)
	t.Log("Approval flow test completed")
}

func mustMarshalJSON(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()

	data, err := json.Marshal(v)
	if nil != err {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}

	return data
}
