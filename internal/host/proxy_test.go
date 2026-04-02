package host

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// mockWriter captures writes for inspection in tests.
type mockWriter struct {
	mu   sync.Mutex
	data []byte
}

func (m *mockWriter) Write(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append(m.data, p...)
	return len(p), nil
}

func (m *mockWriter) bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]byte, len(m.data))
	copy(result, m.data)
	return result
}

// mockWriteCloser wraps a writer to implement io.WriteCloser.
type mockWriteCloser struct {
	io.Writer
}

func (m *mockWriteCloser) Close() error { return nil }

func TestSyncWriterConcurrentWrites(t *testing.T) {
	mock := &mockWriter{}
	sw := &syncWriter{w: &mockWriteCloser{mock}}

	const goroutines = 50
	const writesPerGoroutine = 10

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < writesPerGoroutine; j++ {
				_, _ = sw.Write([]byte("x"))
			}
		}()
	}
	wg.Wait()

	data := mock.bytes()
	if len(data) != goroutines*writesPerGoroutine {
		t.Errorf("expected %d bytes, got %d", goroutines*writesPerGoroutine, len(data))
	}
}

func TestProxyOutputWritesLocally(t *testing.T) {
	pr, pw := io.Pipe()

	localDst := &mockWriter{}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := &Host{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Pass nil client and nil WebSocketHost — proxyOutput will log the append error but continue writing locally.
		h.proxyOutput(ctx, pr, localDst, nil, "test-session", types.StreamStdout, nil)
	}()

	pw.Write([]byte("hello world")) //nolint:errcheck
	time.Sleep(50 * time.Millisecond)
	pw.Close()
	<-done

	got := string(localDst.bytes())
	if got != "hello world" {
		t.Errorf("expected 'hello world', got %q", got)
	}
}

func TestApprovalPauseResume(t *testing.T) {
	h := &Host{}

	// Verify approval is not active initially.
	h.approvalMu.Lock()
	if h.approvalActive {
		t.Error("expected approvalActive to be false initially")
	}
	h.approvalMu.Unlock()

	// Set active manually to simulate the mutex behavior.
	h.approvalMu.Lock()
	h.approvalActive = true
	h.approvalMu.Unlock()

	h.approvalMu.Lock()
	active := h.approvalActive
	h.approvalMu.Unlock()
	if !active {
		t.Error("expected approvalActive to be true after setting")
	}

	// Clear it.
	h.approvalMu.Lock()
	h.approvalActive = false
	h.approvalMu.Unlock()

	h.approvalMu.Lock()
	active = h.approvalActive
	h.approvalMu.Unlock()
	if active {
		t.Error("expected approvalActive to be false after clearing")
	}
}
