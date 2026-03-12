# WebSocket Architecture Proposal

## Executive Summary

This document proposes extending the remote-control system to support WebSocket-based persistent connections alongside the existing polling mechanism. The goal is to reduce latency and improve efficiency when network connectivity is stable, while maintaining the resilience of polling for unreliable network conditions.

**Key Design Decision**: All connections will use a **hybrid mode** that automatically switches between WebSocket and polling based on connection health. There will be no separate polling-only or WebSocket-only modes.

## Current Architecture Analysis

### Polling-Based Communication Model

**Design Philosophy:**
- Intentionally designed for resilience in face of network disruptions
- No persistent connections required
- Clients and hosts can disconnect/reconnect freely
- Server acts as stateful intermediary buffering I/O

**Current Flow:**

```
Host Process                    Server                      Remote Client
    |                              |                              |
    |--POST /sessions (create)---->|                              |
    |<----session_id---------------|                              |
    |                              |                              |
    |--POST /output (chunks)------>|                              |
    |  (every time data arrives)   |                              |
    |                              |<--GET /output (poll)---------|
    |                              |----chunks (if any)---------->|
    |                              |                              |
    |                              |<--GET /stdin (peek)----------|
    |<--stdin entry (if any)-------|                              |
    |--POST /stdin/{id}/accept---->|                              |
    |                              |                              |
```

**Key Characteristics:**
- **Poll Interval**: 100ms default (configurable via `PollIntervalMs`)
- **Backoff Strategy**: Exponential backoff on errors (500ms → 30s max)
- **Client Timeout**: 60s of inactivity (configurable via `ClientTimeoutSeconds`)
- **Memory Management**: Event-driven purging of consumed chunks
- **Latency**: ~100-200ms typical (poll interval + network RTT)

**Strengths:**
- ✅ Resilient to network disruptions
- ✅ Works with mobile devices on unstable connections
- ✅ No connection state to maintain
- ✅ Simple to implement and debug
- ✅ Works through most firewalls/proxies

**Weaknesses:**
- ❌ High latency (~100-200ms minimum)
- ❌ Inefficient: constant polling even when idle
- ❌ Increased server load (many unnecessary requests)
- ❌ Higher bandwidth usage (HTTP overhead per poll)
- ❌ Battery drain on mobile devices

## Proposed Hybrid Architecture

### Design Goals

1. **Low Latency**: Sub-10ms delivery when connections are stable
2. **Backward Compatible**: Existing REST endpoints remain available
3. **Graceful Degradation**: Automatic fallback to polling on connection issues
4. **Transparent**: Minimal changes to host/client application logic
5. **Resilient**: Maintain polling's robustness for unreliable networks
6. **Unified Mode**: Single hybrid mode for all connections (no separate polling/WebSocket modes)

### Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                         Server Layer                             │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │              Session Store (unchanged)                    │   │
│  │  - Buffers output chunks                                  │   │
│  │  - Queues stdin entries                                   │   │
│  │  - Tracks client state                                    │   │
│  └──────────────────────────────────────────────────────────┘   │
│                           ▲                                      │
│                           │                                      │
│  ┌────────────────────────┴──────────────────────────────────┐  │
│  │           Connection Manager (NEW)                        │  │
│  │  - Manages WebSocket connections                          │  │
│  │  - Routes messages to appropriate sessions                │  │
│  │  - Handles connection lifecycle                           │  │
│  │  - Implements heartbeat/keepalive                         │  │
│  └───────────────────────────────────────────────────────────┘  │
│           ▲                                    ▲                 │
│           │                                    │                 │
└───────────┼────────────────────────────────────┼─────────────────┘
            │                                    │
    ┌───────┴────────┐                  ┌────────┴────────┐
    │  WebSocket     │                  │   HTTP/REST     │
    │  Endpoints     │                  │   Endpoints     │
    │  (NEW)         │                  │   (existing)    │
    └───────┬────────┘                  └────────┬────────┘
            │                                    │
    ┌───────┴────────────────────────────────────┴────────┐
    │          Hybrid Connection (NEW)                    │
    │  - Starts with WebSocket                            │
    │  - Falls back to polling on failures                │
    │  - Retries WebSocket upgrade frequently             │
    └─────────────────────────────────────────────────────┘
