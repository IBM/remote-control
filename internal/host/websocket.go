package host

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gorilla/websocket"
)

var wsHostCh = alog.UseChannel("WS_HOST")

// WSMessage matches the server/client message format
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
	MsgTypeStdinAccept      = "stdin_accept"
	MsgTypeStdinReject      = "stdin_reject"
	MsgTypePing             = "ping"
)

// OutputChunkPayload for sending output
type OutputChunkPayload struct {
	Stream    string `json:"stream"`
	Data      string `json:"data"`
	Offset    int64  `json:"offset"`
	Timestamp string `json:"timestamp"`
}

// StdinPayload for stdin operations
type StdinPayload struct {
	ID        string `json:"id,omitempty"`
	Data      string `json:"data,omitempty"`
	Source    string `json:"source,omitempty"`
	Status    string `json:"status,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// SubscribePayload for subscribe messages
type SubscribePayload struct {
	SessionID string `json:"session_id"`
	ClientID  string `json:"client_id"`
}

// WebSocketHost manages WebSocket connection for the host
type WebSocketHost struct {
	url       string
	tlsConfig *tls.Config
	conn      *websocket.Conn
	sessionID string
	clientID  string

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

// StdinEntry represents a pending stdin entry
type StdinEntry struct {
	ID     string
	Source string
	Data   []byte
	Status string
}

// NewWebSocketHost creates a new WebSocket host connection
func NewWebSocketHost(url string, tlsConfig *tls.Config, sessionID, clientID string) *WebSocketHost {
	return &WebSocketHost{
		url:               url,
		tlsConfig:         tlsConfig,
		sessionID:         sessionID,
		clientID:          clientID,
		reconnectDelay:    1 * time.Second,
		maxReconnectDelay: 30 * time.Second,
		send:              make(chan []byte, 256),
		done:              make(chan struct{}),
	}
}

// OnStdinPending registers a callback for pending stdin entries
func (wh *WebSocketHost) OnStdinPending(handler func(StdinEntry)) {
	wh.stdinPendingHandler = handler
}

// OnError registers a callback for errors
func (wh *WebSocketHost) OnError(handler func(error)) {
	wh.errorHandler = handler
}

// Connect establishes the WebSocket connection
func (wh *WebSocketHost) Connect(ctx context.Context) error {
	wh.mu.Lock()
	if wh.connected {
		wh.mu.Unlock()
		return nil
	}
	wh.mu.Unlock()

	// Create WebSocket dialer with TLS config
	dialer := websocket.Dialer{
		TLSClientConfig:  wh.tlsConfig,
		HandshakeTimeout: 10 * time.Second,
	}

	// Connect to WebSocket endpoint
	conn, _, err := dialer.Dial(wh.url, nil)
	if err != nil {
		return fmt.Errorf("WebSocket dial failed: %w", err)
	}

	wh.mu.Lock()
	wh.conn = conn
	wh.connected = true
	wh.reconnectAttempts = 0
	wh.mu.Unlock()

	wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket connected to %s", wh.url)

	// Subscribe to session
	if err := wh.subscribe(); err != nil {
		wh.Close()
		return fmt.Errorf("subscribe failed: %w", err)
	}

	// Start read and write pumps
	go wh.readPump(ctx)
	go wh.writePump(ctx)

	return nil
}

// subscribe sends a subscribe message to the server
func (wh *WebSocketHost) subscribe() error {
	payload := SubscribePayload{
		SessionID: wh.sessionID,
		ClientID:  wh.clientID,
	}
	payloadData, _ := json.Marshal(payload)

	msg := WSMessage{
		Type:      MsgTypeSubscribe,
		SessionID: wh.sessionID,
		ClientID:  wh.clientID,
		Payload:   payloadData,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	wh.mu.RLock()
	conn := wh.conn
	wh.mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("connection not established")
	}

	return conn.WriteMessage(websocket.TextMessage, data)
}

// readPump reads messages from the WebSocket
func (wh *WebSocketHost) readPump(ctx context.Context) {
	defer func() {
		wh.handleDisconnect(ctx)
	}()

	wh.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	wh.conn.SetPongHandler(func(string) error {
		wh.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-wh.done:
			return
		default:
		}

		_, message, err := wh.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket read error: %v", err)
			}
			return
		}

		var msg WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			wsHostCh.Log(alog.DEBUG, "[remote-control] invalid JSON in WebSocket message: %v", err)
			return
		}

		wh.handleMessage(msg)
	}
}

// writePump writes messages to the WebSocket
func (wh *WebSocketHost) writePump(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-wh.done:
			return
		case message, ok := <-wh.send:
			wh.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				wh.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := wh.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket write error: %v", err)
				return
			}

		case <-ticker.C:
			wh.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := wh.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes incoming WebSocket messages
func (wh *WebSocketHost) handleMessage(msg WSMessage) {
	switch msg.Type {
	case MsgTypeStdinPending:
		if wh.stdinPendingHandler != nil {
			var payload StdinPayload
			if err := json.Unmarshal(msg.Payload, &payload); err == nil {
				data, _ := base64.StdEncoding.DecodeString(payload.Data)
				wh.stdinPendingHandler(StdinEntry{
					ID:     payload.ID,
					Source: payload.Source,
					Data:   data,
					Status: payload.Status,
				})
			}
		}

	case MsgTypeError:
		wsHostCh.Log(alog.DEBUG, "[remote-control] server error: %s", string(msg.Payload))
		if wh.errorHandler != nil {
			wh.errorHandler(fmt.Errorf("server error: %s", string(msg.Payload)))
		}

	case MsgTypeSubscribed:
		wsHostCh.Log(alog.DEBUG, "[remote-control] subscribed to session %s", msg.SessionID)

	case MsgTypeUnsubscribed:
		wsHostCh.Log(alog.DEBUG, "[remote-control] unsubscribed from session %s", msg.SessionID)

	case MsgTypePong:
		// Heartbeat response

	default:
		wsHostCh.Log(alog.DEBUG, "[remote-control] unknown message type: %s", msg.Type)
	}
}

// handleDisconnect handles connection loss and attempts reconnection
func (wh *WebSocketHost) handleDisconnect(ctx context.Context) {
	wh.mu.Lock()
	wh.connected = false
	if wh.conn != nil {
		wh.conn.Close()
		wh.conn = nil
	}
	wh.mu.Unlock()

	wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket disconnected, attempting reconnect")

	// Attempt reconnection with exponential backoff
	wh.reconnect(ctx)
}

// reconnect attempts to re-establish the connection
func (wh *WebSocketHost) reconnect(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-wh.done:
			return
		case <-time.After(wh.reconnectDelay):
			wh.reconnectAttempts++
			wsHostCh.Log(alog.DEBUG, "[remote-control] reconnect attempt %d", wh.reconnectAttempts)

			if err := wh.Connect(ctx); err != nil {
				wsHostCh.Log(alog.DEBUG, "[remote-control] reconnect failed: %v", err)
				// Exponential backoff
				wh.reconnectDelay *= 2
				if wh.reconnectDelay > wh.maxReconnectDelay {
					wh.reconnectDelay = wh.maxReconnectDelay
				}
				continue
			}

			// Reconnection successful
			wh.reconnectDelay = 1 * time.Second
			return
		}
	}
}

// SendOutput sends output data via WebSocket
func (wh *WebSocketHost) SendOutput(stream string, data []byte, offset int64, timestamp time.Time) error {
	payload := OutputChunkPayload{
		Stream:    stream,
		Data:      base64.StdEncoding.EncodeToString(data),
		Offset:    offset,
		Timestamp: timestamp.Format(time.RFC3339Nano),
	}
	payloadData, _ := json.Marshal(payload)

	msg := WSMessage{
		Type:      MsgTypeOutputChunk,
		SessionID: wh.sessionID,
		Payload:   payloadData,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	select {
	case wh.send <- msgData:
		return nil
	case <-wh.done:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("send buffer full")
	}
}

// AcceptStdin sends a stdin accept message
func (wh *WebSocketHost) AcceptStdin(entryID string) error {
	payload := StdinPayload{
		ID: entryID,
	}
	payloadData, _ := json.Marshal(payload)

	msg := WSMessage{
		Type:      MsgTypeStdinAccept,
		SessionID: wh.sessionID,
		Payload:   payloadData,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	select {
	case wh.send <- msgData:
		return nil
	case <-wh.done:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("send buffer full")
	}
}

// RejectStdin sends a stdin reject message
func (wh *WebSocketHost) RejectStdin(entryID string) error {
	payload := StdinPayload{
		ID: entryID,
	}
	payloadData, _ := json.Marshal(payload)

	msg := WSMessage{
		Type:      MsgTypeStdinReject,
		SessionID: wh.sessionID,
		Payload:   payloadData,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	select {
	case wh.send <- msgData:
		return nil
	case <-wh.done:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("send buffer full")
	}
}

// IsConnected returns whether the WebSocket is currently connected
func (wh *WebSocketHost) IsConnected() bool {
	wh.mu.RLock()
	defer wh.mu.RUnlock()
	return wh.connected
}

// Close closes the WebSocket connection
func (wh *WebSocketHost) Close() error {
	wh.mu.Lock()
	defer wh.mu.Unlock()

	select {
	case <-wh.done:
		return nil
	default:
		close(wh.done)
	}

	if wh.conn != nil {
		wh.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		wh.conn.Close()
		wh.conn = nil
	}

	wh.connected = false
	return nil
}

// proxyOutputWebSocket sends output chunks via WebSocket instead of HTTP
func (h *Host) proxyOutputWebSocket(ctx context.Context, r io.Reader, dst io.Writer, wsHost *WebSocketHost, stream string, offset *int64, offsetMu *sync.Mutex) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			ts := time.Now()

			// Write to local terminal
			if _, werr := dst.Write(chunk); werr != nil {
				ch.Log(alog.WARNING, "[remote-control] local %s write error: %v", stream, werr)
			}

			// Get current offset
			offsetMu.Lock()
			currentOffset := *offset
			*offset += int64(n)
			offsetMu.Unlock()

			// Forward to server via WebSocket if connected
			if wsHost != nil && wsHost.IsConnected() {
				select {
				case <-ctx.Done():
					return
				default:
					if serr := wsHost.SendOutput(stream, chunk, currentOffset, ts); serr != nil {
						wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket send output error: %v", serr)
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				ch.Log(alog.WARNING, "[remote-control] %s pipe read error: %v", stream, err)
			}
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// proxyPTYOutputWebSocket sends PTY output via WebSocket
func (h *Host) proxyPTYOutputWebSocket(ctx context.Context, ptmx *os.File, wsHost *WebSocketHost, offset *int64, offsetMu *sync.Mutex) {
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			ts := time.Now()

			// Skip local display while an approval prompt is shown
			if !h.pauseOutput.Load() {
				if _, werr := os.Stdout.Write(chunk); werr != nil {
					ch.Log(alog.WARNING, "[remote-control] local stdout write error: %v", werr)
				}
			}

			// Get current offset
			offsetMu.Lock()
			currentOffset := *offset
			*offset += int64(n)
			offsetMu.Unlock()

			// Forward to server via WebSocket if connected
			if wsHost != nil && wsHost.IsConnected() {
				select {
				case <-ctx.Done():
					return
				default:
					if serr := wsHost.SendOutput("stdout", chunk, currentOffset, ts); serr != nil {
						wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket send output error: %v", serr)
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF && !contains(err.Error(), "input/output error") {
				ch.Log(alog.WARNING, "[remote-control] PTY read error: %v", err)
			}
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
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
