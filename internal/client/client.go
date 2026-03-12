package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/tlsconfig"
	"golang.org/x/term"
)

var ch = alog.UseChannel("CLIENT")

// Client manages connecting to a remote session and streaming I/O.
type Client struct {
	cfg      *config.Config
	api      *APIClient
	clientID string // Set after registration with server
}

// NewClient creates a Client from the given config.
func NewClient(cfg *config.Config) *Client {
	httpClient := buildHTTPClient(cfg)
	return &Client{
		cfg: cfg,
		api: NewAPIClient(cfg.ServerURL, httpClient),
		// clientID is set after registration
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
		ch.Log(alog.WARNING, "[remote-control] TLS config error: %v; falling back to plain HTTP", err)
		return &http.Client{Timeout: 30 * time.Second}
	}
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
}

// Run connects to the given session (or prompts the user to pick one),
// displays full history, then uses hybrid connection for output streaming and stdin.
func (c *Client) Run(ctx context.Context, sessionID string) error {
	// If no session ID given, list and prompt.
	if sessionID == "" {
		var err error
		sessionID, err = c.pickSession(ctx)
		if err != nil {
			return err
		}
	}

	// Create our own cancellable context for graceful shutdown
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Register with the session (server assigns client ID).
	clientID, status, err := c.api.RegisterClient(sessionID)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	c.clientID = clientID
	ch.Log(alog.DEBUG, "[remote-control] Registered with client ID: %s", c.clientID)

	if status == "pending" {
		fmt.Fprintln(os.Stderr, "[remote-control] Waiting for host approval...")
		if err := c.waitForApproval(ctx, sessionID); err != nil {
			return err
		}
	}

	// Put client terminal in raw mode if interactive (enables control characters).
	isRawMode := term.IsTerminal(int(os.Stdin.Fd()))
	if isRawMode {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("make raw: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState) //nolint:errcheck
	}

	// Fetch and render full history.
	if err := c.renderHistory(ctx, sessionID); err != nil {
		return fmt.Errorf("render history: %w", err)
	}

	// Extended pause to let terminal query responses (OSC sequences) arrive
	// in response to the host TUI's capability queries.
	if isRawMode {
		time.Sleep(200 * time.Millisecond)
	}

	// Build TLS config for WebSocket
	var tlsCfg *tls.Config
	if c.cfg.ClientTLS.CertFile != "" && c.cfg.ClientTLS.KeyFile != "" && c.cfg.ClientTLS.TrustedCAFile != "" {
		tlsCfg, err = tlsconfig.BuildClientTLSConfig(
			c.cfg.ClientTLS.CertFile,
			c.cfg.ClientTLS.KeyFile,
			c.cfg.ClientTLS.TrustedCAFile,
		)
		if err != nil {
			ch.Log(alog.WARNING, "[remote-control] TLS config error: %v; WebSocket will be disabled", err)
		}
	}

	// Derive WebSocket URL from server URL
	wsURL := deriveWebSocketURL(c.cfg.ServerURL)

	// Create hybrid connection
	hc := NewHybridConnection(
		wsURL,
		tlsCfg,
		c.api,
		sessionID,
		c.clientID,
		c.cfg.WSFailureThreshold,
		time.Duration(c.cfg.WSFailureWindow)*time.Second,
		time.Duration(c.cfg.WSUpgradeCheckInterval)*time.Second,
	)

	// Set up output handler (will be called for each new chunk)
	wsConn := hc.WebSocket()
	wsConn.OnOutput(func(chunk OutputChunk) {
		renderChunk(chunk)
	})

	// Handle stdin input
	wsConn.OnStdinPending(func(entry StdinEntry) {
		ch.Log(alog.DEBUG, "[remote-control] stdin pending callback: %s", entry.ID)
	})

	wsConn.OnError(func(err error) {
		ch.Log(alog.WARNING, "[remote-control] WebSocket error: %v", err)
	})

	// Start hybrid connection
	if err := hc.Start(); err != nil {
		return fmt.Errorf("start hybrid connection: %w", err)
	}

	// Handle terminal resize events (SIGWINCH) in raw mode.
	if isRawMode {
		sigWinchCh := make(chan os.Signal, 1)
		signal.Notify(sigWinchCh, syscall.SIGWINCH)
		defer signal.Stop(sigWinchCh)

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-sigWinchCh:
					cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
					if err == nil {
						// TODO: Send resize event to server when API is implemented
						_ = cols
						_ = rows
					}
				}
			}
		}()
	}

	// Monitor session status: exit when session completes.
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				info, err := c.api.GetSession(sessionID)
				if err != nil {
					continue
				}
				if info.Status == "completed" {
					fmt.Fprintln(os.Stderr, "[remote-control] Session completed.")
					return
				}
			}
		}
	}()

	// Handle stdin with WebSocket or fallback to HTTP
	stopStdin := make(chan struct{})
	defer close(stopStdin)

	go func() {
		if isRawMode {
			// Raw mode: read individual bytes, filter unwanted sequences
			buf := make([]byte, 32)
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopStdin:
					return
				default:
				}

				n, err := os.Stdin.Read(buf)
				if err != nil {
					return
				}
				if n == 0 {
					continue
				}

				data, shouldExit := filterInput(buf[:n])
				if len(data) == 0 && !shouldExit {
					continue
				}

				if len(data) > 0 {
					submitStdin(data, hc, sessionID, c.clientID, c.api)
				}
				if shouldExit {
					// Signal exit by cancelling the context
					cancel()
					return
				}
			}
		} else {
			// Cooked mode: read lines (Ctrl+C will be handled by terminal)
			buf := make([]byte, 4096)
			for {
				select {
				case <-ctx.Done():
					return
				case <-stopStdin:
					return
				default:
				}

				n, err := os.Stdin.Read(buf)
				if err != nil {
					return
				}
				if n == 0 {
					continue
				}

				data := make([]byte, n)
				copy(data, buf[:n])
				submitStdin(data, hc, sessionID, c.clientID, c.api)
			}
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()

	// Close hybrid connection
	return hc.Close()
}