```

### Hybrid Connection Mode

All connections use a single **hybrid mode** that:
- Attempts WebSocket connection first
- Falls back to polling if WebSocket fails
- Continuously attempts to upgrade back to WebSocket
- Provides transparent switching without user intervention

**No separate modes**: The system always uses hybrid behavior, eliminating configuration complexity.

## Detailed Design

### 1. WebSocket Protocol Design

#### Message Format

All WebSocket messages use JSON with a type discriminator:

```json
{
  "type": "output_chunk",
  "session_id": "abc123",
  "client_id": "client-xyz",
  "payload": {
    "stream": "stdout",
    "data": "base64...",
    "offset": 1024,
    "timestamp": "2026-03-09T18:00:00.000Z"
  }
}
```

**Note**: `client_id` is an application-level identifier, not extracted from TLS certificates. It must be explicitly provided in all messages.

#### Message Types

**Server → Client:**
- `output_chunk`: New output data available
- `stdin_pending`: New stdin entry awaiting approval
- `session_completed`: Session has finished
- `client_approved`: Client approval status changed
- `error`: Error notification
- `pong`: Heartbeat response

**Client → Server:**
- `subscribe`: Subscribe to session updates (includes client_id)
- `unsubscribe`: Unsubscribe from session
- `stdin_submit`: Submit stdin data (includes client_id)
- `stdin_accept`: Accept pending stdin (host only)
- `stdin_reject`: Reject pending stdin (host only)
- `ping`: Heartbeat request

**Bidirectional:**
- `ping`/`pong`: Connection keepalive

#### Connection Lifecycle

```
Client                          Server
  |                               |
  |--HTTP Upgrade: websocket----->|  (1) WebSocket upgrade request
  |<--101 Switching Protocols-----|  (2) Server accepts upgrade
  |                               |
  |--subscribe(session_id,------->|  (3) Client subscribes to session
  |   client_id)                  |
  |<--subscribed(session_id)------|  (4) Server confirms subscription
  |                               |
  |<--output_chunk----------------|  (5) Real-time output delivery
  |<--output_chunk----------------|
  |                               |
  |--stdin_submit(client_id)----->|  (6) Client submits stdin
  |<--stdin_pending---------------|  (7) Server notifies of pending stdin
  |                               |
  |--ping------------------------>|  (8) Heartbeat
  |<--pong------------------------|  (9) Heartbeat response
  |                               |
  |--unsubscribe(session_id)----->|  (10) Client unsubscribes
  |<--unsubscribed(session_id)----|  (11) Server confirms
  |                               |
  |--WS Close-------------------->|  (12) Clean connection close
  |<--WS Close--------------------|
```

**Steps 1-2**: Standard WebSocket upgrade handshake using HTTP/1.1 Upgrade header.

### 2. Server-Side Implementation

#### New Components

**File**: `internal/api/websocket.go`

```go
// ConnectionManager manages all active WebSocket connections
type ConnectionManager struct {
    mu          sync.RWMutex
    connections map[string]*Connection // clientID -> Connection
    sessions    map[string]map[string]*Connection // sessionID -> clientID -> Connection
    store       session.Store
}

// Connection represents a single WebSocket connection
type Connection struct {
    clientID    string  // Application-level client identifier
    conn        *websocket.Conn
    send        chan []byte
    sessions    map[string]bool // subscribed sessions
    lastPing    time.Time
    mu          sync.RWMutex
}

// Message types
type WSMessage struct {
    Type      string          `json:"type"`
    SessionID string          `json:"session_id,omitempty"`
    ClientID  string          `json:"client_id,omitempty"`  // Application-level ID
    Payload   json.RawMessage `json:"payload,omitempty"`
}

type OutputChunkMessage struct {
    Stream    string `json:"stream"`
    Data      string `json:"data"`
    Offset    int64  `json:"offset"`
    Timestamp string `json:"timestamp"`
}

