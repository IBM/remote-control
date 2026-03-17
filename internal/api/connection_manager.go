package api

import (
	"sync"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/session"
)

var connCh = alog.UseChannel("CONN")

/* -- Connection Interface -------------------------------------------------- */

// Message type constants
const (
	// Server → Client
	MsgTypeOutputChunk      = "output_chunk"
	MsgTypeStdinPending     = "stdin_pending"
	MsgTypeSessionCompleted = "session_completed"
	MsgTypeClientApproved   = "client_approved"
	MsgTypeError            = "error"
	MsgTypePong             = "pong"
	MsgTypeSubscribed       = "subscribed"
	MsgTypeUnsubscribed     = "unsubscribed"

	// Client → Server
	MsgTypeSubscribe   = "subscribe"
	MsgTypeUnsubscribe = "unsubscribe"
	MsgTypeStdinSubmit = "stdin_submit"
	MsgTypeStdinAck    = "stdin_ack"
	MsgTypePing        = "ping"
)

type HandlerFunc func(messageBody interface{}) error

// Connection is the interface for a client's connection to the server
// TODO: Deconflict with Connection in websocket.go
type Connection interface {
	// Register a handler for the given message type
	RegisterHandler(messageType string, handler HandlerFunc)
	// Send a message to the client on the connection
	Send(messageType string, message interface{}) error
	// Poll for queued messages
	Poll() []interface{}
	// Close the connection
	Close()
}

/* -- Connection Manager ---------------------------------------------------- */

// ConnectionManager manages all active WebSocket connections
type ConnectionManager struct {
	mu       sync.RWMutex
	sessions map[string]map[string]Connection // sessionID -> clientID -> Connection
	handlers map[string]HandlerFunc
}

// NewConnectionManager creates a new ConnectionManager
func NewConnectionManager(store session.Store) *ConnectionManager {
	return &ConnectionManager{
		sessions: make(map[string]map[string]Connection),
		handlers: make(map[string]HandlerFunc),
	}
}

// Register a handler for messages of a given type received from any connection
func (cm *ConnectionManager) RegisterHandler(messageType string, handler HandlerFunc) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Handlers should be registered once at boot
	if _, ok := cm.handlers[messageType]; ok {
		connCh.Log(alog.WARNING, "Overwriting handler for message type %s", messageType)
	}
	cm.handlers[messageType] = handler
}

// Register adds a new connection for the given session/client
func (cm *ConnectionManager) Register(sessionID, clientID string, conn Connection) {

	// Register handlers before locking
	for messageType, handler := range cm.handlers {
		conn.RegisterHandler(messageType, handler)
	}

	// Lock while mutating the internal state
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Close existing connection if any
	connMgrCh.Log(alog.DEBUG2, "Registering connection for client [%s]", clientID)
	if existingSession, okSession := cm.sessions[sessionID]; okSession {
		if existingClient, okClient := existingSession[clientID]; okClient {
			connMgrCh.Log(alog.DEBUG3, "Overwriting existing connection for %s/%s", sessionID, clientID)
			existingClient.Close()
		}
		existingSession[clientID] = conn
	} else {
		cm.sessions[clientID] = make(map[string]Connection)
		cm.sessions[clientID][sessionID] = conn
	}
}

// Unregister removes a connection
func (cm *ConnectionManager) Unregister(sessionID, clientID string) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if existingSession, okSession := cm.sessions[sessionID]; okSession {
		if existingClient, okClient := existingSession[clientID]; okClient {
			existingClient.Close()
			delete(existingSession, clientID)
			if 0 == len(existingSession) {
				delete(cm.sessions, sessionID)
			}
		}
	}
}

// Broadcast sends a message to all subscribers of a session
func (cm *ConnectionManager) Broadcast(sessionID string, messageType string, message interface{}) {
	cm.mu.RLock()
	clients, ok := cm.sessions[sessionID]
	cm.mu.RUnlock()

	if !ok {
		return
	}

	for clientConnID, conn := range clients {
		wsHandlerCh.Log(alog.DEBUG3, "Broadcasting [%s] to client connection %s/%s", messageType, sessionID, clientConnID)
		conn.Send(messageType, message)
	}
}

// Notify the connection manager of a closed session
func (cm *ConnectionManager) CloseSession(sessionID string) {
	cm.mu.RLock()
	clients, ok := cm.sessions[sessionID]
	if ok {
		delete(cm.sessions, sessionID)
	}
	cm.mu.RUnlock()

	if !ok {
		return
	}

	// Close in separate goroutines to avoid blocking some clients on long
	// running close operations
	var wg sync.WaitGroup
	for clientConnID, conn := range clients {
		wsHandlerCh.Log(alog.DEBUG3, "Closing client %s/%s", sessionID, clientConnID)
		wg.Add(1)
		go func(c Connection) {
			c.Close()
		}(conn)
	}
	wg.Wait()
}
