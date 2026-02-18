package session

import (
	"sync"
	"testing"
	"time"
)

func TestAppendAndReadOutput(t *testing.T) {
	s := newSession("test-1", []string{"bash"})

	t1 := time.Now()
	s.AppendOutput(StreamStdout, []byte("hello"), t1)
	t2 := t1.Add(time.Millisecond)
	s.AppendOutput(StreamStdout, []byte(" world"), t2)
	s.AppendOutput(StreamStderr, []byte("err1"), t1)

	// Read all stdout from offset 0
	chunks := s.ReadOutput(StreamStdout, 0)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 stdout chunks, got %d", len(chunks))
	}
	if string(chunks[0].Data) != "hello" || chunks[0].Offset != 0 {
		t.Errorf("unexpected first chunk: %+v", chunks[0])
	}
	if string(chunks[1].Data) != " world" || chunks[1].Offset != 5 {
		t.Errorf("unexpected second chunk: %+v", chunks[1])
	}

	// Stream labels preserved
	for _, ch := range chunks {
		if ch.Stream != StreamStdout {
			t.Errorf("expected stdout stream label, got %q", ch.Stream)
		}
	}

	// Read stderr
	errChunks := s.ReadOutput(StreamStderr, 0)
	if len(errChunks) != 1 || errChunks[0].Stream != StreamStderr {
		t.Errorf("unexpected stderr chunks: %+v", errChunks)
	}
}

func TestReadOutputFromOffset(t *testing.T) {
	s := newSession("test-2", []string{"bash"})
	now := time.Now()
	s.AppendOutput(StreamStdout, []byte("hello"), now)
	s.AppendOutput(StreamStdout, []byte(" world"), now.Add(time.Millisecond))

	// Read from offset 5 (start of second chunk)
	chunks := s.ReadOutput(StreamStdout, 5)
	if len(chunks) != 1 || string(chunks[0].Data) != " world" {
		t.Errorf("expected second chunk from offset 5, got %+v", chunks)
	}

	// Read from offset 3 (within first chunk)
	chunks = s.ReadOutput(StreamStdout, 3)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks from offset 3, got %d", len(chunks))
	}
	if string(chunks[0].Data) != "lo" || chunks[0].Offset != 3 {
		t.Errorf("expected trimmed first chunk 'lo' at offset 3, got %+v", chunks[0])
	}
}

func TestConcurrentAppendOutput(t *testing.T) {
	s := newSession("test-3", []string{"bash"})
	var wg sync.WaitGroup
	writes := 100

	for i := 0; i < writes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.AppendOutput(StreamStdout, []byte("x"), time.Now())
		}()
	}
	wg.Wait()

	chunks := s.ReadOutput(StreamStdout, 0)
	totalBytes := int64(0)
	for _, ch := range chunks {
		totalBytes += int64(len(ch.Data))
	}
	if totalBytes != int64(writes) {
		t.Errorf("expected %d bytes total, got %d", writes, totalBytes)
	}
	// Verify offsets are correct (no overlaps, monotonically increasing).
	var prevEnd int64
	for _, ch := range chunks {
		if ch.Offset != prevEnd {
			t.Errorf("offset gap: expected %d, got %d", prevEnd, ch.Offset)
		}
		prevEnd = ch.Offset + int64(len(ch.Data))
	}
}

func TestStdinFIFO(t *testing.T) {
	s := newSession("test-4", []string{"bash"})
	now := time.Now()

	s.EnqueueStdin(StdinEntry{ID: "1", Source: "client-a", Data: []byte("ls\n"), Status: StdinPending, Timestamp: now})
	s.EnqueueStdin(StdinEntry{ID: "2", Source: "client-b", Data: []byte("pwd\n"), Status: StdinPending, Timestamp: now.Add(time.Millisecond)})

	first := s.DequeueStdin()
	if first == nil || first.ID != "1" {
		t.Errorf("expected first entry ID=1, got %+v", first)
	}
	second := s.DequeueStdin()
	if second == nil || second.ID != "2" {
		t.Errorf("expected second entry ID=2, got %+v", second)
	}
	empty := s.DequeueStdin()
	if empty != nil {
		t.Errorf("expected nil from empty queue, got %+v", empty)
	}
}

func TestComplete(t *testing.T) {
	s := newSession("test-5", []string{"bash"})
	if s.GetStatus() != StatusActive {
		t.Errorf("expected active status initially")
	}
	s.Complete(0)
	if s.GetStatus() != StatusCompleted {
		t.Errorf("expected completed status")
	}
	if s.ExitCode == nil || *s.ExitCode != 0 {
		t.Errorf("expected exit code 0")
	}
	if s.CompletedAt == nil {
		t.Errorf("expected CompletedAt to be set")
	}
}

func TestStdinAcceptReject(t *testing.T) {
	s := newSession("test-6", []string{"bash"})
	now := time.Now()
	s.EnqueueStdin(StdinEntry{ID: "a", Source: "client", Data: []byte("ls\n"), Status: StdinPending, Timestamp: now})
	s.EnqueueStdin(StdinEntry{ID: "b", Source: "client", Data: []byte("pwd\n"), Status: StdinPending, Timestamp: now.Add(time.Millisecond)})

	// PeekStdin returns first pending without removing.
	entry := s.PeekStdin()
	if entry == nil || entry.ID != "a" {
		t.Errorf("expected peek to return 'a', got %+v", entry)
	}
	// Still there after peek.
	entry2 := s.PeekStdin()
	if entry2 == nil || entry2.ID != "a" {
		t.Errorf("expected peek to still return 'a', got %+v", entry2)
	}

	// Accept.
	acceptTime := now.Add(time.Second)
	if err := s.AcceptStdin("a", acceptTime); err != nil {
		t.Errorf("unexpected error accepting: %v", err)
	}
	status, err := s.GetStdinStatus("a")
	if err != nil || status != StdinAccepted {
		t.Errorf("expected accepted status, got %v, err=%v", status, err)
	}

	// RejectAllPending.
	ids := s.RejectAllPending()
	if len(ids) != 1 || ids[0] != "b" {
		t.Errorf("expected ['b'] rejected, got %v", ids)
	}
	status, _ = s.GetStdinStatus("b")
	if status != StdinRejected {
		t.Errorf("expected rejected status for 'b'")
	}
}

func TestStreamOffsets(t *testing.T) {
	s := newSession("test-7", []string{"bash"})
	now := time.Now()
	s.AppendOutput(StreamStdout, []byte("abc"), now)
	s.AppendOutput(StreamStdout, []byte("de"), now.Add(time.Millisecond))
	s.AppendOutput(StreamStderr, []byte("err"), now)

	if got := s.StreamOffset(StreamStdout); got != 5 {
		t.Errorf("expected stdout offset 5, got %d", got)
	}
	if got := s.StreamOffset(StreamStderr); got != 3 {
		t.Errorf("expected stderr offset 3, got %d", got)
	}
}
