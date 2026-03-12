package client

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gorilla/websocket"
)

var wsCh = alog.UseChannel("WS_CLIENT")

// WSMessage is the WebSocket message format (matches server)
type WSMessage struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	ClientID  string          `json:"client_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// Message type constants
const (
	MsgTypeOutputChunk      = "output_chunk"
	MsgTypeStdinPending     = "stdin_pending"
	MsgTypeSessionCompleted = "session_completed"
	MsgTypeError            = "error"
	MsgTypePong             = "pong"
	MsgTypeSubscribed       = "subscribed"
	MsgTypeUnsubscribed     = "unsubscribed"
	MsgTypeSubscribe        = "subscribe"
	MsgTypeUnsubscribe      = "unsubscribe"
	MsgTypeStdinSubmit      = "stdin_submit"
	MsgTypePing             = "ping"
)

// OutputChunkPayload matches server format
type OutputChunkPayload struct {
	Stream    string `json:"stream"`
	Data      string `json:"data"`
	Offset    int64  `json:"offset"`
	Timestamp string `json:"timestamp"`
}

// StdinPayload matches server format
type StdinPayload struct {
	ID        string `json:"id,omitempty"`
	Data      string `json:"data,omitempty"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// StdinEntry represents a stdin entry (for callbacks)
type StdinEntry struct {
	ID     string
	Source string
	Data   string
	Status string
}

// SubscribePayload for subscribe messages
type SubscribePayload struct {
	SessionID string `json:"session_id"`
	ClientID  string `json:"client_id"`
}

// WebSocketConnection manages a WebSocket connection to the server
type WebSocketConnection struct {
	url       string
	tlsConfig *tls.Config
	conn      *websocket.Conn
	clientID  string
	sessionID string

	outputHandler       func(OutputChunk)
	stdinPendingHandler func(StdinEntry)
	errorHandler        func(error)

	reconnectAttempts int
	reconnectDelay    time.Duration
	maxReconnectDelay time.Duration

	send chan []byte
	done chan struct{}
	mu   sync.RWMutex

	connected bool
}

// NewWebSocketConnection creates a new WebSocket connection
func NewWebSocketConnection(url string, tlsConfig *tls.Config, clientID, sessionID string) *WebSocketConnection {
	return &WebSocketConnection{
		url:               url,
		tlsConfig:         tlsConfig,
		clientID:          clientID,
		sessionID:         sessionID,
		reconnectDelay:    1 * time.Second,
		maxReconnectDelay: 30 * time.Second,
		send:              make(chan []byte, 256),
		done:              make(chan struct{}),
	}
}

// OnOutput registers a callback for output chunks
func (ws *WebSocketConnection) OnOutput(handler func(OutputChunk)) {
	ws.outputHandler = handler
}

// OnStdinPending registers a callback for pending stdin
func (ws *WebSocketConnection) OnStdinPending(handler func(StdinEntry)) {
	ws.stdinPendingHandler = handler
}

// OnError registers a callback for errors
func (ws *WebSocketConnection) OnError(handler func(error)) {
	ws.errorHandler = handler
}

// Connect establishes the WebSocket connection
func (ws *WebSocketConnection) Connect(ctx context.Context) error {
	ws.mu.Lock()
	if ws.connected {
		ws.mu.Unlock()
		return nil
	}
	ws.mu.Unlock()

	// Create WebSocket dialer with TLS config
	dialer := websocket.Dialer{
		TLSClientConfig:  ws.tlsConfig,
		HandshakeTimeout: 10 * time.Second,
	}

	// Connect to WebSocket endpoint
	wsCh.Log(alog.DEBUG2, "Dialing WebSocket at [%s]", ws.url)
	conn, _, err := dialer.Dial(ws.url, nil)
	if err != nil {
		return fmt.Errorf("WebSocket dial failed: %w", err)
	}

	ws.mu.Lock()
	ws.conn = conn
	ws.connected = true
	ws.reconnectAttempts = 0
	ws.mu.Unlock()

	wsCh.Log(alog.DEBUG, "[remote-control] WebSocket connected to %s", ws.url)

	// Subscribe to session
	if err := ws.subscribe(); err != nil {
		ws.Close()
		return fmt.Errorf("subscribe failed: %w", err)
	}

	// Start read and write pumps
	go ws.readPump(ctx)
	go ws.writePump(ctx)

	return nil
}

// subscribe sends a subscribe message to the server
func (ws *WebSocketConnection) subscribe() error {
	payload := SubscribePayload{
		SessionID: ws.sessionID,
		ClientID:  ws.clientID,
	}
	payloadData, _ := json.Marshal(payload)

	msg := WSMessage{
		Type:      MsgTypeSubscribe,
		SessionID: ws.sessionID,
		ClientID:  ws.clientID,
		Payload:   payloadData,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	ws.mu.RLock()
	conn := ws.conn
	ws.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("connection not established")
	}

	return conn.WriteMessage(websocket.TextMessage, data)
}

// readPump reads messages from the WebSocket
func (ws *WebSocketConnection) readPump(ctx context.Context) {
	defer func() {
		ws.handleDisconnect(ctx)
	}()

	ws.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	ws.conn.SetPongHandler(func(string) error {
		ws.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-ws.done:
			return
		default:
		}

		_, message, err := ws.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				wsCh.Log(alog.DEBUG, "[remote-control] WebSocket read error: %v", err)
			}
			if ws.isStreamCorruption(err) {
				wsCh.Log(alog.DEBUG, "[remote-control] stream corruption detected")
			}
			return
		}

		var msg WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			wsCh.Log(alog.DEBUG, "[remote-control] invalid JSON in WebSocket message: %v", err)
			// Stream corruption - close and reconnect
			return
		}

		ws.handleMessage(msg)
	}
}

