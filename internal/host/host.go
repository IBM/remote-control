package host

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"

	"github.com/gabe-l-hart/remote-control/internal/config"
	"github.com/gabe-l-hart/remote-control/internal/tlsconfig"
)

var ch = alog.UseChannel("HOST")

// Host manages the subprocess lifecycle and I/O proxying.
type Host struct {
	cfg    *config.Config
	client *APIClient

	// Side-channel: pause proxyLocalStdin when a host prompt needs os.Stdin.
	sideChannelMu     sync.Mutex
	sideChannelActive bool
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
func (h *Host) Run(ctx context.Context, command []string) error {
	// Create a session on the server.
	sessionID, err := h.client.CreateSession(command)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	h.writeSideChannel("[remote-control] Session ID: %s\n", sessionID)

	// Start the subprocess.
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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

	// Set up cancellable context for goroutines.
	proxyCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup

	// 1. Proxy stdout.
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyOutput(proxyCtx, stdoutPipe, os.Stdout, h.client, sessionID, "stdout")
	}()

	// 2. Proxy stderr.
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyOutput(proxyCtx, stderrPipe, os.Stderr, h.client, sessionID, "stderr")
	}()

	// 3. Poll server for client stdin.
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyServerStdin(proxyCtx, stdinPipe, h.client, sessionID)
	}()

	// 4. Read local terminal stdin.
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.proxyLocalStdin(proxyCtx, stdinPipe, h.client, sessionID)
	}()

	// 5. Poll for client approvals (side-channel prompts to host terminal).
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.pollClientApprovals(proxyCtx, sessionID)
	}()

	// Forward signals to the subprocess process group.
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			if cmd.Process != nil {
				syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
			}
		}
	}()
	defer signal.Stop(sigCh)

	// Wait for subprocess to exit.
	exitCode := 0
	if err := cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			ch.Log(alog.WARNING, "[remote-control] subprocess wait error: %v", err)
		}
	}

	// Cancel proxy goroutines and wait for them to finish.
	cancel()
	wg.Wait()

	// Mark the session complete.
	if err := h.client.CompleteSession(sessionID, exitCode); err != nil {
		ch.Log(alog.WARNING, "[remote-control] complete session error: %v", err)
	}
	h.writeSideChannel("[remote-control] Session complete (exit %d)\n", exitCode)
	return nil
}

// writeSideChannel writes a message to os.Stderr directly.
// It never enters the subprocess pipe or session buffer.
func (h *Host) writeSideChannel(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
}

// readSideChannelLine pauses proxyLocalStdin, reads one line from os.Stdin,
// then resumes proxyLocalStdin.
func (h *Host) readSideChannelLine() (string, error) {
	h.sideChannelMu.Lock()
	h.sideChannelActive = true
	defer func() {
		h.sideChannelActive = false
		h.sideChannelMu.Unlock()
	}()
	reader := bufio.NewReader(os.Stdin)
	return reader.ReadString('\n')
}

// pollClientApprovals polls the server for pending client join requests and
// prompts the host user (via side-channel) to approve or deny each one.
func (h *Host) pollClientApprovals(ctx context.Context, sessionID string) {
	ticker := time.NewTicker(5 * time.Second)
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
				h.handleClientApproval(sessionID, client.ClientID, client.CommonName)
			}
		}
	}
}

// handleClientApproval prompts the host user to approve or deny a client.
func (h *Host) handleClientApproval(sessionID, clientID, commonName string) {
	h.writeSideChannel("\n[remote-control] Client %q (%s) wants to join.\n", commonName, clientID)
	h.writeSideChannel("  [a] approve read-write  [r] read-only  [d] deny: ")

	line, err := h.readSideChannelLine()
	if err != nil {
		ch.Log(alog.WARNING, "[remote-control] read approval response error: %v", err)
		return
	}

	switch string([]byte(line)[0]) {
	case "a", "A":
		if err := h.client.ApproveClient(sessionID, clientID, "read-write"); err != nil {
			ch.Log(alog.WARNING, "[remote-control] approve client error: %v", err)
		} else {
			h.writeSideChannel("[remote-control] Client %q approved (read-write)\n", commonName)
		}
	case "r", "R":
		if err := h.client.ApproveClient(sessionID, clientID, "read-only"); err != nil {
			ch.Log(alog.WARNING, "[remote-control] approve client error: %v", err)
		} else {
			h.writeSideChannel("[remote-control] Client %q approved (read-only)\n", commonName)
		}
	default:
		if err := h.client.DenyClient(sessionID, clientID); err != nil {
			ch.Log(alog.WARNING, "[remote-control] deny client error: %v", err)
		} else {
			h.writeSideChannel("[remote-control] Client %q denied\n", commonName)
		}
	}
}
