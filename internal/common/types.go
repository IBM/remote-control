package types

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

/* -- Shared Structs -------------------------------------------------------- */

// Stream identifies which subprocess output stream a chunk came from.
type Stream uint8

const (
	StreamUnknown Stream = 0
	StreamStdout  Stream = 1
	StreamStderr  Stream = 2
)

// Status is the lifecycle state of a session.
type SessionStatus uint8

const (
	SessionStatusUnknown   SessionStatus = 0
	SessionStatusActive    SessionStatus = 1
	SessionStatusCompleted SessionStatus = 2
	SessionStatusError     SessionStatus = 3
)

// OutputChunk is a single contiguous block of data from a subprocess output
// stream.
type OutputChunk struct {
	Stream Stream `json:"stream"`
	Data   []byte `json:"data"`
}

func (c OutputChunk) MarshalJSON() ([]byte, error) {
	s := base64.StdEncoding.EncodeToString(c.Data)
	return json.Marshal(s)
}

func (c *OutputChunk) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); nil != err {
		return err
	} else if decoded, err := base64.StdEncoding.DecodeString(s); nil != err {
		return err
	} else {
		c.Data = decoded
		return nil
	}
}

// StdinEntry is a single unit of stdin data submitted by a client or the host.
type StdinEntry struct {
	Data []byte `json:"data"`
}

// Permission defines what a connected client is allowed to do.
type Permission string

const (
	PermissionReadOnly  Permission = "read-only"
	PermissionReadWrite Permission = "read-write"
)

// ApprovalStatus tracks whether the host has approved or denied a client.
type ApprovalStatus string

const (
	ApprovalPending  ApprovalStatus = "pending"
	ApprovalApproved ApprovalStatus = "approved"
	ApprovalDenied   ApprovalStatus = "denied"
)

// ClientInfo holds metadata about a connected remote client.
type ClientInfo struct {
	ClientID     string         `json:"client_id"`
	JoinedAt     time.Time      `json:"joined_at"`
	Approval     ApprovalStatus `json:"approval"`
	Permission   Permission     `json:"permission"`
	LastPollAt   time.Time      `json:"last_poll_at"`
	StdoutOffset int64          `json:"stdout_offset"`
	StderrOffset int64          `json:"stderr_offset"`
}

type SessionInfo struct {
	ID     string        `json:"id"`
	Status SessionStatus `json:"status"`

	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
}

// Stand-in ClientID for the host's client connection
const HostClientID = "host"

/* -- Synchronous Request/Response Messages --------------------------------- */

// CreateSessionRequest is the body for POST /sessions.
type CreateSessionRequest struct {
	ID string `json:"id,omitempty"`
}

// AppendOutputRequest is the body for POST /sessions/{id}/output.
type AppendOutputRequest struct {
	Stream Stream `json:"stream"` // 1 or 2
	Data   []byte `json:"data"`   // Output bytes
}

func (r AppendOutputRequest) MarshalJSON() ([]byte, error) {
	s := base64.StdEncoding.EncodeToString(r.Data)
	return json.Marshal(s)
}

func (r *AppendOutputRequest) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); nil != err {
		return err
	} else if decoded, err := base64.StdEncoding.DecodeString(s); nil != err {
		return err
	} else {
		r.Data = decoded
		return nil
	}
}

// PollResponse is the body for GET /sessions/{id}/{m_type}/poll
type PollResponse struct {
	Elements interface{} `json:"elements"`
}

// PatchSessionRequest is the body for PATCH /sessions/{id}.
type PatchSessionRequest struct {
	ExitCode int `json:"exit_code"`
}

// StdinRequest is the body for POST /sessions/{id}/stdin.
type StdinRequest struct {
	Data string `json:"data"` // base64-encoded
}

// AckStdinRequest is the body for POST /sessions/{id}/stdin/ack.
type AckStdinRequest struct {
	ID uint64 `json:"id"`
}

// StdinStatusResponse is returned by GET /sessions/{id}/stdin/{sid}/status.
type StdinStatusResponse struct {
	Status string `json:"status"`
}

// ErrorResponse is a standard JSON error body.
type ErrorResponse struct {
	Error string `json:"error"`
}

// RegisterClientRequest is the body for POST /sessions/{id}/clients.
type RegisterClientRequest struct{}

// RegisterClientResponse is returned by POST /sessions/{id}/clients.
type RegisterClientResponse struct {
	ClientID string         `json:"client_id"`
	Status   ApprovalStatus `json:"status"`
}

// ApproveClientRequest is the body for POST /sessions/{id}/clients/{cid}/approve.
type ApproveClientRequest struct {
	Permission Permission `json:"permission,omitempty"`
}

/* -- WebSocket Messaging --------------------------------------------------- */

type WSMessageType uint8

const (
	WSMessageUnknown WSMessageType = 0

	// host -> client
	WSMessageOutput WSMessageType = 10

	// client -> host
	WSMessageStdin WSMessageType = 20

	// server -> host
	WSMessagePendingClient WSMessageType = 30

	// server responses
	WSMessageError WSMessageType = 40
)

func GetWSMessageType(n int) WSMessageType {
	t := WSMessageType(n)
	switch t {
	case WSMessageOutput, WSMessageStdin, WSMessagePendingClient, WSMessageError:
		return t
	}
	return WSMessageUnknown
}

// Generic wrapper for a WebSocket message with type and serialized json
// Using json.RawMessage for Message allows proper type-specific unmarshaling
// by avoiding the map[string]interface{} conversion that happens with interface{}
type WSMessage struct {
	Type    WSMessageType   `json:"type"`
	Message json.RawMessage `json:"message"`
}

// UnmarshalMessage unmarshals the Message field into the provided interface.
// The caller should pass a pointer to the appropriate struct type.
// Examples:
//   - var payload types.AppendOutputRequest; msg.UnmarshalMessage(&payload)
//   - var payload types.StdinRequest; msg.UnmarshalMessage(&payload)
func (w *WSMessage) UnmarshalMessage(v interface{}) error {
	return json.Unmarshal(w.Message, v)
}
