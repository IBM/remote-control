package client

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	types "github.com/gabe-l-hart/remote-control/internal/common"
)

// APIClient is an HTTP client for the remote-control server API.
type APIClient struct {
	baseURL    string
	httpClient *http.Client
}

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
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

// SessionInfo contains session metadata.
type SessionInfo struct {
	ID      string   `json:"id"`
	Command []string `json:"command"`
	Status  string   `json:"status"`
}

// ErrForbidden is returned when the server rejects a request with 403.
var ErrForbidden = fmt.Errorf("forbidden")

// ListSessions returns all sessions from the server.
func (c *APIClient) ListSessions() ([]types.SessionInfo, error) {
	resp, err := c.get("/sessions")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	var sessions []types.SessionInfo
	return sessions, json.NewDecoder(resp.Body).Decode(&sessions)
}

// GetSession returns a single session's metadata.
func (c *APIClient) GetSession(sessionID string) (*types.SessionInfo, error) {
	resp, err := c.get("/sessions/" + sessionID)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("session not found: %s", sessionID)
	}
	var info types.SessionInfo
	return &info, json.NewDecoder(resp.Body).Decode(&info)
}

// RegisterClient registers this client with a session.
func (c *APIClient) RegisterClient(sessionID string) (clientID, status string, err error) {
	resp, err := c.post("/sessions/"+sessionID+"/clients", map[string]string{})
	if err != nil {
		return "", "", err
	}
	defer drainClose(resp)
	var result struct {
		ClientID string `json:"client_id"`
		Status   string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", "", err
	}
	return result.ClientID, result.Status, nil
}

// EnqueueStdin submits stdin data to the server.
func (c *APIClient) EnqueueStdin(sessionID, clientID string, data []byte) (string, error) {
	path := fmt.Sprintf("/sessions/%s/stdin?client_id=%s", sessionID, clientID)
	resp, err := c.post(path, map[string]string{
		"data": base64.StdEncoding.EncodeToString(data),
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

// PollOutput polls for output chunks using WebSocket message type 10 (WSMessageOutput).
func (c *APIClient) PollOutput(sessionID, clientID string, stdoutOffset, stderrOffset int64) (*types.PollResponse, error) {
	path := fmt.Sprintf("/sessions/%s/%d/poll?client_id=%s",
		sessionID, int(types.WSMessageOutput), clientID)
	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll output: server returned %d", resp.StatusCode)
	}
	var result types.PollResponse
	return &result, json.NewDecoder(resp.Body).Decode(&result)
}

// PollMessageQueue polls for queued messages of a specific type.
func (c *APIClient) PollMessageQueue(sessionID, clientID string, mType types.WSMessageType) ([]interface{}, error) {
	path := fmt.Sprintf("/sessions/%s/%d/poll?client_id=%s",
		sessionID, int(mType), clientID)
	resp, err := c.get(path)
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("poll %d: server returned %d", mType, resp.StatusCode)
	}
	var result types.PollResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if elems, ok := result.Elements.([]interface{}); ok {
		return elems, nil
	}
	return make([]interface{}, 0), nil
}

// AckMessageQueue acknowledges processing of messages of a specific type.
func (c *APIClient) AckMessageQueue(sessionID, clientID string, mType types.WSMessageType) error {
	path := fmt.Sprintf("/sessions/%s/%d/ack?client_id=%s",
		sessionID, int(mType), clientID)
	resp, err := c.get(path)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ack %d: server returned %d", mType, resp.StatusCode)
	}
	return nil
}
