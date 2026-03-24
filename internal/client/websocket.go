package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gorilla/websocket"
)

var wsCh = alog.UseChannel("WS_CLIENT")

// WebSocketConnection manages a WebSocket connection to the server
type WebSocketConnection struct {
	url       string
	tlsConfig *tls.Config
	conn      *websocket.Conn
	clientID  string
	sessionID string

	handler OutputHandler
	send    chan []byte
	done    chan struct{}
	stop    chan struct{}
	mu      sync.RWMutex

	connected bool
}

type OutputHandler func(chunk types.OutputChunk)

// NewWebSocketConnection creates a new WebSocket connection
func NewWebSocketConnection(url string, tlsConfig *tls.Config, clientID, sessionID string) *WebSocketConnection {
	return &WebSocketConnection{
		url:       url,
		tlsConfig: tlsConfig,
		clientID:  clientID,
		sessionID: sessionID,
		send:      make(chan []byte, 256),
		done:      make(chan struct{}),
		stop:      make(chan struct{}),
	}
}

// OnOutput registers a callback for output chunks
func (ws *WebSocketConnection) OnOutput(handler OutputHandler) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.handler = handler
}

// Connect establishes the WebSocket connection
func (ws *WebSocketConnection) Connect(ctx context.Context) error {
	ws.mu.Lock()
	if ws.connected {
		ws.mu.Unlock()
		return nil
	}

	if ws.done == nil {
		ws.done = make(chan struct{})
	}
	if ws.stop == nil {
		ws.stop = make(chan struct{})
	}
	ws.mu.Unlock()

	wsURL := ws.url + "/ws/" + ws.sessionID
	wsCh.Log(alog.DEBUG, "Dialing WebSocket at [%s]", wsURL)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("WebSocket dial failed: %w", err)
	}

	ws.mu.Lock()
	ws.conn = conn
	ws.connected = true
	ws.mu.Unlock()

	wsCh.Log(alog.DEBUG, "[remote-control] WebSocket connected to %s", ws.url)

	go ws.readPump(ctx)
	go ws.writePump(ctx)

	return nil
}

// readPump reads messages from the WebSocket
func (ws *WebSocketConnection) readPump(ctx context.Context) {
	defer func() {
		ws.handleDisconnect()
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

		wsCh.Log(alog.DEBUG4, "waiting for ws message")
		_, message, err := ws.conn.ReadMessage()
		wsCh.Log(alog.DEBUG4, "got ws message")
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				wsCh.Log(alog.DEBUG, "WebSocket read error: %v", err)
			}
			return
		}

		var msg types.WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			wsCh.Log(alog.DEBUG, "invalid JSON in WebSocket message: %v", err)
			return
		}

		ws.handleMessage(msg)
	}
}

// writePump writes messages to the WebSocket
func (ws *WebSocketConnection) writePump(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer func() {
		t.Stop()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ws.done:
			return
		case message, ok := <-ws.send:
			ws.mu.RLock()
			conn := ws.conn
			ws.mu.RUnlock()

			if conn == nil {
				return
			}

			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				wsCh.Log(alog.DEBUG, "[remote-control] WebSocket write error: %v", err)
				return
			}
		case <-t.C:
			ws.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// handleMessage processes incoming WebSocket messages
func (ws *WebSocketConnection) handleMessage(msg types.WSMessage) {
	switch msg.Type {
	case types.WSMessageOutput:
		wsCh.Log(alog.DEBUG4, "Received output chunks: %s", msg)
		// TODO --- handle this correctly!!!!!!!!!!!!
		var payload []types.OutputChunk
		if err := msg.UnmarshalMessage(&payload); err != nil {
			wsCh.Log(alog.DEBUG, "Invalid output chunks received")
		} else {
			for _, chunk := range payload {
				wsCh.Log(alog.DEBUG4, "Received output chunk: stream=%d, len=%d", chunk.Stream, len(chunk.Data))
				if ws.handler != nil {
					ws.handler(chunk)
				}
			}
		}
	}
}

// handleDisconnect handles connection loss
func (ws *WebSocketConnection) handleDisconnect() {
	ws.mu.Lock()
	ws.connected = false
	if ws.conn != nil {
		ws.conn.Close()
		ws.conn = nil
	}
	ws.mu.Unlock()

	wsCh.Log(alog.DEBUG, "WebSocket disconnected")
}

// Stop signals the connection to stop
func (ws *WebSocketConnection) Stop() {
	select {
	case <-ws.stop:
		return
	default:
		close(ws.stop)
	}
}

// Close closes the WebSocket connection
func (ws *WebSocketConnection) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()

	select {
	case <-ws.done:
		ws.done = make(chan struct{})
		ws.stop = make(chan struct{})
	default:
		close(ws.done)
	}

	select {
	case <-ws.stop:
	default:
		close(ws.stop)
	}

	if ws.conn != nil {
		ws.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		ws.conn.Close()
		ws.conn = nil
	}

	ws.connected = false
	return nil
}

// IsConnected returns whether the WebSocket is currently connected
func (ws *WebSocketConnection) IsConnected() bool {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.connected
}

// SendStdinSubmit submits host stdin data via WebSocket
func (ws *WebSocketConnection) SendStdinEntry(data []byte) error {
	payload := types.StdinEntry{Data: data}
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
	case ws.send <- msgData:
		return nil
	case <-ws.done:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("send buffer full")
	}
}
