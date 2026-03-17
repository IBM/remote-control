package session

import "time"

// Stream identifies which subprocess output stream a chunk came from.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// Status is the lifecycle state of a session.
type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusError     Status = "error"
)

// OutputChunk is a single contiguous block of data from a subprocess output stream.
// Timestamps are set by the host at the moment data is read from the subprocess pipe.
type OutputChunk struct {
	Stream    Stream    `json:"stream"`
	Data      []byte    `json:"data"`
	Timestamp time.Time `json:"timestamp"`
	// Offset is the byte offset within this stream's total output at which this chunk begins.
	Offset int64 `json:"offset"`
}

// StdinEntry is a single unit of stdin data submitted by a client or the host.
type StdinEntry struct {
	ID   uint64 `json:"id"`
	Data []byte `json:"data"`
}
