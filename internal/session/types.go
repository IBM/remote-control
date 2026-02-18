package session

import "time"

// Stream identifies which subprocess output stream a chunk came from.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// StdinStatus tracks whether a pending stdin entry has been accepted or rejected.
type StdinStatus string

const (
	StdinPending  StdinStatus = "pending"
	StdinAccepted StdinStatus = "accepted"
	StdinRejected StdinStatus = "rejected"
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
	ID     string `json:"id"`
	Source string `json:"source"` // "host" or a client ID

	Data      []byte      `json:"data"`
	Timestamp time.Time   `json:"timestamp"` // host-grounded: set when host accepts the entry
	Status    StdinStatus `json:"status"`
}