type StdinMessage struct {
    ID        string `json:"id,omitempty"`
    Data      string `json:"data"`
    Source    string `json:"source,omitempty"`
    Status    string `json:"status,omitempty"`
}
```

#### WebSocket Endpoints

**New Routes:**
- `GET /ws` - WebSocket upgrade endpoint
  - Accepts WebSocket upgrade request
  - Client must provide `client_id` in first subscribe message
  - Returns 101 Switching Protocols

#### Connection Manager Methods

```go
// Register adds a new WebSocket connection
func (cm *ConnectionManager) Register(clientID string, conn *websocket.Conn) *Connection

// Unregister removes a WebSocket connection
func (cm *ConnectionManager) Unregister(clientID string)

// Subscribe adds a connection to a session's subscriber list
func (cm *ConnectionManager) Subscribe(clientID, sessionID string) error

// Unsubscribe removes a connection from a session's subscriber list
func (cm *ConnectionManager) Unsubscribe(clientID, sessionID string)

// Broadcast sends a message to all subscribers of a session
func (cm *ConnectionManager) Broadcast(sessionID string, msg WSMessage)

// Send sends a message to a specific client
func (cm *ConnectionManager) Send(clientID string, msg WSMessage) error

// Heartbeat checks connection health and removes stale connections
func (cm *ConnectionManager) Heartbeat(timeout time.Duration)
```

#### Integration with Existing Handlers

**File**: `internal/api/handlers.go`

Modify existing handlers to broadcast WebSocket messages:

```go
func (s *Server) handleAppendOutput(w http.ResponseWriter, r *http.Request) {
    // ... existing code ...
    sess.AppendOutput(stream, data, ts)

    // NEW: Broadcast to WebSocket subscribers
    if s.connMgr != nil {
        s.connMgr.Broadcast(id, WSMessage{
            Type:      "output_chunk",
            SessionID: id,
            Payload:   marshalOutputChunk(stream, data, offset, ts),
        })
    }

    // ... rest of existing code ...
}

func (s *Server) handleEnqueueStdin(w http.ResponseWriter, r *http.Request) {
    // ... existing code ...
    sess.EnqueueStdin(entry)

    // NEW: Notify host via WebSocket if connected
    if s.connMgr != nil {
        s.connMgr.Broadcast(id, WSMessage{
            Type:      "stdin_pending",
            SessionID: id,
            Payload:   marshalStdinEntry(&entry),
        })
    }

    // ... rest of existing code ...
}
```

### 3. Client-Side Implementation

#### Hybrid Connection Implementation

**File**: `internal/client/hybrid.go` (NEW)

```go
type HybridConnection struct {
    ws      *WebSocketConnection
    poller  *PollingConnection

    mode    ConnectionMode  // "websocket" or "polling"
    modeMu  sync.RWMutex

    wsFailures      int
    wsFailureWindow time.Duration
    fallbackThreshold int

    upgradeCheckInterval time.Duration  // How often to try upgrading to WS
}

// Start begins with WebSocket, falls back to polling on repeated failures
func (hc *HybridConnection) Start() error {
    // Try WebSocket first
    if err := hc.ws.Connect(); err != nil {
        // Immediate fallback to polling
        hc.switchToPolling()
    }

    // Background goroutine to attempt WebSocket upgrades
    go hc.upgradeLoop()

    return nil
}

// upgradeLoop continuously attempts to upgrade from polling to WebSocket
func (hc *HybridConnection) upgradeLoop() {
    ticker := time.NewTicker(hc.upgradeCheckInterval)
    defer ticker.Stop()

    for range ticker.C {
        if hc.isPolling() {
            hc.tryWebSocketUpgrade()
        }
    }
}

// switchToPolling transitions from WebSocket to polling mode
func (hc *HybridConnection) switchToPolling() {
    hc.modeMu.Lock()
    defer hc.modeMu.Unlock()

    if hc.mode == "polling" {
        return
    }

    ch.Log(alog.INFO, "[remote-control] switching to polling mode")
    hc.mode = "polling"
    hc.ws.Close()
    hc.poller.Start()
}

