package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gorilla/websocket"

	"github.com/gabe-l-hart/remote-control/internal/session"
)

var wsCh = alog.UseChannel("WEBSOC")

/* -- WSConnection ---------------------------------------------------------- */

type WSConnection struct {
	clientID string
	conn     *websocket.Conn
	send     chan []byte
	sessions map[string]bool // subscribed sessions
	lastPing time.Time
	mu       sync.RWMutex
	done     chan struct{}
	handlers map[string]HandlerFunc
}

func (w *WSConnection) RegisterHandler(messageType string, handler HandlerFunc) {
	w.handlers[messageType] = handler
}

// Send queues a message to be sent to the client
func (w *WSConnection) Send(messageType string, message interface{}) error {

	data, err := json.Marshal(message)
	if err != nil {
		return nil
	}

	select {
	case w.send <- data:
		return nil
	case <-w.done:
		return fmt.Errorf("Connection closed, unable to send")
	default:
		// Channel full, drop message
		return fmt.Errorf("Connection full, unable to send")
	}
}

// Poll for WS is a no-op
func (w *WSConnection) Poll() []interface{} {
	return make([]interface{}, 0)
}

// Close closes the connection
func (w *WSConnection) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	select {
	case <-w.done:
		// Already closed
		return
	default:
		close(w.done)
		close(w.send)
		w.conn.Close()
	}
}

////////////////////////////////////////////////////////////////////////////////

var connMgrCh = alog.UseChannel("CONNM")

