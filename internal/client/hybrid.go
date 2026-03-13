package client

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

var hybridCh = alog.UseChannel("HYBRID")

// ConnectionMode represents the current connection mode
type ConnectionMode string

const (
	ModeWebSocket ConnectionMode = "websocket"
	ModePolling   ConnectionMode = "polling"
)

// HybridConnection manages both WebSocket and polling connections
// with automatic fallback and upgrade
type HybridConnection struct {
	ws     *WebSocketConnection
	poller *poller

	mode   ConnectionMode
	modeMu sync.RWMutex

	wsFailures       int
	wsFailureWindow  time.Duration
	failureThreshold int

	upgradeCheckInterval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// WebSocket returns the underlying WebSocket connection (for callbacks)
func (hc *HybridConnection) WebSocket() *WebSocketConnection {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.ws
}

// NewHybridConnection creates a new hybrid connection
func NewHybridConnection(
	wsURL string,
	tlsConfig *tls.Config,
	apiClient *APIClient,
	sessionID string,
	clientID string,
	failureThreshold int,
	failureWindow time.Duration,
	upgradeCheckInterval time.Duration,
) *HybridConnection {
	ctx, cancel := context.WithCancel(context.Background())

	return &HybridConnection{
		ws:                   NewWebSocketConnection(wsURL, tlsConfig, clientID, sessionID),
		poller:               newPoller(apiClient, sessionID, clientID),
		mode:                 ModeWebSocket,
		wsFailureWindow:      failureWindow,
		failureThreshold:     failureThreshold,
		upgradeCheckInterval: upgradeCheckInterval,
		ctx:                  ctx,
		cancel:               cancel,
	}
}

// Start begins the hybrid connection, attempting WebSocket first
func (hc *HybridConnection) Start() error {
	// Set up handlers for WebSocket
	hc.ws.OnOutput(func(chunk OutputChunk) {
		renderChunk(chunk)
	})

	hc.ws.OnError(func(err error) {
		hc.handleWebSocketError(err)
	})

	// Try WebSocket first
	if err := hc.ws.Connect(hc.ctx); err != nil {
		hybridCh.Log(alog.DEBUG, "[remote-control] WebSocket connection failed: %v, falling back to polling", err)
		hc.switchToPolling()
	} else {
		hybridCh.Log(alog.DEBUG, "[remote-control] connected via WebSocket")
	}

	// Start upgrade check loop
	hc.wg.Add(1)
	go hc.upgradeLoop()

	// Start monitoring WebSocket health
	hc.wg.Add(1)
	go hc.monitorWebSocketHealth()

	return nil
}

// upgradeLoop continuously attempts to upgrade from polling to WebSocket
func (hc *HybridConnection) upgradeLoop() {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.upgradeCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-hc.ctx.Done():
			return
		case <-ticker.C:
			if hc.isPolling() {
				hc.tryWebSocketUpgrade()
			}
		}
	}
}

// monitorWebSocketHealth monitors WebSocket connection health
func (hc *HybridConnection) monitorWebSocketHealth() {
	defer hc.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-hc.ctx.Done():
			return
		case <-ticker.C:
			if hc.isWebSocket() && !hc.ws.IsConnected() {
				hybridCh.Log(alog.DEBUG, "[remote-control] WebSocket disconnected, switching to polling")
				hc.handleWebSocketError(nil)
			}
		}
	}
}

// switchToPolling transitions from WebSocket to polling mode
func (hc *HybridConnection) switchToPolling() {
	hc.modeMu.Lock()
	defer hc.modeMu.Unlock()

	if hc.mode == ModePolling {
		return
	}

	hybridCh.Log(alog.INFO, "[remote-control] switching to polling mode")
	hc.mode = ModePolling

	// Stop WebSocket reconnection and close the connection
	hc.ws.StopReconnect()
	hc.ws.Close()

	// Start polling
	hc.wg.Add(1)
	go func() {
		defer hc.wg.Done()
		hc.poller.run(hc.ctx)
	}()
}

// tryWebSocketUpgrade attempts to restore WebSocket connection
func (hc *HybridConnection) tryWebSocketUpgrade() {
	hybridCh.Log(alog.DEBUG, "[remote-control] attempting WebSocket upgrade")

	if err := hc.ws.Connect(hc.ctx); err != nil {
		hybridCh.Log(alog.DEBUG, "[remote-control] WebSocket upgrade failed: %v", err)
		return
	}

	hc.modeMu.Lock()
	defer hc.modeMu.Unlock()

	if hc.mode == ModePolling {
		hybridCh.Log(alog.INFO, "[remote-control] upgraded to WebSocket mode")
		hc.mode = ModeWebSocket
		hc.wsFailures = 0

		// Polling will stop on next context check
		// No need to explicitly stop it
	}
}

// handleWebSocketError handles WebSocket errors and failures
func (hc *HybridConnection) handleWebSocketError(err error) {
	hc.modeMu.Lock()
	hc.wsFailures++
	failures := hc.wsFailures
	threshold := hc.failureThreshold
	hc.modeMu.Unlock()

	if failures >= threshold {
		hybridCh.Log(alog.INFO, "[remote-control] WebSocket failures (%d) exceeded threshold (%d), falling back to polling", failures, threshold)
		hc.switchToPolling()
	} else {
		hybridCh.Log(alog.DEBUG, "[remote-control] WebSocket error (%d/%d failures): %v", failures, threshold, err)
		// WebSocket will attempt reconnection automatically
	}
}

// isPolling returns true if currently in polling mode
func (hc *HybridConnection) isPolling() bool {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.mode == ModePolling
}

// isWebSocket returns true if currently in WebSocket mode
func (hc *HybridConnection) isWebSocket() bool {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.mode == ModeWebSocket
}

// GetMode returns the current connection mode
func (hc *HybridConnection) GetMode() ConnectionMode {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.mode
}

// SubmitStdin submits stdin data via the active connection
func (hc *HybridConnection) SubmitStdin(data string) error {
	if hc.isWebSocket() && hc.ws.IsConnected() {
		return hc.ws.SubmitStdin(data)
	}
	// Fall back to REST API (handled by caller)
	return nil
}

// GetCurrentOffsets returns the current stream offsets
func (hc *HybridConnection) GetCurrentOffsets() (stdout, stderr int64) {
	return hc.poller.currentOffsets()
}

// Close closes the hybrid connection
func (hc *HybridConnection) Close() error {
	hc.cancel()
	hc.ws.Close()
	hc.wg.Wait()
	return nil
}
