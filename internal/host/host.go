package host

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/creack/pty"
	"golang.org/x/term"

	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/gabe-l-hart/remote-control/internal/common/tlsconfig"
)

var ch = alog.UseChannel("HOST")

// Host manages the subprocess lifecycle and I/O proxying.
type Host struct {
	cfg    *config.Config
	client *APIClient

	// WebSocket connection for real-time communication
	wsHost *WebSocketHost

	// Channel-based approval routing
	approvalMu     sync.Mutex
	approvalActive bool
	approvalRespCh chan byte // nil when not active

	// PTY-mode subprocess management.
	// subprocessPid is set once in runPTY before goroutines start; safe to read
	// without synchronization (goroutine-start provides the happens-before edge).
	subprocessPid int
	// pauseOutput suppresses PTY→stdout forwarding during approval prompts so
	// that the subprocess's TUI cannot re-render over the prompt text.
	pauseOutput atomic.Bool
}

// NewHost creates a Host from the given config.
func NewHost(cfg *config.Config) *Host {
	httpClient := buildHTTPClient(cfg)
	return &Host{
		cfg:    cfg,
		client: NewAPIClient(cfg.ServerURL, httpClient),
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

// buildTLSConfig creates a TLS config for WebSocket connections.
func buildTLSConfig(cfg *config.Config) *tls.Config {
	if cfg.ClientTLS.CertFile == "" || cfg.ClientTLS.KeyFile == "" || cfg.ClientTLS.TrustedCAFile == "" {
		return nil
	}
	tlsCfg, err := tlsconfig.BuildClientTLSConfig(
		cfg.ClientTLS.CertFile,
		cfg.ClientTLS.KeyFile,
		cfg.ClientTLS.TrustedCAFile,
	)
	if err != nil {
		ch.Log(alog.WARNING, "[remote-control] TLS config error: %v", err)
		return nil
	}
	return tlsCfg
}

// Run starts the subprocess specified by command, creates a server session,
// proxies all I/O, and waits for the process to exit.
//
// PTY mode is used when os.Stdin is a real TTY (interactive terminal), giving
// the subprocess a proper controlling terminal with PS1, colors, and readline.
// Pipe mode is used otherwise (tests, CI, scripts) and preserves existing behavior.
func (h *Host) Run(ctx context.Context, command []string) error {
	sessionID, err := h.client.CreateSession(command)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if term.IsTerminal(int(os.Stdin.Fd())) {
		return h.runPTY(proxyCtx, cancel, command, sessionID)
	}
	return h.runPipe(proxyCtx, cancel, command, sessionID)
}

// runPTY runs the subprocess with a PTY, giving it a proper controlling terminal.
// The host terminal is put into raw mode and SIGWINCH is forwarded for resize support.
func (h *Host) runPTY(ctx context.Context, cancel context.CancelFunc, command []string, sessionID string) error {
	h.writeSideChannel(false, "[remote-control] Session ID: %s\n", sessionID)

	// Establish WebSocket connection
	h.initWebSocket(ctx, sessionID)
	defer h.closeWebSocket()

	// Use exec.Command (not CommandContext) — PTY lifecycle is managed manually.
	// Do NOT set SysProcAttr here: pty.Start sets Setsid/Setctty/Ctty itself,
	// and pre-setting SysProcAttr (e.g. Setpgid) conflicts with that on macOS,
	// causing EPERM. After Setsid, pgid==pid so Kill(-pid,sig) still works.
	cmd := exec.Command(command[0], command[1:]...)
	cmd.WaitDelay = 3 * time.Second

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty start: %w", err)
	}
	defer ptmx.Close()

	// Record the subprocess PID so handleClientApproval can pause/resume it.
	if cmd.Process != nil {
		h.subprocessPid = cmd.Process.Pid
	}

	// Set PTY size to match host terminal.
	cols, rows, _ := term.GetSize(int(os.Stdin.Fd()))
	_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})

	// Put host terminal in raw mode so the subprocess sees a real TTY.
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("make raw: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState) //nolint:errcheck

	var ptmxMu sync.Mutex

	// Handle SIGWINCH — resize the PTY when the host terminal changes size.
	sigWinchCh := make(chan os.Signal, 1)
	signal.Notify(sigWinchCh, syscall.SIGWINCH)
	defer signal.Stop(sigWinchCh)
	go func() {
		for range sigWinchCh {
			cols, rows, _ := term.GetSize(int(os.Stdin.Fd()))
			ptmxMu.Lock()
			_ = pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
			ptmxMu.Unlock()
		}
	}()

	// Track offsets for output
	var stdoutOffset int64
	var stdoutOffsetMu sync.Mutex

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyPTYOutput(ctx, ptmx, h.client, sessionID, h.wsHost, &stdoutOffset, &stdoutOffsetMu)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyLocalStdinRaw(ctx, ptmx, &ptmxMu, h.client, sessionID, h.wsHost)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyServerStdinPTY(ctx, ptmx, &ptmxMu, h.client, sessionID)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.pollClientApprovals(ctx, sessionID, true)
	}()

	// Forward OS signals to the subprocess process group.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	go func() {
		for sig := range sigCh {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
			}
			if sig == syscall.SIGINT || sig == syscall.SIGTERM {
				cancel()
				return
			}
		}
	}()

	// Wait for subprocess to exit.
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			ch.Log(alog.WARNING, "[remote-control] subprocess wait error: %v", err)
		}
	}

	cancel()
	wg.Wait()

	if err := h.client.CompleteSession(sessionID, exitCode); err != nil {
		ch.Log(alog.WARNING, "[remote-control] complete session error: %v", err)
	}
	h.writeSideChannel(true, "[remote-control] Session complete (exit %d)\n", exitCode)
	return nil
}

