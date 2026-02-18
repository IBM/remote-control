package client

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/tlsconfig"
	"github.com/google/uuid"
)

// Client manages connecting to a remote session and streaming I/O.
type Client struct {
	cfg      *config.Config
	api      *APIClient
	clientID string
}

// NewClient creates a Client from the given config.
func NewClient(cfg *config.Config) *Client {
	httpClient := buildHTTPClient(cfg)
	return &Client{
		cfg:      cfg,
		api:      NewAPIClient(cfg.ServerURL, httpClient),
		clientID: uuid.New().String(),
	}
}

// buildHTTPClient creates an http.Client with TLS configured if certs are available.
func buildHTTPClient(cfg *config.Config) *http.Client {
	if cfg.ClientTLS.CertFile == "" || cfg.ClientTLS.KeyFile == "" || cfg.ClientTLS.TrustedCAFile == "" {
		return &http.Client{Timeout: 30 * time.Second}
	}
	tlsCfg, err := tlsconfig.BuildClientTLSConfig(
		cfg.ClientTLS.CertFile,
		cfg.ClientTLS.KeyFile,
		cfg.ClientTLS.TrustedCAFile,
	)
	if err != nil {
		log.Printf("[remote-control] TLS config error: %v; falling back to plain HTTP", err)
		return &http.Client{Timeout: 30 * time.Second}
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}

// Run connects to the given session (or prompts the user to pick one),
// displays full history, then polls for new output and accepts stdin.
func (c *Client) Run(ctx context.Context, sessionID string) error {
	// If no session ID given, list and prompt.
	if sessionID == "" {
		var err error
		sessionID, err = c.pickSession(ctx)
		if err != nil {
			return err
		}
	}

	// Register with the session (for approval).
	status, err := c.api.RegisterClient(sessionID, c.clientID)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if status == "pending" {
		fmt.Fprintln(os.Stderr, "[remote-control] Waiting for host approval...")
		if err := c.waitForApproval(ctx, sessionID); err != nil {
			return err
		}
	}

	// Fetch and render full history.
	if err := c.renderHistory(ctx, sessionID); err != nil {
		return fmt.Errorf("render history: %w", err)
	}

	// Start polling and input goroutines.
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	pol := newPoller(c.api, sessionID)
	// Set poller offsets to current end of stream (after history was rendered).
	result, err := c.api.PollOutput(sessionID, 0, 0)
	if err == nil {
		pol.stdoutOffset = result.NextOffsets["stdout"]
		pol.stderrOffset = result.NextOffsets["stderr"]
	}

	// Re-render: we fetched the full history already; now only poll new chunks.
	// (Already rendered full history above via renderHistory, so advance offsets.)

	done := make(chan struct{})
	go func() {
		defer close(done)
		pol.run(pollCtx)
	}()

	// Monitor session status: exit when session completes.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				info, err := c.api.GetSession(sessionID)
				if err != nil {
					continue
				}
				if info.Status == "completed" {
					fmt.Fprintln(os.Stderr, "[remote-control] Session completed.")
					cancel()
					return
				}
			}
		}
	}()

	ir := newInputReader(c.api, sessionID, c.clientID)
	ir.run(pollCtx)
	cancel()

	<-done
	return nil
}

// pickSession lists sessions and prompts the user to choose one.
func (c *Client) pickSession(_ context.Context) (string, error) {
	sessions, err := c.api.ListSessions()
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions available")
	}
	fmt.Fprintln(os.Stderr, "Available sessions:")
	for i, s := range sessions {
		fmt.Fprintf(os.Stderr, "  [%d] %s  %v  (%s)\n", i+1, s.ID, s.Command, s.Status)
	}
	fmt.Fprintf(os.Stderr, "Select session [1-%d]: ", len(sessions))

	var choice int
	if _, err := fmt.Fscan(os.Stdin, &choice); err != nil || choice < 1 || choice > len(sessions) {
		return "", fmt.Errorf("invalid selection")
	}
	return sessions[choice-1].ID, nil
}

// renderHistory fetches and renders full session history from offset 0.
func (c *Client) renderHistory(_ context.Context, sessionID string) error {
	result, err := c.api.PollOutput(sessionID, 0, 0)
	if err != nil {
		return err
	}

	// Sort by timestamp.
	chunks := result.Chunks
	sort.Slice(chunks, func(i, j int) bool {
		return parseTimestamp(chunks[i].Timestamp).Before(parseTimestamp(chunks[j].Timestamp))
	})
	for _, ch := range chunks {
		renderChunk(ch)
	}
	return nil
}

// waitForApproval polls until the session shows this client as approved.
func (c *Client) waitForApproval(ctx context.Context, sessionID string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			result, err := c.api.PollOutput(sessionID, 0, 0)
			if err != nil {
				continue
			}
			// If we can poll output, we're approved.
			_ = result
			return nil
		}
	}
}
