package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
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

// run reads lines from os.Stdin and submits them to the server until ctx is cancelled.
func (ir *inputReader) run(ctx context.Context) {
	scanner := bufio.NewScanner(os.Stdin)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if !scanner.Scan() {
			return
		}

		line := scanner.Bytes()
		data := make([]byte, len(line)+1)
		copy(data, line)
		data[len(line)] = '\n'

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
