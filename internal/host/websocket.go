package host

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

var wsHostCh = alog.UseChannel("WS_HOST")

// WebSocketHost manages the host's WebSocket connection to the server
type WebSocketHost struct {
	pipe *ws.WebSocketPipe

	url       string
	tlsConfig *tls.Config
	sessionID string
	clientID  string

	onStdinHandler         func(types.StdinEntry)
	onPendingClientHandler func(string)

	mu sync.RWMutex
}

// NewWebSocketHost creates a new WebSocketHost instance
func NewWebSocketHost(url string, tlsConfig *tls.Config, sessionID, clientID string) *WebSocketHost {
	return &WebSocketHost{
		url:       url,
		tlsConfig: tlsConfig,
		sessionID: sessionID,
		clientID:  clientID,
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

// IsConnected returns whether the WebSocket is currently connected
func (wh *WebSocketHost) IsConnected() bool {
	if wh.pipe == nil {
		return false
	}
	return wh.pipe.IsConnected()
}

// Connect establishes the WebSocket connection
func (wh *WebSocketHost) Connect(ctx context.Context) error {
	if wh.pipe != nil && wh.pipe.IsConnected() {
		return nil
	}

	wsURL := wh.url + "/ws/" + wh.sessionID
	if wh.clientID != "" {
		wsURL += "?client_id=" + wh.clientID
	}

	pipe, err := ws.Dial(ctx, wsURL, wh.tlsConfig)
	if err != nil {
		return err
	}

	wh.pipe = pipe
	pipe.OnMessage(wh.handleMessage)
	pipe.OnDisconnect(func() {
		wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket disconnected")
	})
	pipe.Start(ctx)

	wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket connected to %s", wh.url)
	return nil
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
			var entries []types.StdinEntry
			if err := msg.UnmarshalMessage(&entries); err == nil {
				for _, entry := range entries {
					onStdin(entry)
				}
			}
		}

	case types.WSMessagePendingClient:
		if onPendingClient != nil {
			var clientIDs []string
			if err := msg.UnmarshalMessage(&clientIDs); err == nil {
				for _, clientID := range clientIDs {
					onPendingClient(clientID)
				}
			}
		}

	case types.WSMessageError:
		var errors []types.ErrorResponse
		if err := msg.UnmarshalMessage(&errors); err == nil {
			for _, e := range errors {
				wsHostCh.Log(alog.DEBUG, "[remote-control] server error: %s", e.Error)
			}
		}
	}
}

// SendOutput sends output data via WebSocket
func (wh *WebSocketHost) SendOutput(stream types.Stream, data []byte) error {
	if wh.pipe == nil {
		return fmt.Errorf("not connected")
	}

	wsHostCh.Log(alog.DEBUG4, "sending output on stream %d: %v", stream, data)
	payload := types.OutputChunk{
		Stream: stream,
		Data:   data,
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	msg := types.WSMessage{
		Type:    types.WSMessageOutput,
		Message: payloadBytes,
	}

	msgData, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	wsHostCh.Log(alog.DEBUG4, "full json: %s", msgData)

	return wh.pipe.Send(msgData)
}

// Close closes the WebSocket connection
func (wh *WebSocketHost) Close() error {
	if wh.pipe == nil {
		return nil
	}
	return wh.pipe.Close()
}
