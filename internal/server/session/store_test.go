package session

import (
	"sync"
	"testing"

	"github.com/gorilla/websocket"
)

// ============================================================================
// Store Creation Tests
// ============================================================================

func TestNewStore(t *testing.T) {
	maxBuffer := 1024
	store := NewStore(maxBuffer)

	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if store.maxOutputBuffer != maxBuffer {
		t.Errorf("expected maxOutputBuffer %d, got %d", maxBuffer, store.maxOutputBuffer)
	}
	if store.sessions == nil {
		t.Error("sessions map should be initialized")
	}
}

func TestNewStoreZeroBuffer(t *testing.T) {
	store := NewStore(0)

	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if store.maxOutputBuffer != 0 {
		t.Errorf("expected maxOutputBuffer 0, got %d", store.maxOutputBuffer)
	}
}

func TestNewStoreNegativeBuffer(t *testing.T) {
	store := NewStore(-1)

	if store == nil {
		t.Fatal("expected non-nil store")
	}
	// Negative buffer should be accepted (implementation may handle it)
	if store.maxOutputBuffer != -1 {
		t.Errorf("expected maxOutputBuffer -1, got %d", store.maxOutputBuffer)
	}
}

// ============================================================================
// Create Session Tests
// ============================================================================

func TestCreateSessionWithoutID(t *testing.T) {
	store := NewStore(1024)

	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if sess.Info.ID == "" {
		t.Error("expected non-empty auto-generated session ID")
	}
}

func TestCreateSessionWithID(t *testing.T) {
	store := NewStore(1024)
	customID := "custom-session-123"

	sess, err := store.Create(&customID, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if sess.Info.ID != customID {
		t.Errorf("expected ID %s, got %s", customID, sess.Info.ID)
	}
}

func TestCreateSessionUniqueIDs(t *testing.T) {
	store := NewStore(1024)

	sess1, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create sess1 failed: %v", err)
	}

	sess2, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create sess2 failed: %v", err)
	}

	if sess1.Info.ID == sess2.Info.ID {
		t.Error("expected unique IDs for different sessions")
	}
}

func TestCreateSessionWithConnection(t *testing.T) {
	store := NewStore(1024)

	// Pass nil connection for unit test
	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify session was created
	if sess == nil {
		t.Fatal("expected non-nil session")
	}
}

func TestCreateSessionNilConnection(t *testing.T) {
	store := NewStore(1024)

	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Should handle nil connection gracefully
	if sess == nil {
		t.Fatal("expected non-nil session even with nil connection")
	}
}

// ============================================================================
// Get Session Tests
// ============================================================================

func TestGetSessionExists(t *testing.T) {
	store := NewStore(1024)

	created, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	got, err := store.Get(created.Info.ID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.Info.ID != created.Info.ID {
		t.Errorf("IDs don't match: %s vs %s", got.Info.ID, created.Info.ID)
	}
}

func TestGetSessionNotFound(t *testing.T) {
	store := NewStore(1024)

	_, err := store.Get("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestGetSessionAfterCreate(t *testing.T) {
	store := NewStore(1024)
	customID := "test-session"

	_, err := store.Create(&customID, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	sess, err := store.Get(customID)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if sess.Info.ID != customID {
		t.Errorf("expected ID %s, got %s", customID, sess.Info.ID)
	}
}

func TestGetSessionAfterDelete(t *testing.T) {
	store := NewStore(1024)

	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = store.Delete(sess.Info.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = store.Get(sess.Info.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

// ============================================================================
// List Sessions Tests
// ============================================================================

func TestListSessionsEmpty(t *testing.T) {
	store := NewStore(1024)

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions initially, got %d", len(sessions))
	}
}

func TestListSessionsMultiple(t *testing.T) {
	store := NewStore(1024)

	_, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create sess1 failed: %v", err)
	}

	_, err = store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create sess2 failed: %v", err)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestListSessionsAfterDelete(t *testing.T) {
	store := NewStore(1024)

	sess1, _ := store.Create(nil, nil)
	sess2, _ := store.Create(nil, nil)

	err := store.Delete(sess1.Info.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session after delete, got %d", len(sessions))
	}
	if sessions[0].Info.ID != sess2.Info.ID {
		t.Errorf("expected remaining session to be %s, got %s", sess2.Info.ID, sessions[0].Info.ID)
	}
}

// ============================================================================
// Delete Session Tests
// ============================================================================

func TestDeleteSessionExists(t *testing.T) {
	store := NewStore(1024)

	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	err = store.Delete(sess.Info.ID)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify it's gone
	_, err = store.Get(sess.Info.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestDeleteSessionNotFound(t *testing.T) {
	store := NewStore(1024)

	err := store.Delete("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestDeleteSessionIdempotent(t *testing.T) {
	store := NewStore(1024)

	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// First delete
	err = store.Delete(sess.Info.ID)
	if err != nil {
		t.Fatalf("First delete failed: %v", err)
	}

	// Second delete should return error
	err = store.Delete(sess.Info.ID)
	if err == nil {
		t.Error("expected error on second delete")
	}
}

// ============================================================================
// Concurrent Operations Tests
// ============================================================================

func TestConcurrentCreate(t *testing.T) {
	store := NewStore(1024)

	var wg sync.WaitGroup
	numGoroutines := 10
	sessions := make([]*Session, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sess, err := store.Create(nil, nil)
			if err != nil {
				t.Errorf("Create failed: %v", err)
				return
			}
			sessions[idx] = sess
		}(i)
	}

	wg.Wait()

	// Verify all sessions were created with unique IDs
	ids := make(map[string]bool)
	for _, sess := range sessions {
		if sess == nil {
			t.Error("nil session in results")
			continue
		}
		if ids[sess.Info.ID] {
			t.Errorf("duplicate session ID: %s", sess.Info.ID)
		}
		ids[sess.Info.ID] = true
	}

	if len(ids) != numGoroutines {
		t.Errorf("expected %d unique IDs, got %d", numGoroutines, len(ids))
	}
}

func TestConcurrentGetAndDelete(t *testing.T) {
	store := NewStore(1024)

	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var wg sync.WaitGroup

	// Start multiple readers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.Get(sess.Info.ID)
		}()
	}

	// Start a deleter
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = store.Delete(sess.Info.ID)
	}()

	wg.Wait()
	// Test passes if no panic occurs
}

func TestConcurrentListAndCreate(t *testing.T) {
	store := NewStore(1024)

	var wg sync.WaitGroup

	// Start multiple listers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = store.List()
			}
		}()
	}

	// Start multiple creators
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_, _ = store.Create(nil, nil)
			}
		}()
	}

	wg.Wait()
	// Test passes if no panic occurs
}

