package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// APIClient is an HTTP client for the remote-control server API.
type APIClient struct {
	baseURL    string
	httpClient *http.Client
}

// NewAPIClient creates an APIClient for the given server URL.
func NewAPIClient(baseURL string, httpClient *http.Client) *APIClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &APIClient{baseURL: baseURL, httpClient: httpClient}
}

func (c *APIClient) post(path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Post(c.baseURL+path, "application/json", bytes.NewReader(data))
}

func (c *APIClient) get(path string) (*http.Response, error) {
	return c.httpClient.Get(c.baseURL + path)
}

func drainClose(resp *http.Response) {
	if resp != nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
	}
}

// SessionInfo contains basic session metadata.
type SessionInfo struct {
	ID      string   `json:"id"`
	Command []string `json:"command"`
	Status  string   `json:"status"`
}

// OutputChunk is a single chunk of output from the server.
type OutputChunk struct {
	Stream    string `json:"stream"`
	Data      string `json:"data"`      // base64
	Offset    int64  `json:"offset"`
	Timestamp string `json:"timestamp"` // RFC3339Nano
}

// PollOutputResponse is the response from GET /sessions/{id}/output.
type PollOutputResponse struct {
	Chunks      []OutputChunk    `json:"chunks"`
	NextOffsets map[string]int64 `json:"next_offsets"`
}

// ErrForbidden is returned when the server rejects a request with 403.
var ErrForbidden = fmt.Errorf("forbidden")

// ListSessions returns all sessions from the server.
func (c *APIClient) ListSessions() ([]SessionInfo, error) {
	resp, err := c.get("/sessions")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	var sessions []SessionInfo
	return sessions, json.NewDecoder(resp.Body).Decode(&sessions)
}

// GetSession returns a single session's metadata.
func (c *APIClient) GetSession(sessionID string) (*SessionInfo, error) {
	resp, err := c.get("/sessions/" + sessionID)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	var info SessionInfo
	return &info, json.NewDecoder(resp.Body).Decode(&info)
}

// PollOutput polls for output chunks at the given offsets.
func (c *APIClient) PollOutput(sessionID string, stdoutOffset, stderrOffset int64) (*PollOutputResponse, error) {
	path := fmt.Sprintf("/sessions/%s/output?stdout_offset=%d&stderr_offset=%d",
		sessionID, stdoutOffset, stderrOffset)
	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll output: server returned %d", resp.StatusCode)
	}
	var result PollOutputResponse
	return &result, json.NewDecoder(resp.Body).Decode(&result)
}

// RegisterClient registers this client with a session and returns the approval status.
func (c *APIClient) RegisterClient(sessionID, clientID string) (string, error) {
	resp, err := c.post("/sessions/"+sessionID+"/clients", map[string]string{
		"client_id": clientID,
	})
	if err != nil {
		return "", err
	}
	defer drainClose(resp)
	var result struct {
		Status string `json:"status"`
	}
	return result.Status, json.NewDecoder(resp.Body).Decode(&result)
}

// EnqueueStdin submits stdin data to the server.
// Returns the entry ID or ErrForbidden if the client is not permitted.
func (c *APIClient) EnqueueStdin(sessionID, clientID string, data []byte) (string, error) {
	resp, err := c.post("/sessions/"+sessionID+"/stdin", map[string]string{
		"source": clientID,
		"data":   base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return "", err
	}
	defer drainClose(resp)
	if resp.StatusCode == http.StatusForbidden {
		return "", ErrForbidden
	}
	var result struct {
		ID string `json:"id"`
	}
	return result.ID, json.NewDecoder(resp.Body).Decode(&result)
}

// GetStdinStatus returns the status of a submitted stdin entry.
func (c *APIClient) GetStdinStatus(sessionID, entryID string) (string, error) {
	resp, err := c.get("/sessions/" + sessionID + "/stdin/" + entryID + "/status")
	if err != nil {
		return "", err
	}
	defer drainClose(resp)
	var result struct {
		Status string `json:"status"`
	}
	return result.Status, json.NewDecoder(resp.Body).Decode(&result)
}

// timestampToTime parses an RFC3339Nano timestamp string.
func timestampToTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
