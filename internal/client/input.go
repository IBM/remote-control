package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"golang.org/x/term"
)

// inputReader reads stdin from the user and submits entries to the server.
type inputReader struct {
	client    *APIClient
	sessionID string
	clientID  string
}

func newInputReader(client *APIClient, sessionID, clientID string) *inputReader {
	return &inputReader{
		client:    client,
		sessionID: sessionID,
		clientID:  clientID,
	}
}

// run reads from os.Stdin and submits entries to the server until ctx is cancelled.
// In raw mode (interactive terminal), reads individual bytes for control character support.
// In cooked mode (pipes, redirects), reads complete lines.
func (ir *inputReader) run(ctx context.Context) {
	isRawMode := term.IsTerminal(int(os.Stdin.Fd()))
	
	if isRawMode {
		ir.runRaw(ctx)
	} else {
		ir.runCooked(ctx)
	}
}

// runRaw reads individual bytes from stdin (raw mode) for full control character support.
func (ir *inputReader) runRaw(ctx context.Context) {
	buf := make([]byte, 1)
	for {
		select {
		case <-ctx.Done():
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

		// Send raw byte immediately (no buffering for responsiveness).
		entryID, err := ir.client.EnqueueStdin(ir.sessionID, ir.clientID, buf[:n])
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				// In raw mode, can't write to stderr without disrupting TUI.
				// Just log and continue.
				ch.Log(alog.WARNING, "[remote-control] stdin not permitted")
			} else {
				ch.Log(alog.WARNING, "[remote-control] enqueue stdin error: %v", err)
			}
			continue
		}

		// Poll for acceptance or rejection in background.
		go ir.watchStatus(ctx, entryID)
	}
}

// runCooked reads complete lines from stdin (cooked mode) for non-interactive use.
func (ir *inputReader) runCooked(ctx context.Context) {
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
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

		entryID, err := ir.client.EnqueueStdin(ir.sessionID, ir.clientID, data)
		if err != nil {
			if errors.Is(err, ErrForbidden) {
				fmt.Fprintf(os.Stderr, "[remote-control] stdin not permitted (read-only or not approved)\n")
			} else {
				ch.Log(alog.WARNING, "[remote-control] enqueue stdin error: %v", err)
			}
			continue
		}

		// Poll for acceptance or rejection.
		go ir.watchStatus(ctx, entryID)
	}
}

// watchStatus polls the server until the entry is accepted or rejected.
func (ir *inputReader) watchStatus(ctx context.Context, entryID string) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
			status, err := ir.client.GetStdinStatus(ir.sessionID, entryID)
			if err != nil {
				return
			}
			switch status {
			case "accepted":
				return
			case "rejected":
				fmt.Fprintf(os.Stderr, "[remote-control] Input rejected — host submitted input first\n")
				return
			}
		}
	}
}
