package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/common/tlsconfig"
	ws "github.com/gabe-l-hart/remote-control/internal/common/websocket"
)

var ch = alog.UseChannel("CLIENT")

type Client struct {
	cfg       *config.Config
	api       *types.APIClient
	clientID  string
	tlsConfig *tls.Config
	wsClient  *WebSocketConnection
}

func NewClient(cfg *config.Config) *Client {
	httpClient, tlsCfg := buildHTTPClient(cfg)
	return &Client{
		cfg:       cfg,
		api:       types.NewAPIClient(cfg.ServerURL, httpClient),
		clientID:  "",
		tlsConfig: tlsCfg,
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
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}, tlsCfg
}

func buildWebSocketConfig(cfg *config.Config) *ws.WebSocketConfig {
	return &ws.WebSocketConfig{
		ReconnectInterval: time.Duration(cfg.WebSocketReconnectIntervalSeconds) * time.Second,
		ReconnectTimeout:  time.Duration(cfg.WebSocketReconnectTimeoutSeconds) * time.Second,
		MaxQueueLength:    cfg.WebSocketMaxQueueLength,
	}
}

func filterInput(input []byte) ([]byte, bool) {
	data := make([]byte, 0, len(input))
	i := 0

	for i < len(input) {
		b := input[i]

		switch b {
		case 0x03:
			ch.Log(alog.INFO, "[remote-control] Client received Ctrl+C, exiting...")
			return nil, true
		case 0x1c:
			ch.Log(alog.INFO, "[remote-control] Client received Ctrl+\\, exiting...")
			return nil, true
		case 0x1a:
			ch.Log(alog.DEBUG, "[remote-control] Client ignoring Ctrl+Z")
			i++
			continue
		}

		if b == 0x1b && i+1 < len(input) {
			next := input[i+1]

			if next == ']' {
				i += 2
				for i < len(input) {
					if input[i] == 0x07 {
						i++
						break
					}
					if input[i] == 0x1b && i+1 < len(input) && input[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
				continue
			}

			if next == '[' && i+2 < len(input) {
				third := input[i+2]

				if third == '<' {
					i += 3
					for i < len(input) && input[i] != 'M' && input[i] != 'm' {
						i++
					}
					if i < len(input) {
						i++
					}
					continue
				}

				if third == 'M' && i+5 < len(input) {
					i += 6
					continue
				}
			}
		}

		data = append(data, b)
		i++
	}

	return data, false
}

func (c *Client) pickSession(_ context.Context) (string, error) {
	sessions, err := c.api.ListSessions()
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no session available")
	} else if len(sessions) == 1 {
		return sessions[0].ID, nil
	}
	fmt.Fprintln(os.Stderr, "Available sessions:")
	for i, s := range sessions {
		fmt.Fprintf(os.Stderr, "  [%d] %s  (%s)\n", i+1, s.ID, string(s.Status))
	}
	fmt.Fprintf(os.Stderr, "Select session [1-%d]:", len(sessions))

	var choice int
	if _, err := fmt.Fscan(os.Stdin, &choice); err != nil || choice < 1 || choice > len(sessions) {
		return "", fmt.Errorf("invalid selection")
	}
	return sessions[choice-1].ID, nil
}

func (c *Client) pollOutput(ctx context.Context, sessionID string) {
	ticker := time.NewTicker(time.Duration(c.cfg.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Don't poll if websocket connected
			if c.wsClient.IsConnected() {
				continue
			}

			pollResp, err := c.api.Poll(sessionID, types.HostClientID, types.WSMessageOutput)
			if err != nil {
				continue
			}

			for _, entry := range pollResp.Elements {
				var chunk types.OutputChunk
				if err := json.Unmarshal(entry, &chunk); nil != err {
					ch.Log(alog.DEBUG, "Invalid output chunk: %v", err)
				} else {
					renderChunk(chunk)
				}
			}

			if err := c.api.Ack(sessionID, types.HostClientID, types.WSMessageOutput); err != nil {
				ch.Log(alog.DEBUG, "[remote-control] poll ack error: %v", err)
			}
		}
	}
}

func (c *Client) pollSessionCompletion(cancelCtx context.Context, sessionID string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cancelCtx.Done():
			return
		case <-ticker.C:
			info, err := c.api.GetSession(sessionID)
			if err != nil {
				continue
			}
			if info.Status == types.SessionStatusCompleted {
				fmt.Fprintln(os.Stderr, "[remote-control] Session completed.")
				return
			}
		}
	}
}

func (c *Client) waitForApproval(cancelCtx context.Context, sessionID string) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-cancelCtx.Done():
			return cancelCtx.Err()
		case <-ticker.C:
			// If poll succeeds, client is authorized. Result is ignored. Polled
			// elements will be marked on the server, but not purged without an
			// ack, so they will be re-sent with the full poll start.
			_, err := c.api.Poll(sessionID, c.clientID, types.WSMessageOutput)
			if err != nil {
				continue
			}
			return nil
		}
	}
}

