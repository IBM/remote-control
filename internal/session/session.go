package session

import (
	"sync"
	"time"
)

// Session holds all state for a single remote-control session.
type Session struct {
	ID     string `json:"id"`
	Status Status `json:"status"`

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

func newSession(id string) *Session {
	return &Session{
		ID:        id,
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
// If fromOffset is before the earliest available chunk (due to purging), it adjusts
// to start from the earliest available offset.
// Returns the chunks and the actual starting offset used.
func (s *Session) ReadOutput(stream Stream, fromOffset int64) ([]OutputChunk, int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var chunks []OutputChunk
	var currentOffset int64
	switch stream {
	case StreamStdout:
		chunks = s.stdoutChunks
		currentOffset = s.stdoutOffset
	case StreamStderr:
		chunks = s.stderrChunks
		currentOffset = s.stderrOffset
	}

	// Determine the earliest available offset
	var earliestOffset int64
	if len(chunks) > 0 {
		earliestOffset = chunks[0].Offset
	} else {
		earliestOffset = currentOffset
	}

	// Adjust fromOffset if it's before the earliest available data
	actualOffset := fromOffset
	if fromOffset < earliestOffset {
		actualOffset = earliestOffset
	}

	// Find the first chunk whose Offset + len(Data) > actualOffset.
	// We may need to slice the first chunk if actualOffset falls within it.
	result := make([]OutputChunk, 0, len(chunks))
	for _, ch := range chunks {
		chunkEnd := ch.Offset + int64(len(ch.Data))
		if chunkEnd <= actualOffset {
			continue
		}
		if ch.Offset >= actualOffset {
			// Whole chunk is after actualOffset.
			result = append(result, ch)
		} else {
			// Partial chunk: trim the leading bytes already seen.
			skip := actualOffset - ch.Offset
			trimmed := OutputChunk{
				Stream:    ch.Stream,
				Data:      make([]byte, int64(len(ch.Data))-skip),
				Timestamp: ch.Timestamp,
				Offset:    actualOffset,
			}
			copy(trimmed.Data, ch.Data[skip:])
			result = append(result, trimmed)
		}
	}
	return result, actualOffset
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

// purgeOldChunks removes the oldest chunks from the slice to keep total bytes under maxBytes.
// Returns the number of chunks purged.
func (s *Session) purgeOldChunks(chunks *[]OutputChunk, currentOffset int64, maxBytes int64) int {
	if len(*chunks) == 0 {
		return 0
	}

	// Calculate total bytes in buffer
	var totalBytes int64
	for _, chunk := range *chunks {
		totalBytes += int64(len(chunk.Data))
	}

	// If under limit, keep everything
	if totalBytes <= maxBytes {
		return 0
	}

	// Find the cutoff point: keep chunks from this index onward
	var keptBytes int64
	cutoffIndex := len(*chunks)

	// Walk backwards from the end, accumulating bytes
	for i := len(*chunks) - 1; i >= 0; i-- {
		chunkSize := int64(len((*chunks)[i].Data))
		if keptBytes+chunkSize > maxBytes {
			// This chunk would exceed the limit
			cutoffIndex = i + 1
			break
		}
		keptBytes += chunkSize
	}

	// If we need to purge everything (even the last chunk exceeds limit), keep at least the last chunk
	if cutoffIndex >= len(*chunks) {
		cutoffIndex = len(*chunks) - 1
	}

	purged := cutoffIndex
	if purged > 0 {
		*chunks = (*chunks)[cutoffIndex:]
	}

	return purged
}

// PurgeConsumedOutput removes OutputChunks that all active approved clients have consumed.
// If maxInitialBufferBytes > 0 and no approved clients exist, keeps the most recent chunks
// up to that byte limit to preserve TUI state for late-joining clients.
// Returns the number of chunks purged for stdout and stderr.
func (s *Session) PurgeConsumedOutput(maxInitialBufferBytes int64) (purgedStdout, purgedStderr int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Find minimum offset across all active approved clients
	var minStdoutOffset, minStderrOffset int64 = -1, -1

	for _, client := range s.clients {
		// Only consider approved clients
		if client.Approval != ApprovalApproved {
			continue
		}

		if minStdoutOffset == -1 || client.StdoutOffset < minStdoutOffset {
			minStdoutOffset = client.StdoutOffset
		}
		if minStderrOffset == -1 || client.StderrOffset < minStderrOffset {
			minStderrOffset = client.StderrOffset
		}
	}

	// If no approved clients, purge old chunks beyond the buffer limit
	if minStdoutOffset == -1 {
		if maxInitialBufferBytes > 0 {
			// Keep most recent chunks up to maxInitialBufferBytes
			purgedStdout = s.purgeOldChunks(&s.stdoutChunks, s.stdoutOffset, maxInitialBufferBytes)
			purgedStderr = s.purgeOldChunks(&s.stderrChunks, s.stderrOffset, maxInitialBufferBytes)
		} else {
			// No limit: purge everything
			purgedStdout = len(s.stdoutChunks)
			purgedStderr = len(s.stderrChunks)
			s.stdoutChunks = nil
			s.stderrChunks = nil
		}
		return purgedStdout, purgedStderr
	}

	// Purge stdout chunks
	newStdoutChunks := make([]OutputChunk, 0, len(s.stdoutChunks))
	for _, chunk := range s.stdoutChunks {
		chunkEnd := chunk.Offset + int64(len(chunk.Data))
		if chunkEnd > minStdoutOffset {
			newStdoutChunks = append(newStdoutChunks, chunk)
		} else {
			purgedStdout++
		}
	}
	s.stdoutChunks = newStdoutChunks

	// Purge stderr chunks
	newStderrChunks := make([]OutputChunk, 0, len(s.stderrChunks))
	for _, chunk := range s.stderrChunks {
		chunkEnd := chunk.Offset + int64(len(chunk.Data))
		if chunkEnd > minStderrOffset {
			newStderrChunks = append(newStderrChunks, chunk)
		} else {
			purgedStderr++
		}
	}
	s.stderrChunks = newStderrChunks

	return purgedStdout, purgedStderr
}

// PurgeConsumedStdin removes StdinEntries that have been accepted by the host.
// Returns the number of entries purged.
func (s *Session) PurgeConsumedStdin() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	newStdin := make([]*StdinEntry, 0, len(s.stdin))
	purged := 0

	for _, entry := range s.stdin {
		if entry.Status == StdinAccepted {
			purged++
		} else {
			newStdin = append(newStdin, entry)
		}
	}

	s.stdin = newStdin
	return purged
}
