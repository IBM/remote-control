package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/session"
	"github.com/gorilla/websocket"
)

var wsHandlerCh = alog.UseChannel("WS_HANDLER")

// handleWebSocket handles WebSocket upgrade requests
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Upgrade HTTP connection to WebSocket
	conn, err := s.connMgr.upgrader.Upgrade(w, r, nil)
	if err != nil {
		wsHandlerCh.Log(alog.DEBUG, "[remote-control] WebSocket upgrade failed: %v", err)
		return
	}

	// Client ID will be provided in the first subscribe message
	// For now, use a temporary ID based on remote address
	tempClientID := r.RemoteAddr

	wsHandlerCh.Log(alog.INFO, "[remote-control] WebSocket connection established from %s", tempClientID)

	// Create connection object
	connection := s.connMgr.Register(tempClientID, conn)

	// Start read and write pumps
	go s.writePump(connection)
	go s.readPump(connection, tempClientID)
}

// readPump reads messages from the WebSocket connection
func (s *Server) readPump(connection *Connection, initialClientID string) {
	defer func() {
		s.connMgr.Unregister(connection.clientID)
	}()

	connection.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	connection.conn.SetPongHandler(func(string) error {
		connection.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		connection.UpdatePing()
		return nil
	})

	actualClientID := initialClientID
	clientIDSet := false

	for {
		_, message, err := connection.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				wsHandlerCh.Log(alog.DEBUG, "[remote-control] WebSocket read error: %v", err)
			}
			break
		}

		var msg WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			wsHandlerCh.Log(alog.DEBUG, "[remote-control] invalid JSON in WebSocket message: %v", err)
			s.sendError(connection, "", "invalid message format")
			continue
		}

		// Handle message based on type
		switch msg.Type {
		case MsgTypePing:
			connection.UpdatePing()
			s.sendPong(connection)

		case MsgTypeSubscribe:
			if err := s.handleSubscribe(connection, msg, &actualClientID, &clientIDSet); err != nil {
				wsHandlerCh.Log(alog.DEBUG, "[remote-control] subscribe error: %v", err)
				s.sendError(connection, msg.SessionID, err.Error())
			}

		case MsgTypeUnsubscribe:
			s.handleUnsubscribe(connection, msg)

		case MsgTypeStdinSubmit:
			wsHandlerCh.Log(alog.DEBUG3, "Received stdin submit: %v", msg)
			if err := s.handleStdinSubmitWS(connection, msg); err != nil {
				wsHandlerCh.Log(alog.DEBUG, "[remote-control] stdin submit error: %v", err)
				s.sendError(connection, msg.SessionID, err.Error())
			}

		case MsgTypeStdinAck:
			if err := s.handleStdinAckWS(connection, msg); err != nil {
				wsHandlerCh.Log(alog.DEBUG, "[remote-control] stdin ack error: %v", err)
				s.sendError(connection, msg.SessionID, err.Error())
			}

		default:
			wsHandlerCh.Log(alog.DEBUG, "[remote-control] unknown message type: %s", msg.Type)
			s.sendError(connection, msg.SessionID, "unknown message type")
		}
	}
}

// writePump writes messages to the WebSocket connection
func (s *Server) writePump(connection *Connection) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		connection.conn.Close()
	}()

	for {
		select {
		case message, ok := <-connection.send:
			connection.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// Channel closed
				connection.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := connection.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				wsHandlerCh.Log(alog.DEBUG, "[remote-control] WebSocket write error: %v", err)
				return
			}

		case <-ticker.C:
			connection.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := connection.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-connection.done:
			return
		}
	}
}

// handleSubscribe processes subscribe messages
func (s *Server) handleSubscribe(connection *Connection, msg WSMessage, actualClientID *string, clientIDSet *bool) error {
	var payload SubscribePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	// Set the actual client ID from the first subscribe message
	if !*clientIDSet {
		*actualClientID = payload.ClientID
		*clientIDSet = true

		// Re-register connection with actual client ID
		s.connMgr.mu.Lock()
		delete(s.connMgr.connections, connection.clientID)
		connection.clientID = payload.ClientID
		s.connMgr.connections[payload.ClientID] = connection
		s.connMgr.mu.Unlock()

		wsHandlerCh.Log(alog.DEBUG, "[remote-control] client ID set to %s", payload.ClientID)
	}

	// Auto-register client with session if not already registered
	// This is needed when the server restarts and loses client state
	if sess, err := s.store.Get(payload.SessionID); err == nil {
		sess.EnsureClientRecord(payload.ClientID)
	}

	// Subscribe to session
	if err := s.connMgr.Subscribe(payload.ClientID, payload.SessionID); err != nil {
		return err
	}

	wsHandlerCh.Log(alog.DEBUG, "[remote-control] client %s subscribed to session %s", payload.ClientID, payload.SessionID)

	// Send confirmation
	s.sendSubscribed(connection, payload.SessionID)

	return nil
}