// WSMessage is the top-level WebSocket message format
type WSMessage struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	ClientID  string          `json:"client_id,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// OutputChunkPayload is the payload for output_chunk messages
type OutputChunkPayload struct {
	Stream    string `json:"stream"`
	Data      string `json:"data"` // base64-encoded
	Offset    int64  `json:"offset"`
	Timestamp string `json:"timestamp"` // RFC3339Nano
}

// StdinPayload is the payload for stdin-related messages
type StdinPayload struct {
	ID     uint64 `json:"id,omitempty"`
	Data   string `json:"data,omitempty"` // base64-encoded
	Source string `json:"source,omitempty"`
}

// SubscribePayload is the payload for subscribe messages
type SubscribePayload struct {
	SessionID string `json:"session_id"`
	ClientID  string `json:"client_id"`
}

// ErrorPayload is the payload for error messages
type ErrorPayload struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// Connection represents a single WebSocket connection
type Connection_OLD struct {
	clientID string
	conn     *websocket.Conn
	send     chan []byte
	sessions map[string]bool // subscribed sessions
	lastPing time.Time
	mu       sync.RWMutex
	done     chan struct{}
}

// NewConnection creates a new Connection
func NewConnection(clientID string, conn *websocket.Conn) *Connection_OLD {
	return &Connection_OLD{
		clientID: clientID,
		conn:     conn,
		send:     make(chan []byte, 256),
		sessions: make(map[string]bool),
		lastPing: time.Now(),
		done:     make(chan struct{}),
	}
}

// Subscribe adds a session to this connection's subscription list
func (c *Connection_OLD) Subscribe(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessions[sessionID] = true
}

// Unsubscribe removes a session from this connection's subscription list
func (c *Connection_OLD) Unsubscribe(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.sessions, sessionID)
}

// IsSubscribed checks if this connection is subscribed to a session
func (c *Connection_OLD) IsSubscribed(sessionID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessions[sessionID]
}

// UpdatePing updates the last ping timestamp
func (c *Connection_OLD) UpdatePing() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastPing = time.Now()
}

// LastPing returns the last ping timestamp
func (c *Connection_OLD) LastPing() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastPing
}

// Send queues a message to be sent to the client
func (c *Connection_OLD) Send(message []byte) bool {
	select {
	case c.send <- message:
		return true
	case <-c.done:
		return false
	default:
		// Channel full, drop message
		return false
	}
}

// Close closes the connection
func (c *Connection_OLD) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		// Already closed
		return
	default:
		close(c.done)
		close(c.send)
		c.conn.Close()
	}
}

// ConnectionManager manages all active WebSocket connections
type ConnectionManager_OLD struct {
	mu          sync.RWMutex
	connections map[string]*Connection_OLD            // clientID -> Connection
	sessions    map[string]map[string]*Connection_OLD // sessionID -> clientID -> Connection
	store       session.Store
	upgrader    websocket.Upgrader
}

// NewConnectionManager creates a new ConnectionManager
func NewConnectionManager_OLD(store session.Store) *ConnectionManager_OLD {
	return &ConnectionManager_OLD{
		connections: make(map[string]*Connection_OLD),
		sessions:    make(map[string]map[string]*Connection_OLD),
		store:       store,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				// Allow all origins since we use mTLS for auth
				return true
			},
		},
	}
}

// Register adds a new WebSocket connection
func (cm *ConnectionManager_OLD) Register(clientID string, conn *websocket.Conn) *Connection_OLD {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Close existing connection if any
	if existing, ok := cm.connections[clientID]; ok {
		existing.Close()
	}

	connection := NewConnection(clientID, conn)
	connMgrCh.Log(alog.DEBUG2, "Registering connection for client [%s]", clientID)
	cm.connections[clientID] = connection
	return connection
}

// Unregister removes a WebSocket connection
func (cm *ConnectionManager_OLD) Unregister(clientID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	connection, ok := cm.connections[clientID]
	if !ok {
		return
	}

	// Remove from all session subscriptions
	for sessionID := range connection.sessions {
		if clients, ok := cm.sessions[sessionID]; ok {
			delete(clients, clientID)
			if len(clients) == 0 {
				delete(cm.sessions, sessionID)
			}
		}
	}

	connection.Close()
	delete(cm.connections, clientID)
}

// Subscribe adds a connection to a session's subscriber list
func (cm *ConnectionManager_OLD) Subscribe(clientID, sessionID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	connection, ok := cm.connections[clientID]
	if !ok {
		return errNotFound(clientID)
	}

	// Verify session exists
	if _, err := cm.store.Get(sessionID); err != nil {
		return err
	}

	connection.Subscribe(sessionID)

	if _, ok := cm.sessions[sessionID]; !ok {
		cm.sessions[sessionID] = make(map[string]*Connection_OLD)
	}
	cm.sessions[sessionID][clientID] = connection

	return nil
}

// Unsubscribe removes a connection from a session's subscriber list
func (cm *ConnectionManager_OLD) Unsubscribe(clientID, sessionID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	connection, ok := cm.connections[clientID]
	if !ok {
		return
	}

	connection.Unsubscribe(sessionID)

	if clients, ok := cm.sessions[sessionID]; ok {
		delete(clients, clientID)
		if len(clients) == 0 {
			delete(cm.sessions, sessionID)
		}
	}
}

// Broadcast sends a message to all subscribers of a session
func (cm *ConnectionManager_OLD) Broadcast(sessionID string, msg WSMessage) {
	cm.mu.RLock()
	clients, ok := cm.sessions[sessionID]
	cm.mu.RUnlock()

	if !ok {
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return
	}

	for clientConnID, conn := range clients {
		wsHandlerCh.Log(alog.DEBUG3, "Broadcasting to client connection %s", clientConnID)
		conn.Send(data)
	}
}

// SendToClient sends a message to a specific client
func (cm *ConnectionManager_OLD) SendToClient(clientID string, msg WSMessage) error {
	cm.mu.RLock()
	conn, ok := cm.connections[clientID]
	cm.mu.RUnlock()

	if !ok {
		return errNotFound(clientID)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	if !conn.Send(data) {
		return errConnectionClosed(clientID)
	}

	return nil
}

// Heartbeat checks connection health and removes stale connections
func (cm *ConnectionManager_OLD) Heartbeat(timeout time.Duration) {
	cm.mu.RLock()
	staleClients := make([]string, 0)
	now := time.Now()

	for clientID, conn := range cm.connections {
		if now.Sub(conn.LastPing()) > timeout {
			staleClients = append(staleClients, clientID)
		}
	}
	cm.mu.RUnlock()

	for _, clientID := range staleClients {
		cm.Unregister(clientID)
	}
}

// GetConnection returns a connection by client ID
func (cm *ConnectionManager_OLD) GetConnection(clientID string) (*Connection_OLD, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	conn, ok := cm.connections[clientID]
	return conn, ok
}

// ConnectionCount returns the number of active connections
func (cm *ConnectionManager_OLD) ConnectionCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.connections)
}

// errConnectionClosed returns an error for a closed connection
func errConnectionClosed(clientID string) error {
	return fmt.Errorf("connection closed for client: %s", clientID)
}

// errNotFound returns an error for a not found resource
func errNotFound(id string) error {
	return fmt.Errorf("not found: %s", id)
}
