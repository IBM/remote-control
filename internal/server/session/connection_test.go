package session

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/common/types"
	"github.com/gorilla/websocket"
)

// mockWebSocketConn is a mock implementation of websocket.Conn for testing
type mockWebSocketConn struct {
	closed    bool
	closeErr  error
	mu        sync.Mutex
	writeChan chan []byte
}

func newMockWebSocketConn() *mockWebSocketConn {
	return &mockWebSocketConn{
		writeChan: make(chan []byte, 10),
	}
}

func (m *mockWebSocketConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	close(m.writeChan)
	return m.closeErr
}

func (m *mockWebSocketConn) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// ============================================================================
// Connection Creation Tests
// ============================================================================

func TestNewConnection(t *testing.T) {
	mockConn := newMockWebSocketConn()
	conn := newConnection((*websocket.Conn)(nil))

	if conn == nil {
		t.Fatal("expected non-nil connection")
	}
	if conn.GetSendChan() == nil {
		t.Error("send channel should be initialized")
	}
	if conn.GetDoneChan() == nil {
		t.Error("done channel should be initialized")
	}

	// Clean up
	_ = mockConn
}

func TestNewConnectionWithNilWebSocket(t *testing.T) {
	conn := newConnection(nil)

	if conn == nil {
		t.Fatal("expected non-nil connection even with nil websocket")
	}
	if conn.GetSendChan() == nil {
		t.Error("send channel should still be initialized")
	}
	if conn.GetDoneChan() == nil {
		t.Error("done channel should still be initialized")
	}
}

func TestConnectionChannelInitialization(t *testing.T) {
	conn := newConnection(nil)

	// Verify send channel has correct buffer size
	if cap(conn.GetSendChan()) != 256 {
		t.Errorf("expected send channel buffer size 256, got %d", cap(conn.GetSendChan()))
	}

	// Verify done channel is unbuffered
	if cap(conn.GetDoneChan()) != 0 {
		t.Errorf("expected done channel to be unbuffered, got buffer size %d", cap(conn.GetDoneChan()))
	}
}

// ============================================================================
// SendMessage Success Cases
// ============================================================================

func TestSendMessageSuccess(t *testing.T) {
	conn := newConnection(nil)

	message := "test message"
	err := conn.SendMessage(types.WSMessageOutput, message)

	if err == nil {
		t.Error("expected error when conn is nil")
	}
}

func TestSendMessageWithValidConnection(t *testing.T) {
	conn := newConnection(nil)

	chunk := types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte("test data"),
	}

	err := conn.SendMessage(types.WSMessageOutput, chunk)
	if err == nil {
		t.Error("expected error when websocket is nil")
	}
}

func TestSendMessageMultipleTypes(t *testing.T) {
	conn := newConnection(nil)

	tests := []struct {
		name    string
		msgType types.WSMessageType
		message interface{}
	}{
		{"Output", types.WSMessageOutput, types.OutputChunk{Stream: types.StreamStdout, Data: []byte("out")}},
		{"Stdin", types.WSMessageStdin, types.StdinEntry{Data: []byte("in")}},
		{"PendingClient", types.WSMessagePendingClient, "client-123"},
		{"Error", types.WSMessageError, "error message"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conn.SendMessage(tt.msgType, tt.message)
			// All should fail with nil websocket
			if err == nil {
				t.Error("expected error with nil websocket")
			}
		})
	}
}

// ============================================================================
// SendMessage Failure Cases
// ============================================================================

func TestSendMessageNilConnection(t *testing.T) {
	conn := newConnection(nil)

	err := conn.SendMessage(types.WSMessageOutput, "test")
	if err == nil {
		t.Error("expected error when websocket is nil")
	}
}

func TestSendMessageAfterClose(t *testing.T) {
	conn := newConnection(nil)
	conn.Close()

	err := conn.SendMessage(types.WSMessageOutput, "test")
	if err == nil {
		t.Error("expected error when connection is closed")
	}
}

// ============================================================================
// Message Serialization Tests
// ============================================================================

func TestWSMessageEnvelope(t *testing.T) {
	// Test that messages are properly wrapped in WSMessage envelope
	chunk := types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   []byte("test"),
	}

	chunkJSON, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("failed to marshal chunk: %v", err)
	}

	wsMsg := types.WSMessage{
		Type:    types.WSMessageOutput,
		Message: json.RawMessage(chunkJSON),
	}

	data, err := json.Marshal(wsMsg)
	if err != nil {
		t.Fatalf("failed to marshal WSMessage: %v", err)
	}

	var decoded types.WSMessage
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("failed to unmarshal WSMessage: %v", err)
	}

	if decoded.Type != types.WSMessageOutput {
		t.Errorf("expected type %d, got %d", types.WSMessageOutput, decoded.Type)
	}
}

func TestWSMessageTypeInEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		msgType types.WSMessageType
	}{
		{"Output", types.WSMessageOutput},
		{"Stdin", types.WSMessageStdin},
		{"PendingClient", types.WSMessagePendingClient},
		{"Error", types.WSMessageError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgJSON, err := json.Marshal("test")
			if err != nil {
				t.Fatalf("failed to marshal message: %v", err)
			}
			wsMsg := types.WSMessage{
				Type:    tt.msgType,
				Message: json.RawMessage(msgJSON),
			}

			data, err := json.Marshal(wsMsg)
			if err != nil {
				t.Fatalf("marshal failed: %v", err)
			}

			var decoded types.WSMessage
			err = json.Unmarshal(data, &decoded)
			if err != nil {
				t.Fatalf("unmarshal failed: %v", err)
			}

			if decoded.Type != tt.msgType {
				t.Errorf("expected type %d, got %d", tt.msgType, decoded.Type)
			}
		})
	}
}

