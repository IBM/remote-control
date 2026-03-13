package host

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

func (c *APIClient) patch(path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPatch, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.httpClient.Do(req)
}

func (c *APIClient) delete(path string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}

func drainClose(resp *http.Response) {
	if resp != nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
	}
}

// CreateSession creates a new session on the server and returns its ID.
func (c *APIClient) CreateSession(command []string) (string, error) {
	resp, err := c.post("/sessions", map[string]any{})
	if err != nil {
		return "", err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("create session: server returned %d", resp.StatusCode)
	}
	var result struct {
		ID string `json:"id"`
	}
	return result.ID, json.NewDecoder(resp.Body).Decode(&result)
}

// AppendOutput sends a chunk of output to the server.
func (c *APIClient) AppendOutput(sessionID string, stream string, data []byte, timestamp time.Time) error {
	body := map[string]string{
		"stream":    stream,
		"data":      base64.StdEncoding.EncodeToString(data),
		"timestamp": timestamp.Format(time.RFC3339Nano),
	}
	resp, err := c.post("/sessions/"+sessionID+"/output", body)
	if err != nil {
		return err
	}
	drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("append output: server returned %d", resp.StatusCode)
	}
	return nil
}

// EnqueueStdin sends stdin data to the server queue.
func (c *APIClient) EnqueueStdin(sessionID, source string, data []byte) (string, error) {
	body := map[string]string{
		"source": source,
		"data":   base64.StdEncoding.EncodeToString(data),
	}
	resp, err := c.post("/sessions/"+sessionID+"/stdin", body)
	if err != nil {
		return "", err
	}
	defer drainClose(resp)
	var result struct {
		ID string `json:"id"`
	}
	return result.ID, json.NewDecoder(resp.Body).Decode(&result)
}

// PeekStdin returns the oldest pending stdin entry, or nil if none.
func (c *APIClient) PeekStdin(sessionID string) (*PendingStdin, error) {
	resp, err := c.get("/sessions/" + sessionID + "/stdin")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peek stdin: server returned %d", resp.StatusCode)
	}
	var result PendingStdin
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// PendingStdin is a pending stdin entry returned from the server.
type PendingStdin struct {
	ID     uint64 `json:"id"`
	Source string `json:"source"`
	Data   string `json:"data"` // base64
}

// AckStdin marks a stdin entry as processed.
func (c *APIClient) AckStdin(sessionID string, entryID uint64) error {
	resp, err := c.post("/sessions/"+sessionID+"/stdin/ack", map[string]uint64{"id": entryID})
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}

// CompleteSession marks the session as completed with the given exit code.
func (c *APIClient) CompleteSession(sessionID string, exitCode int) error {
	resp, err := c.patch("/sessions/"+sessionID, map[string]int{"exit_code": exitCode})
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}

// DeleteSession removes a session from the server.
func (c *APIClient) DeleteSession(sessionID string) error {
	resp, err := c.delete("/sessions/" + sessionID)
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}

// ListPendingClients returns clients waiting for approval.
func (c *APIClient) ListPendingClients(sessionID string) ([]PendingClient, error) {
	resp, err := c.get("/sessions/" + sessionID + "/clients?status=pending")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	var clients []PendingClient
	if err := json.NewDecoder(resp.Body).Decode(&clients); err != nil {
		return nil, err
	}
	return clients, nil
}

// PendingClient describes a client waiting for host approval.
type PendingClient struct {
	ClientID string `json:"client_id"`
}

// ApproveClient approves a client with the given permission.
func (c *APIClient) ApproveClient(sessionID, clientID, permission string) error {
	resp, err := c.post("/sessions/"+sessionID+"/clients/"+clientID+"/approve",
		map[string]string{"permission": permission})
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}

// DenyClient denies a client.
func (c *APIClient) DenyClient(sessionID, clientID string) error {
	resp, err := c.post("/sessions/"+sessionID+"/clients/"+clientID+"/deny", nil)
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}

// SubmitHostStdin submits host stdin entries through the server queue.
func (c *APIClient) SubmitHostStdin(sessionID string, data []byte) (string, error) {
	body := map[string]string{
		"source": "host",
		"data":   base64.StdEncoding.EncodeToString(data),
	}
	resp, err := c.post("/sessions/"+sessionID+"/stdin", body)
	if err != nil {
		return "", err
	}
	defer drainClose(resp)
	var result struct {
		ID string `json:"id"`
	}
	return result.ID, json.NewDecoder(resp.Body).Decode(&result)
}