// runPipe runs the subprocess with stdin/stdout/stderr pipes (non-TTY mode).
// This is the path taken in tests, CI, and any non-interactive invocation.
func (h *Host) runPipe(ctx context.Context, cancel context.CancelFunc, command []string, sessionID string) error {
	h.writeSideChannel(false, "[remote-control] Session ID: %s\n", sessionID)

	// Establish WebSocket connection
	h.initWebSocket(ctx, sessionID)
	defer h.closeWebSocket()

	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 3 * time.Second

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}
	stdinPipeRaw, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdinPipe := &syncWriter{w: stdinPipeRaw}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start subprocess: %w", err)
	}

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyOutput(ctx, stdoutPipe, os.Stdout, h.client, sessionID, types.StreamStdout, h.wsHost)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyOutput(ctx, stderrPipe, os.Stderr, h.client, sessionID, types.StreamStderr, h.wsHost)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyLocalStdin(ctx, stdinPipe, h.client, sessionID)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyServerStdin(ctx, stdinPipe, h.client, sessionID)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.pollClientApprovals(ctx, sessionID, false)
	}()

	// Forward OS signals to the subprocess process group.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)
	go func() {
		for sig := range sigCh {
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
			}
			if sig == syscall.SIGINT || sig == syscall.SIGTERM {
				cancel()
				return
			}
		}
	}()

	// Wait for subprocess to exit.
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			ch.Log(alog.WARNING, "[remote-control] subprocess wait error: %v", err)
		}
	}

	cancel()
	wg.Wait()

	if err := h.client.CompleteSession(sessionID, exitCode); err != nil {
		ch.Log(alog.WARNING, "[remote-control] complete session error: %v", err)
	}
	h.writeSideChannel(false, "[remote-control] Session complete (exit %d)\n", exitCode)
	return nil
}

// writeSideChannel writes a message to os.Stderr directly.
// It never enters the subprocess pipe or session buffer.
// In rawMode, \n is replaced with \r\n for raw terminal compatibility.
func (h *Host) writeSideChannel(rawMode bool, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if rawMode {
		msg = strings.ReplaceAll(msg, "\n", "\r\n")
	}
	fmt.Fprint(os.Stderr, msg)
}

// initWebSocket establishes a WebSocket connection for real-time communication.
func (h *Host) initWebSocket(ctx context.Context, sessionID string) {
	// Check if WebSocket is enabled in config
	if !h.cfg.EnableWebSocket {
		ch.Log(alog.DEBUG, "[remote-control] WebSocket disabled, using HTTP polling mode")
		h.wsHost = nil
		return
	}

	// Build TLS config if available
	tlsCfg := buildTLSConfig(h.cfg)

	// Derive WebSocket URL from ServerURL
	wsURL := deriveWebSocketURL(h.cfg.ServerURL)
	ch.Log(alog.DEBUG, "[remote-control] WebSocket URL: %s (session: %s)", wsURL, sessionID)

	// Create WebSocket host connection
	h.wsHost = NewWebSocketHost(wsURL, tlsCfg, sessionID, types.HostClientID)

	// Set up pending client handler - uses WebSocket callback with HTTP poll/ack fallback
	h.wsHost.OnPendingClient(func(clientID string) {
		if !h.cfg.RequireApproval {
			return
		}
		h.handleClientApproval(ctx, sessionID, clientID, true)
	})

	// Set up stdin handler - receives stdin from other clients via WebSocket
	h.wsHost.OnStdin(func(entry types.StdinEntry) {
		// stdin from other clients is handled in proxyServerStdin
		// this callback is kept for potential future use
		_ = entry
	})

	// Set up error handler
	h.wsHost.OnError(func(err error) {
		ch.Log(alog.DEBUG, "[remote-control] WebSocket error: %v", err)
	})

	err := h.wsHost.Connect(ctx)
	if err != nil {
		ch.Log(alog.DEBUG, "[remote-control] WebSocket connection failed, will use HTTP polling: %v", err)
		h.wsHost = nil
	}
}

func (h *Host) closeWebSocket() {
	if h.wsHost != nil {
		h.wsHost.Close()
	}
}

// deriveWebSocketURL converts http(s):// URLs to ws(s):// URLs
// It strips any existing path and query parameters since the WebSocket path is constructed separately
func deriveWebSocketURL(httpURL string) string {
	parsed, err := url.Parse(httpURL)
	if err != nil {
		return httpURL
	}

	switch parsed.Scheme {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	default:
		// Return as-is if already ws/wss or unknown
		return httpURL
	}

	// Reset path and query - the caller will add /ws/{sessionID}
	parsed.Path = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""

	return parsed.String()
}