// deriveWebSocketURL converts http(s):// URLs to ws(s):// URLs
func deriveWebSocketURL(httpURL string) string {
	parsed, err := url.Parse(httpURL)
	if err != nil {
		return httpURL
	}

	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else if parsed.Scheme == "http" {
		parsed.Scheme = "ws"
	}

	return parsed.String() + "/ws"
}

// filterInput filters out control characters and sequences that should not be
// forwarded to the host: signals (Ctrl+C, etc.), mouse events, and terminal query responses.
// Returns filtered data and a boolean indicating if the client should exit.
func filterInput(input []byte) ([]byte, bool) {
	data := make([]byte, 0, len(input))
	i := 0

	for i < len(input) {
		b := input[i]

		// Check for signal-generating control characters
		switch b {
		case 0x03: // Ctrl+C (SIGINT) - exit client gracefully
			ch.Log(alog.INFO, "[remote-control] Client received Ctrl+C, exiting...")
			return nil, true
		case 0x1c: // Ctrl+\ (SIGQUIT) - exit client
			ch.Log(alog.INFO, "[remote-control] Client received Ctrl+\\, exiting...")
			return nil, true
		case 0x1a: // Ctrl+Z (SIGTSTP) - don't forward, ignore
			ch.Log(alog.DEBUG, "[remote-control] Client ignoring Ctrl+Z")
			i++
			continue
		}

		// Check for escape sequences to filter
		if b == 0x1b && i+1 < len(input) {
			next := input[i+1]

			// OSC sequences: ESC ] ... (terminal query responses like color queries)
			// Format: ESC ] Ps ; Pt BEL or ESC ] Ps ; Pt ESC \
			if next == ']' {
				// Skip until BEL (0x07) or ST (ESC \
				i += 2
				for i < len(input) {
					if input[i] == 0x07 { // BEL
						i++
						break
					}
					if input[i] == 0x1b && i+1 < len(input) && input[i+1] == '\\' { // ST
						i += 2
						break
					}
					i++
				}
				continue
			}

			// CSI sequences for mouse tracking: ESC [ < ... or ESC [ M ...
			if next == '[' && i+2 < len(input) {
				third := input[i+2]

				// SGR mouse mode: ESC [ < Pb ; Px ; Py M/m
				if third == '<' {
					i += 3
					// Skip until M or m
					for i < len(input) && input[i] != 'M' && input[i] != 'm' {
						i++
					}
					if i < len(input) {
						i++ // Skip the M/m
					}
					continue
				}

				// X10/Normal mouse mode: ESC [ M Cb Cx Cy (3 bytes after M)
				if third == 'M' && i+5 < len(input) {
					i += 6 // Skip ESC [ M and 3 data bytes
					continue
				}
			}
		}

		// Regular character - keep it
		data = append(data, b)
		i++
	}

	return data, false
}

// submitStdin submits data via WebSocket or falls back to HTTP.
func submitStdin(data []byte, hc *HybridConnection, sessionID, clientID string, api *APIClient) {
	if hc.WebSocket() != nil && hc.WebSocket().IsConnected() {
		// Use WebSocket for stdin
		if err := hc.WebSocket().SubmitStdin(string(data)); err != nil {
			ch.Log(alog.WARNING, "[remote-control] WebSocket submit stdin error: %v", err)
		}
	} else {
		// Fallback to HTTP
		if _, err := api.EnqueueStdin(sessionID, clientID, data); err != nil {
			if errors.Is(err, ErrForbidden) {
				ch.Log(alog.WARNING, "[remote-control] stdin not permitted")
			} else {
				ch.Log(alog.WARNING, "[remote-control] enqueue stdin error: %v", err)
			}
		}
	}
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
	result, err := c.api.PollOutput(sessionID, c.clientID, 0, 0)
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
			result, err := c.api.PollOutput(sessionID, c.clientID, 0, 0)
			if err != nil {
				continue
			}
			// If we can poll output, we're approved.
			_ = result
			return nil
		}
	}
}
