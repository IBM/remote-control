package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/session"
	"github.com/google/uuid"
)

var handlerCh = alog.UseChannel("HANDLER")

/* --- Helpers -------------------------------------------------------------- */

func sessionToResponse(s *session.Session) SessionResponse {
	return SessionResponse{
		ID:          s.ID,
		Status:      string(s.GetStatus()),
		CreatedAt:   s.CreatedAt,
		CompletedAt: s.CompletedAt,
		ExitCode:    s.ExitCode,
	}
}

func stdinEntryToResponse(e *session.StdinEntry) StdinResponse {
	return StdinResponse{
		ID:   e.ID,
		Data: base64.StdEncoding.EncodeToString(e.Data),
	}
}

/* --- Session CRUD --------------------------------------------------------- */

func (s *Server) handleCreateSession(req CreateSessionRequest) (int, interface{}) {
	var inputId *string = nil
	if req.ID != "" {
		inputId = &req.ID
	}
	sess, err := s.store.Create(inputId)
	if err != nil {
		return http.StatusInternalServerError, ErrorResponse{Error: err.Error()}
	}
	return http.StatusCreated, sessionToResponse(sess)
}

func (s *Server) handleListSessions() (int, interface{}) {
	sessions, err := s.store.List()
	if err != nil {
		return http.StatusInternalServerError, ErrorResponse{Error: err.Error()}
	}
	resp := make([]SessionResponse, 0, len(sessions))
	for _, sess := range sessions {
		resp = append(resp, sessionToResponse(sess))
	}
	return http.StatusOK, resp
}

func (s *Server) handleGetSession(id string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}
	return http.StatusOK, sessionToResponse(sess)
}

func (s *Server) handleDeleteSession(id string) (int, interface{}) {
	if err := s.store.Delete(id); err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}
	return http.StatusNoContent, nil
}

func (s *Server) handlePatchSession(id string, req PatchSessionRequest) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
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

func (s *Server) handleAppendOutput(id string, req AppendOutputRequest) (int, interface{}) {
	handlerCh.Log(alog.DEBUG2, "Appending output for session %s", id)
	sess, err := s.store.Get(id)

	// If session is unknown, create it. This allows a session to revive after
	// server restart
	respSuccess := http.StatusNoContent
	if err != nil {
		handlerCh.Log(alog.DEBUG, "Recreating unknown session %s", id)
		sess, err = s.store.Create(&id)
		if err != nil {
			return http.StatusInternalServerError, ErrorResponse{Error: "Unable to recreate session"}
		}
		respSuccess = http.StatusCreated
	}

	stream, data, ts, err := req.decode()
	if err != nil {
		return http.StatusBadRequest, ErrorResponse{Error: err.Error()}
	}
	if stream != session.StreamStdout && stream != session.StreamStderr {
		return http.StatusBadRequest, ErrorResponse{Error: "stream must be 'stdout' or 'stderr'"}
	}

	// Get offset before appending
	offset := sess.StreamOffset(stream)
	sess.AppendOutput(stream, data, ts)

	// Broadcast to WebSocket subscribers
	//!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	if s.connMgr != nil {
		payload := OutputChunkPayload{
			Stream:    string(stream),
			Data:      base64.StdEncoding.EncodeToString(data),
			Offset:    offset,
			Timestamp: ts.Format(time.RFC3339Nano),
		}
		payloadData, _ := json.Marshal(payload)
		s.connMgr.Broadcast(id, WSMessage{
			Type:      MsgTypeOutputChunk,
			SessionID: id,
			Payload:   payloadData,
		})
	}

	// Event-driven cleanup: remove inactive clients when host sends new data
	if s.cfg.ClientTimeoutSeconds > 0 {
		handlerCh.Log(alog.DEBUG3, "Checking for client timeout after %v", s.clientTimeout)
		removed := sess.RemoveInactiveClients(s.clientTimeout)
		if len(removed) > 0 {
			handlerCh.Log(alog.DEBUG, "[remote-control] removed inactive clients: %v", removed)
		}
	}

	// Event-driven cleanup: purge consumed output
	purgedStdout, purgedStderr := sess.PurgeConsumedOutput(s.cfg.MaxInitialBufferBytes)
	if purgedStdout > 0 || purgedStderr > 0 {
		handlerCh.Log(alog.DEBUG, "[remote-control] purged output chunks: stdout=%d, stderr=%d", purgedStdout, purgedStderr)
	}

	return respSuccess, nil
}

