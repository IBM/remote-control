package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	ws "github.com/gabe-l-hart/remote-control/internal/common/websocket"
)

var wsCh = alog.UseChannel("WS_CLIENT")

// WebSocketConnection manages a WebSocket connection to the server
type WebSocketConnection struct {
	pipe *ws.WebSocketPipe

	url       string
	tlsConfig *tls.Config
	clientID  string
	sessionID string
	wsConfig  *ws.WebSocketConfig

	handler OutputHandler
	mu      sync.RWMutex
}

type OutputHandler func(chunk types.OutputChunk)

// NewWebSocketConnection creates a new WebSocket connection
func NewWebSocketConnection(url string, tlsConfig *tls.Config, clientID, sessionID string, wsConfig *ws.WebSocketConfig) *WebSocketConnection {
	return &WebSocketConnection{
		url:       url,
		tlsConfig: tlsConfig,
		clientID:  clientID,
		sessionID: sessionID,
		wsConfig:  wsConfig,
	}
}

// OnOutput registers a callback for output chunks
func (c *WebSocketConnection) OnOutput(handler OutputHandler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handler = handler
}

// Connect establishes the WebSocket connection
func (c *WebSocketConnection) Connect(ctx context.Context) error {
	if c.pipe != nil && c.pipe.IsConnected() {
		return nil
	}

	wsURL := c.url + "/ws/" + c.sessionID
	wsCh.Log(alog.DEBUG, "Dialing WebSocket at [%s]", wsURL)

	pipe, err := ws.Dial(ctx, wsURL, c.tlsConfig, c.wsConfig)
	if err != nil {
		return err
	}

	c.pipe = pipe
	pipe.OnMessage(c.handleMessage)
	pipe.OnDisconnect(func() {
		wsCh.Log(alog.DEBUG, "WebSocket disconnected")
	})
	pipe.Start(ctx)

	wsCh.Log(alog.DEBUG, "[remote-control] WebSocket connected to %s", c.url)
	return nil
}

// handleMessage processes incoming WebSocket messages
func (c *WebSocketConnection) handleMessage(msg types.WSMessage) {
	switch msg.Type {
	case types.WSMessageOutput:
		wsCh.Log(alog.DEBUG4, "Received output chunks: %s", msg)
		var payload []types.OutputChunk
		if err := msg.UnmarshalMessage(&payload); err != nil {
			wsCh.Log(alog.DEBUG, "Invalid output chunks received")
		} else {
			c.mu.RLock()
			handler := c.handler
			c.mu.RUnlock()
			for _, chunk := range payload {
				wsCh.Log(alog.DEBUG4, "Received output chunk: stream=%d, len=%d", chunk.Stream, len(chunk.Data))
				if handler != nil {
					handler(chunk)
				}
			}
		}
	}
}

// IsConnected returns whether the WebSocket is currently connected
func (c *WebSocketConnection) IsConnected() bool {
	if c.pipe == nil {
		return false
	}
	return c.pipe.IsConnected()
}

// SendStdinEntry submits host stdin data via WebSocket
func (c *WebSocketConnection) SendStdinEntry(data []byte) error {
	if c.pipe == nil {
		return fmt.Errorf("not connected")
	}

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

	return c.pipe.Send(msgData)
}

// Close closes the WebSocket connection
func (c *WebSocketConnection) Close() error {
	if c.pipe == nil {
		return nil
	}
	return c.pipe.Close()
}

// Stop signals the connection to stop (alias for Close)
func (c *WebSocketConnection) Stop() {
	c.Close()
}
