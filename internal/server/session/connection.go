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

// Send output chunks to the client
func (c *Connection) SendOutput(chunks []*types.OutputChunk) error {
	if nil == c.conn {
		return fmt.Errorf("No websocket")
	}

	// Serialize to the WS wire format
	data, err := json.Marshal(chunks)
	if nil != err {
		return fmt.Errorf("Json marshal error: %v", err)
	}

	// Send on the send channel
	select {
	case c.send <- data:
		return nil
	case <-c.done:
		return fmt.Errorf("Connection closed, unable to send")
	default:
		// Channel full, drop message
		return fmt.Errorf("Connection full, unable to send")
	}
}

// Close the connection
func (c *Connection) Close() {
	if nil == c.conn {
		return
	}
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
