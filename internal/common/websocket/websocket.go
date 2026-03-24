package ws

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gorilla/websocket"
)

var ch = alog.UseChannel("WEBSOCKET")

// DeriveWebSocketURL converts http(s):// URLs to ws(s):// URLs.
// It strips any existing path and query parameters since the WebSocket path
// is constructed separately.
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
		return httpURL
	}

	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String()
}

/* -- WebSocketPipe --------------------------------------------------------- */

// MessageHandler processes an incoming WSMessage.
type MessageHandler func(msg types.WSMessage)

// DisconnectHandler is called when the connection drops.
type DisconnectHandler func()

// WebSocketPipe manages the read/write pumps over a single WebSocket
// connection. It is used by the host, client, and server to avoid duplicating
// pump logic.
type WebSocketPipe struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	mu   sync.RWMutex

	connected atomic.Bool

	onMessage    MessageHandler
	onDisconnect DisconnectHandler
}

// NewPipe creates a WebSocketPipe from an existing connection with its own
// send and done channels.
func NewPipe(conn *websocket.Conn) *WebSocketPipe {
	return &WebSocketPipe{
		conn: conn,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}
}

// NewPipeWithChannels creates a WebSocketPipe using externally-managed
// channels. This is useful when the channels are owned by another structure
// (e.g. a server session client).
func NewPipeWithChannels(conn *websocket.Conn, send chan []byte, done chan struct{}) *WebSocketPipe {
	return &WebSocketPipe{
		conn: conn,
		send: send,
		done: done,
	}
}

// Dial creates a new outbound WebSocket connection and returns a started pipe.
func Dial(ctx context.Context, wsURL string, tlsConfig *tls.Config) (*WebSocketPipe, error) {
	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	if tlsConfig != nil {
		dialer.TLSClientConfig = tlsConfig
	}

	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("WebSocket dial failed: %w", err)
	}

	p := NewPipe(conn)
	p.connected.Store(true)
	return p, nil
}

// OnMessage sets the handler called for every incoming WSMessage.
func (p *WebSocketPipe) OnMessage(h MessageHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onMessage = h
}

// OnDisconnect sets the handler called when the connection drops.
func (p *WebSocketPipe) OnDisconnect(h DisconnectHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onDisconnect = h
}

// Start launches the read and write pump goroutines.
func (p *WebSocketPipe) Start(ctx context.Context) {
	p.connected.Store(true)
	go p.readPump(ctx)
	go p.writePump(ctx)
}

// IsConnected returns whether the pipe is currently connected.
func (p *WebSocketPipe) IsConnected() bool {
	return p.connected.Load()
}

// Send queues raw bytes on the send channel.
func (p *WebSocketPipe) Send(data []byte) error {
	select {
	case p.send <- data:
		return nil
	case <-p.done:
		return fmt.Errorf("connection closed")
	default:
		return fmt.Errorf("send buffer full")
	}
}

// SendMessage marshals a typed WSMessage and queues it for sending.
func (p *WebSocketPipe) SendMessage(mType types.WSMessageType, payload any) error {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	msg := types.WSMessage{
		Type:    mType,
		Message: payloadBytes,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return p.Send(data)
}

// SendChan returns the send channel for external integration.
func (p *WebSocketPipe) SendChan() chan []byte {
	return p.send
}

// DoneChan returns the done channel for external integration.
func (p *WebSocketPipe) DoneChan() chan struct{} {
	return p.done
}

// Close gracefully shuts down the pipe.
func (p *WebSocketPipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	select {
	case <-p.done:
		return nil
	default:
		close(p.done)
	}

	if p.conn != nil {
		p.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		p.conn.Close()
		p.conn = nil
	}

	p.connected.Store(false)
	return nil
}

/* -- Pumps ----------------------------------------------------------------- */

// readPump reads messages from the WebSocket.
func (p *WebSocketPipe) readPump(ctx context.Context) {
	defer p.handleDisconnect()

	p.mu.RLock()
	conn := p.conn
	p.mu.RUnlock()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		ch.Log(alog.DEBUG3, "received websocket message: %v", message)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				ch.Log(alog.DEBUG, "WebSocket read error: %v", err)
			}
			return
		}

		var msg types.WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			ch.Log(alog.DEBUG, "invalid JSON in WebSocket message: %v", err)
			continue
		}

		p.mu.RLock()
		handler := p.onMessage
		p.mu.RUnlock()
		if handler != nil {
			handler(msg)
		}
	}
}

// writePump writes messages to the WebSocket.
func (p *WebSocketPipe) writePump(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case message, ok := <-p.send:
			p.mu.RLock()
			conn := p.conn
			p.mu.RUnlock()

			if conn == nil {
				return
			}

			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			ch.Log(alog.DEBUG4, "Sending websocket message: %s", message)
			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				ch.Log(alog.DEBUG, "WebSocket write error: %v", err)
				return
			}

		case <-ticker.C:
			p.mu.RLock()
			conn := p.conn
			p.mu.RUnlock()

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

// handleDisconnect cleans up on connection loss.
func (p *WebSocketPipe) handleDisconnect() {
	p.mu.Lock()
	p.connected.Store(false)
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
	onDisconnect := p.onDisconnect
	p.mu.Unlock()

	if onDisconnect != nil {
		onDisconnect()
	}
}
