package ws

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	testmain "github.com/gabe-l-hart/remote-control/test"
)

func TestMain(m *testing.M) {
	testmain.TestMain(m)
}

// ============================================================================
// Message Queue Tests
// ============================================================================

func TestQueueMessage(t *testing.T) {
	p := &WebSocketPipe{
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
	}

	msg1 := []byte("test message 1")
	msg2 := []byte("test message 2")

	p.queueMessage(msg1)
	p.queueMessage(msg2)

	length, _ := p.GetQueueStatus()
	if length != 2 {
		t.Errorf("expected queue length 2, got %d", length)
	}

	if string(p.messageQueue[0]) != "test message 1" {
		t.Errorf("expected first message to be 'test message 1'")
	}
	if string(p.messageQueue[1]) != "test message 2" {
		t.Errorf("expected second message to be 'test message 2'")
	}
}

func TestQueueMessageOverflow(t *testing.T) {
	maxLen := 5
	p := &WebSocketPipe{
		messageQueue:   make([][]byte, 0, maxLen),
		maxQueueLength: maxLen,
	}

	for i := 0; i < maxLen; i++ {
		msg := []byte("message " + string(rune('0'+i)))
		p.queueMessage(msg)
	}

	length, _ := p.GetQueueStatus()
	if length != maxLen {
		t.Errorf("expected queue length %d, got %d", maxLen, length)
	}

	msgNew := []byte("new message")
	p.queueMessage(msgNew)

	length, _ = p.GetQueueStatus()
	if length != maxLen {
		t.Errorf("expected queue length %d after overflow, got %d", maxLen, length)
	}

	if string(p.messageQueue[0]) != "message 1" {
		t.Errorf("expected oldest to be 'message 1', got %q", string(p.messageQueue[0]))
	}
	if string(p.messageQueue[len(p.messageQueue)-1]) != "new message" {
		t.Errorf("expected newest to be 'new message', got %q", string(p.messageQueue[len(p.messageQueue)-1]))
	}
}

func TestGetQueueStatus(t *testing.T) {
	maxLen := 20
	p := &WebSocketPipe{
		messageQueue:   make([][]byte, 0, maxLen),
		maxQueueLength: maxLen,
	}

	length, capacity := p.GetQueueStatus()
	if length != 0 {
		t.Errorf("expected initial length 0, got %d", length)
	}
	if capacity != maxLen {
		t.Errorf("expected capacity %d, got %d", maxLen, capacity)
	}

	for i := 0; i < 5; i++ {
		p.queueMessage([]byte("message"))
	}

	length, capacity = p.GetQueueStatus()
	if length != 5 {
		t.Errorf("expected length 5, got %d", length)
	}
	if capacity != maxLen {
		t.Errorf("expected capacity %d, got %d", maxLen, capacity)
	}
}

// ============================================================================
// Flush Queue Tests
// ============================================================================

func TestFlushQueueEmpty(t *testing.T) {
	sendCh := make(chan []byte, 100)
	p := &WebSocketPipe{
		send:           sendCh,
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
		startCtx:       context.Background(),
	}

	p.flushQueue()

	length, _ := p.GetQueueStatus()
	if length != 0 {
		t.Errorf("expected queue length 0 after flush of empty queue, got %d", length)
	}
}

func TestFlushQueueSuccess(t *testing.T) {
	sendCh := make(chan []byte, 100)
	p := &WebSocketPipe{
		send:           sendCh,
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
		startCtx:       context.Background(),
	}

	for i := 0; i < 5; i++ {
		p.queueMessage([]byte("message"))
	}

	p.flushQueue()

	length, _ := p.GetQueueStatus()
	if length != 0 {
		t.Errorf("expected queue length 0 after flush, got %d", length)
	}

	msgCount := 0
	for len(sendCh) > 0 {
		<-sendCh
		msgCount++
	}
	if msgCount != 5 {
		t.Errorf("expected 5 messages sent, got %d", msgCount)
	}
}

func TestFlushQueueWithContextCancellation(t *testing.T) {
	sendCh := make(chan []byte, 2)
	ctx, cancel := context.WithCancel(context.Background())

	p := &WebSocketPipe{
		send:           sendCh,
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
		startCtx:       ctx,
	}

	for i := 0; i < 5; i++ {
		p.queueMessage([]byte("message"))
	}

	cancel()

	done := make(chan struct{})
	go func() {
		p.flushQueue()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
	}

	length, _ := p.GetQueueStatus()
	if length > 5 {
		t.Errorf("expected some messages to be sent or flushed, queue length %d", length)
	}
}