// tryWebSocketUpgrade attempts to restore WebSocket connection
func (hc *HybridConnection) tryWebSocketUpgrade() {
    if err := hc.ws.Connect(); err != nil {
        ch.Log(alog.DEBUG, "[remote-control] WebSocket upgrade failed: %v", err)
        return
    }

    hc.modeMu.Lock()
    defer hc.modeMu.Unlock()

    ch.Log(alog.INFO, "[remote-control] upgraded to WebSocket mode")
    hc.mode = "websocket"
    hc.poller.Stop()
    hc.wsFailures = 0
}
```

#### WebSocket Client Implementation

**File**: `internal/client/websocket.go` (NEW)

```go
type WebSocketConnection struct {
    url         string
    tlsConfig   *tls.Config
    conn        *websocket.Conn
    clientID    string  // Application-level identifier

    outputHandler      func(OutputChunk)
    stdinPendingHandler func(StdinEntry)

    reconnectAttempts int
    reconnectDelay    time.Duration
    maxReconnectDelay time.Duration

    done chan struct{}
    mu   sync.RWMutex
}

// Connect establishes the WebSocket connection
func (ws *WebSocketConnection) Connect() error

// readPump continuously reads messages from the WebSocket
func (ws *WebSocketConnection) readPump()

// writePump sends messages to the WebSocket
func (ws *WebSocketConnection) writePump()

// handleCorruptedStream detects and handles corrupted WebSocket streams
func (ws *WebSocketConnection) handleCorruptedStream(err error) {
    // If we detect invalid JSON or frame corruption, close and reconnect
    if isStreamCorruption(err) {
        ch.Log(alog.DEBUG, "[remote-control] detected stream corruption, reconnecting")
        ws.Close()
        ws.reconnect()
    }
}

// reconnect attempts to re-establish the connection
func (ws *WebSocketConnection) reconnect() error
```

### 4. Host-Side Implementation

Similar to client, but with additional stdin handling:

**File**: `internal/host/websocket.go` (NEW)

```go
type WebSocketHost struct {
    conn      *HybridConnection
    sessionID string
    clientID  string  // Application-level identifier

    // Channels for proxying I/O
    outputCh chan OutputChunk
    stdinCh  chan StdinEntry
}

// proxyOutputWebSocket sends output chunks via WebSocket
func (h *WebSocketHost) proxyOutputWebSocket(ctx context.Context, r io.Reader, stream string)

