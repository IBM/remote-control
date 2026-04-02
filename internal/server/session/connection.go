package session

import (
	"encoding/json"
	"fmt"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gorilla/websocket"

	types "github.com/gabe-l-hart/remote-control/internal/common"
	ws "github.com/gabe-l-hart/remote-control/internal/common/websocket"
)

var connCh = alog.UseChannel("CONN")

// Connection wraps a WebSocketPipe for use within a session client.
// It provides send/done channel access and message sending helpers.
type Connection struct {
	pipe    *ws.WebSocketPipe
	hasConn bool // true when backed by a real websocket connection
}

// newConnection creates a new Connection wrapping a WebSocketPipe.
func newConnection(conn *websocket.Conn) *Connection {
	return &Connection{
		pipe:    ws.NewPipe(conn),
		hasConn: conn != nil,
	}
}

// Close the connection
func (c *Connection) Close() {
	c.pipe.Close()
}

// GetSendChan returns the underlying send channel.
func (c *Connection) GetSendChan() chan []byte {
	return c.pipe.SendChan()
}

// GetDoneChan returns the underlying done channel.
func (c *Connection) GetDoneChan() chan struct{} {
	return c.pipe.DoneChan()
}

// SendMessage sends a serialized message to the client.
func (c *Connection) SendMessage(mType types.WSMessageType, message any) error {
	if !c.hasConn {
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
	return c.pipe.Send(data)
}
