package server

import (
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/server/session"
	"github.com/gorilla/websocket"
)

var handlerCh = alog.UseChannel("HANDLER")

/* --- Helpers -------------------------------------------------------------- */

// checkClientApproved verifies that the requesting client is approved.
// Returns (approved, readWrite). On false, the handler should return 403.
func checkClientApproval(client *session.SessionClient, needWrite bool) bool {
	if client.Info.Approval != types.ApprovalApproved {
		return false
	}
	if needWrite && client.Info.Permission == types.PermissionReadOnly {
		return false
	}
	return true
}

/* --- Session CRUD --------------------------------------------------------- */

func (s *Server) handleCreateSession(req types.CreateSessionRequest, conn *websocket.Conn) (int, interface{}) {
	var inputId *string = nil
	if req.ID != "" {
		inputId = &req.ID
	}
	sess, err := s.store.Create(inputId, conn)
	if err != nil {
		return http.StatusInternalServerError, types.ErrorResponse{Error: err.Error()}
	}
	return http.StatusCreated, sess.Info
}

func (s *Server) handleListSessions() (int, interface{}) {
	sessions, err := s.store.List()
	if err != nil {
		return http.StatusInternalServerError, types.ErrorResponse{Error: err.Error()}
	}
	resp := make([]types.SessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		resp = append(resp, sess.Info)
	}
	return http.StatusOK, resp
}

func (s *Server) handleGetSession(id string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}
	return http.StatusOK, sess.Info
}

func (s *Server) handleDeleteSession(id string) (int, interface{}) {
	if err := s.store.Delete(id); err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}
	return http.StatusNoContent, nil
}

func (s *Server) handlePatchSession(id string, req types.PatchSessionRequest) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}
	sess.Complete(req.ExitCode)

	// Immediately delete completed session from memory
	if err := s.store.Delete(id); err != nil {
		handlerCh.Log(alog.WARNING, "[remote-control] failed to delete completed session: %v", err)
	} else {
		handlerCh.Log(alog.DEBUG, "[remote-control] deleted completed session: %s", id)
	}

	return http.StatusOK, sess.Info
}

/* --- Output --------------------------------------------------------------- */

func (s *Server) handleAppendOutput(id string, req types.AppendOutputRequest, conn *websocket.Conn) (int, interface{}) {
	handlerCh.Log(alog.DEBUG2, "Appending output for session %s", id)
	sess, err := s.store.Get(id)

	// If session is unknown, create it. This allows a session to revive after
	// server restart
	respSuccess := http.StatusNoContent
	if err != nil {
		handlerCh.Log(alog.DEBUG, "Recreating unknown session %s", id)
		sess, err = s.store.Create(&id, conn)
		if err != nil {
			return http.StatusInternalServerError, types.ErrorResponse{Error: "Unable to recreate session"}
		}
		respSuccess = http.StatusCreated
	}

	// Decode the output data to bytes
	if req.Stream != types.StreamStdout && req.Stream != types.StreamStderr {
		return http.StatusBadRequest, types.ErrorResponse{
			Error: fmt.Sprintf("stream must be %d (stdout) or %d (stderr). Got %d", types.StreamStdout, types.StreamStderr, req.Stream),
		}
	}

	// Add the output to the session
	sess.AppendOutput(req.Stream, req.Data)

	// Event-driven cleanup: remove inactive clients when host sends new data
	if s.cfg.ClientTimeoutSeconds > 0 {
		handlerCh.Log(alog.DEBUG3, "Checking for client timeout after %v", s.clientTimeout)
		removed := sess.RemoveInactiveClients(s.clientTimeout)
		if len(removed) > 0 {
			handlerCh.Log(alog.DEBUG, "[remote-control] removed inactive clients: %v", removed)
		}
	}

	return respSuccess, nil
}

func (s *Server) handlePoll(sessionID, clientID string, mType types.WSMessageType) (int, interface{}) {
	// Get the session
	sess, err := s.store.Get(sessionID)
	if nil != err {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	// Peek at the queue for the given session
	queued := sess.PeekClientQueue(clientID, mType)
	return http.StatusOK, types.PollResponse{Elements: queued}
}

func (s *Server) handleAck(sessionID, clientID string, mType types.WSMessageType) (int, interface{}) {
	// Get the session
	sess, err := s.store.Get(sessionID)
	if nil != err {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	// Clear the queue for the given session/client/type
	sess.ClearClientQueue(clientID, mType)
	return http.StatusOK, nil
}

/* --- STDIN ---------------------------------------------------------------- */

func (s *Server) handleEnqueueStdin(id, clientID string, req types.StdinRequest) (int, interface{}) {
	handlerCh.Log(alog.DEBUG3, "Handling stdin from client [%s] for session [%s]", clientID, id)

	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	// Enforce client approval and write permission for non-host submissions
	if s.cfg.RequireApproval {
		client := sess.GetClient(clientID)
		if nil == client || !checkClientApproval(client, true) {
			return http.StatusForbidden, types.ErrorResponse{Error: "not approved or read-only"}
		}
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return http.StatusBadRequest, types.ErrorResponse{Error: "data must be base64"}
	}

	sess.EnqueueStdin(data)
	return http.StatusCreated, nil
}

/* -- Client approvals ------------------------------------------------------ */

// handleRegisterClient handles POST /sessions/{id}/clients.
// Server generates a unique client ID and returns it to the client.
func (s *Server) handleRegisterClient(id string, conn *websocket.Conn) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	// Server generates the client ID
	clientID, client := sess.RegisterClient(conn)

	// If approval is not required, auto-approve with default permission.
	if !s.cfg.RequireApproval {
		perm := types.Permission(s.cfg.DefaultPermission)
		if perm != types.PermissionReadOnly && perm != types.PermissionReadWrite {
			handlerCh.Log(alog.WARNING, "Misconfiguration: invalid default permission [%d]", perm)
			perm = types.PermissionReadOnly
		}
		_ = sess.ApproveClient(clientID, perm)
	}

	return http.StatusOK, types.RegisterClientResponse{
		ClientID: clientID,
		Status:   client.Info.Approval,
	}
}

// handleApproveClient handles POST /sessions/{id}/clients/{cid}/approve.
func (s *Server) handleApproveClient(id, cid string, req types.ApproveClientRequest) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	// Default to read only
	perm := req.Permission
	if perm != types.PermissionReadOnly && req.Permission != types.PermissionReadWrite {
		perm = types.PermissionReadOnly
	}

	if err := sess.ApproveClient(cid, perm); err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}
	return http.StatusNoContent, nil
}

// handleDenyClient handles POST /sessions/{id}/clients/{cid}/deny.
func (s *Server) handleDenyClient(id, cid string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}
	if err := sess.DenyClient(cid); err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}
	return http.StatusNoContent, nil
}
