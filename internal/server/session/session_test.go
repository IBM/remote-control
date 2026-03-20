package session

import (
	"sync"
	"testing"
	"time"

	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// ============================================================================
// Session Creation Tests
// ============================================================================

func TestNewSession(t *testing.T) {
	id := "test-session"
	maxBuffer := 1024
	sess := newSession(id, maxBuffer, nil)

	if sess == nil {
		t.Fatal("expected non-nil session")
	}
	if sess.Info.ID != id {
		t.Errorf("expected ID %s, got %s", id, sess.Info.ID)
	}
	if sess.Info.Status != types.SessionStatusActive {
		t.Errorf("expected status Active, got %d", sess.Info.Status)
	}
	if sess.maxOutputBuffer != maxBuffer {
		t.Errorf("expected maxOutputBuffer %d, got %d", maxBuffer, sess.maxOutputBuffer)
	}
	if sess.hostConn == nil {
		t.Error("expected host connection to be initialized")
	}
	if sess.clients == nil {
		t.Error("expected clients map to be initialized")
	}
	if sess.outputBuffer == nil {
		t.Error("expected output buffer to be initialized")
	}
}

func TestNewSessionWithMaxOutputBuffer(t *testing.T) {
	maxBuffer := 2048
	sess := newSession("test", maxBuffer, nil)

	if sess.maxOutputBuffer != maxBuffer {
		t.Errorf("expected maxOutputBuffer %d, got %d", maxBuffer, sess.maxOutputBuffer)
	}
}

func TestNewSessionHostConnection(t *testing.T) {
	sess := newSession("test", 1024, nil)

	if sess.hostConn == nil {
		t.Fatal("expected host connection to be initialized")
	}
	if sess.hostConn.Info.ClientID != types.HostClientID {
		t.Errorf("expected host client ID to be %s, got %s", types.HostClientID, sess.hostConn.Info.ClientID)
	}
	if sess.hostConn.Info.Approval != types.ApprovalApproved {
		t.Errorf("expected host to be auto-approved")
	}
}

func TestNewSessionEmptyClients(t *testing.T) {
	sess := newSession("test", 1024, nil)

	if len(sess.clients) != 0 {
		t.Errorf("expected 0 clients initially, got %d", len(sess.clients))
	}
}

func TestNewSessionInfo(t *testing.T) {
	id := "test-session-123"
	sess := newSession(id, 1024, nil)

	if sess.Info.ID != id {
		t.Errorf("ID mismatch")
	}
	if sess.Info.Status != types.SessionStatusActive {
		t.Errorf("expected Active status")
	}
	if sess.Info.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if sess.Info.CompletedAt != nil {
		t.Error("CompletedAt should be nil initially")
	}
	if sess.Info.ExitCode != nil {
		t.Error("ExitCode should be nil initially")
	}
}

// ============================================================================
// AppendOutput Tests
// ============================================================================

func TestAppendOutputStdout(t *testing.T) {
	sess := newSession("test", 1024, nil)

	data := []byte("stdout data")
	sess.AppendOutput(types.StreamStdout, data)

	if len(sess.outputBuffer) != 1 {
		t.Errorf("expected 1 chunk in buffer, got %d", len(sess.outputBuffer))
	}
	if sess.outputBuffer[0].Stream != types.StreamStdout {
		t.Error("expected stdout stream")
	}
	if string(sess.outputBuffer[0].Data) != string(data) {
		t.Error("data mismatch")
	}
}

func TestAppendOutputStderr(t *testing.T) {
	sess := newSession("test", 1024, nil)

	data := []byte("stderr data")
	sess.AppendOutput(types.StreamStderr, data)

	if len(sess.outputBuffer) != 1 {
		t.Errorf("expected 1 chunk in buffer, got %d", len(sess.outputBuffer))
	}
	if sess.outputBuffer[0].Stream != types.StreamStderr {
		t.Error("expected stderr stream")
	}
}

func TestAppendOutputEmptyData(t *testing.T) {
	sess := newSession("test", 1024, nil)

	sess.AppendOutput(types.StreamStdout, []byte{})

	if len(sess.outputBuffer) != 0 {
		t.Errorf("expected 0 chunks for empty data, got %d", len(sess.outputBuffer))
	}
}

func TestAppendOutputMultipleChunks(t *testing.T) {
	sess := newSession("test", 1024, nil)

	sess.AppendOutput(types.StreamStdout, []byte("chunk1"))
	sess.AppendOutput(types.StreamStdout, []byte("chunk2"))
	sess.AppendOutput(types.StreamStderr, []byte("error"))

	if len(sess.outputBuffer) != 3 {
		t.Errorf("expected 3 chunks, got %d", len(sess.outputBuffer))
	}
}

func TestAppendOutputBufferTruncation(t *testing.T) {
	maxBuffer := 5
	sess := newSession("test", maxBuffer, nil)

	// Add more chunks than the buffer can hold
	for i := 0; i < 10; i++ {
		sess.AppendOutput(types.StreamStdout, []byte("x"))
	}

	if len(sess.outputBuffer) != maxBuffer {
		t.Errorf("expected buffer size %d, got %d", maxBuffer, len(sess.outputBuffer))
	}
}

func TestAppendOutputNoTruncation(t *testing.T) {
	maxBuffer := 10
	sess := newSession("test", maxBuffer, nil)

	// Add fewer chunks than the buffer can hold
	for i := 0; i < 5; i++ {
		sess.AppendOutput(types.StreamStdout, []byte("x"))
	}

	if len(sess.outputBuffer) != 5 {
		t.Errorf("expected 5 chunks, got %d", len(sess.outputBuffer))
	}
}

func TestAppendOutputZeroMaxBuffer(t *testing.T) {
	sess := newSession("test", 0, nil)

	// With zero max buffer, no truncation should occur
	for i := 0; i < 10; i++ {
		sess.AppendOutput(types.StreamStdout, []byte("x"))
	}

	if len(sess.outputBuffer) != 10 {
		t.Errorf("expected 10 chunks with zero max buffer, got %d", len(sess.outputBuffer))
	}
}

// ============================================================================
// AppendOutput - Client Delivery Tests
// ============================================================================

func TestAppendOutputNotSentToHost(t *testing.T) {
	sess := newSession("test", 1024, nil)

	// Host queue should be empty before append
	hostQueue := sess.hostConn.GetAllQueue(types.WSMessageOutput)
	if len(hostQueue) != 0 {
		t.Errorf("expected empty host queue initially, got %d", len(hostQueue))
	}

	sess.AppendOutput(types.StreamStdout, []byte("data"))

	// Host queue should still be empty (host doesn't receive its own output)
	hostQueue = sess.hostConn.GetAllQueue(types.WSMessageOutput)
	if len(hostQueue) != 0 {
		t.Errorf("expected empty host queue after append, got %d", len(hostQueue))
	}
}

// ============================================================================
// EnqueueStdin Tests
// ============================================================================

func TestEnqueueStdin(t *testing.T) {
	sess := newSession("test", 1024, nil)

	data := []byte("stdin data")
	sess.EnqueueStdin(data)

	// Verify stdin was queued for host
	queue := sess.hostConn.GetAllQueue(types.WSMessageStdin)
	if len(queue) != 1 {
		t.Errorf("expected 1 stdin entry in host queue, got %d", len(queue))
	}
}

func TestEnqueueStdinMultipleEntries(t *testing.T) {
	sess := newSession("test", 1024, nil)

	sess.EnqueueStdin([]byte("entry1"))
	sess.EnqueueStdin([]byte("entry2"))
	sess.EnqueueStdin([]byte("entry3"))

	queue := sess.hostConn.GetAllQueue(types.WSMessageStdin)
	if len(queue) != 3 {
		t.Errorf("expected 3 stdin entries, got %d", len(queue))
	}
}

func TestEnqueueStdinDataCopied(t *testing.T) {
	sess := newSession("test", 1024, nil)

	original := []byte("test data")
	sess.EnqueueStdin(original)

	// Modify original
	original[0] = 'X'

	// Queue should have original data
	queue := sess.hostConn.GetAllQueue(types.WSMessageStdin)
	if len(queue) != 1 {
		t.Fatal("expected 1 entry")
	}

	entry, ok := queue[0].(types.StdinEntry)
	if !ok {
		t.Fatal("expected StdinEntry type")
	}

	if entry.Data[0] == 'X' {
		t.Error("data should be copied, not referenced")
	}
}

// ============================================================================
// Client Registration Tests
// ============================================================================

func TestRegisterClient(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, client := sess.RegisterClient(nil)

	if clientID == "" {
		t.Error("expected non-empty client ID")
	}
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	if client.Info.ClientID != clientID {
		t.Error("client ID mismatch")
	}
}

func TestRegisterClientPendingStatus(t *testing.T) {
	sess := newSession("test", 1024, nil)

	_, client := sess.RegisterClient(nil)

	if client.Info.Approval != types.ApprovalPending {
		t.Errorf("expected pending status, got %s", client.Info.Approval)
	}
}

func TestRegisterClientUniqueIDs(t *testing.T) {
	sess := newSession("test", 1024, nil)

	id1, _ := sess.RegisterClient(nil)
	id2, _ := sess.RegisterClient(nil)

	if id1 == id2 {
		t.Error("expected unique client IDs")
	}
}

func TestRegisterClientNotifiesHost(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	// Check host's pending client queue
	queue := sess.hostConn.GetAllQueue(types.WSMessagePendingClient)
	if len(queue) != 1 {
		t.Errorf("expected 1 pending client notification, got %d", len(queue))
	}

	if queue[0] != clientID {
		t.Errorf("expected client ID %s in queue, got %v", clientID, queue[0])
	}
}

// ============================================================================
// Client Approval Tests
// ============================================================================

func TestApproveClient(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	err := sess.ApproveClient(clientID, types.PermissionReadWrite)
	if err != nil {
		t.Fatalf("ApproveClient failed: %v", err)
	}

	client := sess.GetClient(clientID)
	if client.Info.Approval != types.ApprovalApproved {
		t.Error("expected approved status")
	}
	if client.Info.Permission != types.PermissionReadWrite {
		t.Error("expected read-write permission")
	}
}

func TestApproveClientWithReadOnly(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	err := sess.ApproveClient(clientID, types.PermissionReadOnly)
	if err != nil {
		t.Fatalf("ApproveClient failed: %v", err)
	}

	client := sess.GetClient(clientID)
	if client.Info.Permission != types.PermissionReadOnly {
		t.Error("expected read-only permission")
	}
}

func TestApproveClientNotFound(t *testing.T) {
	sess := newSession("test", 1024, nil)

	err := sess.ApproveClient("nonexistent", types.PermissionReadWrite)
	if err == nil {
		t.Error("expected error for nonexistent client")
	}
}

// ============================================================================
// Client Denial Tests
// ============================================================================

func TestDenyClient(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	err := sess.DenyClient(clientID)
	if err != nil {
		t.Fatalf("DenyClient failed: %v", err)
	}

	client := sess.GetClient(clientID)
	if client.Info.Approval != types.ApprovalDenied {
		t.Error("expected denied status")
	}
}

func TestDenyClientNotFound(t *testing.T) {
	sess := newSession("test", 1024, nil)

	err := sess.DenyClient("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent client")
	}
}

