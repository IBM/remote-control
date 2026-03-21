package client

import (
	"context"
	"crypto/tls"
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
)

var hybridCh = alog.UseChannel("HYBRID")

// ConnectionMode represents the current connection mode
type ConnectionMode string

const (
	ModeWebSocket ConnectionMode = "websocket"
	ModePolling   ConnectionMode = "polling"
)

// HybridConnection manages both WebSocket and polling connections
type HybridConnection struct {
	ws        *WebSocketConnection
	poller    *poller
	api       *APIClient
	sessionID string
	clientID  string

	mode   ConnectionMode
	modeMu sync.RWMutex

	upgradeCheckInterval time.Duration

	ctx context.Context
	wg  sync.WaitGroup
}

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
	ctx := context.Background()

	return &HybridConnection{
		ws:                   NewWebSocketConnection(wsURL, tlsConfig, clientID, sessionID),
		poller:               newPoller(apiClient, sessionID, clientID),
		api:                  apiClient,
		sessionID:            sessionID,
		clientID:             clientID,
		mode:                 ModeWebSocket,
		upgradeCheckInterval: upgradeCheckInterval,
		ctx:                  ctx,
	}
}

func (hc *HybridConnection) WebSocket() *WebSocketConnection {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.ws
}

func (hc *HybridConnection) Start() error {
	hc.ws.OnOutput(func(chunk types.OutputChunk) {
		renderChunk(chunk)
	})

	if err := hc.ws.Connect(hc.ctx); err != nil {
		hybridCh.Log(alog.DEBUG, "WebSocket connection failed: %v, falling back to polling", err)
		hc.switchToPolling()
	} else {
		hybridCh.Log(alog.DEBUG, "connected via WebSocket")
	}

	hc.wg.Add(1)
	go hc.upgradeLoop()

	return nil
}

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

func (hc *HybridConnection) switchToPolling() {
	hc.modeMu.Lock()
	if hc.mode == ModePolling {
		hc.modeMu.Unlock()
		return
	}

	hybridCh.Log(alog.DEBUG, "switching to polling mode")
	hc.mode = ModePolling
	hc.modeMu.Unlock()

	hc.ws.Close()

	hc.poller.run(hc.ctx)
}

func (hc *HybridConnection) tryWebSocketUpgrade() {
	hybridCh.Log(alog.DEBUG, "attempting WebSocket upgrade")

	if err := hc.ws.Connect(hc.ctx); err != nil {
		hybridCh.Log(alog.DEBUG, "WebSocket upgrade failed: %v", err)
		return
	}

	hc.modeMu.Lock()
	defer hc.modeMu.Unlock()

	if hc.mode == ModePolling {
		hybridCh.Log(alog.DEBUG, "upgraded to WebSocket mode")
		hc.mode = ModeWebSocket
	}
}

func (hc *HybridConnection) isPolling() bool {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.mode == ModePolling
}

func (hc *HybridConnection) isWebSocket() bool {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.mode == ModeWebSocket
}

func (hc *HybridConnection) GetMode() ConnectionMode {
	hc.modeMu.RLock()
	defer hc.modeMu.RUnlock()
	return hc.mode
}

func (hc *HybridConnection) SubmitStdin(data string) error {
	return nil
}

func (hc *HybridConnection) Close() error {
	hc.ws.Close()
	hc.wg.Wait()
	return nil
}
