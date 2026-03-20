package host

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gorilla/websocket"
)

var wsHostCh = alog.UseChannel("WS_HOST")

// WebSocketHost manages the host's WebSocket connection to the server
type WebSocketHost struct {
	url       string
	tlsConfig *tls.Config
	conn      *websocket.Conn
	sessionID string
	clientID  string

	onStdinHandler         func(types.StdinEntry)
	onPendingClientHandler func(string)

	reconnectAttempts int
	reconnectDelay    time.Duration
	maxReconnectDelay time.Duration

	send chan []byte
	done chan struct{}
	mu   sync.RWMutex

	connected atomic.Bool
}

// NewWebSocketHost creates a new WebSocketHost instance
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

// OnStdin registers a callback for incoming stdin messages from the server
func (wh *WebSocketHost) OnStdin(handler func(types.StdinEntry)) {
	wh.mu.Lock()
	defer wh.mu.Unlock()
	wh.onStdinHandler = handler
}

// OnPendingClient registers a callback for pending client notifications
func (wh *WebSocketHost) OnPendingClient(handler func(string)) {
	wh.mu.Lock()
	defer wh.mu.Unlock()
	wh.onPendingClientHandler = handler
}

// OnError registers a callback for errors
func (wh *WebSocketHost) OnError(handler func(error)) {
	wh.mu.Lock()
	defer wh.mu.Unlock()
	// Error handler removed - errors are logged internally
	_ = handler
}

// IsConnected returns whether the WebSocket is currently connected
func (wh *WebSocketHost) IsConnected() bool {
	return wh.connected.Load()
}

// Connect establishes the WebSocket connection
func (wh *WebSocketHost) Connect(ctx context.Context) error {
	wh.mu.Lock()
	if wh.connected.Load() {
		wh.mu.Unlock()
		return nil
	}
	wh.mu.Unlock()

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	if wh.tlsConfig != nil {
		dialer.TLSClientConfig = wh.tlsConfig
	}

	conn, _, err := dialer.Dial(wh.url+"/ws/"+wh.sessionID, nil)
	if err != nil {
		return fmt.Errorf("WebSocket dial failed: %w", err)
	}

	wh.mu.Lock()
	wh.conn = conn
	wh.connected.Store(true)
	wh.reconnectAttempts = 0
	wh.mu.Unlock()

	wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket connected to %s", wh.url)

	go wh.readPump(ctx)
	go wh.writePump(ctx)

	return nil
}

// readPump reads messages from the WebSocket
func (wh *WebSocketHost) readPump(ctx context.Context) {
	defer func() {
		wh.handleDisconnect(ctx)
	}()

	wh.mu.RLock()
	conn := wh.conn
	wh.mu.RUnlock()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-wh.done:
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket read error: %v", err)
			}
			return
		}

		var msg types.WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			wsHostCh.Log(alog.DEBUG, "[remote-control] invalid JSON in WebSocket message: %v", err)
			continue
		}

		wh.handleMessage(msg)
	}
}

// writePump writes messages to the WebSocket
func (wh *WebSocketHost) writePump(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-wh.done:
			return
		case message, ok := <-wh.send:
			wh.mu.RLock()
			conn := wh.conn
			wh.mu.RUnlock()

			if conn == nil {
				return
			}

			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket write error: %v", err)
				return
			}

		case <-ticker.C:
			wh.mu.RLock()
			conn := wh.conn
			wh.mu.RUnlock()

			if conn == nil {
				return
			}

			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes incoming WebSocket messages
func (wh *WebSocketHost) handleMessage(msg types.WSMessage) {
	wh.mu.RLock()
	onStdin := wh.onStdinHandler
	onPendingClient := wh.onPendingClientHandler
	wh.mu.RUnlock()

	switch msg.Type {
	case types.WSMessageStdin:
		if onStdin != nil {
			var entry types.StdinEntry
			if err := msg.UnmarshalMessage(&entry); err == nil {
				onStdin(entry)
			}
		}

	case types.WSMessagePendingClient:
		if onPendingClient != nil {
			var clientID string
			if err := msg.UnmarshalMessage(&clientID); err == nil {
				onPendingClient(clientID)
			}
		}

	case types.WSMessageError:
		var errMsg string
		if err := msg.UnmarshalMessage(&errMsg); err == nil {
			wsHostCh.Log(alog.DEBUG, "[remote-control] server error: %s", errMsg)
		}
	}
}

