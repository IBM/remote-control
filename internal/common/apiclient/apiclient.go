package apiclient

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/common/tlsconfig"
	"github.com/gabe-l-hart/remote-control/internal/common/types"
)

var ch = alog.UseChannel("APICLIENT")

// APIClient is an HTTP client for the remote-control server API.
type APIClient struct {
	BaseURL   string
	TLSConfig *tls.Config // Exported so websocket can share

	httpClient *http.Client
	apiKey     string
}

// NewAPIClient creates an APIClient for the given server URL.
func NewAPIClient(cfg *config.Config) *APIClient {
	httpClient, tlsCfg := buildHTTPClient(cfg)
	return &APIClient{
		BaseURL:    cfg.ServerURL,
		TLSConfig:  tlsCfg,
		httpClient: httpClient,
		apiKey:     cfg.ClientApiKey,
	}
}

/* -- Private Helpers ------------------------------------------------------- */

func buildHTTPClient(cfg *config.Config) (*http.Client, *tls.Config) {
	tlsCfg, err := tlsconfig.BuildClientTLSConfig(
		cfg.ClientTLS.CertFile,
		cfg.ClientTLS.KeyFile,
		cfg.ClientTLS.TrustedCAFile,
		cfg.Auth.Mode,
	)
	timeout := time.Duration(cfg.ClientTimeoutSeconds) * time.Second
	if err != nil {
		ch.Log(alog.WARNING, "[remote-control] TLS config error: %v; falling back to plain HTTP", err)
		return &http.Client{Timeout: timeout}, nil
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: tlsCfg,
		},
	}, tlsCfg
}

func (c *APIClient) req(method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, path, body)
	if err != nil {
		return nil, err
	}
	if nil != body {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.apiKey))
	}
	return req, nil
}

func (c *APIClient) post(path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := c.req(http.MethodPost, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}

func (c *APIClient) get(path string) (*http.Response, error) {
	req, err := c.req(http.MethodGet, c.BaseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}

func (c *APIClient) patch(path string, body any) (*http.Response, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := c.req(http.MethodPatch, c.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(req)
}

func (c *APIClient) delete(path string) (*http.Response, error) {
	req, err := c.req(http.MethodDelete, c.BaseURL+path, nil)
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

/* -- Public [host] --------------------------------------------------------- */

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
func (c *APIClient) AppendOutput(sessionID string, stream types.Stream, data []byte) error {
	body := types.OutputChunk{
		Stream: stream,
		Data:   data,
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
func (c *APIClient) ListPendingClients(sessionID string) ([]types.ClientInfo, error) {
	resp, err := c.get("/sessions/" + sessionID + "/clients?status=pending")
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	var clients []types.ClientInfo
	if err := json.NewDecoder(resp.Body).Decode(&clients); err != nil {
		return nil, err
	}
	return clients, nil
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

/* -- Public [client] ------------------------------------------------------- */

// RegisterClient registers this client with a session.
func (c *APIClient) RegisterClient(sessionID, clientSelfID string) (clientID string, status types.ApprovalStatus, err error) {
	url := "/sessions/" + sessionID + "/clients"
	if "" != clientSelfID {
		url = url + "?client_id=" + clientSelfID
	}
	resp, err := c.post(url, map[string]string{})
	if err != nil {
		return "", types.ApprovalUnknown, err
	}
	defer drainClose(resp)
	var result types.RegisterClientResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", types.ApprovalUnknown, err
	}
	return result.ClientID, result.Status, nil
}

// EnqueueStdin sends stdin data to the server queue.
func (c *APIClient) EnqueueStdin(sessionID, source string, data []byte) error {
	body := types.StdinEntry{Data: data}
	resp, err := c.post("/sessions/"+sessionID+"/stdin", body)
	if err != nil {
		return err
	}
	defer drainClose(resp)
	return nil
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

/* -- Public [shared] ------------------------------------------------------- */

// Poll returns the list of queued message for the given client.
func (c *APIClient) Poll(sessionID, clientID string, mType types.WSMessageType) (*types.PollResponse, error) {
	resp, err := c.get(fmt.Sprintf("/sessions/%s/%d/poll?client_id=%s", sessionID, mType, clientID))
	if err != nil {
		return nil, err
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("peek stdin: server returned %d", resp.StatusCode)
	}
	var pollResp types.PollResponse
	if err := json.NewDecoder(resp.Body).Decode(&pollResp); err != nil {
		return nil, err
	}
	return &pollResp, nil
}

// Ack acknowledges receipt of the currently polled messages
func (c *APIClient) Ack(sessionID, clientID string, mType types.WSMessageType) error {
	resp, err := c.get(fmt.Sprintf("/sessions/%s/%d/ack?client_id=%s", sessionID, mType, clientID))
	if err != nil {
		return err
	}
	drainClose(resp)
	return nil
}
