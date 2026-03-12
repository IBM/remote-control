package session

import "time"

// Permission defines what a connected client is allowed to do.
type Permission string

const (
	PermissionReadOnly  Permission = "read-only"
	PermissionReadWrite Permission = "read-write"
)

// ApprovalStatus tracks whether the host has approved or denied a client.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
)

// ClientRecord holds metadata about a connected remote client.
type ClientRecord struct {
	ClientID     string         `json:"client_id"`
	JoinedAt     time.Time      `json:"joined_at"`
	Approval     ApprovalStatus `json:"approval"`
	Permission   Permission     `json:"permission"`
	LastPollAt   time.Time      `json:"last_poll_at"`
	StdoutOffset int64          `json:"stdout_offset"`
	StderrOffset int64          `json:"stderr_offset"`
}

// RegisterClient adds a new client record to the session in pending state.
func (s *Session) RegisterClient(clientID string) *ClientRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	record := &ClientRecord{
		ClientID:     clientID,
		JoinedAt:     now,
		Approval:     ApprovalPending,
		LastPollAt:   now,
		StdoutOffset: 0,
		StderrOffset: 0,
	}
	s.clients[clientID] = record
	return record
}

// ApproveClient approves a client with the given permission level.
func (s *Session) ApproveClient(clientID string, perm Permission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.clients[clientID]
	if !ok {
		return errNotFound(clientID)
	}
	rec.Approval = ApprovalApproved
	rec.Permission = perm
	return nil
}

// DenyClient denies a client.
func (s *Session) DenyClient(clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.clients[clientID]
	if !ok {
		return errNotFound(clientID)
	}
	rec.Approval = ApprovalDenied
	return nil
}

// GetClient returns the client record for the given ID.
func (s *Session) GetClient(clientID string) (*ClientRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.clients[clientID]
	if !ok {
		return nil, errNotFound(clientID)
	}
	cp := *rec
	return &cp, nil
}

// ListPendingClients returns all clients in pending approval state.
func (s *Session) ListPendingClients() []*ClientRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*ClientRecord
	for _, rec := range s.clients {
		if rec.Approval == ApprovalPending {
			cp := *rec
			result = append(result, &cp)
		}
	}
	return result
}

// ListClients returns all client records.
func (s *Session) ListClients() []*ClientRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*ClientRecord, 0, len(s.clients))
	for _, rec := range s.clients {
		cp := *rec
		result = append(result, &cp)
	}
	return result
}

// UpdateClientActivity updates the last poll timestamp and stream offsets for a client.
func (s *Session) UpdateClientActivity(clientID string, stdoutOffset, stderrOffset int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.clients[clientID]
	if !ok {
		return errNotFound(clientID)
	}
	rec.LastPollAt = time.Now()
	rec.StdoutOffset = stdoutOffset
	rec.StderrOffset = stderrOffset
	return nil
}

// RemoveInactiveClients removes clients that haven't polled within the timeout period.
// Returns the list of removed client IDs.
func (s *Session) RemoveInactiveClients(timeout time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var removed []string

	for clientID, rec := range s.clients {
		if now.Sub(rec.LastPollAt) > timeout {
			delete(s.clients, clientID)
			removed = append(removed, clientID)
		}
	}

	return removed
}
