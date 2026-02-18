package session

import (
	"sync"
	"time"
)

// Session holds all state for a single remote-control session.
type Session struct {
	ID      string   `json:"id"`
	Command []string `json:"command"`
	Status  Status   `json:"status"`

	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`

	mu sync.RWMutex

	// Output buffers indexed by stream.
	stdoutChunks []OutputChunk
	stderrChunks []OutputChunk

	// stdoutOffset and stderrOffset track total bytes appended for each stream.
	stdoutOffset int64
	stderrOffset int64

	// stdin is the ordered queue of all stdin entries (including history).
	stdin []*StdinEntry

	// Approval state (populated in Phase 7).
	clients map[string]*ClientRecord
}

func newSession(id string, command []string) *Session {
	return &Session{
		ID:        id,
		Command:   command,
		Status:    StatusActive,
		CreatedAt: time.Now(),
		clients:   make(map[string]*ClientRecord),
	}
}

// AppendOutput adds a new chunk to the specified stream's buffer.
// The chunk's Offset is set to the current total bytes for that stream.
// timestamp is provided by the caller (host-grounded).
func (s *Session) AppendOutput(stream Stream, data []byte, timestamp time.Time) {
	if len(data) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	chunk := OutputChunk{
		Stream:    stream,
		Data:      make([]byte, len(data)),
		Timestamp: timestamp,
	}
	copy(chunk.Data, data)

	switch stream {
	case StreamStdout:
		chunk.Offset = s.stdoutOffset
		s.stdoutOffset += int64(len(data))
		s.stdoutChunks = append(s.stdoutChunks, chunk)
	case StreamStderr:
		chunk.Offset = s.stderrOffset
		s.stderrOffset += int64(len(data))
		s.stderrChunks = append(s.stderrChunks, chunk)
	}
}

// ReadOutput returns all chunks for the given stream starting at fromOffset.
// The returned slice is a copy and safe to use after releasing the lock.
func (s *Session) ReadOutput(stream Stream, fromOffset int64) []OutputChunk {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var chunks []OutputChunk
	switch stream {
	case StreamStdout:
		chunks = s.stdoutChunks
	case StreamStderr:
		chunks = s.stderrChunks
	}

	// Find the first chunk whose Offset + len(Data) > fromOffset.
	// We may need to slice the first chunk if fromOffset falls within it.
	result := make([]OutputChunk, 0, len(chunks))
	for _, ch := range chunks {
		chunkEnd := ch.Offset + int64(len(ch.Data))
		if chunkEnd <= fromOffset {
			continue
		}
		if ch.Offset >= fromOffset {
			// Whole chunk is after fromOffset.
			result = append(result, ch)
		} else {
			// Partial chunk: trim the leading bytes already seen.
			skip := fromOffset - ch.Offset
			trimmed := OutputChunk{
				Stream:    ch.Stream,
				Data:      make([]byte, int64(len(ch.Data))-skip),
				Timestamp: ch.Timestamp,
				Offset:    fromOffset,
			}
			copy(trimmed.Data, ch.Data[skip:])
			result = append(result, trimmed)
		}
	}
	return result
}

// StreamOffset returns the total bytes written to the given stream.
func (s *Session) StreamOffset(stream Stream) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	switch stream {
	case StreamStdout:
		return s.stdoutOffset
	case StreamStderr:
		return s.stderrOffset
	}
	return 0
}

// EnqueueStdin appends a new stdin entry to the session's STDIN queue.
func (s *Session) EnqueueStdin(entry StdinEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := entry
	s.stdin = append(s.stdin, &cp)
}

// DequeueStdin removes and returns the first stdin entry from the queue.
// Returns nil if the queue is empty.
func (s *Session) DequeueStdin() *StdinEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stdin) == 0 {
		return nil
	}
	entry := s.stdin[0]
	s.stdin = s.stdin[1:]
	return entry
}

// PeekStdin returns the oldest pending stdin entry without removing it.
// Returns nil if no pending entries exist.
func (s *Session) PeekStdin() *StdinEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.stdin {
		if e.Status == StdinPending {
			cp := *e
			return &cp
		}
	}
	return nil
}

// AcceptStdin marks the given entry as accepted and sets a host-grounded timestamp.
func (s *Session) AcceptStdin(id string, ts time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.stdin {
		if e.ID == id {
			e.Status = StdinAccepted
			e.Timestamp = ts
			return nil
		}
	}
	return errNotFound(id)
}

// RejectStdin marks the given entry as rejected.
func (s *Session) RejectStdin(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.stdin {
		if e.ID == id {
			e.Status = StdinRejected
			return nil
		}
	}
	return errNotFound(id)
}

// RejectAllPending rejects all currently pending stdin entries and returns their IDs.
func (s *Session) RejectAllPending() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, e := range s.stdin {
		if e.Status == StdinPending {
			e.Status = StdinRejected
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// GetStdinStatus returns the status of the stdin entry with the given ID.
func (s *Session) GetStdinStatus(id string) (StdinStatus, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.stdin {
		if e.ID == id {
			return e.Status, nil
		}
	}
	return "", errNotFound(id)
}

// Complete marks the session as completed with the given exit code.
func (s *Session) Complete(exitCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.Status = StatusCompleted
	s.CompletedAt = &now
	s.ExitCode = &exitCode
}

// GetStatus returns the current session status (safe for concurrent use).
func (s *Session) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Status
}