// initWebSocket establishes a WebSocket connection for real-time communication.
func (c *Client) initWebSocket(ctx context.Context, sessionID string) {
	// Check if WebSocket is enabled in config
	if !c.cfg.EnableWebSocket {
		ch.Log(alog.DEBUG, "[remote-control] WebSocket disabled, using HTTP polling mode")
		c.wsClient = nil
		return
	}

	// Derive WebSocket URL from ServerURL
	wsURL := ws.DeriveWebSocketURL(c.cfg.ServerURL)
	ch.Log(alog.DEBUG, "[remote-control] WebSocket URL: %s (session: %s)", wsURL, sessionID)

	// Build WebSocket config
	wsConfig := buildWebSocketConfig(c.cfg)

	// Create WebSocket connection
	c.wsClient = NewWebSocketConnection(wsURL, c.tlsConfig, c.clientID, sessionID, wsConfig)

	// Render output when received on websocket
	c.wsClient.OnOutput(func(chunk types.OutputChunk) {
		renderChunk(chunk)
	})

	// Attempt to connect
	err := c.wsClient.Connect(ctx)
	if err != nil {
		ch.Log(alog.DEBUG, "[remote-control] WebSocket connection failed, will use HTTP polling: %v", err)
		c.wsClient = nil
	}
}

func (c *Client) closeWebSocket() {
	if c.wsClient != nil {
		c.wsClient.Close()
	}
}

func (c *Client) sendStdin(sessionID string, data []byte) error {
	// Send to server via WebSocket if connected, otherwise HTTP
	if c.wsClient != nil && c.wsClient.IsConnected() {
		if err := c.wsClient.SendStdinEntry(data); err != nil {
			ch.Log(alog.DEBUG, "[remote-control] WebSocket send stdin error: %v", err)
		} else {
			return nil
		}
	}
	if err := c.api.EnqueueStdin(sessionID, c.clientID, data); err != nil {
		ch.Log(alog.DEBUG, "[remote-control] enqueue stdin error: %v", err)
		return err
	}
	return nil
}

/* -- Public ---------------------------------------------------------------- */

func (c *Client) Run(ctx context.Context, sessionID string) error {
	// If no explicit session provided, run the picker sequence
	if sessionID == "" {
		var err error
		sessionID, err = c.pickSession(ctx)
		if err != nil {
			return err
		}
	}

	// Set up the context for shutdown
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Register with the server
	clientID, status, err := c.api.RegisterClient(sessionID, c.clientID)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	c.clientID = clientID
	ch.Log(alog.DEBUG, "[remote-control] Registered with client ID: %s", c.clientID)

	// If approval stuck in pending, wait a bit for approval, then error
	if status == types.ApprovalPending {
		fmt.Fprintln(os.Stderr, "[remote-control] Waiting for host approval...")
		if err := c.waitForApproval(cancelCtx, sessionID); err != nil {
			return err
		}
	}

	// Enter raw mode if acting as a terminal so bytes are rendered exactly
	isRawMode := term.IsTerminal(int(os.Stdin.Fd()))
	if isRawMode {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("make raw: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	// Initialize the websocket
	ch.Log(alog.DEBUG2, "Initializing websocket")
	c.initWebSocket(ctx, sessionID)
	defer c.closeWebSocket()

	// Set up persistent polls in isolated goroutines
	ch.Log(alog.DEBUG2, "Setting up polling")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c.pollOutput(cancelCtx, sessionID)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.pollSessionCompletion(cancelCtx, sessionID)
	}()

	// Small sleep in raw mode
	// GLG: do we need this??
	if isRawMode {
		time.Sleep(200 * time.Millisecond)
	}

	// In raw mode, handle SIGWINCH for terminal size adjustments
	if isRawMode {
		ch.Log(alog.DEBUG2, "Enabling SIGWINCH")
		sigWinchCh := make(chan os.Signal, 1)
		signal.Notify(sigWinchCh, syscall.SIGWINCH)
		defer signal.Stop(sigWinchCh)

		go func() {
			for {
				select {
				case <-cancelCtx.Done():
					return
				case <-sigWinchCh:
					cols, rows, err := term.GetSize(int(os.Stdin.Fd()))
					if err == nil {
						// TODO: adjust local terminal size
						_ = cols
						_ = rows
					}
				}
			}
		}()
	}

	stopStdin := make(chan struct{})
	defer close(stopStdin)

	go func() {
		if isRawMode {
			buf := make([]byte, 32)
			for {
				select {
				case <-cancelCtx.Done():
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
					c.sendStdin(sessionID, data)
				}
				if shouldExit {
					cancel()
					return
				}
			}
		} else {
			buf := make([]byte, 4096)
			for {
				select {
				case <-cancelCtx.Done():
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
				c.sendStdin(sessionID, data)
			}
		}
	}()

	ch.Log(alog.DEBUG2, "Waiting for completion")
	<-cancelCtx.Done()
	wg.Wait()

	return nil
}
