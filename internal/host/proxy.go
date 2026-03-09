package host

import (
	"bufio"
	"context"
	"encoding/base64"
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

// proxyLocalStdin reads lines from os.Stdin, writes each line to the subprocess
// stdin pipe, and bulk-rejects any pending client stdin entries (local wins).
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
				// stdinPipe open so that server stdin (proxyServerStdin) can
				// still flow through. The subprocess's stdin is implicitly
				// closed when it exits or the context is cancelled.
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
			if _, err := stdinPipe.Write(lineWithNewline); err != nil {
				ch.Log(alog.WARNING, "[remote-control] subprocess stdin write error: %v", err)
				return
			}

			// Reject all pending client stdin (host wins).
			if err := client.RejectAllPending(sessionID); err != nil {
				ch.Log(alog.DEBUG, "[remote-control] reject-all error: %v", err)
			}
		}
	}
}

// proxyLocalStdinRaw reads individual bytes from os.Stdin (which is in raw
// terminal mode) and routes them: approval bytes go to the approval channel,
// all other bytes go directly to the PTY master.
//
// A 1-byte read buffer is used for responsiveness (no buffering lag).
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

	for {
		select {
		case <-ctx.Done():
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

			ptmxMu.Lock()
			_, _ = ptmx.Write([]byte{b})
			ptmxMu.Unlock()

			// CR (0x0d) is Enter in raw mode — host typed input, so reject
			// any pending client stdin (host wins).
			if b == 0x0d {
				if err := client.RejectAllPending(sessionID); err != nil {
					ch.Log(alog.DEBUG, "[remote-control] reject-all error: %v", err)
				}
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
				ch.Log(alog.DEBUG, "[remote-control] peek stdin error: %v", err)
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
				ch.Log(alog.DEBUG, "[remote-control] accept stdin error: %v", err)
				continue
			}

			if _, err := stdinPipe.Write(data); err != nil {
				ch.Log(alog.DEBUG, "[remote-control] subprocess stdin write error: %v", err)
				return
			}
		}
	}
}

// proxyServerStdinPTY polls the server for pending client stdin entries and
// forwards accepted ones to the PTY master (under ptmxMu for safe concurrent access).
func (h *Host) proxyServerStdinPTY(ctx context.Context, ptmx *os.File, ptmxMu *sync.Mutex, client *APIClient, sessionID string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			entry, err := client.PeekStdin(sessionID)
			if err != nil {
				ch.Log(alog.DEBUG, "[remote-control] peek stdin error: %v", err)
				continue
			}
			if entry == nil {
				continue
			}

			data, err := base64.StdEncoding.DecodeString(entry.Data)
			if err != nil || len(data) == 0 {
				_ = client.RejectStdin(sessionID, entry.ID)
				continue
			}

			if err := client.AcceptStdin(sessionID, entry.ID); err != nil {
				ch.Log(alog.DEBUG, "[remote-control] accept stdin error: %v", err)
				continue
			}

			ptmxMu.Lock()
			_, err = ptmx.Write(data)
			ptmxMu.Unlock()
			if err != nil {
				ch.Log(alog.WARNING, "[remote-control] PTY stdin write error: %v", err)
				return
			}
		}
	}
}
