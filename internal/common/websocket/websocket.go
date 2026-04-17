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
	"github.com/gabe-l-hart/remote-control/internal/common/types"
	"github.com/gorilla/websocket"
)

var ch = alog.UseChannel("WEBSOCKET")

// WebSocketConfig holds configuration for WebSocket reconnection.
type WebSocketConfig struct {
	ReconnectInterval time.Duration
	ReconnectTimeout  time.Duration
	MaxQueueLength    int
}

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

	reconnectURL      string
	tlsConfig         *tls.Config
	messageQueue      [][]byte
	queueMu           sync.Mutex
	maxQueueLength    int
	reconnectInterval time.Duration
	reconnectTimeout  time.Duration
	reconnectCancel   context.CancelFunc
	reconnecting      atomic.Bool
	startCtx          context.Context
	connectionGen     atomic.Uint32 // Tracks connection generation to detect stale pumps
	closeSignal       chan struct{} // Signal to writePump to send close frame
}

// NewPipe creates a WebSocketPipe from an existing connection with its own
// send and done channels.
func NewPipe(conn *websocket.Conn) *WebSocketPipe {
	return &WebSocketPipe{
		conn:        conn,
		send:        make(chan []byte, 256),
		done:        make(chan struct{}),
		closeSignal: make(chan struct{}),
	}
}

// NewPipeWithChannels creates a WebSocketPipe using externally-managed
// channels. This is useful when the channels are owned by another structure
// (e.g. a server session client).
func NewPipeWithChannels(conn *websocket.Conn, send chan []byte, done chan struct{}) *WebSocketPipe {
	return &WebSocketPipe{
		conn:        conn,
		send:        send,
		done:        done,
		closeSignal: make(chan struct{}),
	}
}

// queueMessage adds a message to the queue, dropping oldest if at capacity.
func (p *WebSocketPipe) queueMessage(data []byte) {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()

	// Only queue if maxQueueLength is set and queue is not empty
	if p.maxQueueLength > 0 && len(p.messageQueue) >= p.maxQueueLength {
		ch.Log(alog.DEBUG, "Message queue full, dropping oldest message")
		p.messageQueue = p.messageQueue[1:]
	}

	p.messageQueue = append(p.messageQueue, data)
	ch.Log(alog.DEBUG3, "Queued message, queue length: %d", len(p.messageQueue))
}

// Dial creates a new outbound WebSocket connection and returns a started pipe.
func Dial(ctx context.Context, wsURL string, tlsConfig *tls.Config, config *WebSocketConfig) (*WebSocketPipe, error) {
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

	p.reconnectURL = wsURL
	p.tlsConfig = tlsConfig
	p.messageQueue = make([][]byte, 0, config.MaxQueueLength)

	if config.ReconnectInterval > 0 {
		p.reconnectInterval = config.ReconnectInterval
	} else {
		p.reconnectInterval = 5 * time.Second
	}

	if config.ReconnectTimeout > 0 {
		p.reconnectTimeout = config.ReconnectTimeout
	} else {
		p.reconnectTimeout = 10 * time.Second
	}

	if config.MaxQueueLength > 0 {
		p.maxQueueLength = config.MaxQueueLength
	} else {
		p.maxQueueLength = 100
	}

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
	p.startCtx = ctx

	// Initialize connection generation to 1 for initial connection
	initialGen := p.connectionGen.Load()
	if initialGen == 0 {
		initialGen = p.connectionGen.Add(1)
	}

	go p.readPump(ctx, initialGen)
	go p.writePump(ctx, initialGen)
}

// IsConnected returns whether the pipe is currently connected.
func (p *WebSocketPipe) IsConnected() bool {
	return p.connected.Load()
}

// Send queues raw bytes on the send channel.
func (p *WebSocketPipe) Send(data []byte) error {
	select {
	case <-p.done:
		p.queueMessage(data)
		return fmt.Errorf("connection closed, message queued")
	default:
		select {
		case p.send <- data:
			return nil
		default:
			p.queueMessage(data)
			return fmt.Errorf("send buffer full, message queued")
		}
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

// startReconnectLoop initiates the reconnection process (idempotent).
func (p *WebSocketPipe) startReconnectLoop() {
	if !p.reconnecting.CompareAndSwap(false, true) {
		ch.Log(alog.DEBUG, "Reconnection loop already running")
		return
	}

	ch.Log(alog.DEBUG, "Starting reconnection loop")

	// Ensure we have a valid reconnect interval
	interval := p.reconnectInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}

	ctx, cancel := context.WithCancel(p.startCtx)
	p.reconnectCancel = cancel

	go func() {
		defer func() {
			p.reconnecting.Store(false)
			ch.Log(alog.DEBUG, "Reconnection loop exited")
		}()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := p.attemptReconnect(); err != nil {
					ch.Log(alog.DEBUG2, "Reconnection attempt failed: %v", err)
				} else {
					ch.Log(alog.DEBUG, "Reconnection successful")
					p.flushQueue()
					return
				}
			}
		}
	}()
}

