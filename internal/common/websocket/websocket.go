package ws

import (
	"net/url"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

var ch = alog.UseChannel("WEBSOCKET")

// deriveWebSocketURL converts http(s):// URLs to ws(s):// URLs
// It strips any existing path and query parameters since the WebSocket path is constructed separately
func DeriveWebSocketURL(httpURL string) string {
	parsed, err := url.Parse(httpURL)
	if err != nil {
		return httpURL
	}

	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		// Return as-is if already ws/wss or unknown
		return httpURL
	}

	// Reset path and query - the caller will add /ws/{sessionID}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String()
}

/* -- WebSocketPipe --------------------------------------------------------- */

// TODO: Implement common WebSocketPipe shared between components

// type WebSocketPipe struct {
// 	url       string
// 	tlsConfig *tls.Config

// 	reconnectAttempts int
// 	reconnectDelay    time.Duration
// 	maxReconnectDelay time.Duration

// 	send chan []byte
// 	done chan struct{}
// 	mu   sync.RWMutex

// 	connected atomic.Bool
// }

// // NewWebSocketHost creates a new WebSocketHost instance
// func NewWebSocketPipe(url string, cfg *config.Config, tlsConfig *tls.Config) *WebSocketPipe {
// 	return &WebSocketPipe{
// 		url:               url,
// 		tlsConfig:         tlsConfig,
// 		reconnectDelay:    time.Duration(cfg.WSReconnectDelay) * time.Second,
// 		maxReconnectDelay: time.Duration(cfg.WSMaxReconnectDelay) * time.Second,
// 		send:              make(chan []byte, 256),
// 		done:              make(chan struct{}),
// 	}
// }

// // IsConnected returns whether the WebSocket is currently connected
// func (wp *WebSocketPipe) IsConnected() bool {
// 	return wp.connected.Load()
// }

// Connect establishes the WebSocket connection
// func (wp *WebSocketPipe) Connect(ctx context.Context) error {
// 	wp.mu.Lock()
// 	if wp.connected.Load() {
// 		wp.mu.Unlock()
// 		return nil
// 	}
// 	wp.mu.Unlock()

// 	dialer := websocket.Dialer{
// 		HandshakeTimeout: 10 * time.Second,
// 	}
// 	if wp.tlsConfig != nil {
// 		dialer.TLSClientConfig = wp.tlsConfig
// 	}

// 	wsURL := wp.url + "/ws/" + wh.sessionID
// 	if wh.clientID != "" {
// 		wsURL += "?client_id=" + wh.clientID
// 	}
// 	conn, _, err := dialer.Dial(wsURL, nil)
// 	if err != nil {
// 		return fmt.Errorf("WebSocket dial failed: %w", err)
// 	}

// 	wh.mu.Lock()
// 	wh.conn = conn
// 	wh.connected.Store(true)
// 	wh.reconnectAttempts = 0
// 	wh.mu.Unlock()

// 	wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket connected to %s", wh.url)

// 	go wh.readPump(ctx)
// 	go wh.writePump(ctx)

// 	return nil
// }