// ============================================================================
// GetClient Tests
// ============================================================================

func TestGetClientExists(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	client := sess.GetClient(clientID)
	if client == nil {
		t.Error("expected non-nil client")
	}
	if client.Info.ClientID != clientID {
		t.Error("client ID mismatch")
	}
}

func TestGetClientNotFound(t *testing.T) {
	sess := newSession("test", 1024, nil)

	client := sess.GetClient("nonexistent")
	if client != nil {
		t.Error("expected nil for nonexistent client")
	}
}

func TestGetClientHost(t *testing.T) {
	sess := newSession("test", 1024, nil)

	client := sess.GetClient(types.HostClientID)
	if client == nil {
		t.Fatal("expected non-nil host client")
	}
	if client.Info.ClientID != types.HostClientID {
		t.Error("expected host client ID")
	}
}

// ============================================================================
// Queue Operations Tests
// ============================================================================

func TestPeekClientQueueEmpty(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	queue := sess.PeekClientQueue(clientID, types.WSMessageOutput)
	if len(queue) != 0 {
		t.Errorf("expected empty queue, got %d items", len(queue))
	}
}

func TestClearClientQueue(t *testing.T) {
	sess := newSession("test", 1024, nil)

	_, _ = sess.RegisterClient(nil)

	// Enqueue some data
	sess.EnqueueStdin([]byte("test"))

	// Clear the queue
	sess.ClearClientQueue(types.HostClientID, types.WSMessageStdin)

	// Verify queue is empty
	queue := sess.PeekClientQueue(types.HostClientID, types.WSMessageStdin)
	if len(queue) != 0 {
		t.Errorf("expected empty queue after clear, got %d items", len(queue))
	}
}