// proxyStdinWebSocket receives stdin entries via WebSocket
func (h *WebSocketHost) proxyStdinWebSocket(ctx context.Context, stdinPipe *syncWriter)
```

### 5. Configuration

**File**: `internal/config/config.go`

Add new configuration options:

```go
type Config struct {
    // ... existing fields ...

    // WebSocket configuration
    EnableWebSocket       bool   `json:"enable_websocket"`
    WebSocketPath         string `json:"websocket_path"`
    WebSocketPingInterval int    `json:"websocket_ping_interval_seconds"`
    WebSocketPongTimeout  int    `json:"websocket_pong_timeout_seconds"`

    // Hybrid mode configuration (always enabled)
    WSFailureThreshold    int    `json:"ws_failure_threshold"`
    WSFailureWindow       int    `json:"ws_failure_window_seconds"`
    WSUpgradeCheckInterval int   `json:"ws_upgrade_check_interval_seconds"`
}
```

**Defaults:**
```go
EnableWebSocket:         true,
WebSocketPath:           "/ws",
WebSocketPingInterval:   30,  // 30 seconds
WebSocketPongTimeout:    10,  // 10 seconds
WSFailureThreshold:      3,   // 3 failures in window
WSFailureWindow:         60,  // 60 seconds
WSUpgradeCheckInterval:  10,  // 10 seconds (aggressive upgrade attempts)
```

### 6. Backward Compatibility

**Key Principles:**
1. **Existing clients continue to work**: All REST endpoints remain unchanged
2. **Hybrid mode is automatic**: No configuration needed
3. **Server-side transparent**: Session store doesn't know about connection type
4. **Graceful degradation**: WebSocket failures fall back to polling

**Migration Path:**
- Phase 1: Deploy server with WebSocket support (clients still use polling)
- Phase 2: Update clients to use hybrid mode (automatic fallback)
- Phase 3: Monitor WebSocket adoption and stability

**Note**: Since this is a pre-release project, API breaking changes are acceptable. The hybrid mode will be the only supported mode going forward.

### 7. Connection Health Management

#### Heartbeat Protocol

**Client → Server:**
- Send `ping` every 30 seconds (configurable)
- Expect `pong` within 10 seconds
- After 3 missed pongs, reconnect

**Server → Client:**
- Respond to `ping` with `pong`
- Track last ping time per connection
- Remove connections with no ping for 60 seconds

#### Reconnection Strategy

**Exponential Backoff:**
```
Attempt 1: 1 second
Attempt 2: 2 seconds
Attempt 3: 4 seconds
Attempt 4: 8 seconds
Attempt 5: 16 seconds
Attempt 6+: 30 seconds (max)
```

**Fallback to Polling:**
- After 3 failed reconnection attempts within 60 seconds
- Switch to polling mode
- Retry WebSocket upgrade every 10 seconds (aggressive)

#### Stream Corruption Handling

**Detection:**
- Invalid JSON in WebSocket frame
- Partial frame followed by valid frames
- Unexpected frame types or malformed data

**Recovery:**
1. Detect corruption in `readPump()`
2. Close WebSocket connection immediately
3. Attempt reconnection with exponential backoff
4. If reconnection fails repeatedly, fall back to polling
5. Continue attempting WebSocket upgrade every 10 seconds

**Example:**
```go
func (ws *WebSocketConnection) readPump() {
    for {
        _, message, err := ws.conn.ReadMessage()
        if err != nil {
            if isStreamCorruption(err) {
                ch.Log(alog.DEBUG, "[remote-control] stream corruption detected")
                ws.handleCorruptedStream(err)
                return
            }
            // Handle other errors...
        }

        var msg WSMessage
        if err := json.Unmarshal(message, &msg); err != nil {
            ch.Log(alog.DEBUG, "[remote-control] invalid JSON in WebSocket frame")
            ws.handleCorruptedStream(err)
            return
        }

        // Process message...
    }
}
```

#### Connection State Machine

```
┌─────────────┐
│ Disconnected│
└──────┬──────┘
       │
       ▼
┌─────────────┐
│ Connecting  │
│ (WebSocket) │
└──────┬──────┘
       │
       ├─────────────────┐
       │                 │
       ▼                 ▼
┌─────────────┐   ┌─────────────┐
│  Connected  │   │  Polling    │
│ (WebSocket) │   │  (Fallback) │
└──────┬──────┘   └──────┬──────┘
       │                 │
       │                 ├──────────┐
       │                 │          │
       ▼                 ▼          ▼
┌─────────────┐   ┌─────────────┐  │
│Reconnecting │   │  Upgrading  │  │
│ (WebSocket) │   │(to WebSocket)│◄─┘
└──────┬──────┘   └──────┬──────┘
       │                 │
       └─────────────────┘
