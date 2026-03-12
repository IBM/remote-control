package api

import (
	"net/http"

	"github.com/gabe-l-hart/remote-control/internal/session"
	"github.com/google/uuid"
)

// handleRegisterClient handles POST /sessions/{id}/clients.
// Server generates a unique client ID and returns it to the client.
func (s *Server) handleRegisterClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
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

	writeJSON(w, http.StatusOK, map[string]string{
		"client_id": rec.ClientID,
		"status":    string(rec.Approval),
	})
}

// handleListClients handles GET /sessions/{id}/clients.
// Supports ?status=pending to filter to pending clients.
func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}

	statusFilter := r.URL.Query().Get("status")
	var clients []*session.ClientRecord
	if statusFilter == "pending" {
		clients = sess.ListPendingClients()
	} else {
		clients = sess.ListClients()
	}

	writeJSON(w, http.StatusOK, clients)
}

// handleApproveClient handles POST /sessions/{id}/clients/{cid}/approve.
func (s *Server) handleApproveClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cid := r.PathValue("cid")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}

	var body struct {
		Permission string `json:"permission"`
	}
	_ = readJSON(r, &body)
	perm := session.Permission(body.Permission)
	if perm == "" {
		perm = session.PermissionReadWrite
	}

	if err := sess.ApproveClient(cid, perm); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDenyClient handles POST /sessions/{id}/clients/{cid}/deny.
func (s *Server) handleDenyClient(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cid := r.PathValue("cid")
	sess, err := s.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	if err := sess.DenyClient(cid); err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
