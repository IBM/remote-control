package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/session"
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
		ID:     e.ID,
		Source: e.Source,
		Data:   base64.StdEncoding.EncodeToString(e.Data),
	}
}

// writeJSON encodes v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// readJSON decodes JSON from r.Body into v.
func readJSON(r *http.Request, v any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(v)
}

/* --- Main registration ---------------------------------------------------- */

// handlers wires all HTTP routes onto mux.
func (s *Server) registerRoutes() {
	mux := s.mux

	// WebSocket
	mux.HandleFunc("GET /ws", s.handleWebSocket)

	// Session CRUD
	mux.HandleFunc("POST /sessions", s.handleCreateSession)
	mux.HandleFunc("GET /sessions", s.handleListSessions)
	mux.HandleFunc("GET /sessions/{id}", s.handleGetSession)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSession)
	mux.HandleFunc("PATCH /sessions/{id}", s.handlePatchSession)

	// I/O
	mux.HandleFunc("POST /sessions/{id}/output", s.handleAppendOutput)
	mux.HandleFunc("GET /sessions/{id}/output", s.handlePollOutput)

	// STDIN
	mux.HandleFunc("POST /sessions/{id}/stdin", s.handleEnqueueStdin)
	mux.HandleFunc("GET /sessions/{id}/stdin", s.handlePeekStdin)
	mux.HandleFunc("POST /sessions/{id}/stdin/ack", s.handleAckStdin)

	// Approval (Phase 7)
	mux.HandleFunc("POST /sessions/{id}/clients", s.handleRegisterClient)
	mux.HandleFunc("GET /sessions/{id}/clients", s.handleListClients)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/approve", s.handleApproveClient)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/deny", s.handleDenyClient)
}

/* --- Session CRUD --------------------------------------------------------- */

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid json"})
		return
	}
	var inputId *string = nil
	if req.ID != "" {
		inputId = &req.ID
	}
	sess, err := s.store.Create(inputId)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, sessionToResponse(sess))
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: err.Error()})
		return
	}
	resp := make([]SessionResponse, 0, len(sessions))
	for _, sess := range sessions {
		resp = append(resp, sessionToResponse(sess))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, sessionToResponse(sess))
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.Delete(id); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	var req PatchSessionRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid body"})
		return
	}
	sess.Complete(req.ExitCode)

	// Immediately delete completed session from memory
	if err := s.store.Delete(id); err != nil {
		handlerCh.Log(alog.WARNING, "[remote-control] failed to delete completed session: %v", err)
	} else {
		handlerCh.Log(alog.DEBUG, "[remote-control] deleted completed session: %s", id)
	}

	writeJSON(w, http.StatusOK, sessionToResponse(sess))
}

/* --- Output --------------------------------------------------------------- */

func (s *Server) handleAppendOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	handlerCh.Log(alog.DEBUG2, "Appending output for session %s", id)
	sess, err := s.store.Get(id)

	// If session is unknown, create it. This allows a session to revive after
	// server restart
	respSuccess := http.StatusNoContent
	if err != nil {
		handlerCh.Log(alog.DEBUG, "Recreating unknown session %s", id)
		sess, err = s.store.Create(&id)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "Unable to recreate session"})
			return
		}
		respSuccess = http.StatusCreated
	}

	var req AppendOutputRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid body"})
		return
	}
	stream, data, ts, err := req.decode()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	if stream != session.StreamStdout && stream != session.StreamStderr {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "stream must be 'stdout' or 'stderr'"})
		return
	}

	// Get offset before appending
	offset := sess.StreamOffset(stream)
	sess.AppendOutput(stream, data, ts)

	// Broadcast to WebSocket subscribers
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

	w.WriteHeader(respSuccess)
}

func (s *Server) handlePollOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}

	// Extract client_id from query parameter (required for clients, optional for host)
	clientID := r.URL.Query().Get("client_id")

	// If client_id is provided, enforce approval and track activity
	if clientID != "" {
		// Enforce client approval for output polling
		if s.cfg.RequireApproval {
			if !s.checkClientApproval(sess, clientID, false) {
				writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "not approved"})
				return
			}
		}
	}

	stdoutOffset, _ := strconv.ParseInt(r.URL.Query().Get("stdout_offset"), 10, 64)
	stderrOffset, _ := strconv.ParseInt(r.URL.Query().Get("stderr_offset"), 10, 64)

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

	writeJSON(w, http.StatusOK, PollOutputResponse{
		Chunks:        chunks,
		NextOffsets:   map[string]int64{"stdout": nextStdout, "stderr": nextStderr},
		ActualOffsets: map[string]int64{"stdout": actualStdoutOffset, "stderr": actualStderrOffset},
	})
}

/* --- STDIN ---------------------------------------------------------------- */

func (s *Server) handleEnqueueStdin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Extract client_id from query parameter (optional, for client submissions)
	clientID := r.URL.Query().Get("client_id")
	handlerCh.Log(alog.DEBUG3, "Handling stdin from client [%s] for session [%s]", clientID, id)

	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}

	// Determine if this is a host submission (no client_id provided)
	isHostSubmission := clientID == ""

	// Enforce client approval and write permission for non-host submissions
	if !isHostSubmission && s.cfg.RequireApproval {
		if clientID == "" {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "client_id query parameter is required for client submissions"})
			return
		}
		if !s.checkClientApproval(sess, clientID, true) {
			writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "not approved or read-only"})
			return
		}
	}

	var req StdinRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid body"})
		return
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "data must be base64"})
		return
	}

	// Use "host" as source identifier for host submissions
	source := clientID
	// TODO: remove source from stdin
	if isHostSubmission {
		source = "host"
	}
	entry := sess.EnqueueStdin(source, data)

	writeJSON(w, http.StatusCreated, stdinEntryToResponse(&entry))
}

func (s *Server) handlePeekStdin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	entry := sess.PeekStdin()
	if entry == nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, stdinEntryToResponse(entry))
}

func (s *Server) handleAckStdin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req AckStdinRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid json"})
		return
	}
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	if err := sess.AckStdin(req.ID); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