```

### 8. Error Handling

#### WebSocket-Specific Errors

**Connection Errors:**
- Network unreachable → Reconnect with backoff
- TLS handshake failure → Fall back to polling
- Authentication failure → Abort (don't retry)

**Protocol Errors:**
- Invalid message format → Log and ignore (DEBUG level)
- Unknown message type → Log and ignore (DEBUG level)
- Session not found → Unsubscribe and notify user

**Stream Corruption:**
- Partial frames → Close connection, reconnect
- Invalid JSON → Close connection, reconnect
- Garbage data → Close connection, fall back to polling

**Resource Errors:**
- Too many connections → Return 503, client retries
- Memory pressure → Close idle connections

#### Graceful Degradation

```go
// Example: Hybrid connection with automatic fallback
func (hc *HybridConnection) handleWebSocketError(err error) {
    hc.wsFailures++

    if hc.wsFailures >= hc.fallbackThreshold {
        ch.Log(alog.INFO, "[remote-control] WebSocket failures exceeded threshold, falling back to polling")
        hc.switchToPolling()
    } else {
        // Immediate reconnect attempt
        hc.reconnect()
    }
}
```

### 9. Performance Considerations

#### Latency Comparison

| Scenario | Polling | WebSocket | Improvement |
|----------|---------|-----------|-------------|
| Idle (no data) | 100ms poll overhead | 0ms | 100% |
| Single keystroke | ~150ms (poll + RTT) | ~5ms (RTT only) | 97% |
| Burst output | ~150ms first chunk | ~5ms first chunk | 97% |
| Continuous stream | ~100ms per chunk | ~5ms per chunk | 95% |

#### Bandwidth Comparison

**Polling (100ms interval, idle session):**
- 10 requests/second
- ~500 bytes per request (HTTP headers)
- ~5 KB/s overhead

**WebSocket (idle session):**
- 1 ping every 30 seconds
- ~50 bytes per ping
- ~1.7 bytes/s overhead

**Savings: 99.97% reduction in idle bandwidth**

#### Server Resource Usage

**Polling:**
- 10 requests/second per client
- Short-lived connections
- High CPU (request processing)
- Low memory (no connection state)

**WebSocket:**
- 1 persistent connection per client
- Long-lived connections
- Low CPU (event-driven)
- Higher memory (connection state)

**Trade-off:** WebSocket uses more memory but significantly less CPU

### 10. Security Considerations

#### Authentication

**WebSocket connections use TLS but client_id is application-level:**
- TLS provides transport security
- Client must provide `client_id` in subscribe message
- Same approval workflow applies based on `client_id`
- `client_id` is NOT extracted from TLS certificate CN

#### Authorization

**Per-message authorization:**
- Each message includes `session_id` and `client_id`
- Server validates client has access to session
- Same approval checks as REST endpoints

#### Rate Limiting

**Not implemented in initial version:**
- This tool requires trusted clients (mTLS)
- Rate limiting deferred as TODO for inadvertent abuse scenarios
- Can be added later if needed for incorrect usage patterns

**TODO**: Consider rate limiting if trusted clients inadvertently abuse the system (e.g., wrapped commands that refresh too frequently).

#### Connection Hijacking

**Mitigation:**
- TLS encryption for all WebSocket traffic
- Application-level client_id validation
- Connection tokens (optional enhancement)

### 11. Data Delivery Guarantees

#### Current Best-Effort Design

**Output Chunk Delivery:**
- Server broadcasts chunks to all WebSocket subscribers
- No receipt/acknowledgment mechanism
- Clients may miss chunks if connection drops during transmission
- Purging logic relies on server's view of delivery (not client receipt)

**Known Limitation:**
- If a client's WebSocket connection drops mid-chunk, that chunk may be lost
- Client will reconnect and request from last known offset
- If chunk was already purged, client will receive data from earliest available offset

**TODO**: Consider implementing receipt/acknowledgment mechanism for guaranteed delivery:
- Client sends `ack` message after receiving each chunk
- Server only purges chunks after all clients have acknowledged
- Trade-off: Increased complexity and message overhead
- Decision: Monitor in production; implement only if chunk loss becomes a problem

**Current Mitigation:**
- Aggressive reconnection attempts minimize window for data loss
- Polling fallback ensures eventual consistency
- Offset tracking allows clients to detect missed data

### 12. Testing Strategy

#### Unit Tests

**File**: `internal/api/websocket_test.go`
- Connection lifecycle
- Message routing
- Subscription management
- Heartbeat handling
- Error scenarios
- Stream corruption detection

#### Integration Tests

**File**: `test/integration/websocket_test.go`
- WebSocket + REST interoperability
- Multiple clients on same session
- Connection failures and recovery
- Memory cleanup with WebSocket connections
- Hybrid mode switching

#### End-to-End Tests

**File**: `test/e2e/websocket_test.go`
- Full session lifecycle via WebSocket
- Hybrid mode fallback scenarios
- Performance benchmarks (latency, throughput)
- Stream corruption recovery

#### Load Tests

**File**: `test/load/websocket_test.go`
- 1000+ concurrent WebSocket connections
- Mixed polling + WebSocket clients
- Connection churn (frequent connect/disconnect)
- Memory and CPU profiling

#### Make Targets

Add separate make targets for different test types:

```makefile
# Run all tests
test: test-unit test-integration test-e2e

