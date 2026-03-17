package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// handlers wires all HTTP routes onto mux.
func (s *Server) registerRoutes() {
	mux := s.mux

	// WebSocket
	mux.HandleFunc("GET /ws", s.handleWebSocket)

	// Session CRUD
	mux.HandleFunc("POST /sessions", s.handleCreateSessionRoute)
	mux.HandleFunc("GET /sessions", s.handleListSessionsRoute)
	mux.HandleFunc("GET /sessions/{id}", s.handleGetSessionRoute)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSessionRoute)
	mux.HandleFunc("PATCH /sessions/{id}", s.handlePatchSessionRoute)

	// I/O
	mux.HandleFunc("POST /sessions/{id}/output", s.handleAppendOutputRoute)
	mux.HandleFunc("GET /sessions/{id}/output", s.handlePollOutputRoute)

	// STDIN
	mux.HandleFunc("POST /sessions/{id}/stdin", s.handleEnqueueStdinRoute)
	mux.HandleFunc("GET /sessions/{id}/stdin", s.handlePeekStdinRoute)
	mux.HandleFunc("POST /sessions/{id}/stdin/ack", s.handleAckStdinRoute)

	// Approval
	mux.HandleFunc("POST /sessions/{id}/clients", s.handleRegisterClientRoute)
	mux.HandleFunc("GET /sessions/{id}/clients", s.handleListClientsRoute)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/approve", s.handleApproveClientRoute)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/deny", s.handleDenyClientRoute)
}

/* --- Routes --------------------------------------------------------------- */

func (s *Server) handleCreateSessionRoute(w http.ResponseWriter, r *http.Request) {
	var req CreateSessionRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid json"})
		return
	}
	status, resp := s.handleCreateSession(req)
	writeJSON(w, status, resp)
}

func (s *Server) handleListSessionsRoute(w http.ResponseWriter, r *http.Request) {
	status, resp := s.handleListSessions()
	writeJSON(w, status, resp)
}

func (s *Server) handleGetSessionRoute(w http.ResponseWriter, r *http.Request) {
	status, resp := s.handleGetSession(r.PathValue("id"))
	writeJSON(w, status, resp)
}

func (s *Server) handleDeleteSessionRoute(w http.ResponseWriter, r *http.Request) {
	if status, resp := s.handleDeleteSession(r.PathValue("id")); nil != resp {
		writeJSON(w, status, resp)
	} else {
		w.WriteHeader(status)
	}
}

func (s *Server) handlePatchSessionRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req PatchSessionRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid body"})
		return
	}

	status, resp := s.handlePatchSession(id, req)
	writeJSON(w, status, resp)
}

func (s *Server) handleAppendOutputRoute(w http.ResponseWriter, r *http.Request) {

	var req AppendOutputRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid body"})
	}

	if status, resp := s.handleAppendOutput(r.PathValue("id"), req); nil != resp {
		writeJSON(w, status, resp)
	} else {
		w.WriteHeader(status)
	}
}

func (s *Server) handlePollOutputRoute(w http.ResponseWriter, r *http.Request) {
	stdoutOffset, _ := strconv.ParseInt(r.URL.Query().Get("stdout_offset"), 10, 64)
	stderrOffset, _ := strconv.ParseInt(r.URL.Query().Get("stderr_offset"), 10, 64)
	status, resp := s.handlePollOutput(r.PathValue("id"), r.URL.Query().Get("client_id"), stdoutOffset, stderrOffset)
	writeJSON(w, status, resp)
}

func (s *Server) handleEnqueueStdinRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	clientID := r.URL.Query().Get("client_id")

	var req StdinRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid body"})
		return
	}

	status, resp := s.handleEnqueueStdin(id, clientID, req)
	writeJSON(w, status, resp)
}

func (s *Server) handlePeekStdinRoute(w http.ResponseWriter, r *http.Request) {
	status, resp := s.handlePeekStdin(r.PathValue("id"))
	writeJSON(w, status, resp)
}

func (s *Server) handleAckStdinRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req AckStdinRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid json"})
		return
	}
	if status, resp := s.handleAckStdin(id, req); nil == resp {
		w.WriteHeader(status)
	} else {
		writeJSON(w, status, resp)
	}
}

// handleRegisterClient handles POST /sessions/{id}/clients.
// Server generates a unique client ID and returns it to the client.
func (s *Server) handleRegisterClientRoute(w http.ResponseWriter, r *http.Request) {
	status, resp := s.handleRegisterClient(r.PathValue("id"))
	writeJSON(w, status, resp)
}

// handleListClients handles GET /sessions/{id}/clients.
// Supports ?status=pending to filter to pending clients.
func (s *Server) handleListClientsRoute(w http.ResponseWriter, r *http.Request) {
	status, resp := s.handleListClients(r.PathValue("id"), r.URL.Query().Get("status"))
	writeJSON(w, status, resp)
}

// handleApproveClient handles POST /sessions/{id}/clients/{cid}/approve.
func (s *Server) handleApproveClientRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cid := r.PathValue("cid")

	var req ApproveClientRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid body"})
		return
	}

	if status, resp := s.handleApproveClient(id, cid, req); nil == resp {
		w.WriteHeader(status)
	} else {
		writeJSON(w, status, resp)
	}
}

// handleDenyClient handles POST /sessions/{id}/clients/{cid}/deny.
func (s *Server) handleDenyClientRoute(w http.ResponseWriter, r *http.Request) {
	if status, resp := s.handleDenyClient(r.PathValue("id"), r.PathValue("cid")); nil == resp {
		w.WriteHeader(status)
	} else {
		writeJSON(w, status, resp)
	}
}

/* -- Helpers --------------------------------------------------------------- */

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
