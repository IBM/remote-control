package host

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/creack/pty"
	"golang.org/x/term"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/tlsconfig"
)

var ch = alog.UseChannel("HOST")

// Host manages the subprocess lifecycle and I/O proxying.
type Host struct {
	cfg    *config.Config
	client *APIClient

	// Channel-based approval routing (replaces sideChannelMu/sideChannelActive).
	// No mutex is held during I/O — eliminates the deadlock present in the
	// previous sideChannel design.
	approvalMu     sync.Mutex
	approvalActive bool
	approvalRespCh chan byte // nil when not active
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

// buildHTTPClientTLS creates an http.Client from an explicit tls.Config.
func buildHTTPClientTLS(tlsCfg *tls.Config) *http.Client {
	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
	}
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

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyPTYOutput(ctx, ptmx, h.client, sessionID)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyLocalStdinRaw(ctx, ptmx, &ptmxMu, h.client, sessionID)
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
	// Still in raw mode here (defer restores after return); use \r\n.
	h.writeSideChannel(true, "[remote-control] Session complete (exit %d)\n", exitCode)
	return nil
}

// runPipe runs the subprocess with stdin/stdout/stderr pipes (non-TTY mode).
// This is the path taken in tests, CI, and any non-interactive invocation.
func (h *Host) runPipe(ctx context.Context, cancel context.CancelFunc, command []string, sessionID string) error {
	h.writeSideChannel(false, "[remote-control] Session ID: %s\n", sessionID)

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
		h.proxyOutput(ctx, stdoutPipe, os.Stdout, h.client, sessionID, "stdout")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyOutput(ctx, stderrPipe, os.Stderr, h.client, sessionID, "stderr")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyServerStdin(ctx, stdinPipe, h.client, sessionID)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyLocalStdin(ctx, stdinPipe, h.client, sessionID)
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

// waitForApprovalByte signals that a host approval prompt is active and waits
// for a single byte response from proxyLocalStdin or proxyLocalStdinRaw.
// No mutex is held during the wait — this is the deadlock-free replacement
// for the old readSideChannelLine design.
func (h *Host) waitForApprovalByte(ctx context.Context) (byte, error) {
	respCh := make(chan byte, 1)
	h.approvalMu.Lock()
	h.approvalActive = true
	h.approvalRespCh = respCh
	h.approvalMu.Unlock()

	defer func() {
		h.approvalMu.Lock()
		h.approvalActive = false
		h.approvalRespCh = nil
		h.approvalMu.Unlock()
	}()

	select {
	case b := <-respCh:
		return b, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// pollClientApprovals polls the server for pending client join requests and
// prompts the host user (via side-channel) to approve or deny each one.
func (h *Host) pollClientApprovals(ctx context.Context, sessionID string, rawMode bool) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !h.cfg.RequireApproval {
				continue
			}
			clients, err := h.client.ListPendingClients(sessionID)
			if err != nil {
				ch.Log(alog.WARNING, "[remote-control] list pending clients error: %v", err)
				continue
			}
			for _, client := range clients {
				h.handleClientApproval(ctx, sessionID, client.ClientID, client.CommonName, rawMode)
			}
		}
	}
}

// handleClientApproval prompts the host user to approve or deny a client.
func (h *Host) handleClientApproval(ctx context.Context, sessionID, clientID, commonName string, rawMode bool) {
	h.writeSideChannel(rawMode, "\n[remote-control] Client %q (%s) wants to join.\n", commonName, clientID)
	h.writeSideChannel(rawMode, "  [a] approve read-write  [r] read-only  [d] deny: ")

	b, err := h.waitForApprovalByte(ctx)
	if err != nil {
		ch.Log(alog.WARNING, "[remote-control] read approval response error: %v", err)
		return
	}

	// Echo the typed character back.
	h.writeSideChannel(rawMode, "%c\n", b)

	switch b {
	case 'a', 'A':
		if err := h.client.ApproveClient(sessionID, clientID, "read-write"); err != nil {
			ch.Log(alog.WARNING, "[remote-control] approve client error: %v", err)
		} else {
			h.writeSideChannel(rawMode, "[remote-control] Client %q approved (read-write)\n", commonName)
		}
	case 'r', 'R':
		if err := h.client.ApproveClient(sessionID, clientID, "read-only"); err != nil {
			ch.Log(alog.WARNING, "[remote-control] approve client error: %v", err)
		} else {
			h.writeSideChannel(rawMode, "[remote-control] Client %q approved (read-only)\n", commonName)
		}
	default:
		if err := h.client.DenyClient(sessionID, clientID); err != nil {
			ch.Log(alog.WARNING, "[remote-control] deny client error: %v", err)
		} else {
			h.writeSideChannel(rawMode, "[remote-control] Client %q denied\n", commonName)
		}
	}
}
