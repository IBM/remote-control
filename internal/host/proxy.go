package host

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
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
func (h *Host) proxyOutput(ctx context.Context, r io.Reader, dst io.Writer, client *types.APIClient, sessionID string, stream types.Stream, wsHost *WebSocketHost) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			// Write to local terminal.
			if _, werr := dst.Write(chunk); werr != nil {
				ch.Log(alog.DEBUG, "[remote-control] local %s write error: %v", stream, werr)
			}

			// Forward to server (prefer WebSocket, fallback to HTTP).
			if wsHost != nil && wsHost.IsConnected() {
				if serr := wsHost.SendOutput(stream, chunk); serr != nil {
					wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket send output error: %v", serr)
				}
			} else if client != nil {
				select {
				case <-ctx.Done():
					return
				default:
					if serr := client.AppendOutput(sessionID, stream, chunk); serr != nil {
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
func (h *Host) proxyPTYOutput(ctx context.Context, ptmx *os.File, client *types.APIClient, sessionID string, wsHost *WebSocketHost) {
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])

			// Skip local display while an approval prompt is shown so the
			// subprocess TUI cannot re-render over the prompt text.
			if !h.pauseOutput.Load() {
				if _, werr := os.Stdout.Write(chunk); werr != nil {
					ch.Log(alog.WARNING, "[remote-control] local stdout write error: %v", werr)
				}
			}

			// Forward to server (prefer WebSocket, fallback to HTTP).
			if wsHost != nil && wsHost.IsConnected() {
				if serr := wsHost.SendOutput(types.StreamStdout, chunk); serr != nil {
					wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket send output error: %v", serr)
				}
			} else if client != nil {
				select {
				case <-ctx.Done():
					return
				default:
					if serr := client.AppendOutput(sessionID, types.StreamStdout, chunk); serr != nil {
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

// processHostStdinEntry submits host stdin data via WebSocket or HTTP fallback
func (h *Host) processHostStdinEntry(ctx context.Context, data []byte, client *types.APIClient, sessionID string, writeDirectly bool, ptmx *os.File, ptmxMu *sync.Mutex, wsHost *WebSocketHost) error {
	// Write directly to PTY before submission for immediate terminal echo
	if writeDirectly && ptmx != nil {
		ptmxMu.Lock()
		_, _ = ptmx.Write(data)
		ptmxMu.Unlock()
	}

	// Send to server via WebSocket if connected, otherwise HTTP
	if wsHost != nil && wsHost.IsConnected() {
		if err := wsHost.SendStdinSubmit(data); err != nil {
			wsHostCh.Log(alog.DEBUG, "[remote-control] WebSocket send stdin error: %v", err)
			return err
		}
	} else if client != nil {
		if err := client.EnqueueStdin(sessionID, "host", data); err != nil {
			ch.Log(alog.DEBUG, "[remote-control] enqueue stdin error: %v", err)
			return err
		}
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
func (h *Host) proxyLocalStdin(ctx context.Context, stdinPipe *syncWriter, client *types.APIClient, sessionID string) {
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

			// Submit directly to stdin
			// TODO: This is not right!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
			writeErr := h.processHostStdinEntry(ctx, lineWithNewline, client, sessionID, true, nil, nil, nil)
			if writeErr != nil {
				ch.Log(alog.DEBUG, "[remote-control] process host stdin entry error: %v", writeErr)
			}
		}
	}
}

// proxyLocalStdinRaw reads individual bytes from os.Stdin (raw terminal mode)
//
// Each keystroke is submitted individually (threshold: 1 byte) for responsiveness.
func (h *Host) proxyLocalStdinRaw(ctx context.Context, ptmx *os.File, ptmxMu *sync.Mutex, client *types.APIClient, sessionID string, wsHost *WebSocketHost) {
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

// processServerStdinEntryFromCallback handles a stdin entry received from the server via WebSocket.
// Only writes client entries; skips host entries to avoid echo loop since they were already written directly to PTY.
func (h *Host) processServerStdinEntryFromCallback(ctx context.Context, entry types.StdinEntry, writeFunc func([]byte) error) error {
	// Skip empty data
	if len(entry.Data) == 0 {
		return nil
	}

	// Write client entries to subprocess
	return writeFunc(entry.Data)
}

// proxyServerStdin handles server stdin entries.
// When WebSocket is connected, stdin comes via WebSocket callbacks.
// When WebSocket is disconnected, falls back to polling.
func (h *Host) proxyServerStdin(ctx context.Context, stdinPipe *syncWriter, client *types.APIClient, sessionID string) {
	// If WebSocket is connected, stdin entries are handled via callbacks
	// If WebSocket is disconnected, we can poll for queued stdin entries
	if h.wsHost == nil || !h.wsHost.IsConnected() {
		h.pollStdin(ctx, sessionID, func(data []byte) error {
			_, err := stdinPipe.Write(data)
			return err
		})
	} else {
		// Wait for WebSocket callbacks
		<-ctx.Done()
	}
}

// proxyServerStdinPTY is the PTY variant of proxyServerStdin.
func (h *Host) proxyServerStdinPTY(ctx context.Context, ptmx *os.File, ptmxMu *sync.Mutex, client *types.APIClient, sessionID string) {
	// If WebSocket is connected, stdin entries are handled via callbacks
	// If WebSocket is disconnected, falls back to polling
	if h.wsHost == nil || !h.wsHost.IsConnected() {
		h.pollStdin(ctx, sessionID, func(data []byte) error {
			ptmxMu.Lock()
			defer ptmxMu.Unlock()
			_, err := ptmx.Write(data)
			return err
		})
	} else {
		// Wait for WebSocket callbacks
		<-ctx.Done()
	}
}

// pollStdin polls for stdin entries when WebSocket is disconnected
func (h *Host) pollStdin(ctx context.Context, sessionID string, writeFunc func([]byte) error) {
	ticker := time.NewTicker(time.Duration(h.cfg.PollIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollResp, err := h.client.Poll(sessionID, types.HostClientID, types.WSMessageStdin)
			if err != nil {
				continue
			}

			for _, entry := range pollResp.Elements {
				var stdinEntry types.StdinEntry
				if err := json.Unmarshal(entry, &stdinEntry); nil != err {
					wsHostCh.Log(alog.DEBUG, "Got bad stdin entry: %v", err)
				} else {
					if err := writeFunc(stdinEntry.Data); err != nil {
						wsHostCh.Log(alog.DEBUG, "[remote-control] stdin write error: %v", err)
					}
				}
			}

			if err := h.client.Ack(sessionID, types.HostClientID, types.WSMessageStdin); err != nil {
				wsHostCh.Log(alog.DEBUG, "[remote-control] poll ack error: %v", err)
			}
		}
	}
}

// pollPendingClients polls for pending client notifications when WebSocket is disconnected
func (h *Host) pollPendingClients(ctx context.Context, sessionID string, rawMode bool) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Don't poll if websocket is connected
			if h.wsHost.IsConnected() {
				continue
			}
			pollResp, err := h.client.Poll(sessionID, types.HostClientID, types.WSMessagePendingClient)
			if err != nil {
				continue
			}

			for _, entry := range pollResp.Elements {
				var clientID string
				if err := json.Unmarshal(entry, &clientID); nil != err {
					wsHostCh.Log(alog.DEBUG, "Invalid pending client id %v: %v", entry, err)
				} else {
					h.handleClientApproval(ctx, sessionID, clientID, rawMode)
				}
			}

			if err := h.client.Ack(sessionID, types.HostClientID, types.WSMessagePendingClient); err != nil {
				wsHostCh.Log(alog.DEBUG, "[remote-control] poll ack error: %v", err)
			}
		}
	}
}
