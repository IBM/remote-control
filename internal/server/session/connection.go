package session

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gorilla/websocket"

	types "github.com/gabe-l-hart/remote-control/internal/common"
)

var connCh = alog.UseChannel("CONN")

// Connection implements the WebSocket for a given client
type Connection struct {
	conn *websocket.Conn
	send chan []byte
	mu   sync.RWMutex
	done chan struct{}
}

// NewConnection creates a new Connection
func newConnection(conn *websocket.Conn) *Connection {
	return &Connection{
		conn: conn,
		send: make(chan []byte, 256),
		done: make(chan struct{}),
	}
}

// Close the connection
func (c *Connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.done:
		// Already closed
		return
	default:
		close(c.done)
		close(c.send)
		if c.conn != nil {
			c.conn.Close()
		}
	}
}

// Send a serialized message to the client
// NOTE: Implemented as a free-function to support generic message type
func SendConnectionMessage[T any](c *Connection, mType types.WSMessageType, message T) error {
	if nil == c.conn {
		return fmt.Errorf("no websocket")
	}

	// Serialize the message payload first
	payloadBytes, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %v", err)
	}
	connCh.Log(alog.DEBUG4, "Serialized payload: %s", payloadBytes)

	// Wrap in the WSMessage envelope
	wsMsg := types.WSMessage{
		Type:    mType,
		Message: payloadBytes,
	}

	// Serialize to the WS wire format
	data, err := json.Marshal(wsMsg)
	if err != nil {
		return fmt.Errorf("failed to serialize WebSocket message: %v", err)
	}

	connCh.Log(alog.DEBUG4, "Input data: %s", message)
	connCh.Log(alog.DEBUG4, "Sending data on client connection: %s", data)
	select {
	case c.send <- data:
		return nil
	case <-c.done:
		return fmt.Errorf("connection closed, unable to send")
	default:
		// Channel full, drop message
		return fmt.Errorf("connection full, unable to send")
	}
}