// ============================================================================
// Session Completion Tests
// ============================================================================

func TestComplete(t *testing.T) {
	sess := newSession("test", 1024, nil)

	exitCode := 0
	sess.Complete(exitCode)

	if sess.Info.Status != types.SessionStatusCompleted {
		t.Error("expected completed status")
	}
	if sess.Info.CompletedAt == nil {
		t.Error("CompletedAt should be set")
	}
	if sess.Info.ExitCode == nil || *sess.Info.ExitCode != exitCode {
		t.Errorf("expected exit code %d", exitCode)
	}
}

func TestCompleteExitCode(t *testing.T) {
	sess := newSession("test", 1024, nil)

	sess.Complete(42)

	if sess.Info.ExitCode == nil || *sess.Info.ExitCode != 42 {
		t.Error("exit code mismatch")
	}
}

func TestCompleteTimestamp(t *testing.T) {
	sess := newSession("test", 1024, nil)

	before := time.Now()
	sess.Complete(0)
	after := time.Now()

	if sess.Info.CompletedAt == nil {
		t.Fatal("CompletedAt should be set")
	}

	if sess.Info.CompletedAt.Before(before) || sess.Info.CompletedAt.After(after) {
		t.Error("CompletedAt timestamp out of range")
	}
}

// ============================================================================
// Client Activity Tracking Tests
// ============================================================================