// handleDisconnect handles connection loss
func (wh *WebSocketHost) handleDisconnect(ctx context.Context) {
	wh.mu.Lock()
	wh.connected.Store(false)
	if wh.conn != nil {
		wh.conn.Close()
		wh.conn = nil
	}
	wh.mu.Unlock()

	wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket disconnected")
}

// SendOutput sends output data via WebSocket
func (wh *WebSocketHost) SendOutput(stream types.Stream, data []byte, offset int64, timestamp time.Time) error {
	wsHostCh.Log(alog.DEBUG4, "sending output on stream %d: %v", stream, data)
	payload := types.OutputChunk{
		Stream: stream,
		Data:   data,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	wsHostCh.Log(alog.DEBUG4, "payload bytes: %v", payloadBytes)
	wsHostCh.Log(alog.DEBUG4, "payload json: %s", payloadBytes)

	msg := types.WSMessage{
		Type:    types.WSMessageOutput,
		Message: payloadBytes,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	wsHostCh.Log(alog.DEBUG4, "full bytes: %v", msgData)
	wsHostCh.Log(alog.DEBUG4, "full json: %s", msgData)

	select {
	case wh.send <- msgData:
		return nil
	case <-wh.done:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("send buffer full")
	}
}

// SendStdinSubmit submits host stdin data via WebSocket
func (wh *WebSocketHost) SendStdinSubmit(data []byte) error {
	// Encode as base64 for JSON transport
	encoded := base64.StdEncoding.EncodeToString(data)

	payload := types.StdinRequest{Data: encoded}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	msg := types.WSMessage{
		Type:    types.WSMessageStdin,
		Message: payloadBytes,
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

	wh.connected.Store(false)
	return nil
}

// pollStdin polls for stdin entries when WebSocket is disconnected
func (h *Host) pollStdin(ctx context.Context, sessionID string, writeFunc func([]byte) error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			mType := types.WSMessageStdin
			resp, err := h.client.get(fmt.Sprintf("/sessions/%s/%d/poll?client_id=%s", sessionID, mType, types.HostClientID))
			if err != nil {
				continue
			}

			var pollResp types.PollResponse
			if err := json.NewDecoder(resp.Body).Decode(&pollResp); err != nil {
				resp.Body.Close()
				continue
			}
			resp.Body.Close()

			if entries, ok := pollResp.Elements.([]interface{}); ok && len(entries) > 0 {
				for _, entry := range entries {
					if entryMap, ok := entry.(map[string]interface{}); ok {
						if dataStr, ok := entryMap["data"].(string); ok {
							data, err := base64.StdEncoding.DecodeString(dataStr)
							if err == nil && len(data) > 0 {
								if err := writeFunc(data); err != nil {
									wsHostCh.Log(alog.DEBUG, "[remote-control] stdin write error: %v", err)
								}
							}
						}
					}
				}

				_, err := h.client.post(fmt.Sprintf("/sessions/%s/%d/ack?client_id=%s", sessionID, mType, types.HostClientID), nil)
				if err != nil {
					wsHostCh.Log(alog.DEBUG, "[remote-control] poll ack error: %v", err)
				}
			}
		}
	}
}

// pollPendingClients polls for pending client notifications when WebSocket is disconnected
func (h *Host) pollPendingClients(ctx context.Context, sessionID string, rawMode bool) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Don't poll if websocket is connected
			if h.wsHost.IsConnected() {
				continue
			}
			mType := types.WSMessagePendingClient
			resp, err := h.client.get(fmt.Sprintf("/sessions/%s/%d/poll?client_id=%s", sessionID, mType, types.HostClientID))
			if err != nil {
				continue
			}

			var pollResp types.PollResponse
			if err := json.NewDecoder(resp.Body).Decode(&pollResp); err != nil {
				resp.Body.Close()
				continue
			}
			resp.Body.Close()

			if clients, ok := pollResp.Elements.([]interface{}); ok && len(clients) > 0 {
				for _, client := range clients {
					if clientIDStr, ok := client.(string); ok {
						h.handleClientApproval(ctx, sessionID, clientIDStr, rawMode)
					}
				}

				_, err := h.client.post(fmt.Sprintf("/sessions/%s/%d/ack?client_id=%s", sessionID, mType, types.HostClientID), nil)
				if err != nil {
					wsHostCh.Log(alog.DEBUG, "[remote-control] poll ack error: %v", err)
				}
			}
		}
	}
}
