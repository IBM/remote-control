package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/server/session"
	"github.com/gorilla/websocket"
)

var wsHandlerCh = alog.UseChannel("WS_HANDLER")

// handleWebSocket handles WebSocket upgrade requests
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		wsHandlerCh.Log(alog.DEBUG, "[remote-control] WebSocket upgrade failed: %v", err)
		return
	}
	sessionID := r.PathValue("id")
	clientID := r.URL.Query().Get("client_id")
	status, resp := s.handleRegisterClient(sessionID, clientID, conn)
	if nil == resp {
		wsHandlerCh.Log(alog.DEBUG, "failed to register websocket client with status [%d]: %v", status, resp)
		return
	}
	clientResp, ok := resp.(types.RegisterClientResponse)
	if !ok {
		wsHandlerCh.Log(alog.DEBUG, "Bad type returned by handleRegisterClient")
		return
	}
	clientID = clientResp.ClientID
	wsHandlerCh.Log(alog.INFO, "WebSocket connection established from %s", clientID)

	// Get the client connection
	var client *session.SessionClient
	if session, err := s.store.Get(sessionID); nil != err {
		wsHandlerCh.Log(alog.DEBUG, "Failed to get session %s", sessionID)
		return
	} else if client = session.GetClient(clientID); nil == client {
		wsHandlerCh.Log(alog.DEBUG, "Failed to get session client for %s/%s", sessionID, clientID)
		return
	}

	// Start read and write pumps
	go s.readPump(client, conn, sessionID, clientID)
	go s.writePump(client, conn)
}

/* -- Read/Write Pumps ------------------------------------------------------ */

// writePump writes messages to the WebSocket connection
func (s *Server) writePump(client *session.SessionClient, conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.GetSendChan():
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				// Channel closed
				conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
				wsHandlerCh.Log(alog.DEBUG, "[remote-control] WebSocket write error: %v", err)
				return
			}

		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-client.GetDoneChan():
			return
		}
	}
}

// readPump reads messages from the WebSocket connection
func (s *Server) readPump(client *session.SessionClient, conn *websocket.Conn, sessionID, clientID string) {
	//GLG!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	//TODO: Create a proactive deferred unregister through the stack
	// defer func() {
	// 	s.sess.Unregister(clientID)
	// }()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, message, err := conn.ReadMessage()
		wsHandlerCh.Log(alog.DEBUG4, "Received websocket bytes: %v", message)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				wsHandlerCh.Log(alog.DEBUG, "WebSocket read error: %v", err)
			}
			break
		}

		var msg types.WSMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			wsHandlerCh.Log(alog.DEBUG, "invalid JSON in WebSocket message: %v", err)
			s.sendError(client, "invalid message format")
			continue
		}
		wsHandlerCh.Log(alog.DEBUG4, "Payload bytes: %v", msg.Message)

		// Handle message based on type
		switch msg.Type {
		case types.WSMessageOutput:
			wsHandlerCh.Log(alog.DEBUG3, "Received output")
			if err := s.handleAppendOutputWS(msg, client, sessionID, clientID); err != nil {
				wsHandlerCh.Log(alog.DEBUG, "output append error: %v", err)
				s.sendError(client, err.Error())
			}

		case types.WSMessageStdin:
			wsHandlerCh.Log(alog.DEBUG3, "Received stdin submit")
			if err := s.handleStdinSubmitWS(msg, client, sessionID, clientID); err != nil {
				wsHandlerCh.Log(alog.DEBUG, "stdin submit error: %v", err)
				s.sendError(client, err.Error())
			}

		default:
			wsHandlerCh.Log(alog.DEBUG, "[remote-control] unknown message type: %d", msg.Type)
			s.sendError(client, "unknown message type")
		}
	}
}

/* -- Handlers -------------------------------------------------------------- */

// handleAppendOutputWS processes output append messages via WebSocket
func (s *Server) handleAppendOutputWS(msg types.WSMessage, client *session.SessionClient, sessionID, clientID string) error {
	var payload types.OutputChunk
	wsHandlerCh.Log(alog.DEBUG4, "Unwrapping message: %s", msg.Message)
	if err := json.Unmarshal(msg.Message, &payload); err != nil {
		return fmt.Errorf("invalid OutputChunk received for session %s / client %s: %v", sessionID, clientID, err)
	}
	status, resp := s.handleAppendOutput(sessionID, payload, nil)
	if status != http.StatusCreated && status != http.StatusNoContent {
		s.sendErrorJSON(client, resp)
		return fmt.Errorf("%v", resp)
	}
	return nil
}

// handleStdinSubmitWS processes stdin submit messages via WebSocket
func (s *Server) handleStdinSubmitWS(msg types.WSMessage, client *session.SessionClient, sessionID, clientID string) error {
	var payload types.StdinEntry
	if err := msg.UnmarshalMessage(&payload); err != nil {
		return fmt.Errorf("invalid StdinEntry received for session %s / client %s: %v", sessionID, clientID, err)
	}

	status, resp := s.handleEnqueueStdin(sessionID, clientID, payload)
	if status != http.StatusCreated {
		s.sendErrorJSON(client, resp)
		return fmt.Errorf("%v", resp)
	}
	return nil
}

/* -- Error Helpers --------------------------------------------------------- */

func (s *Server) sendErrorJSON(client *session.SessionClient, v any) {
	if message, err := json.Marshal(v); nil != err {
		s.sendError(client, fmt.Sprintf("Unable to send json error: %v", err))
	} else {
		s.sendError(client, string(message))
	}
}

func (s *Server) sendError(client *session.SessionClient, message string) {
	payload := types.ErrorResponse{Error: message}
	client.Send(types.WSMessageError, payload)
}
