package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// handlers wires all HTTP routes onto mux.
func (s *Server) registerRoutes() {
	mux := s.mux

	// WebSocket
	mux.HandleFunc("GET /ws/{id}", s.handleWebSocket)

	// Session CRUD
	mux.HandleFunc("POST /sessions", s.handleCreateSessionRoute)
	mux.HandleFunc("GET /sessions", s.handleListSessionsRoute)
	mux.HandleFunc("GET /sessions/{id}", s.handleGetSessionRoute)
	mux.HandleFunc("DELETE /sessions/{id}", s.handleDeleteSessionRoute)
	mux.HandleFunc("PATCH /sessions/{id}", s.handlePatchSessionRoute)

	// I/O
	mux.HandleFunc("POST /sessions/{id}/output", s.handleAppendOutputRoute)
	mux.HandleFunc("POST /sessions/{id}/stdin", s.handleEnqueueStdinRoute)

	// Poll / Ack
	mux.HandleFunc("GET / sessions/{id}/{m_type}/poll", s.handlePollRoute)
	mux.HandleFunc("GET / sessions/{id}/{m_type}/ack", s.handleAckRoute)

	// Approval
	mux.HandleFunc("POST /sessions/{id}/clients", s.handleRegisterClientRoute)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/approve", s.handleApproveClientRoute)
	mux.HandleFunc("POST /sessions/{id}/clients/{cid}/deny", s.handleDenyClientRoute)
}

/* --- Routes --------------------------------------------------------------- */

func (s *Server) handleCreateSessionRoute(w http.ResponseWriter, r *http.Request) {
	var req types.CreateSessionRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: "invalid json"})
		return
	}
	status, resp := s.handleCreateSession(req, nil)
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
	var req types.PatchSessionRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: "invalid body"})
		return
	}

	status, resp := s.handlePatchSession(id, req)
	writeJSON(w, status, resp)
}

func (s *Server) handleAppendOutputRoute(w http.ResponseWriter, r *http.Request) {

	var req types.OutputChunk
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: "invalid body"})
	}

	if status, resp := s.handleAppendOutput(r.PathValue("id"), req, nil); nil != resp {
		writeJSON(w, status, resp)
	} else {
		w.WriteHeader(status)
	}
}

func (s *Server) handlePollRoute(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	clientID := r.URL.Query().Get("client_id")
	mTypeStr := r.PathValue("m_type")
	mTypeInt, err := strconv.Atoi(mTypeStr)
	if nil != err {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: fmt.Sprintf("Invalid message type: %s", mTypeStr)})
		return
	}
	mType := types.GetWSMessageType(mTypeInt)
	if types.WSMessageUnknown == mType {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: fmt.Sprintf("Unknown message type: %d", mTypeInt)})
	}
	status, resp := s.handlePoll(sessionID, clientID, mType)
	writeJSON(w, status, resp)
}

func (s *Server) handleAckRoute(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	clientID := r.URL.Query().Get("client_id")
	mTypeStr := r.PathValue("m_type")
	mTypeInt, err := strconv.Atoi(mTypeStr)
	if nil != err {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: fmt.Sprintf("Invalid message type: %s", mTypeStr)})
		return
	}
	mType := types.GetWSMessageType(mTypeInt)
	if types.WSMessageUnknown == mType {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: fmt.Sprintf("Unknown message type: %d", mTypeInt)})
	}
	if status, resp := s.handleAck(sessionID, clientID, mType); nil == resp {
		w.WriteHeader(status)
	} else {
		writeJSON(w, status, resp)
	}
}

func (s *Server) handleEnqueueStdinRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	clientID := r.URL.Query().Get("client_id")

	var req types.StdinRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: "invalid body"})
		return
	}

	if status, resp := s.handleEnqueueStdin(id, clientID, req); nil == resp {
		w.WriteHeader(status)
	} else {
		writeJSON(w, status, resp)
	}
}

// handleRegisterClient handles POST /sessions/{id}/clients.
// Server generates a unique client ID and returns it to the client.
func (s *Server) handleRegisterClientRoute(w http.ResponseWriter, r *http.Request) {
	// Get client_id query parameter
	clientID := r.URL.Query().Get("client_id")
	status, resp := s.handleRegisterClient(r.PathValue("id"), clientID, nil)
	writeJSON(w, status, resp)
}

// handleApproveClient handles POST /sessions/{id}/clients/{cid}/approve.
func (s *Server) handleApproveClientRoute(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cid := r.PathValue("cid")

	var req types.ApproveClientRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, types.ErrorResponse{Error: "invalid body"})
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