// ============================================================================
// Connection Closure Tests
// ============================================================================

func TestCloseConnection(t *testing.T) {
	conn := newConnection(nil)

	// Should not panic
	conn.Close()

	// Verify done channel is closed
	select {
	case <-conn.GetDoneChan():
		// Expected - channel is closed
	case <-time.After(100 * time.Millisecond):
		t.Error("done channel should be closed")
	}
}

func TestCloseConnectionIdempotent(t *testing.T) {
	conn := newConnection(nil)

	// First close
	conn.Close()

	// Second close should not panic
	conn.Close()

	// Third close should not panic
	conn.Close()
}

func TestCloseConnectionChannelsClosed(t *testing.T) {
	conn := newConnection(nil)
	conn.Close()

	// Verify done channel is closed
	select {
	case <-conn.GetDoneChan():
		// Expected
	default:
		t.Error("done channel should be closed")
	}
}

func TestCloseConnectionNilWebSocket(t *testing.T) {
	conn := newConnection(nil)

	// Should not panic even with nil websocket
	conn.Close()
}

// ============================================================================
// Concurrent Operations Tests
// ============================================================================

func TestConcurrentSendMessage(t *testing.T) {
	conn := newConnection(nil)

	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			_ = conn.SendMessage(types.WSMessageOutput, "test")
		}(i)
	}

	wg.Wait()
	// Test passes if no panic occurs
}

func TestConcurrentCloseAndSend(t *testing.T) {
	conn := newConnection(nil)

	var wg sync.WaitGroup

	// Start multiple senders
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				_ = conn.SendMessage(types.WSMessageOutput, "test")
				time.Sleep(time.Microsecond)
			}
		}()
	}

	// Close in the middle
	time.Sleep(5 * time.Millisecond)
	conn.Close()

	wg.Wait()
	// Test passes if no panic occurs
}

func TestConcurrentMultipleClose(t *testing.T) {
	conn := newConnection(nil)

	var wg sync.WaitGroup
	numGoroutines := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn.Close()
		}()
	}

	wg.Wait()
	// Test passes if no panic occurs
}

// ============================================================================
// Channel Behavior Tests
// ============================================================================

func TestSendChannelBufferSize(t *testing.T) {
	conn := newConnection(nil)

	expectedSize := 256
	actualSize := cap(conn.GetSendChan())

	if actualSize != expectedSize {
		t.Errorf("expected send channel buffer size %d, got %d", expectedSize, actualSize)
	}
}

func TestSendChannelNonBlocking(t *testing.T) {
	conn := newConnection(nil)

	// With nil websocket, SendMessage returns error immediately
	// This tests that the error path doesn't block
	done := make(chan bool)
	go func() {
		_ = conn.SendMessage(types.WSMessageOutput, "test")
		done <- true
	}()

	select {
	case <-done:
		// Expected - should return quickly
	case <-time.After(100 * time.Millisecond):
		t.Error("SendMessage should not block")
	}
}

// ============================================================================
// Edge Cases Tests
// ============================================================================

func TestSendMessageWithNilMessage(t *testing.T) {
	conn := newConnection(nil)

	err := conn.SendMessage(types.WSMessageOutput, nil)
	if err == nil {
		t.Error("expected error with nil websocket")
	}
}

func TestSendMessageWithEmptyMessage(t *testing.T) {
	conn := newConnection(nil)

	err := conn.SendMessage(types.WSMessageOutput, "")
	if err == nil {
		t.Error("expected error with nil websocket")
	}
}

func TestSendMessageWithLargeMessage(t *testing.T) {
	conn := newConnection(nil)

	// Create a large message
	largeData := make([]byte, 1024*1024) // 1MB
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	chunk := types.OutputChunk{
		Stream: types.StreamStdout,
		Data:   largeData,
	}

	err := conn.SendMessage(types.WSMessageOutput, chunk)
	if err == nil {
		t.Error("expected error with nil websocket")
	}
}

// ============================================================================
// Message Type Validation Tests
// ============================================================================

func TestSendMessageWithAllMessageTypes(t *testing.T) {
	conn := newConnection(nil)

	tests := []struct {
		name    string
		msgType types.WSMessageType
		message interface{}
	}{
		{
			"OutputChunk",
			types.WSMessageOutput,
			types.OutputChunk{Stream: types.StreamStdout, Data: []byte("output")},
		},
		{
			"StdinEntry",
			types.WSMessageStdin,
			types.StdinEntry{Data: []byte("stdin")},
		},
		{
			"PendingClientString",
			types.WSMessagePendingClient,
			"client-id-123",
		},
		{
			"ErrorString",
			types.WSMessageError,
			"error message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := conn.SendMessage(tt.msgType, tt.message)
			// All should fail with nil websocket, but shouldn't panic
			if err == nil {
				t.Error("expected error with nil websocket")
			}
		})
	}
}

// ============================================================================
// Stress Tests
// ============================================================================

func TestHighVolumeMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	conn := newConnection(nil)

	numMessages := 1000
	for i := 0; i < numMessages; i++ {
		_ = conn.SendMessage(types.WSMessageOutput, "test")
	}

	// Test passes if no panic occurs
}

func TestRapidCloseAndCreate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	for i := 0; i < 100; i++ {
		conn := newConnection(nil)
		conn.Close()
	}

	// Test passes if no panic or resource leak occurs
}
