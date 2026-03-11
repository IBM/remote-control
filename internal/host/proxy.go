package host

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
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
				ch.Log(alog.DEBUG, "[remote-control] local %s write error: %v", stream, werr)
			}

			// Forward to server (non-blocking on context cancel).
			if client != nil {
				select {
				case <-ctx.Done():
					return
				default:
					if serr := client.AppendOutput(sessionID, stream, chunk, ts); serr != nil {
						ch.Log(alog.DEBUG, "[remote-control] append output error: %v", serr)
					}
				}
			}
		}
		if err != nil {
			if err != io.EOF {
				ch.Log(alog.DEBUG, "[remote-control] %s pipe read error: %v", stream, err)
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

// proxyPTYOutput reads from the PTY master (which merges stdout+stderr),
// writes raw bytes to os.Stdout, and forwards each chunk to the server as
// "stdout" stream output. io.EOF and "input/output error" are treated as
// normal PTY HUP (subprocess exited).
func (h *Host) proxyPTYOutput(ctx context.Context, ptmx *os.File, client *APIClient, sessionID string) {
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			ts := time.Now()

			// Skip local display while an approval prompt is shown so the
			// subprocess TUI cannot re-render over the prompt text.
			if !h.pauseOutput.Load() {
				if _, werr := os.Stdout.Write(chunk); werr != nil {
					ch.Log(alog.WARNING, "[remote-control] local stdout write error: %v", werr)
				}
			}

			if client != nil {
				select {
				case <-ctx.Done():
					return
				default:
					if serr := client.AppendOutput(sessionID, "stdout", chunk, ts); serr != nil {
						ch.Log(alog.DEBUG, "[remote-control] append output error: %v", serr)
					}
				}
			}
		}
		if err != nil {
			// io.EOF and "input/output error" are normal when the PTY slave closes.
			if err != io.EOF && !strings.Contains(err.Error(), "input/output error") {
				ch.Log(alog.WARNING, "[remote-control] PTY read error: %v", err)
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

// processHostStdinEntry submits a host stdin entry to the server queue and
// writes it to the subprocess after acceptance.
func (h *Host) processHostStdinEntry(ctx context.Context, data []byte, client *APIClient, sessionID string, writeFunc func([]byte) error) error {
	// Submit to server queue
	entryID, err := client.SubmitHostStdin(sessionID, data)
	if err != nil {
		ch.Log(alog.WARNING, "[remote-control] host stdin enqueue error: %v", err)
		return err
	}

	// Poll for acceptance
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status := client.GetHostStdinStatus(sessionID, entryID)
			switch status {
			case "accepted":
				return writeFunc(data)
			case "rejected":
				// Should not happen for host entries, but handle gracefully
				return fmt.Errorf("host stdin entry unexpectedly rejected")
			case "consumed":
				return fmt.Errorf("host stdin entry consumed")
			default:
				// Still pending, continue polling
			}
		}
	}
}

// writeBatch writes bytes to either stdin pipe or PTY.
func writeBatch(data []byte, stdinPipe *syncWriter, ptmx *os.File, ptmxMu *sync.Mutex) error {
	if stdinPipe != nil {
		_, err := stdinPipe.Write(data)
		return err
	}
	// PTY mode
	if ptmxMu != nil {
		ptmxMu.Lock()
		defer ptmxMu.Unlock()
	}
	_, err := ptmx.Write(data)
	return err
}

// proxyLocalStdin reads lines from os.Stdin, submits each line to the server
// queue, and writes to the subprocess after acceptance. This ensures FIFO
// ordering between host and client inputs.
//
// When an approval prompt is active (approvalActive == true), the first byte of
// the line is routed to approvalRespCh instead of the subprocess — no mutex is
// held during I/O, eliminating the deadlock present in the prior design.
//
// Stdin is read in a background goroutine so the outer loop can select on
// ctx.Done() and exit promptly when the context is cancelled.
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
				// os.Stdin reached EOF. Stop proxying local input but leave
				// stdinPipe open so that server stdin can still flow through.
				return
			}

			// Briefly check whether an approval prompt is waiting for input.
			h.approvalMu.Lock()
			active := h.approvalActive
			var respCh chan byte
			if active {
				respCh = h.approvalRespCh
			}
			h.approvalMu.Unlock()

			if active {
				// Route the first byte to the approval handler; discard rest of line.
				if len(res.line) > 0 {
					select {
					case respCh <- res.line[0]:
					default:
					}
				}
				continue
			}

			lineWithNewline := append(res.line, '\n')

			// Submit through server queue, wait for acceptance, then write
			writeErr := h.processHostStdinEntry(ctx, lineWithNewline, client, sessionID, func(data []byte) error {
				return writeBatch(data, stdinPipe, nil, nil)
			})

			if writeErr != nil {
				ch.Log(alog.DEBUG, "[remote-control] process host stdin entry error: %v", writeErr)
			}
		}
	}
}