// handleUnsubscribe processes unsubscribe messages
func (s *Server) handleUnsubscribe(connection *Connection, msg WSMessage) {
	s.connMgr.Unsubscribe(connection.clientID, msg.SessionID)
	wsHandlerCh.Log(alog.DEBUG, "[remote-control] client %s unsubscribed from session %s", connection.clientID, msg.SessionID)
	s.sendUnsubscribed(connection, msg.SessionID)
}

// handleStdinSubmitWS processes stdin submit messages via WebSocket
func (s *Server) handleStdinSubmitWS(connection *Connection, msg WSMessage) error {
	var payload StdinPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	// Verify client is subscribed to this session
	if !connection.IsSubscribed(msg.SessionID) {
		wsHandlerCh.Log(alog.DEBUG2, "Connection is not subscribed to session %s", msg.SessionID)
		return errNotSubscribed(msg.SessionID)
	}

	// Get session
	sess, err := s.store.Get(msg.SessionID)
	if err != nil {
		wsHandlerCh.Log(alog.DEBUG2, "Failed to get session %s from store", msg.SessionID)
		return err
	}

	// Check approval
	if s.cfg.RequireApproval {
		if !s.checkClientApproval(sess, msg.ClientID, true) {
			wsHandlerCh.Log(alog.DEBUG2, "Client %s is not approved", msg.ClientID)
			return errNotApproved()
		}
	}

	// Decode and enqueue stdin
	data, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		wsHandlerCh.Log(alog.DEBUG2, "Failed to decode base64 payload: %v", err)
		return err
	}

	wsHandlerCh.Log(alog.DEBUG3, "Enqueuing stdin to session", sess.ID)
	entry := sess.EnqueueStdin(data)

	// Broadcast to all subscribers (including host)
	s.connMgr.Broadcast(msg.SessionID, WSMessage{
		Type:      MsgTypeStdinPending,
		SessionID: msg.SessionID,
		Payload:   marshalStdinEntry(&entry),
	})

	return nil
}

// handleStdinAckWS processes stdin ack messages via WebSocket
func (s *Server) handleStdinAckWS(connection *Connection, msg WSMessage) error {
	var payload StdinPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	sess, err := s.store.Get(msg.SessionID)
	if err != nil {
		return err
	}

	if err := sess.AckStdin(payload.ID); err != nil {
		return err
	}

	return nil
}

// Helper functions to send messages

func (s *Server) sendPong(connection *Connection) {
	msg := WSMessage{Type: MsgTypePong}
	data, _ := json.Marshal(msg)
	connection.Send(data)
}

func (s *Server) sendSubscribed(connection *Connection, sessionID string) {
	msg := WSMessage{
		Type:      MsgTypeSubscribed,
		SessionID: sessionID,
	}
	data, _ := json.Marshal(msg)
	connection.Send(data)
}

func (s *Server) sendUnsubscribed(connection *Connection, sessionID string) {
	msg := WSMessage{
		Type:      MsgTypeUnsubscribed,
		SessionID: sessionID,
	}
	data, _ := json.Marshal(msg)
	connection.Send(data)
}

func (s *Server) sendError(connection *Connection, sessionID, message string) {
	payload := ErrorPayload{Message: message}
	payloadData, _ := json.Marshal(payload)
	msg := WSMessage{
		Type:      MsgTypeError,
		SessionID: sessionID,
		Payload:   payloadData,
	}
	data, _ := json.Marshal(msg)
	connection.Send(data)
}

// Helper functions for marshaling

func marshalStdinEntry(entry *session.StdinEntry) json.RawMessage {
	payload := StdinPayload{
		ID:   entry.ID,
		Data: base64.StdEncoding.EncodeToString(entry.Data),
	}
	data, _ := json.Marshal(payload)
	return data
}

// Error helpers

func errNotSubscribed(sessionID string) error {
	return fmt.Errorf("not subscribed to session: %s", sessionID)
}

func errNotApproved() error {
	return fmt.Errorf("not approved or read-only")
}
