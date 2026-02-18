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
	ClientID   string         `json:"client_id"`
	CommonName string         `json:"common_name"` // from TLS peer cert CN
	JoinedAt   time.Time      `json:"joined_at"`
	Approval   ApprovalStatus `json:"approval"`
	Permission Permission     `json:"permission"`
}

// RegisterClient adds a new client record to the session in pending state.
func (s *Session) RegisterClient(clientID, commonName string) *ClientRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := &ClientRecord{
		ClientID:   clientID,
		CommonName: commonName,
		JoinedAt:   time.Now(),
		Approval:   ApprovalPending,
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