// proxyLocalStdinRaw reads individual bytes from os.Stdin (which is in raw
// terminal mode), batches them, submits to the server queue, and writes after
// acceptance. This ensures FIFO ordering between host and client inputs.
//
// Bytes are batched (threshold: 16 bytes or on CR/Enter) to reduce API calls
// while maintaining responsiveness for interactive use.
func (h *Host) proxyLocalStdinRaw(ctx context.Context, ptmx *os.File, ptmxMu *sync.Mutex, client *APIClient, sessionID string) {
	byteCh := make(chan byte, 16)

	go func() {
		buf := make([]byte, 1)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				select {
				case byteCh <- buf[0]:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				close(byteCh)
				return
			}
		}
	}()

	// Batch incoming bytes for submission
	const batchThreshold = 16
	var batch []byte

	for {
		select {
		case <-ctx.Done():
			// Flush remaining batch
			if len(batch) > 0 {
				_ = h.processHostStdinEntry(ctx, batch, client, sessionID, func(data []byte) error {
					return writeBatch(data, nil, ptmx, ptmxMu)
				})
			}
			return
		case b, ok := <-byteCh:
			if !ok {
				return
			}

			h.approvalMu.Lock()
			active := h.approvalActive
			var respCh chan byte
			if active {
				respCh = h.approvalRespCh
			}
			h.approvalMu.Unlock()

			if active {
				select {
				case respCh <- b:
				default:
				}
				continue
			}

			// Handle CR - flush any accumulated batch first, then submit CR as separate entry
			if b == 0x0d {
				if len(batch) > 0 {
					_ = h.processHostStdinEntry(ctx, batch, client, sessionID, func(data []byte) error {
						return writeBatch(data, nil, ptmx, ptmxMu)
					})
					batch = nil
				}
				// Submit CR as single-byte entry for immediate processing
				_ = h.processHostStdinEntry(ctx, []byte{b}, client, sessionID, func(data []byte) error {
					return writeBatch(data, nil, ptmx, ptmxMu)
				})
				continue
			}

			// Accumulate byte into batch
			batch = append(batch, b)

			// Flush batch when threshold reached
			if len(batch) >= batchThreshold {
				_ = h.processHostStdinEntry(ctx, batch, client, sessionID, func(data []byte) error {
					return writeBatch(data, nil, ptmx, ptmxMu)
				})
				batch = nil
			}
		}
	}
}

// processServerStdinEntry accepts a pending entry from the server and writes
// it to the subprocess. Handles both client and host entries.
func (h *Host) processServerStdinEntry(ctx context.Context, client *APIClient, sessionID string, writeFunc func([]byte) error) error {
	// Peek the oldest pending entry
	entry, err := client.PeekStdin(sessionID)
	if err != nil {
		ch.Log(alog.DEBUG, "[remote-control] peek stdin error: %v", err)
		return err
	}
	if entry == nil {
		return nil // No pending entries
	}

	// Auto-accept host entries, accept client entries
	isHostEntry := entry.Source == "host"
	if !isHostEntry {
		// For client entries, we need explicit acceptance
		// (Note: In the unified model, host decides to accept or reject)
	}

	// Accept the entry first
	if err := client.AcceptStdin(sessionID, entry.ID); err != nil {
		ch.Log(alog.DEBUG, "[remote-control] accept stdin error: %v", err)
		// Reject if accept failed to avoid stale entries
		_ = client.RejectStdin(sessionID, entry.ID)
		return err
	}

	// Decode and write
	data, err := base64.StdEncoding.DecodeString(entry.Data)
	if err != nil || len(data) == 0 {
		_ = client.RejectStdin(sessionID, entry.ID)
		return fmt.Errorf("invalid entry data")
	}

	return writeFunc(data)
}

// proxyServerStdin polls the server for pending stdin entries (both host and
// client) and processes them by accepting and writing to the subprocess.
// This runs in parallel with proxyLocalStdin to handle both input sources.
func (h *Host) proxyServerStdin(ctx context.Context, stdinPipe *syncWriter, client *APIClient, sessionID string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := h.processServerStdinEntry(ctx, client, sessionID, func(data []byte) error {
				_, err := stdinPipe.Write(data)
				if err != nil {
					return err
				}
				return nil
			})
			if err != nil && err.Error() != "no pending entries" {
				// Ignore "no pending" errors - just means nothing to process
				if err.Error() != "" {
					ch.Log(alog.DEBUG, "[remote-control] server stdin processing: %v", err)
				}
			}
		}
	}
}

// proxyServerStdinPTY polls the server for pending stdin entries (both host
// and client) and processes them for PTY mode.
func (h *Host) proxyServerStdinPTY(ctx context.Context, ptmx *os.File, ptmxMu *sync.Mutex, client *APIClient, sessionID string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = h.processServerStdinEntry(ctx, client, sessionID, func(data []byte) error {
				ptmxMu.Lock()
				defer ptmxMu.Unlock()
				_, err := ptmx.Write(data)
				return err
			})
		}
	}
}
