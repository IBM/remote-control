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
// If wsHost is available, sends output via WebSocket, otherwise uses HTTP.
func (h *Host) proxyOutput(ctx context.Context, r io.Reader, dst io.Writer, client *APIClient, sessionID, stream string, wsHost *WebSocketHost) {
	var offset int64
	var offsetMu sync.Mutex

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

			// Forward to server (prefer WebSocket, fallback to HTTP).
			if wsHost != nil && wsHost.IsConnected() {
				offsetMu.Lock()
				currentOffset := offset
				offset += int64(n)
				offsetMu.Unlock()

				if serr := wsHost.SendOutput(stream, chunk, currentOffset, ts); serr != nil {
					wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket send output error: %v", serr)
				}
			} else if client != nil {
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
// If wsHost is available, sends output via WebSocket, otherwise uses HTTP.
func (h *Host) proxyPTYOutput(ctx context.Context, ptmx *os.File, client *APIClient, sessionID string, wsHost *WebSocketHost, offset *int64, offsetMu *sync.Mutex) {
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

			// Forward to server (prefer WebSocket, fallback to HTTP).
			if wsHost != nil && wsHost.IsConnected() {
				offsetMu.Lock()
				currentOffset := *offset
				*offset += int64(n)
				offsetMu.Unlock()

				if serr := wsHost.SendOutput("stdout", chunk, currentOffset, ts); serr != nil {
					wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket send output error: %v", serr)
				}
			} else if client != nil {
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

// processHostStdinEntry submits a host stdin entry to the server queue.
// For PTY mode, the entry is written directly before submission to avoid echo loop.
// If wsHost is available, uses WebSocket to submit; otherwise uses HTTP polling.
func (h *Host) processHostStdinEntry(ctx context.Context, data []byte, client *APIClient, sessionID string, writeDirectly bool, ptmx *os.File, ptmxMu *sync.Mutex, wsHost *WebSocketHost) error {
	// Write directly to PTY before submission for immediate terminal echo
	if writeDirectly && ptmx != nil {
		ptmxMu.Lock()
		_, _ = ptmx.Write(data)
		ptmxMu.Unlock()
	}

	// Submit to server queue for client visibility (prefer WebSocket, fallback to HTTP)
	// var entryID string
	if _, err := client.SubmitHostStdin(sessionID, data); nil != err {
		ch.Log(alog.DEBUG, "[remote-control] host stdin enqueue error: %v", err)
		return nil
	}

	return nil
}

// proxyLocalStdin reads lines from os.Stdin, submits each line to the server
// queue, and writes to the subprocess after acceptance. This ensures FIFO
// ordering between host and client inputs.
//
// When an approval prompt is active (approvalActive == true), the first byte of
// the line is routed to approvalRespCh instead of the subprocess — no mutex is
// held during I/O, eliminating the deadlock present in the prior design.
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
				// os.Stdin reached EOF. Stop proxying local input.
				return
			}

			// Check for approval prompt
			h.approvalMu.Lock()
			active := h.approvalActive
			var respCh chan byte
			if active {
				respCh = h.approvalRespCh
			}
			h.approvalMu.Unlock()

			if active {
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
			writeErr := h.processHostStdinEntry(ctx, lineWithNewline, client, sessionID, true, nil, nil, nil)
			if writeErr != nil {
				ch.Log(alog.DEBUG, "[remote-control] process host stdin entry error: %v", writeErr)
			}
		}
	}
}

// proxyLocalStdinRaw reads individual bytes from os.Stdin (raw terminal mode),
// writes them directly to the PTY for immediate echo, and submits to the server
// queue for client visibility. This avoids the echo loop by:
// 1. Writing directly to PTY immediately (terminal handles echo)
// 2. Submitting to server (marked as "host" source)
// 3. Skipping write when polling sees "host" entries
//
// Each keystroke is submitted individually (threshold: 1 byte) for responsiveness.
func (h *Host) proxyLocalStdinRaw(ctx context.Context, ptmx *os.File, ptmxMu *sync.Mutex, client *APIClient, sessionID string, wsHost *WebSocketHost) {
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

	// Immediate submission (1 byte threshold) for responsive typing
	const batchThreshold = 1
	var batch []byte

	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-byteCh:
			if !ok {
				return
			}

			// Check for approval prompt
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

			// Special keys: write directly to PTY (no queue processing)
			if b == 0x03 || b == 0x04 { // Ctrl+C or Ctrl+D
				ptmxMu.Lock()
				_, _ = ptmx.Write([]byte{b})
				ptmxMu.Unlock()
				continue
			}

			// Write byte directly to PTY for immediate terminal echo
			ptmxMu.Lock()
			_, _ = ptmx.Write([]byte{b})
			ptmxMu.Unlock()

			// Accumulate for submission
			batch = append(batch, b)

			// Submit immediately
			if len(batch) >= batchThreshold {
				_ = h.processHostStdinEntry(ctx, batch, client, sessionID, false, nil, nil, wsHost)
				batch = nil
			}
		}
	}
}

// processServerStdinEntry accepts a pending entry from the server and writes
// it to the subprocess. Only writes client entries; skips host entries to
// avoid echo loop since they were already written directly to PTY.
func (h *Host) processServerStdinEntry(ctx context.Context, client *APIClient, sessionID string, writeFunc func([]byte) error) error {
	entry, err := client.PeekStdin(sessionID)
	if err != nil {
		ch.Log(alog.DEBUG, "[remote-control] peek stdin error: %v", err)
		return err
	}
	if entry == nil {
		return nil
	}

	// Check source - skip host entries (already written)
	isHostEntry := entry.Source == "host"

	// Accept the entry
	if err := client.AcceptStdin(sessionID, entry.ID); err != nil {
		ch.Log(alog.DEBUG, "[remote-control] accept stdin error: %v", err)
		_ = client.RejectStdin(sessionID, entry.ID)
		return err
	}

	data, err := base64.StdEncoding.DecodeString(entry.Data)
	if err != nil || len(data) == 0 {
		_ = client.RejectStdin(sessionID, entry.ID)
		return fmt.Errorf("invalid entry data")
	}

	// Skip host entries to avoid echo loop
	if isHostEntry {
		return nil
	}

	// Write client entries to subprocess
	return writeFunc(data)
}

// proxyServerStdin polls the server for pending stdin entries and processes
// them. Runs in parallel with proxyLocalStdin in pipe mode.
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
				return err
			})
			// Silently ignore "no pending" or processing errors
			_ = err
		}
	}
}

// proxyServerStdinPTY is the PTY variant of proxyServerStdin.
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