// Close gracefully shuts down the pipe.
func (p *WebSocketPipe) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.reconnectCancel != nil {
		p.reconnectCancel()
	}

	select {
	case <-p.done:
		return nil
	default:
		close(p.done)
	}

	// Signal writePump to send close frame by closing closeSignal channel
	// Only close if not already closed
	select {
	case <-p.closeSignal:
		// Already closed
	default:
		close(p.closeSignal)
	}

	// Don't write directly to conn - let writePump handle it
	// The connection will be closed when writePump exits

	p.connected.Store(false)
	return nil
}

/* -- Pumps ----------------------------------------------------------------- */

// readPump reads messages from the WebSocket.
// gen is the connection generation that this pump must match to remain active.
func (p *WebSocketPipe) readPump(ctx context.Context, gen uint32) {
	defer p.handleDisconnect()

	p.mu.RLock()
	conn := p.conn
	p.mu.RUnlock()

	// Verify we're still operating on the correct connection generation
	if p.connectionGen.Load() != gen {
		ch.Log(alog.DEBUG, "readPump exiting due to generation change")
		return
	}

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

		// Check generation again before processing
		if p.connectionGen.Load() != gen {
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
// gen is the connection generation that this pump must match to remain active.
func (p *WebSocketPipe) writePump(ctx context.Context, gen uint32) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-p.done:
			return
		case <-p.closeSignal:
			// Close signal received, send close frame
			p.mu.RLock()
			conn := p.conn
			p.mu.RUnlock()
			if conn != nil {
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				conn.Close()
			}
			return
		case message, ok := <-p.send:
			p.mu.RLock()
			conn := p.conn
			p.mu.RUnlock()

			// Verify we're still operating on the correct connection generation
			if p.connectionGen.Load() != gen {
				ch.Log(alog.DEBUG, "writePump exiting due to generation change")
				return
			}

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

			// Check generation before sending ping
			if p.connectionGen.Load() != gen {
				return
			}

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

	ch.Log(alog.DEBUG, "WebSocket disconnected")

	if p.reconnectURL != "" {
		p.startReconnectLoop()
	}

	if onDisconnect != nil {
		onDisconnect()
	}
}

// attemptReconnect tries to re-establish the WebSocket connection.
func (p *WebSocketPipe) attemptReconnect() error {
	ctx, cancel := context.WithTimeout(p.startCtx, p.reconnectTimeout)
	defer cancel()

	ch.Log(alog.DEBUG, "Attempting to reconnect to %s", p.reconnectURL)

	dialer := websocket.Dialer{
		HandshakeTimeout: p.reconnectTimeout,
	}
	if p.tlsConfig != nil {
		dialer.TLSClientConfig = p.tlsConfig
	}

	conn, _, err := dialer.DialContext(ctx, p.reconnectURL, nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}

	// Increment generation counter for the new connection
	newGen := p.connectionGen.Add(1)

	p.mu.Lock()
	p.conn = conn
	p.connected.Store(true)
	p.mu.Unlock()

	// Start pumps with the new connection, passing the current generation
	go p.readPump(p.startCtx, newGen)
	go p.writePump(p.startCtx, newGen)

	return nil
}

// flushQueue sends all queued messages to the send channel.
func (p *WebSocketPipe) flushQueue() {
	p.queueMu.Lock()
	queueLen := len(p.messageQueue)
	p.queueMu.Unlock()

	if queueLen == 0 {
		return
	}

	ch.Log(alog.DEBUG2, "Flushing %d queued messages", queueLen)

	p.queueMu.Lock()
	defer p.queueMu.Unlock()

	for _, msg := range p.messageQueue {
		select {
		case <-p.startCtx.Done():
			ch.Log(alog.DEBUG, "Context canceled during queue flush")
			return
		case p.send <- msg:
		}
	}

	p.messageQueue = p.messageQueue[:0]
	ch.Log(alog.DEBUG2, "Queue flush complete")
}