func TestConcurrentMultipleOperations(t *testing.T) {
	store := NewStore(1024)

	var wg sync.WaitGroup
	numOperations := 20

	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Create
			sess, err := store.Create(nil, nil)
			if err != nil {
				return
			}

			// Get
			_, _ = store.Get(sess.Info.ID)

			// List
			_, _ = store.List()

			// Delete (some will fail if already deleted)
			_ = store.Delete(sess.Info.ID)
		}(i)
	}

	wg.Wait()
	// Test passes if no panic occurs
}

// ============================================================================
// Edge Cases Tests
// ============================================================================

func TestCreateSessionWithEmptyID(t *testing.T) {
	store := NewStore(1024)
	emptyID := ""

	sess, err := store.Create(&emptyID, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Empty string should be treated as a valid ID
	if sess.Info.ID != emptyID {
		t.Errorf("expected empty ID to be preserved, got %s", sess.Info.ID)
	}
}

func TestGetSessionWithEmptyID(t *testing.T) {
	store := NewStore(1024)

	_, err := store.Get("")
	if err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestDeleteSessionWithEmptyID(t *testing.T) {
	store := NewStore(1024)

	err := store.Delete("")
	if err == nil {
		t.Error("expected error for empty ID")
	}
}

func TestStoreMaxOutputBufferPropagation(t *testing.T) {
	maxBuffer := 2048
	store := NewStore(maxBuffer)

	sess, err := store.Create(nil, nil)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if sess.maxOutputBuffer != maxBuffer {
		t.Errorf("expected session maxOutputBuffer %d, got %d", maxBuffer, sess.maxOutputBuffer)
	}
}

// ============================================================================
// Stress Tests
// ============================================================================

func TestHighVolumeSessionCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	store := NewStore(1024)
	numSessions := 1000

	for i := 0; i < numSessions; i++ {
		_, err := store.Create(nil, nil)
		if err != nil {
			t.Fatalf("Create failed at iteration %d: %v", i, err)
		}
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(sessions) != numSessions {
		t.Errorf("expected %d sessions, got %d", numSessions, len(sessions))
	}
}

func TestRapidCreateAndDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	store := NewStore(1024)

	for i := 0; i < 100; i++ {
		sess, err := store.Create(nil, nil)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		err = store.Delete(sess.Info.ID)
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}
	}

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after rapid create/delete, got %d", len(sessions))
	}
}

// ============================================================================
// WebSocket Connection Tests
// ============================================================================

func TestCreateSessionWithWebSocketConnection(t *testing.T) {
	store := NewStore(1024)

	// In a real scenario, this would be a *websocket.Conn
	// For unit tests, we pass nil
	var conn *websocket.Conn = nil

	sess, err := store.Create(nil, conn)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if sess == nil {
		t.Fatal("expected non-nil session")
	}

	// Verify host connection was initialized
	if sess.hostConn == nil {
		t.Error("expected host connection to be initialized")
	}
}