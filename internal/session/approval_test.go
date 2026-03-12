package session

import (
	"testing"
	"time"
)

func TestRegisterClient(t *testing.T) {
	s := newSession("test")
	rec := s.RegisterClient("client-1")

	if rec.ClientID != "client-1" {
		t.Errorf("expected client-1, got %s", rec.ClientID)
	}
	if rec.Approval != ApprovalPending {
		t.Errorf("expected pending approval, got %s", rec.Approval)
	}
	if time.Since(rec.JoinedAt) > time.Second {
		t.Error("JoinedAt is not recent")
	}
}

func TestRegisterClientStoredInSession(t *testing.T) {
	s := newSession("test")
	s.RegisterClient("client-1")

	got, err := s.GetClient("client-1")
	if err != nil {
		t.Fatalf("GetClient error: %v", err)
	}
	if got.ClientID != "client-1" {
		t.Errorf("expected client-1, got %s", got.ClientID)
	}
}

func TestApproveClientReadWrite(t *testing.T) {
	s := newSession("test")
	s.RegisterClient("client-1")

	if err := s.ApproveClient("client-1", PermissionReadWrite); err != nil {
		t.Fatalf("ApproveClient error: %v", err)
	}
	rec, err := s.GetClient("client-1")
	if err != nil {
		t.Fatalf("GetClient error: %v", err)
	}
	if rec.Approval != ApprovalApproved {
		t.Errorf("expected approved, got %s", rec.Approval)
	}
	if rec.Permission != PermissionReadWrite {
		t.Errorf("expected read-write, got %s", rec.Permission)
	}
}

func TestApproveClientReadOnly(t *testing.T) {
	s := newSession("test")
	s.RegisterClient("client-1")

	if err := s.ApproveClient("client-1", PermissionReadOnly); err != nil {
		t.Fatalf("ApproveClient error: %v", err)
	}
	rec, _ := s.GetClient("client-1")
	if rec.Permission != PermissionReadOnly {
		t.Errorf("expected read-only, got %s", rec.Permission)
	}
}

func TestApproveClientNotFound(t *testing.T) {
	s := newSession("test")

	err := s.ApproveClient("nonexistent", PermissionReadWrite)
	if err == nil {
		t.Fatal("expected error for nonexistent client")
	}
	if !IsNotFound(err) {
		t.Errorf("expected NotFound error, got %T: %v", err, err)
	}
}

func TestDenyClient(t *testing.T) {
	s := newSession("test")
	s.RegisterClient("client-1")

	if err := s.DenyClient("client-1"); err != nil {
		t.Fatalf("DenyClient error: %v", err)
	}
	rec, err := s.GetClient("client-1")
	if err != nil {
		t.Fatalf("GetClient error: %v", err)
	}
	if rec.Approval != ApprovalDenied {
		t.Errorf("expected denied, got %s", rec.Approval)
	}
}

func TestDenyClientNotFound(t *testing.T) {
	s := newSession("test")

	err := s.DenyClient("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent client")
	}
	if !IsNotFound(err) {
		t.Errorf("expected NotFound error, got %T: %v", err, err)
	}
}

func TestGetClientNotFound(t *testing.T) {
	s := newSession("test")

	_, err := s.GetClient("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent client")
	}
	if !IsNotFound(err) {
		t.Errorf("expected NotFound error, got %T: %v", err, err)
	}
}

func TestListPendingClients(t *testing.T) {
	s := newSession("test")
	s.RegisterClient("client-1")
	s.RegisterClient("client-2")
	s.RegisterClient("client-3")

	s.ApproveClient("client-1", PermissionReadWrite) //nolint:errcheck
	s.DenyClient("client-2")                         //nolint:errcheck

	pending := s.ListPendingClients()
	if len(pending) != 1 {
		t.Errorf("expected 1 pending client, got %d", len(pending))
	}
	if len(pending) == 1 && pending[0].ClientID != "client-3" {
		t.Errorf("expected client-3, got %s", pending[0].ClientID)
	}
}

func TestListPendingClientsEmpty(t *testing.T) {
	s := newSession("test")

	pending := s.ListPendingClients()
	if len(pending) != 0 {
		t.Errorf("expected 0 pending clients, got %d", len(pending))
	}
}

func TestListClients(t *testing.T) {
	s := newSession("test")
	s.RegisterClient("client-1")
	s.RegisterClient("client-2")

	clients := s.ListClients()
	if len(clients) != 2 {
		t.Errorf("expected 2 clients, got %d", len(clients))
	}
}

func TestListClientsEmpty(t *testing.T) {
	s := newSession("test")

	clients := s.ListClients()
	if len(clients) != 0 {
		t.Errorf("expected 0 clients, got %d", len(clients))
	}
}

func TestGetClientReturnsCopy(t *testing.T) {
	s := newSession("test")
	s.RegisterClient("client-1")

	rec, _ := s.GetClient("client-1")
	rec.Approval = ApprovalApproved // mutate the returned copy

	// Re-fetch: internal state should still show pending.
	rec2, _ := s.GetClient("client-1")
	if rec2.Approval != ApprovalPending {
		t.Errorf("expected pending after mutating copy, got %s", rec2.Approval)
	}
}

func TestIsNotFound(t *testing.T) {
	s := newSession("x")

	// Errors from session methods use notFoundError.
	err := s.ApproveClient("missing", PermissionReadWrite)
	if !IsNotFound(err) {
		t.Errorf("expected IsNotFound=true for missing client, got %v", err)
	}

	// nil is not a not-found error.
	if IsNotFound(nil) {
		t.Error("IsNotFound(nil) should be false")
	}
}
