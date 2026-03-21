package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/common/tlsconfig"
)

var ch = alog.UseChannel("CLIENT")

type Client struct {
	cfg      *config.Config
	api      *APIClient
	clientID string
}

func NewClient(cfg *config.Config) *Client {
	httpClient := buildHTTPClient(cfg)
	return &Client{
		cfg: cfg,
		api: NewAPIClient(cfg.ServerURL, httpClient),
	}
}

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

func (c *Client) Run(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		var err error
		sessionID, err = c.pickSession(ctx)
		if err != nil {
			return err
		}
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	clientID, status, err := c.api.RegisterClient(sessionID)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	c.clientID = clientID
	ch.Log(alog.DEBUG, "[remote-control] Registered with client ID: %s", c.clientID)

	if status == string(types.ApprovalPending) {
		fmt.Fprintln(os.Stderr, "[remote-control] Waiting for host approval...")
		if err := c.waitForApproval(cancelCtx, sessionID); err != nil {
			return err
		}
	}

	isRawMode := term.IsTerminal(int(os.Stdin.Fd()))
	if isRawMode {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err != nil {
			return fmt.Errorf("make raw: %w", err)
		}
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	if err := c.renderHistory(cancelCtx, sessionID); err != nil {
		return fmt.Errorf("render history: %w", err)
	}

	if isRawMode {
		time.Sleep(200 * time.Millisecond)
	}

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

	wsURL := deriveWebSocketURL(c.cfg.ServerURL, sessionID, c.clientID)

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

	hc.OnOutput(func(chunk types.OutputChunk) {
		renderChunk(chunk)
	})

	hc.OnStdinPending(func(entry StdinEntry) {
		ch.Log(alog.DEBUG, "[remote-control] stdin pending callback: %s", entry.ID)
	})

	if err := hc.Start(); err != nil {
		return fmt.Errorf("start hybrid connection: %w", err)
	}

	if isRawMode {
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
						_ = cols
						_ = rows
					}
				}
			}
		}()
	}

	go func() {
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
	}()

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
					hc.SubmitStdin(string(data))
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
				hc.SubmitStdin(string(data))
			}
		}
	}()

	<-cancelCtx.Done()

	return hc.Close()
}

func deriveWebSocketURL(httpURL, sessionID, clientID string) string {
	parsed, err := url.Parse(httpURL)
	if err != nil {
		return httpURL
	}

	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else if parsed.Scheme == "http" {
		parsed.Scheme = "ws"
	}

	return fmt.Sprintf("%s/ws/%s?client_id=%s", parsed.String(), sessionID, clientID)
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
		fmt.Fprintf(os.Stderr, "  [%d] %s  (%s)\n", i+1, s.ID, s.Status)
	}
	fmt.Fprintf(os.Stderr, "Select session [1-%d]:", len(sessions))

	var choice int
	if _, err := fmt.Fscan(os.Stdin, &choice); err != nil || choice < 1 || choice > len(sessions) {
		return "", fmt.Errorf("invalid selection")
	}
	return sessions[choice-1].ID, nil
}

func (c *Client) renderHistory(_ context.Context, sessionID string) error {
	result, err := c.api.PollOutput(sessionID, c.clientID, 0, 0)
	if err != nil {
		return err
	}

	chunks, ok := result.Elements.([]interface{})
	if !ok {
		return fmt.Errorf("unexpected type for chunks")
	}
	for _, elem := range chunks {
		if chunkMap, ok := elem.(map[string]interface{}); ok {
			chunk := parseOutputChunk(chunkMap)
			renderChunk(chunk)
		}
	}
	return nil
}

func parseOutputChunk(m map[string]interface{}) types.OutputChunk {
	stream := types.StreamUnknown
	if s, ok := m["stream"].(float64); ok {
		stream = types.Stream(s)
	}

	data := make([]byte, 0)
	if d, ok := m["data"].(string); ok {
		data = []byte(d)
	}

	return types.OutputChunk{
		Stream: stream,
		Data:   data,
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
			result, err := c.api.PollOutput(sessionID, c.clientID, 0, 0)
			if err != nil {
				continue
			}
			_ = result
			return nil
		}
	}
}

func (hc *HybridConnection) OnOutput(handler func(types.OutputChunk)) {
	if hc.ws != nil {
		hc.ws.OnOutput(handler)
	}
}

func (hc *HybridConnection) OnStdinPending(handler func(StdinEntry)) {
	if hc.ws != nil {
		hc.ws.OnStdinPending(handler)
	}
}