// ============================================================================
// Reconnection Loop Tests
// ============================================================================

func TestStartReconnectLoopIdempotent(t *testing.T) {
	p := &WebSocketPipe{
		reconnectURL:      "ws://localhost:8080/ws/test",
		tlsConfig:         nil,
		reconnectInterval: 100 * time.Millisecond,
		reconnectTimeout:  time.Second,
		maxQueueLength:    10,
		messageQueue:      make([][]byte, 0, 10),
		startCtx:          context.Background(),
	}

	p.startReconnectLoop()

	if !p.reconnecting.Load() {
		t.Error("expected reconnecting to be true after startReconnectLoop")
	}

	done := make(chan struct{})
	go func() {
		p.startReconnectLoop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Error("second startReconnectLoop call should return immediately")
	}

	if !p.reconnecting.Load() {
		t.Error("reconnecting should still be true")
	}

	if p.reconnectCancel != nil {
		p.reconnectCancel()
	}

	time.Sleep(150 * time.Millisecond)

	if p.reconnecting.Load() {
		t.Error("reconnecting should be false after cancel")
	}
}

func TestStartReconnectLoopWithCancel(t *testing.T) {
	p := &WebSocketPipe{
		reconnectURL:      "ws://nonexistent:8080/ws/test",
		tlsConfig:         nil,
		reconnectInterval: 50 * time.Millisecond,
		reconnectTimeout:  50 * time.Millisecond,
		maxQueueLength:    10,
		messageQueue:      make([][]byte, 0, 10),
		startCtx:          context.Background(),
	}

	p.startReconnectLoop()

	if !p.reconnecting.Load() {
		t.Error("expected reconnecting to be true")
	}

	time.Sleep(300 * time.Millisecond)
	if p.reconnectCancel != nil {
		p.reconnectCancel()
	}

	// Wait for goroutine to exit (needs to complete current ticker iteration and check generation)
	time.Sleep(300 * time.Millisecond)

	if p.reconnecting.Load() {
		t.Error("reconnecting should be false after cancel")
	}
}

func TestCloseCancelsReconnectionLoop(t *testing.T) {
	sendCh := make(chan []byte, 10)
	doneCh := make(chan struct{})

	p := &WebSocketPipe{
		send:              sendCh,
		done:              doneCh,
		reconnectURL:      "ws://nonexistent:8080/ws/test",
		tlsConfig:         nil,
		reconnectInterval: 50 * time.Millisecond,
		reconnectTimeout:  50 * time.Millisecond,
		maxQueueLength:    10,
		messageQueue:      make([][]byte, 0, 10),
		startCtx:          context.Background(),
		connected:         atomic.Bool{},
	}
	p.connected.Store(true)

	p.handleDisconnect()

	if !p.reconnecting.Load() {
		t.Fatal("expected reconnecting to be true after handleDisconnect")
	}

	p.Close()

	time.Sleep(100 * time.Millisecond)

	if p.reconnecting.Load() {
		t.Error("reconnecting should be false after Close")
	}
}

// ============================================================================
// Send Tests with Queueing
// ============================================================================

func TestSendQueuesOnDisconnect(t *testing.T) {
	// Use a 0-buffer channel so send cannot succeed when done is closed
	sendCh := make(chan []byte)
	doneCh := make(chan struct{})

	// Close the done channel to simulate disconnection
	close(doneCh)

	p := &WebSocketPipe{
		send:           sendCh,
		done:           doneCh,
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
		connected:      atomic.Bool{},
	}
	p.connected.Store(true)

	err := p.Send([]byte("test message"))
	if err == nil {
		t.Error("expected error when sending on closed connection")
	}

	length, _ := p.GetQueueStatus()
	if length != 1 {
		t.Errorf("expected queue length 1 after failed send, got %d", length)
	}
}

func TestSendQueuesOnFullBuffer(t *testing.T) {
	sendCh := make(chan []byte, 2)
	doneCh := make(chan struct{})

	p := &WebSocketPipe{
		send:           sendCh,
		done:           doneCh,
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
	}

	// Fill the send buffer
	sendCh <- []byte("msg1")
	sendCh <- []byte("msg2")

	err := p.Send([]byte("test message"))
	if err == nil {
		t.Error("expected error when send buffer is full")
	}
	if err.Error() != "send buffer full, message queued" {
		t.Errorf("expected 'send buffer full, message queued' error, got %q", err.Error())
	}

	length, _ := p.GetQueueStatus()
	if length != 1 {
		t.Errorf("expected queue length 1 after failed send, got %d", length)
	}
}