# Run only unit tests
test-unit:
	go test -v ./internal/...

# Run integration tests
test-integration:
	go test -v ./test/integration/...

# Run end-to-end tests
test-e2e:
	go test -v ./test/e2e/...

# Run load tests (separate from regular test suite)
test-load:
	go test -v -timeout=30m ./test/load/...

# Run benchmarks
bench:
	go test -bench=. -benchmem ./...
```

### 13. Monitoring and Observability

#### Metrics

**Connection Metrics:**
- `websocket_connections_active`: Current active WebSocket connections
- `websocket_connections_total`: Total WebSocket connections established
- `websocket_messages_sent`: Messages sent via WebSocket
- `websocket_messages_received`: Messages received via WebSocket
- `websocket_errors_total`: WebSocket errors by type
- `websocket_reconnects_total`: Reconnection attempts
- `websocket_stream_corruptions`: Stream corruption events

**Performance Metrics:**
- `websocket_message_latency`: Message delivery latency
- `websocket_ping_latency`: Ping/pong round-trip time
- `connection_mode`: Current connection mode (websocket/polling)

**Health Metrics:**
- `websocket_heartbeat_failures`: Missed heartbeats
- `websocket_fallback_events`: Fallback to polling events
- `websocket_upgrade_events`: Upgrade to WebSocket events

#### Logging

**Connection Events:**
- WebSocket upgrade (INFO)
- Subscription changes (DEBUG)
- Fallback to polling (INFO)
- Upgrade to WebSocket (INFO)
- Reconnection attempts (DEBUG)

**Error Events:**
- Stream corruption (DEBUG)
- Connection errors (DEBUG)
- Invalid messages (DEBUG - handled gracefully)
- Authorization failures (WARNING - requires user intervention)

**Logging Levels:**
- **WARNING**: Only for events requiring user intervention
  - Authorization failures (unrecoverable)
  - Critical system errors
- **INFO**: Important state changes
  - Mode switches (WebSocket ↔ Polling)
  - Session lifecycle events
- **DEBUG**: Detailed operational information
  - Connection attempts
  - Message routing
  - Error recovery
  - Stream corruption detection

**Note**: Many wrapped commands have TUI states that break with unexpected output. Use WARNING sparingly to avoid disrupting user experience.

### 14. Implementation Phases

#### Phase 1: Foundation (Week 1-2)
- [ ] Design WebSocket message protocol
- [ ] Implement ConnectionManager
- [ ] Add WebSocket endpoint to server
- [ ] Basic connection lifecycle (connect, disconnect)
- [ ] Unit tests for core components

#### Phase 2: Integration (Week 3-4)
- [ ] Integrate with existing session store
- [ ] Modify handlers to broadcast WebSocket messages
- [ ] Implement heartbeat protocol
- [ ] Add configuration options
- [ ] Integration tests

#### Phase 3: Client Implementation (Week 5-6)
- [ ] Implement WebSocketConnection client
- [ ] Implement HybridConnection with fallback
- [ ] Add stream corruption detection and recovery
- [ ] Update client CLI to support hybrid mode
- [ ] Update host to support hybrid mode
- [ ] End-to-end tests

#### Phase 4: Stability & Performance (Week 7-8)
- [ ] Reconnection logic and error handling
- [ ] Performance benchmarks
- [ ] Load testing
- [ ] Memory profiling and optimization
- [ ] Documentation

#### Phase 5: Production Readiness (Week 9-10)
- [ ] Monitoring and metrics
- [ ] Operational runbooks
- [ ] Migration guide
- [ ] Beta testing with real users
- [ ] Final bug fixes and polish

### 15. Migration Guide

#### For Server Operators

**Step 1: Update Configuration**
```json
{
  "enable_websocket": true,
  "websocket_path": "/ws",
  "ws_upgrade_check_interval_seconds": 10
}
```

**Step 2: Deploy Server**
- Deploy new server version
- Existing polling clients continue to work
- Monitor WebSocket metrics

**Step 3: Update Clients**
- Update client binaries
- Clients automatically use hybrid mode
- Monitor fallback/upgrade event