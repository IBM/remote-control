package host

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// WebSocketClient manages the host's WebSocket connection with fallback to HTTP polling
type WebSocketClient struct {
	ws      *WebSocketHost
	client  *APIClient
	session string

	mode ConnectionMode
}

type ConnectionMode int

const (
	ModeWebSocket ConnectionMode = iota
	ModePolling
)

// NewWebSocketClient creates a WebSocketClient with HTTP fallback
func NewWebSocketClient(serverURL, sessionID string, client *APIClient) *WebSocketClient {
	return &WebSocketClient{
		client:  client,
		session: sessionID,
	}
}

// connectWebSocket attempts to establish a WebSocket connection
func (wc *WebSocketClient) connectWebSocket(ctx context.Context, clientID string, tlsCfg *tls.Config) error {
	wsURL := deriveWebSocketURL(wc.client.baseURL)

	wc.ws = NewWebSocketHost(wsURL, tlsCfg, wc.session, clientID)

	return wc.ws.Connect(ctx)
}

// deriveWebSocketURL converts http(s):// URLs to ws(s):// URLs
func deriveWebSocketURL(httpURL string) string {
	parsed, err := url.Parse(httpURL)
	if err != nil {
		return httpURL
	}

	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else if parsed.Scheme == "http" {
		parsed.Scheme = "ws"
	}

	return parsed.String()
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

// APIClient returns the underlying HTTP client (used for polling fallback)
func (wc *WebSocketClient) APIClient() *APIClient {
	return wc.client
}

// WebSocketHost returns the WebSocketHost (may be nil if in polling mode)
func (wc *WebSocketClient) WebSocketHost() *WebSocketHost {
	return wc.ws
}

// SendOutput sends output data via WebSocket or HTTP fallback
func (wc *WebSocketClient) SendOutput(stream types.Stream, data []byte, offset int64, timestamp time.Time) error {
	if wc.mode == ModeWebSocket && wc.ws != nil {
		return wc.ws.SendOutput(stream, data, offset, timestamp)
	}
	// Fallback to HTTP
	if wc.client != nil {
		return wc.client.AppendOutput(wc.session, stream, data, timestamp)
	}
	return fmt.Errorf("no connection available")
}
