package host

import (
	"context"
	"crypto/tls"
	"fmt"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// WebSocketClient manages the host's WebSocket connection with fallback to HTTP polling
type WebSocketClient struct {
	ws      *WebSocketHost
	client  *types.APIClient
	session string

	mode ConnectionMode
}

type ConnectionMode int

const (
	ModeWebSocket ConnectionMode = iota
	ModePolling
)

// NewWebSocketClient creates a WebSocketClient with HTTP fallback
func NewWebSocketClient(serverURL, sessionID string, client *types.APIClient) *WebSocketClient {
	return &WebSocketClient{
		client:  client,
		session: sessionID,
	}
}

// connectWebSocket attempts to establish a WebSocket connection
func (wc *WebSocketClient) connectWebSocket(ctx context.Context, clientID string, tlsCfg *tls.Config) error {
	wsURL := types.DeriveWebSocketURL(wc.client.BaseURL)

	wc.ws = NewWebSocketHost(wsURL, tlsCfg, wc.session, clientID)

	return wc.ws.Connect(ctx)
}

// Connect initializes the WebSocket connection
func (wc *WebSocketClient) Connect(ctx context.Context, clientID string, tlsCfg *tls.Config) error {
	if tlsCfg == nil {
		wc.mode = ModePolling
		ch.Log(alog.INFO, "[remote-control] No TLS config, using HTTP polling mode")
		return nil
	}

	if err := wc.connectWebSocket(ctx, clientID, tlsCfg); err != nil {
		ch.Log(alog.DEBUG, "[remote-control] WebSocket connect failed: %v, falling back to polling", err)
		wc.mode = ModePolling
		return nil
	}

	wc.mode = ModeWebSocket
	ch.Log(alog.INFO, "[remote-control] WebSocket mode active")
	return nil
}

// Close shuts down the WebSocket connection
func (wc *WebSocketClient) Close() {
	if wc.ws != nil {
		wc.ws.Close()
	}
}

// IsWebSocket returns true if WebSocket is active
func (wc *WebSocketClient) IsWebSocket() bool {
	return wc.mode == ModeWebSocket
}

// types.APIClient returns the underlying HTTP client (used for polling fallback)
func (wc *WebSocketClient) APIClient() *types.APIClient {
	return wc.client
}

// WebSocketHost returns the WebSocketHost (may be nil if in polling mode)
func (wc *WebSocketClient) WebSocketHost() *WebSocketHost {
	return wc.ws
}

// SendOutput sends output data via WebSocket or HTTP fallback
func (wc *WebSocketClient) SendOutput(stream types.Stream, data []byte, offset int64) error {
	if wc.mode == ModeWebSocket && wc.ws != nil {
		return wc.ws.SendOutput(stream, data)
	}
	// Fallback to HTTP
	if wc.client != nil {
		return wc.client.AppendOutput(wc.session, stream, data)
	}
	return fmt.Errorf("no connection available")
}
