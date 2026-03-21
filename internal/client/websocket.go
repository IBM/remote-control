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
	url         string
	tlsConfig   *tls.Config
	conn        *websocket.Conn
	clientID    string
	sessionID   string
	outputBuf   []types.OutputChunk
	outputBufMu sync.Mutex

	handler      OutputHandler
	stdinHandler func(StdinEntry)
	done         chan struct{}
	stop         chan struct{}
	mu           sync.RWMutex

	nextPollStdout int64
	nextPollStderr int64

	connected bool
}

type OutputHandler func(chunk types.OutputChunk)

// NewWebSocketConnection creates a new WebSocket connection
func NewWebSocketConnection(url string, tlsConfig *tls.Config, clientID, sessionID string) *WebSocketConnection {
	return &WebSocketConnection{
		url:            url,
		tlsConfig:      tlsConfig,
		clientID:       clientID,
		sessionID:      sessionID,
		done:           make(chan struct{}),
		stop:           make(chan struct{}),
		outputBuf:      make([]types.OutputChunk, 0),
		nextPollStdout: 0,
		nextPollStderr: 0,
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

	wsCh.Log(alog.DEBUG, "Dialing WebSocket at [%s]", ws.url)
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, ws.url, nil)
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

		_, message, err := ws.conn.ReadMessage()
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
		var payload types.OutputChunk
		if err := msg.UnmarshalMessage(&payload); err == nil {
			wsCh.Log(alog.DEBUG4, "Received output chunk: stream=%d, len=%d", payload.Stream, len(payload.Data))
			if ws.handler != nil {
				ws.handler(payload)
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

// OnStdinPending registers a callback for pending stdin
func (ws *WebSocketConnection) OnStdinPending(handler func(StdinEntry)) {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.stdinHandler = handler
}

// StdinEntry represents a stdin entry (for callbacks)
type StdinEntry struct {
	ID     string
	Source string
	Data   string
	Status string
}

// IsConnected returns whether the WebSocket is currently connected
func (ws *WebSocketConnection) IsConnected() bool {
	ws.mu.RLock()
	defer ws.mu.RUnlock()
	return ws.connected
}

// AddOutput buffers output chunks to be consumed via polling
func (ws *WebSocketConnection) AddOutput(chunk types.OutputChunk) {
	ws.outputBufMu.Lock()
	defer ws.outputBufMu.Unlock()
	ws.outputBuf = append(ws.outputBuf, chunk)
}

// PopOutputs returns all buffered output chunks and advances the poll offset
func (ws *WebSocketConnection) PopOutputs(stdoutOffset, stderrOffset int64) ([]types.OutputChunk, int64, int64) {
	ws.outputBufMu.Lock()
	defer ws.outputBufMu.Unlock()

	result := make([]types.OutputChunk, 0)
	newStdoutOffset := stdoutOffset
	newStderrOffset := stderrOffset

	for _, chunk := range ws.outputBuf {
		if chunk.Stream == types.StreamStdout {
			if int64(len(chunk.Data)) <= stdoutOffset {
				continue
			}
		}

		result = append(result, chunk)

		// Update offsets
		if chunk.Stream == types.StreamStdout {
			newStdoutOffset += int64(len(chunk.Data))
		} else if chunk.Stream == types.StreamStderr {
			newStderrOffset += int64(len(chunk.Data))
		}
	}

	ws.outputBuf = ws.outputBuf[:0]
	return result, newStdoutOffset, newStderrOffset
}