func (s *Server) handlePollOutput(id, clientID string, stdoutOffset, stderrOffset int64) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}

	// If client_id is provided, enforce approval and track activity
	if clientID != "" {
		// Enforce client approval for output polling
		if s.cfg.RequireApproval {
			if !s.checkClientApproval(sess, clientID, false) {
				return http.StatusForbidden, ErrorResponse{Error: "not approved"}
			}
		}
	}

	// Update client activity if client_id is provided
	if clientID != "" {
		if err := sess.UpdateClientActivity(clientID, stdoutOffset, stderrOffset); err != nil {
			// Client not registered - this is OK for host polls
			handlerCh.Log(alog.DEBUG, "[remote-control] client activity update failed: %v", err)
		}
	}

	// Event-driven cleanup: remove inactive clients
	if s.cfg.ClientTimeoutSeconds > 0 {
		removed := sess.RemoveInactiveClients(s.clientTimeout)
		if len(removed) > 0 {
			handlerCh.Log(alog.DEBUG, "[remote-control] removed inactive clients: %v", removed)
		}
	}

	// Read output with adjusted offsets if data was purged
	stdoutChunks, actualStdoutOffset := sess.ReadOutput(session.StreamStdout, stdoutOffset)
	stderrChunks, actualStderrOffset := sess.ReadOutput(session.StreamStderr, stderrOffset)

	// Event-driven cleanup: purge consumed output
	purgedStdout, purgedStderr := sess.PurgeConsumedOutput(s.cfg.MaxInitialBufferBytes)
	if purgedStdout > 0 || purgedStderr > 0 {
		handlerCh.Log(alog.DEBUG, "[remote-control] purged output chunks: stdout=%d, stderr=%d", purgedStdout, purgedStderr)
	}

	// Merge and sort by timestamp.
	all := make([]session.OutputChunk, 0, len(stdoutChunks)+len(stderrChunks))
	all = append(all, stdoutChunks...)
	all = append(all, stderrChunks...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})

	chunks := make([]OutputChunkResponse, 0, len(all))
	for _, ch := range all {
		chunks = append(chunks, OutputChunkResponse{
			Stream:    string(ch.Stream),
			Data:      base64.StdEncoding.EncodeToString(ch.Data),
			Offset:    ch.Offset,
			Timestamp: ch.Timestamp.Format(time.RFC3339Nano),
		})
	}

	nextStdout := stdoutOffset
	if len(stdoutChunks) > 0 {
		last := stdoutChunks[len(stdoutChunks)-1]
		nextStdout = last.Offset + int64(len(last.Data))
	}
	nextStderr := stderrOffset
	if len(stderrChunks) > 0 {
		last := stderrChunks[len(stderrChunks)-1]
		nextStderr = last.Offset + int64(len(last.Data))
	}

	return http.StatusOK, PollOutputResponse{
		Chunks:        chunks,
		NextOffsets:   map[string]int64{"stdout": nextStdout, "stderr": nextStderr},
		ActualOffsets: map[string]int64{"stdout": actualStdoutOffset, "stderr": actualStderrOffset},
	}
}

/* --- STDIN ---------------------------------------------------------------- */

func (s *Server) handleEnqueueStdin(id, clientID string, req StdinRequest) (int, interface{}) {
	handlerCh.Log(alog.DEBUG3, "Handling stdin from client [%s] for session [%s]", clientID, id)

	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}

	// Determine if this is a host submission (no client_id provided)
	isHostSubmission := clientID == ""

	// Enforce client approval and write permission for non-host submissions
	if !isHostSubmission && s.cfg.RequireApproval {
		if clientID == "" {
			return http.StatusBadRequest, ErrorResponse{Error: "client_id query parameter is required for client submissions"}
		}
		if !s.checkClientApproval(sess, clientID, true) {
			return http.StatusForbidden, ErrorResponse{Error: "not approved or read-only"}
		}
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return http.StatusBadRequest, ErrorResponse{Error: "data must be base64"}
	}

	entry := sess.EnqueueStdin(data)
	return http.StatusCreated, stdinEntryToResponse(&entry)
}

func (s *Server) handlePeekStdin(id string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}
	entry := sess.PeekStdin()
	if entry == nil {
		return http.StatusOK, nil
	}
	return http.StatusOK, stdinEntryToResponse(entry)
}

func (s *Server) handleAckStdin(id string, req AckStdinRequest) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}
	if err := sess.AckStdin(req.ID); err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}

	return http.StatusNoContent, nil
}

/* -- Client approvals ------------------------------------------------------ */

// handleRegisterClient handles POST /sessions/{id}/clients.
// Server generates a unique client ID and returns it to the client.
func (s *Server) handleRegisterClient(id string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}

	// Server generates the client ID
	clientID := uuid.New().String()
	rec := sess.RegisterClient(clientID)

	// If approval is not required, auto-approve with default permission.
	if !s.cfg.RequireApproval {
		perm := session.Permission(s.cfg.DefaultPermission)
		if perm == "" {
			perm = session.PermissionReadWrite
		}
		_ = sess.ApproveClient(clientID, perm)
		rec.Approval = session.ApprovalApproved
		rec.Permission = perm
	}

	return http.StatusOK, map[string]string{
		"client_id": rec.ClientID,
		"status":    string(rec.Approval),
	}
}

// handleListClients handles GET /sessions/{id}/clients.
// Supports ?status=pending to filter to pending clients.
func (s *Server) handleListClients(id, statusFilter string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}

	var clients []*session.ClientRecord
	if statusFilter == "pending" {
		clients = sess.ListPendingClients()
	} else {
		clients = sess.ListClients()
	}

	return http.StatusOK, clients
}

// handleApproveClient handles POST /sessions/{id}/clients/{cid}/approve.
func (s *Server) handleApproveClient(id, cid string, req ApproveClientRequest) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}

	perm := session.Permission(req.Permission)
	if perm == "" {
		perm = session.PermissionReadWrite
	}

	if err := sess.ApproveClient(cid, perm); err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}
	return http.StatusNoContent, nil
}

// handleDenyClient handles POST /sessions/{id}/clients/{cid}/deny.
func (s *Server) handleDenyClient(id, cid string) (int, interface{}) {
	sess, err := s.store.Get(id)
	if err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}
	if err := sess.DenyClient(cid); err != nil {
		return http.StatusNotFound, ErrorResponse{Error: err.Error()}
	}
	return http.StatusNoContent, nil
}
