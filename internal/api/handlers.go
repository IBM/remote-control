package api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/session"
	"github.com/google/uuid"
)

// writeJSON encodes v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// readJSON decodes JSON from r.Body into v.
func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// sessionID extracts the session ID path segment from the request URL.
// Expects path like /sessions/{id}/... or /sessions/{id}
func sessionIDFromPath(path string) string {
	// path: /sessions/{id}[/...]
	parts := strings.SplitN(strings.TrimPrefix(path, "/sessions/"), "/", 2)
	return parts[0]
}

// handlers wires all HTTP routes onto mux.
func (s *Server) registerRoutes() {
	mux := s.mux

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
	mux.HandleFunc("POST /sessions/{id}/stdin/{sid}/accept", s.handleAcceptStdin)
	mux.HandleFunc("POST /sessions/{id}/stdin/{sid}/reject", s.handleRejectStdin)
	mux.HandleFunc("POST /sessions/{id}/stdin/reject-all", s.handleRejectAllStdin)
	mux.HandleFunc("GET /sessions/{id}/stdin/{sid}/status", s.handleStdinStatus)

	// Approval (Phase 7)
	mux.HandleFunc("POST /sessions/{id}/clients", s.handleRegisterClient)
	mux.HandleFunc("GET /sessions/{id}/clients", s.handleListClients)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/approve", s.handleApproveClient)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/deny", s.handleDenyClient)
}

// --- Session CRUD ---

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := readJSON(r, &req); err != nil || len(req.Command) == 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "command is required"})
		return
	}
	sess, err := s.store.Create(req.Command)
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
	writeJSON(w, http.StatusOK, sessionToResponse(sess))
}

// --- Output ---

func (s *Server) handleAppendOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
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
	sess.AppendOutput(stream, data, ts)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePollOutput(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}

	// Enforce client approval for output polling.
	if s.cfg.RequireApproval {
		clientID, _ := s.clientIdentity(r)
		if !s.checkClientApproval(sess, clientID, false) {
			writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "not approved"})
			return
		}
	}

	stdoutOffset, _ := strconv.ParseInt(r.URL.Query().Get("stdout_offset"), 10, 64)
	stderrOffset, _ := strconv.ParseInt(r.URL.Query().Get("stderr_offset"), 10, 64)

	stdoutChunks := sess.ReadOutput(session.StreamStdout, stdoutOffset)
	stderrChunks := sess.ReadOutput(session.StreamStderr, stderrOffset)

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
		Chunks:      chunks,
		NextOffsets: map[string]int64{"stdout": nextStdout, "stderr": nextStderr},
	})
}

// --- STDIN ---

func (s *Server) handleEnqueueStdin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}

	// Enforce client approval and write permission for stdin.
	if s.cfg.RequireApproval {
		clientID, _ := s.clientIdentity(r)
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

	entry := session.StdinEntry{
		ID:        uuid.New().String(),
		Source:    req.Source,
		Data:      data,
		Timestamp: time.Now(),
		Status:    session.StdinPending,
	}
	sess.EnqueueStdin(entry)

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

func (s *Server) handleAcceptStdin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	if err := sess.AcceptStdin(sid, time.Now()); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRejectStdin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	if err := sess.RejectStdin(sid); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRejectAllStdin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	ids := sess.RejectAllPending()
	writeJSON(w, http.StatusOK, map[string][]string{"rejected_ids": ids})
}

func (s *Server) handleStdinStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	status, err := sess.GetStdinStatus(sid)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, StdinStatusResponse{Status: string(status)})
}
