package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/common/types"
	ws "github.com/gabe-l-hart/remote-control/internal/common/websocket"
	"github.com/gabe-l-hart/remote-control/internal/server/session"
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

	// Create a pipe using the client's existing send/done channels and start
	// the read/write pumps
	pipe := ws.NewPipeWithChannels(conn, client.GetSendChan(), client.GetDoneChan())
	pipe.OnMessage(func(msg types.WSMessage) {
		s.handleServerMessage(msg, client, sessionID, clientID)
	})
	pipe.Start(context.Background())
}

// handleServerMessage dispatches incoming WebSocket messages to the
// appropriate handler based on message type.
func (s *Server) handleServerMessage(msg types.WSMessage, client *session.SessionClient, sessionID, clientID string) {
	wsHandlerCh.Log(alog.DEBUG4, "Payload bytes: %v", msg.Message)

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
	session.Send(client, types.WSMessageError, payload)
}
