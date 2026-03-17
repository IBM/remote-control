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

func sessionToResponse(s *session.Session) types.SessionResponse {
	return types.SessionResponse{
		ID:          s.Info.ID,
		Status:      int(s.Info.Status),
		CreatedAt:   s.Info.CreatedAt,
		CompletedAt: s.Info.CompletedAt,
		ExitCode:    s.Info.ExitCode,
	}
}

func stdinEntryToResponse(e *types.StdinEntry) types.StdinResponse {
	return types.StdinResponse{Data: base64.StdEncoding.EncodeToString(e.Data)}
}

func outputChunkToResponse(c *types.OutputChunk) types.OutputChunkResponse {
	return types.OutputChunkResponse{Data: base64.StdEncoding.EncodeToString(c.Data)}
}

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
	return http.StatusCreated, sessionToResponse(sess)
}

func (s *Server) handleListSessions() (int, interface{}) {
	sessions, err := s.store.List()
	if err != nil {
		return http.StatusInternalServerError, types.ErrorResponse{Error: err.Error()}
	}
	resp := make([]types.SessionResponse, 0, len(sessions))
	for _, sess := range sessions {
		resp = append(resp, sessionToResponse(sess))
	}
	return http.StatusOK, resp
}

func (s *Server) handleGetSession(id string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}
	return http.StatusOK, sessionToResponse(sess)
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

	return http.StatusOK, sessionToResponse(sess)
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
	stream, data, err := req.Decode()
	if err != nil {
		return http.StatusBadRequest, types.ErrorResponse{Error: err.Error()}
	}
	if stream != types.StreamStdout && stream != types.StreamStderr {
		return http.StatusBadRequest, types.ErrorResponse{
			Error: fmt.Sprintf("stream must be %d (stdout) or %d (stderr)", types.StreamStdout, types.StreamStderr),
		}
	}

	// Add the output to the session
	sess.AppendOutput(stream, data)

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

func (s *Server) handlePollOutput(id, clientID string, stdoutOffset, stderrOffset int64) (int, interface{}) {
	// Get the session
	sess, err := s.store.Get(id)
	if nil != err {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	// Get the client connection
	client := sess.GetClient(clientID)
	if nil == client {
		return http.StatusNotFound, types.ErrorResponse{Error: fmt.Sprintf("Invalid client id %s for session %s", clientID, id)}
	}

	// Enforce client approval for output polling
	if s.cfg.RequireApproval {
		if !checkClientApproval(client, false) {
			return http.StatusForbidden, types.ErrorResponse{Error: "not approved"}
		}
	}

	// Update client activity
	if err := sess.UpdateClientActivity(clientID); err != nil {
		handlerCh.Log(alog.DEBUG, "[remote-control] client activity update failed: %v", err)
	}

	// Event-driven cleanup: remove inactive clients
	if s.cfg.ClientTimeoutSeconds > 0 {
		removed := sess.RemoveInactiveClients(s.clientTimeout)
		if len(removed) > 0 {
			handlerCh.Log(alog.DEBUG, "[remote-control] removed inactive clients: %v", removed)
		}
	}

	// Pop the data from the client queue
	chunks := client.PopAllQueue()
	outputChunks := make([]types.OutputChunkResponse, len(chunks))
	for _, chunk := range chunks {
		outputChunks = append(outputChunks, outputChunkToResponse(chunk))
	}
	return http.StatusOK, types.PollOutputResponse{Chunks: outputChunks}
}

/* --- STDIN ---------------------------------------------------------------- */

func (s *Server) handleEnqueueStdin(id, clientID string, req types.StdinRequest) (int, interface{}) {
	handlerCh.Log(alog.DEBUG3, "Handling stdin from client [%s] for session [%s]", clientID, id)

	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	// Determine if this is a host submission (no client_id provided)
	isHostSubmission := clientID == ""

	// Enforce client approval and write permission for non-host submissions
	if !isHostSubmission && s.cfg.RequireApproval {
		client := sess.GetClient(clientID)
		if nil == client || !checkClientApproval(client, true) {
			return http.StatusForbidden, types.ErrorResponse{Error: "not approved or read-only"}
		}
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return http.StatusBadRequest, types.ErrorResponse{Error: "data must be base64"}
	}

	entry := sess.EnqueueStdin(data)
	return http.StatusCreated, stdinEntryToResponse(&entry)
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

// handleListClients handles GET /sessions/{id}/clients.
// Supports ?status=pending to filter to pending clients.
func (s *Server) handleListClients(id, statusFilter string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, types.ErrorResponse{Error: err.Error()}
	}

	if statusFilter == "pending" {
		return http.StatusOK, sess.ListPendingClients()
	}
	return http.StatusOK, sess.ListClients()
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