// writePump writes messages to the WebSocket
func (ws *WebSocketConnection) writePump(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ws.done:
			return
		case message, ok := <-ws.send:
			ws.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				ws.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := ws.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				wsCh.Log(alog.DEBUG, "[remote-control] WebSocket write error: %v", err)
				return
			}

		case <-ticker.C:
			ws.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes incoming WebSocket messages
func (ws *WebSocketConnection) handleMessage(msg WSMessage) {
	switch msg.Type {
	case MsgTypeOutputChunk:
		if ws.outputHandler != nil {
			var payload OutputChunkPayload
			if err := json.Unmarshal(msg.Payload, &payload); err == nil {
				ws.outputHandler(OutputChunk{
					Stream:    payload.Stream,
					Data:      payload.Data,
					Offset:    payload.Offset,
					Timestamp: payload.Timestamp,
				})
			}
		}

	case MsgTypeStdinPending:
		if ws.stdinPendingHandler != nil {
			var payload StdinPayload
			if err := json.Unmarshal(msg.Payload, &payload); err == nil {
				ws.stdinPendingHandler(StdinEntry{
					ID:     payload.ID,
					Source: payload.Source,
					Data:   payload.Data,
					Status: payload.Status,
				})
			}
		}

	case MsgTypeError:
		wsCh.Log(alog.DEBUG, "[remote-control] server error: %s", string(msg.Payload))
		if ws.errorHandler != nil {
			ws.errorHandler(fmt.Errorf("server error: %s", string(msg.Payload)))
		}

	case MsgTypeSubscribed:
		wsCh.Log(alog.DEBUG, "[remote-control] subscribed to session %s", msg.SessionID)

	case MsgTypeUnsubscribed:
		wsCh.Log(alog.DEBUG, "[remote-control] unsubscribed from session %s", msg.SessionID)

	case MsgTypePong:
		// Heartbeat response

	default:
		wsCh.Log(alog.DEBUG, "[remote-control] unknown message type: %s", msg.Type)
	}
}

// handleDisconnect handles connection loss and attempts reconnection
func (ws *WebSocketConnection) handleDisconnect(ctx context.Context) {
	ws.mu.Lock()
	ws.connected = false
	if ws.conn != nil {
		ws.conn.Close()
		ws.conn = nil
	}
	ws.mu.Unlock()

	wsCh.Log(alog.INFO, "[remote-control] WebSocket disconnected, attempting reconnect")

	// Attempt reconnection with exponential backoff
	ws.reconnect(ctx)
}

// reconnect attempts to re-establish the connection
func (ws *WebSocketConnection) reconnect(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ws.done:
			return
		case <-time.After(ws.reconnectDelay):
			ws.reconnectAttempts++
			wsCh.Log(alog.DEBUG, "[remote-control] reconnect attempt %d", ws.reconnectAttempts)

			if err := ws.Connect(ctx); err != nil {
				wsCh.Log(alog.DEBUG, "[remote-control] reconnect failed: %v", err)
				// Exponential backoff
				ws.reconnectDelay *= 2
				if ws.reconnectDelay > ws.maxReconnectDelay {
					ws.reconnectDelay = ws.maxReconnectDelay
				}
				continue
			}

			// Reconnection successful
			ws.reconnectDelay = 1 * time.Second
			return
		}
	}
}

// isStreamCorruption checks if an error indicates stream corruption
func (ws *WebSocketConnection) isStreamCorruption(err error) bool {
	if err == nil {
		return false
	}
	// Check for common corruption indicators
	errStr := err.Error()
	return websocket.IsUnexpectedCloseError(err) ||
		contains(errStr, "invalid") ||
		contains(errStr, "corrupt") ||
		contains(errStr, "malformed")
}

// SubmitStdin sends stdin data via WebSocket
func (ws *WebSocketConnection) SubmitStdin(data string) error {
	payload := StdinPayload{
		Data:   base64.StdEncoding.EncodeToString([]byte(data)),
		Source: "client",
	}
	payloadData, _ := json.Marshal(payload)

	msg := WSMessage{
		Type:      MsgTypeStdinSubmit,
		SessionID: ws.sessionID,
		ClientID:  ws.clientID,
		Payload:   payloadData,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	select {
	case ws.send <- msgData:
		return nil
	case <-ws.done:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("send buffer full")
	}
}

// IsConnected returns whether the WebSocket is currently connected
func (ws *WebSocketConnection) IsConnected() bool {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.connected
}

// ReconnectAttempts returns the number of reconnection attempts
func (ws *WebSocketConnection) ReconnectAttempts() int {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.reconnectAttempts
}

// Close closes the WebSocket connection
func (ws *WebSocketConnection) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	select {
	case <-ws.done:
		return nil
	default:
		close(ws.done)
	}

	if ws.conn != nil {
		ws.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		ws.conn.Close()
		ws.conn = nil
	}

	ws.connected = false
	return nil
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsMiddle(s, substr)))
}

func containsMiddle(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