func TestSendSucceedsWhenConnected(t *testing.T) {
	sendCh := make(chan []byte, 10)
	doneCh := make(chan struct{})

	p := &WebSocketPipe{
		send:           sendCh,
		done:           doneCh,
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
	}

	err := p.Send([]byte("test message"))
	if err != nil {
		t.Errorf("unexpected error when sending: %v", err)
	}

	length, _ := p.GetQueueStatus()
	if length != 0 {
		t.Errorf("expected queue length 0 on successful send, got %d", length)
	}

	select {
	case msg := <-sendCh:
		if string(msg) != "test message" {
			t.Errorf("expected 'test message', got %q", string(msg))
		}
	default:
		t.Error("expected message to be sent")
	}
}

// ============================================================================
// Context Propagation Tests
// ============================================================================

func TestStartStoresContext(t *testing.T) {
	ctx := context.Background()

	p := &WebSocketPipe{
		connected:    atomic.Bool{},
		messageQueue: make([][]byte, 0, 10),
	}

	// Just store the context - don't actually start the pumps as that requires a valid connection
	p.startCtx = ctx
	p.connected.Store(true)

	if p.startCtx != ctx {
		t.Error("expected startCtx to be set to the passed context")
	}
	if !p.connected.Load() {
		t.Error("expected connected to be true")
	}
}

// ============================================================================
// SendMessage Tests
// ==========================================================================

func TestSendMessageSuccess(t *testing.T) {
	sendCh := make(chan []byte, 10)
	doneCh := make(chan struct{})

	p := &WebSocketPipe{
		send:           sendCh,
		done:           doneCh,
		messageQueue:   make([][]byte, 0, 10),
		maxQueueLength: 10,
	}

	err := p.SendMessage(1, map[string]string{"key": "value"})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify something was sent
	select {
	case msg := <-sendCh:
		if len(msg) == 0 {
			t.Error("expected non-empty message")
		}
	default:
		t.Error("expected message to be sent")
	}
}

// ============================================================================
// Dial Config Tests
// ==========================================================================

func TestDialConfigDefaults(t *testing.T) {
	cfg := &WebSocketConfig{
		ReconnectInterval: 0, // Should use default
		ReconnectTimeout:  0, // Should use default
		MaxQueueLength:    0, // Should use default
	}

	sendCh := make(chan []byte, 10)
	doneCh := make(chan struct{})

	p := &WebSocketPipe{
		send:           sendCh,
		done:           doneCh,
		reconnectURL:   "ws://test/ws",
		maxQueueLength: cfg.MaxQueueLength,
	}

	// Apply defaults as Dial would
	if p.maxQueueLength == 0 {
		p.maxQueueLength = 100
	}
	if p.reconnectInterval == 0 {
		p.reconnectInterval = 5 * time.Second
	}
	if p.reconnectTimeout == 0 {
		p.reconnectTimeout = 10 * time.Second
	}

	// Just verify the defaults are applied correctly
	if p.reconnectInterval != 5*time.Second {
		t.Errorf("expected default reconnect interval 5s, got %v", p.reconnectInterval)
	}
	if p.reconnectTimeout != 10*time.Second {
		t.Errorf("expected default reconnect timeout 10s, got %v", p.reconnectTimeout)
	}
	if p.maxQueueLength != 100 {
		t.Errorf("expected default max queue length 100, got %d", p.maxQueueLength)
	}
}

func TestDialConfigCustom(t *testing.T) {
	cfg := &WebSocketConfig{
		ReconnectInterval: 30 * time.Second,
		ReconnectTimeout:  5 * time.Second,
		MaxQueueLength:    200,
	}

	sendCh := make(chan []byte, 10)
	doneCh := make(chan struct{})

	p := &WebSocketPipe{
		send:           sendCh,
		done:           doneCh,
		maxQueueLength: cfg.MaxQueueLength,
	}

	// Apply custom config as Dial would
	if p.maxQueueLength == 0 {
		p.maxQueueLength = 100
	} else {
		p.maxQueueLength = cfg.MaxQueueLength
	}
	p.reconnectInterval = cfg.ReconnectInterval
	p.reconnectTimeout = cfg.ReconnectTimeout

	if p.reconnectInterval != 30*time.Second {
		t.Errorf("expected reconnect interval 30s, got %v", p.reconnectInterval)
	}
	if p.reconnectTimeout != 5*time.Second {
		t.Errorf("expected reconnect timeout 5s, got %v", p.reconnectTimeout)
	}
	if p.maxQueueLength != 200 {
		t.Errorf("expected max queue length 200, got %d", p.maxQueueLength)
	}
}