// pollClientApprovals handles client approval notifications.
// Uses HTTP polling as fallback when WebSocket is disconnected.
func (h *Host) pollClientApprovals(ctx context.Context, sessionID string, rawMode bool) {
	// Always use the poll/ack fallback for client approvals
	// WebSocket callbacks are handled by the WebSocket readPump
	h.pollPendingClients(ctx, sessionID, rawMode)
}

// armApprovalChannel marks the host as waiting for an approval byte and returns
// a buffered channel that will receive it. The caller must call disarmApprovalChannel
// when done, typically via defer. Arming before writing the prompt eliminates the
// race window where a fast keypress could reach the subprocess instead.
func (h *Host) armApprovalChannel() chan byte {
	respCh := make(chan byte, 1)
	h.approvalMu.Lock()
	h.approvalActive = true
	h.approvalRespCh = respCh
	h.approvalMu.Unlock()
	return respCh
}

// disarmApprovalChannel clears the approval state set by armApprovalChannel.
func (h *Host) disarmApprovalChannel() {
	h.approvalMu.Lock()
	h.approvalActive = false
	h.approvalRespCh = nil
	h.approvalMu.Unlock()
}

// handleClientApproval prompts the host user to approve or deny a client.
//
// For PTY-mode subprocesses (subprocessPid != 0) the subprocess is paused with
// SIGSTOP before the prompt is displayed and resumed with SIGCONT only after all
// side-channel status messages have been written. This prevents full-screen TUI
// frameworks like Ink (used by Claude Code) from re-rendering over the prompt.
//
// The approval channel is armed before writing the prompt to eliminate the race
// window between displaying the prompt and starting to capture the response byte.
func (h *Host) handleClientApproval(ctx context.Context, sessionID, clientID string, rawMode bool) {
	// 1. Arm the approval channel BEFORE writing the prompt.
	respCh := h.armApprovalChannel()
	defer h.disarmApprovalChannel()

	// 2. In PTY mode, pause the subprocess so its TUI cannot overwrite the prompt.
	if h.subprocessPid != 0 {
		if err := syscall.Kill(-h.subprocessPid, syscall.SIGSTOP); nil != err {
			ch.Log(alog.WARNING, "[remote-control] error sending SIGSTOP to subprocess: %v", ctx.Err())
		}
		h.pauseOutput.Store(true)
		// Brief sleep to let any PTY bytes already in flight drain through
		// proxyPTYOutput before we write to stderr.
		time.Sleep(50 * time.Millisecond)
	}

	// 3. Write the approval prompt.
	h.writeSideChannel(rawMode, "\n[remote-control] Client (%s) wants to join.\n", clientID)
	h.writeSideChannel(rawMode, "  [a] approve read-write  [r] read-only  [d] deny: ")

	// 4. Wait for the operator's response byte.
	for true {
		var b byte
		select {
		case b = <-respCh:
		case <-ctx.Done():
			ch.Log(alog.DEBUG, "[remote-control] read approval response error: %v", ctx.Err())
			if h.subprocessPid != 0 {
				h.pauseOutput.Store(false)
				_ = syscall.Kill(-h.subprocessPid, syscall.SIGCONT)
			}
			return
		}

		// 5. Echo the typed character and write the status message while the
		//    subprocess is still paused, so the TUI doesn't overwrite them.
		h.writeSideChannel(rawMode, "%c\n", b)

		var done bool = false
		switch b {
		case 'a', 'A':
			if err := h.client.ApproveClient(sessionID, clientID, "read-write"); err != nil {
				ch.Log(alog.DEBUG, "[remote-control] approve client error: %v", err)
			} else {
				h.writeSideChannel(rawMode, "[remote-control] Client %q approved (read-write)\n", clientID)
				done = true
			}
		case 'r', 'R':
			if err := h.client.ApproveClient(sessionID, clientID, "read-only"); err != nil {
				ch.Log(alog.DEBUG, "[remote-control] approve client error: %v", err)
			} else {
				h.writeSideChannel(rawMode, "[remote-control] Client %q approved (read-only)\n", clientID)
				done = true
			}
		case 'd', 'D':
			if err := h.client.DenyClient(sessionID, clientID); err != nil {
				ch.Log(alog.DEBUG, "[remote-control] deny client error: %v", err)
			} else {
				h.writeSideChannel(rawMode, "[remote-control] Client %q denied\n", clientID)
				done = true
			}
		default:
			ch.Log(alog.DEBUG, "[remote-control] invalid response: %v", b)
		}
		if done {
			break
		} else {
			respCh = h.armApprovalChannel()
		}
	}

	// 6. Resume the subprocess AFTER all side-channel messages are written.
	if h.subprocessPid != 0 {
		h.pauseOutput.Store(false)
		_ = syscall.Kill(-h.subprocessPid, syscall.SIGCONT)
	}
}