func TestRemoveInactiveClients(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	// Set last poll time to past
	client := sess.GetClient(clientID)
	client.Info.LastPollAt = time.Now().Add(-2 * time.Hour)

	// Remove inactive clients with 1 hour timeout
	removed := sess.RemoveInactiveClients(1 * time.Hour)

	if len(removed) != 1 {
		t.Errorf("expected 1 removed client, got %d", len(removed))
	}
	if removed[0] != clientID {
		t.Errorf("expected removed client ID %s, got %s", clientID, removed[0])
	}

	// Verify client is gone
	if sess.GetClient(clientID) != nil {
		t.Error("client should be removed")
	}
}

func TestRemoveInactiveClientsNoneRemoved(t *testing.T) {
	sess := newSession("test", 1024, nil)

	clientID, _ := sess.RegisterClient(nil)

	// Client is active (just registered)
	removed := sess.RemoveInactiveClients(1 * time.Hour)

	if len(removed) != 0 {
		t.Errorf("expected 0 removed clients, got %d", len(removed))
	}

	// Verify client still exists
	if sess.GetClient(clientID) == nil {
		t.Error("client should not be removed")
	}
}

func TestRemoveInactiveClientsMultiple(t *testing.T) {
	sess := newSession("test", 1024, nil)

	// Register multiple clients
	id1, _ := sess.RegisterClient(nil)
	id2, _ := sess.RegisterClient(nil)
	id3, _ := sess.RegisterClient(nil)

	// Make two inactive
	sess.GetClient(id1).Info.LastPollAt = time.Now().Add(-2 * time.Hour)
	sess.GetClient(id2).Info.LastPollAt = time.Now().Add(-2 * time.Hour)

	removed := sess.RemoveInactiveClients(1 * time.Hour)

	if len(removed) != 2 {
		t.Errorf("expected 2 removed clients, got %d", len(removed))
	}

	// Verify active client still exists
	if sess.GetClient(id3) == nil {
		t.Error("active client should not be removed")
	}
}

// ============================================================================
// Concurrent Operations Tests
// ============================================================================

func TestConcurrentAppendOutput(t *testing.T) {
	sess := newSession("test", 1024, nil)

	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.AppendOutput(types.StreamStdout, []byte("x"))
		}()
	}

	wg.Wait()

	if len(sess.outputBuffer) != numGoroutines {
		t.Errorf("expected %d chunks, got %d", numGoroutines, len(sess.outputBuffer))
	}
}

func TestConcurrentEnqueueStdin(t *testing.T) {
	sess := newSession("test", 1024, nil)

	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess.EnqueueStdin([]byte("x"))
		}()
	}

	wg.Wait()

	queue := sess.hostConn.GetAllQueue(types.WSMessageStdin)
	if len(queue) != numGoroutines {
		t.Errorf("expected %d stdin entries, got %d", numGoroutines, len(queue))
	}
}

func TestConcurrentClientRegistration(t *testing.T) {
	sess := newSession("test", 1024, nil)

	var wg sync.WaitGroup
	numGoroutines := 10
	clientIDs := make([]string, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id, _ := sess.RegisterClient(nil)
			clientIDs[idx] = id
		}(i)
	}

	wg.Wait()

	// Verify all IDs are unique
	idMap := make(map[string]bool)
	for _, id := range clientIDs {
		if idMap[id] {
			t.Errorf("duplicate client ID: %s", id)
		}
		idMap[id] = true
	}

	if len(idMap) != numGoroutines {
		t.Errorf("expected %d unique IDs, got %d", numGoroutines, len(idMap))
	}
}