package api

import (
	"encoding/base64"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/session"
)

// CreateSessionRequest is the body for POST /sessions.
type CreateSessionRequest struct {
	Command []string `json:"command"`
}

// SessionResponse is returned by session CRUD endpoints.
type SessionResponse struct {
	ID        string     `json:"id"`
	Command   []string   `json:"command"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	ExitCode  *int       `json:"exit_code,omitempty"`
}

func sessionToResponse(s *session.Session) SessionResponse {
	return SessionResponse{
		ID:          s.ID,
		Command:     s.Command,
		Status:      string(s.GetStatus()),
		CreatedAt:   s.CreatedAt,
		CompletedAt: s.CompletedAt,
		ExitCode:    s.ExitCode,
	}
}

// AppendOutputRequest is the body for POST /sessions/{id}/output.
type AppendOutputRequest struct {
	Stream    string `json:"stream"`             // "stdout" or "stderr"
	Data      string `json:"data"`               // base64-encoded bytes
	Timestamp string `json:"timestamp"`           // RFC3339Nano
}

func (r *AppendOutputRequest) decode() (session.Stream, []byte, time.Time, error) {
	data, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	ts, err := time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	return session.Stream(r.Stream), data, ts, nil
}

// OutputChunkResponse is a single chunk in the poll response.
type OutputChunkResponse struct {
	Stream    string `json:"stream"`
	Data      string `json:"data"`      // base64-encoded
	Offset    int64  `json:"offset"`
	Timestamp string `json:"timestamp"` // RFC3339Nano
}

// PollOutputResponse is returned by GET /sessions/{id}/output.
type PollOutputResponse struct {
	Chunks        []OutputChunkResponse `json:"chunks"`
	NextOffsets   map[string]int64      `json:"next_offsets"`
	ActualOffsets map[string]int64      `json:"actual_offsets"`
}

// PatchSessionRequest is the body for PATCH /sessions/{id}.
type PatchSessionRequest struct {
	ExitCode int `json:"exit_code"`
}

// StdinRequest is the body for POST /sessions/{id}/stdin.
type StdinRequest struct {
	Source string `json:"source"` // "host" or client ID
	Data   string `json:"data"`   // base64-encoded
}

// StdinResponse is returned by GET /sessions/{id}/stdin.
type StdinResponse struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Data      string `json:"data"`   // base64-encoded
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

func stdinEntryToResponse(e *session.StdinEntry) StdinResponse {
	return StdinResponse{
		ID:        e.ID,
		Source:    e.Source,
		Data:      base64.StdEncoding.EncodeToString(e.Data),
		Status:    string(e.Status),
		Timestamp: e.Timestamp.Format(time.RFC3339Nano),
	}
}

// StdinStatusResponse is returned by GET /sessions/{id}/stdin/{sid}/status.
type StdinStatusResponse struct {
	Status string `json:"status"`
}

// ErrorResponse is a standard JSON error body.
type ErrorResponse struct {
	Error string `json:"error"`
}
