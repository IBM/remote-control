package host

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"os"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

// syncWriter wraps an io.Writer with a mutex so concurrent writes are safe.
// Used for the subprocess's stdin pipe which can receive from both local
// terminal input and remote server stdin entries.
type syncWriter struct {
	mu sync.Mutex
	w  io.WriteCloser
}

func (sw *syncWriter) Write(p []byte) (n int, err error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}

func (sw *syncWriter) Close() error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Close()
}

// proxyOutput reads from r (a subprocess pipe), writes to local dst, and
// forwards each chunk to the server as timestamped output.
// stream is "stdout" or "stderr".
func (h *Host) proxyOutput(ctx context.Context, r io.Reader, dst io.Writer, client *APIClient, sessionID, stream string) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			ts := time.Now() // host-grounded timestamp

			// Write to local terminal.
			if _, werr := dst.Write(chunk); werr != nil {
				ch.Log(alog.WARNING, "[remote-control] local %s write error: %v", stream, werr)
			}

			// Forward to server (non-blocking on context cancel).
			if client != nil {
				select {
				case <-ctx.Done():
					return
				default:
					if serr := client.AppendOutput(sessionID, stream, chunk, ts); serr != nil {
						ch.Log(alog.WARNING, "[remote-control] append output error: %v", serr)
						// TODO Phase 10: buffer locally and retry
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				ch.Log(alog.WARNING, "[remote-control] %s pipe read error: %v", stream, err)
			}
			return
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// proxyLocalStdin reads lines from os.Stdin, writes each line to the subprocess
// stdin pipe, and bulk-rejects any pending client stdin entries (local wins).
//
// Stdin is read in a background goroutine so the outer loop can select on
// ctx.Done() and exit promptly when the context is cancelled (e.g., on SIGTERM).
// The background goroutine may remain blocked on scanner.Scan() after the main
// loop exits, but since the process is terminating at that point this is safe.
func (h *Host) proxyLocalStdin(ctx context.Context, stdinPipe *syncWriter, client *APIClient, sessionID string) {
	type scanResult struct{ line []byte }
	lineCh := make(chan scanResult, 1)

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			line := make([]byte, len(scanner.Bytes()))
			copy(line, scanner.Bytes())
			select {
			case lineCh <- scanResult{line: line}:
			case <-ctx.Done():
				return
			}
		}
		close(lineCh)
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case res, ok := <-lineCh:
			if !ok {
				// stdin closed (EOF) — close the subprocess stdin pipe.
				stdinPipe.Close()
				return
			}

			// Check whether the side-channel is active (a prompt is being shown).
			h.sideChannelMu.Lock()
			active := h.sideChannelActive
			h.sideChannelMu.Unlock()
			if active {
				// Side-channel owns os.Stdin; skip this line.
				continue
			}

			lineWithNewline := append(res.line, '\n')
			if _, err := stdinPipe.Write(lineWithNewline); err != nil {
				ch.Log(alog.WARNING, "[remote-control] subprocess stdin write error: %v", err)
				return
			}

			// Reject all pending client stdin (host wins).
			if err := client.RejectAllPending(sessionID); err != nil {
				ch.Log(alog.WARNING, "[remote-control] reject-all error: %v", err)
			}
		}
	}
}

// proxyServerStdin polls the server for pending client stdin entries and
// forwards accepted ones to the subprocess stdin pipe.
func (h *Host) proxyServerStdin(ctx context.Context, stdinPipe *syncWriter, client *APIClient, sessionID string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entry, err := client.PeekStdin(sessionID)
			if err != nil {
				ch.Log(alog.WARNING, "[remote-control] peek stdin error: %v", err)
				continue
			}
			if entry == nil {
				continue
			}

			// Decode and write to subprocess.
			data, err := base64.StdEncoding.DecodeString(entry.Data)
			if err != nil || len(data) == 0 {
				_ = client.RejectStdin(sessionID, entry.ID)
				continue
			}

			// Accept first (sets host-grounded timestamp on server), then write.
			if err := client.AcceptStdin(sessionID, entry.ID); err != nil {
				ch.Log(alog.WARNING, "[remote-control] accept stdin error: %v", err)
				continue
			}

			if _, err := stdinPipe.Write(data); err != nil {
				ch.Log(alog.WARNING, "[remote-control] subprocess stdin write error: %v", err)
				return
			}
		}
	}
}
