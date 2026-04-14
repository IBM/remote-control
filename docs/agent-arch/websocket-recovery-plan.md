# WebSocket Automatic Recovery Plan

## Overview

This document outlines the plan to implement automatic recovery for WebSocket connections in the `remote-control` project. The recovery mechanism will be implemented in `internal/common/websocket/websocket.go` to provide generic recovery capabilities for both host and client processes.

## Requirements Summary

Based on architectural discussions, the recovery system must:

1. **Reconnect Strategy**: Use a fixed interval (5s default, configurable) that persists indefinitely without blocking process exit
2. **Message Queueing**: Queue failed messages with a configurable max queue length (100 default), dropping oldest messages first (FIFO)
3. **Reconnect Timeout**: Use a configurable timeout (10s default) appropriate for responsive HTTP servers
4. **Generic Implementation**: All logic in `common/websocket.go` to work for both host and client
5. **Always Enabled**: Recovery is enabled by default for all WebSocket connections

## Current Architecture Analysis

### Existing Components

The current `WebSocketPipe` in `internal/common/websocket/websocket.go` provides:
- `Dial()`: Creates outbound WebSocket connections
- `Send()` / `SendMessage()`: Queues messages for sending
- `readPump()` / `writePump()`: Goroutines for reading/writing messages
- `handleDisconnect()`: Called when connection drops
- `OnDisconnect()`: Callback registration for disconnect events

### Current Limitations

1. No message queueing on send failure
2. No automatic reconnection logic
3. No retry mechanism for failed sends
4. Disconnect handler only logs and cleans up

## Architectural Decisions

### 1. Recovery Trigger Mechanism

**Decision**: Queue messages on send failure, but only start reconnection on disconnect notification.

**Rationale**: 
- Failed sends can occur before the read/write pumps detect disconnection
- Queueing immediately prevents message loss during the race condition
- Reconnection starts when `handleDisconnect()` fires, which is the central point for connection loss detection
- This approach is idempotent and avoids multiple reconnection attempts

### 2. Reconnection Loop Lifecycle

**Decision**: Single idempotent reconnection goroutine per pipe.

**Rationale**:
- Start one goroutine when `handleDisconnect()` fires
- If already running, do nothing (idempotent check)
- Goroutine exits only on successful reconnection or explicit `Close()`
- Prevents goroutine leaks and simplifies lifecycle management

### 3. Send Failure Behavior

**Decision**: Queue immediately on any send failure, even before disconnect is detected.

**Rationale**:
- Handles race condition where sends fail before pumps detect disconnection
- Ensures no messages are lost during the transition period
- Queued messages will be flushed when reconnection succeeds

### 4. Queue Flushing Strategy

**Decision**: Push queued messages to send channel, let write pump handle them.

**Rationale**:
- Reuses existing infrastructure (write pump)
- Maintains natural message flow
- Preserves message ordering through the send channel
- Simplifies implementation

### 5. Queue Flush Overflow Handling

**Decision**: Block until all queued messages are flushed to the send channel.

**Rationale**:
- Send channel buffer (256) is larger than default queue size (100)
- Blocking ensures all messages are delivered
- Overflow should be rare in normal operation
- Guarantees message delivery over speed

### 6. Read/Write Pump Restart

**Decision**: Single-shot pumps - start new pump goroutines after reconnection.

**Rationale**:
- Aligns with current architecture where pumps exit on connection errors
- Old pumps have already exited when `handleDisconnect()` is called
- Reconnection logic simply starts fresh pumps with the new connection
- Clean separation of concerns

### 7. Context Management

**Decision**: Use original `Start()` context for all operations; `Close()` cancels reconnection immediately.

**Rationale**:
- Maintains consistent lifecycle across the pipe's lifetime
- Ensures all goroutines (pumps and reconnection) share the same lifecycle
- `Close()` provides clean shutdown by canceling the context
- Prevents orphaned reconnection attempts after close

### 8. Error Handling During Reconnection

**Decision**: Log reconnection failures only, no callbacks or notifications.

**Rationale**:
- Keeps implementation simple
- Transient failures are expected and normal
- Reduces noise for callers
- Successful reconnection is transparent to the caller
- Debug logs provide visibility for troubleshooting

### 9. Configuration Structure

**Decision**: Global configuration in `config.go`, always enabled by default.

**Defaults**:
- `ReconnectInterval`: 5 seconds
- `ReconnectTimeout`: 10 seconds  
- `MaxQueueLength`: 100 messages

**Rationale**:
- Simplifies configuration management
- Consistent behavior across all WebSocket connections
- No API changes required
- Reasonable defaults for most use cases
- Can be tuned via config file if needed

### 10. API Design

**Decision**: Always enabled, no API changes, add `GetQueueStatus()` monitoring method.

**Rationale**:
- Zero breaking changes to existing code
- Recovery is transparent to callers
- Monitoring method provides visibility when needed
- Maintains backward compatibility

## Detailed Design

### Configuration Structure

Add to `internal/common/config/config.go`:

```go
type Config struct {
    // ... existing fields ...
    
    WebSocket WebSocketConfig `json:"websocket"`
}

type WebSocketConfig struct {
    // ReconnectInterval is the fixed interval between reconnection attempts
    ReconnectInterval time.Duration `json:"reconnect_interval"`
    
    // ReconnectTimeout is the timeout for each reconnection attempt
    ReconnectTimeout time.Duration `json:"reconnect_timeout"`
    
    // MaxQueueLength is the maximum number of messages to queue during disconnection
    MaxQueueLength int `json:"max_queue_length"`
}
```

Default values in config loading:
```go
func DefaultConfig() *Config {
    return &Config{
        // ... existing defaults ...
        WebSocket: WebSocketConfig{
            ReconnectInterval: 5 * time.Second,
            ReconnectTimeout:  10 * time.Second,
            MaxQueueLength:    100,
        },
    }
}
```

### Enhanced WebSocketPipe Structure

```go
type WebSocketPipe struct {
    // Existing fields
    conn *websocket.Conn
    send chan []byte
    done chan struct{}
    mu   sync.RWMutex
    connected atomic.Bool
    onMessage    MessageHandler
    onDisconnect DisconnectHandler
    
    // Recovery fields
    reconnectURL      string              // URL to reconnect to
    tlsConfig         *tls.Config         // TLS config for reconnection
    messageQueue      [][]byte            // Queue for failed messages
    queueMu           sync.Mutex          // Protects messageQueue
    maxQueueLength    int                 // Max messages in queue
    reconnectInterval time.Duration       // Fixed interval between attempts
    reconnectTimeout  time.Duration       // Timeout per attempt
    reconnectCancel   context.CancelFunc  // Cancel function for reconnect loop
    reconnecting      atomic.Bool         // True if reconnection loop is active
    startCtx          context.Context     // Original context from Start()
}
```

### Message Queueing Implementation

```go
// queueMessage adds a message to the queue, dropping oldest if at capacity
func (p *WebSocketPipe) queueMessage(data []byte) {
    p.queueMu.Lock()
    defer p.queueMu.Unlock()
    
    // Drop oldest if at capacity
    if len(p.messageQueue) >= p.maxQueueLength {
        ch.Log(alog.DEBUG, "Message queue full, dropping oldest message")
        p.messageQueue = p.messageQueue[1:]
    }
    
    // Append new message
    p.messageQueue = append(p.messageQueue, data)
    ch.Log(alog.DEBUG3, "Queued message, queue length: %d", len(p.messageQueue))
}

// Modified Send to queue on failure
func (p *WebSocketPipe) Send(data []byte) error {
    select {
    case <-p.done:
        p.queueMessage(data)
        return fmt.Errorf("connection closed, message queued")
    case p.send <- data:
        return nil
    default:
        p.queueMessage(data)
        return fmt.Errorf("send buffer full, message queued")
    }
}
```

### Reconnection Loop Implementation

```go
// startReconnectLoop initiates the reconnection process (idempotent)
func (p *WebSocketPipe) startReconnectLoop() {
    // Check if already reconnecting
    if !p.reconnecting.CompareAndSwap(false, true) {
        ch.Log(alog.DEBUG, "Reconnection loop already running")
        return
    }
    
    ch.Log(alog.INFO, "Starting reconnection loop")
    
    ctx, cancel := context.WithCancel(p.startCtx)
    p.reconnectCancel = cancel
    
    go func() {
        defer func() {
            p.reconnecting.Store(false)
            ch.Log(alog.DEBUG, "Reconnection loop exited")
        }()
        
        ticker := time.NewTicker(p.reconnectInterval)
        defer ticker.Stop()
        
        for {
            select {
            case <-ctx.Done():
                return
            case <-ticker.C:
                if err := p.attemptReconnect(); err != nil {
                    ch.Log(alog.DEBUG, "Reconnection attempt failed: %v", err)
                } else {
                    ch.Log(alog.INFO, "Reconnection successful")
                    p.flushQueue()
                    return
                }
            }
        }
    }()
}
```

### Reconnection Attempt Implementation

```go
// attemptReconnect tries to re-establish the WebSocket connection
func (p *WebSocketPipe) attemptReconnect() error {
    ctx, cancel := context.WithTimeout(p.startCtx, p.reconnectTimeout)
    defer cancel()
    
    ch.Log(alog.DEBUG, "Attempting to reconnect to %s", p.reconnectURL)
    
    dialer := websocket.Dialer{
        HandshakeTimeout: p.reconnectTimeout,
    }
    if p.tlsConfig != nil {
        dialer.TLSClientConfig = p.tlsConfig
    }
    
    conn, _, err := dialer.DialContext(ctx, p.reconnectURL, nil)
    if err != nil {
        return fmt.Errorf("dial failed: %w", err)
    }
    
    // Replace connection
    p.mu.Lock()
    p.conn = conn
    p.connected.Store(true)
    p.mu.Unlock()
    
    // Restart pumps with original context
    go p.readPump(p.startCtx)
    go p.writePump(p.startCtx)
    
    return nil
}
```

### Queue Flushing Implementation

```go
// flushQueue sends all queued messages to the send channel
func (p *WebSocketPipe) flushQueue() {
    p.queueMu.Lock()
    queueLen := len(p.messageQueue)
    p.queueMu.Unlock()
    
    if queueLen == 0 {
        return
    }
    
    ch.Log(alog.INFO, "Flushing %d queued messages", queueLen)
    
    p.queueMu.Lock()
    defer p.queueMu.Unlock()
    
    // Push all messages to send channel, blocking if necessary
    for _, msg := range p.messageQueue {
        select {
        case <-p.startCtx.Done():
            ch.Log(alog.DEBUG, "Context canceled during queue flush")
            return
        case p.send <- msg:
            // Message sent successfully
        }
    }
    
    // Clear the queue
    p.messageQueue = p.messageQueue[:0]
    ch.Log(alog.INFO, "Queue flush complete")
}
```

### Modified handleDisconnect

```go
// handleDisconnect cleans up on connection loss and starts reconnection
func (p *WebSocketPipe) handleDisconnect() {
    p.mu.Lock()
    p.connected.Store(false)
    if p.conn != nil {
        p.conn.Close()
        p.conn = nil
    }
    onDisconnect := p.onDisconnect
    p.mu.Unlock()
    
    ch.Log(alog.INFO, "WebSocket disconnected")
    
    // Start reconnection loop
    p.startReconnectLoop()
    
    // Call user's disconnect handler
    if onDisconnect != nil {
        onDisconnect()
    }
}
```

### Modified Dial Function

```go
// Dial creates a new outbound WebSocket connection with recovery enabled
func Dial(ctx context.Context, wsURL string, tlsConfig *tls.Config, config *WebSocketConfig) (*WebSocketPipe, error) {
    dialer := websocket.Dialer{
        HandshakeTimeout: 10 * time.Second,
    }
    if tlsConfig != nil {
        dialer.TLSClientConfig = tlsConfig
    }
    
    conn, _, err := dialer.DialContext(ctx, wsURL, nil)
    if err != nil {
        return nil, fmt.Errorf("WebSocket dial failed: %w", err)
    }
    
    p := NewPipe(conn)
    p.connected.Store(true)
    
    // Configure recovery
    p.reconnectURL = wsURL
    p.tlsConfig = tlsConfig
    p.reconnectInterval = config.ReconnectInterval
    p.reconnectTimeout = config.ReconnectTimeout
    p.maxQueueLength = config.MaxQueueLength
    p.messageQueue = make([][]byte, 0, config.MaxQueueLength)
    
    return p, nil
}
```

### Modified Start Function

```go
// Start launches the read and write pump goroutines
func (p *WebSocketPipe) Start(ctx context.Context) {
    p.connected.Store(true)
    p.startCtx = ctx  // Store for reconnection use
    go p.readPump(ctx)
    go p.writePump(ctx)
}
```

### Modified Close Function

```go
// Close gracefully shuts down the pipe and cancels reconnection
func (p *WebSocketPipe) Close() error {
    p.mu.Lock()
    defer p.mu.Unlock()
    
    // Cancel reconnection loop if active
    if p.reconnectCancel != nil {
        p.reconnectCancel()
    }
    
    select {
    case <-p.done:
        return nil
    default:
        close(p.done)
    }
    
    if p.conn != nil {
        p.conn.WriteMessage(websocket.CloseMessage,
            websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
        p.conn.Close()
        p.conn = nil
    }
    
    p.connected.Store(false)
    return nil
}
```

### New Monitoring Method

```go
// GetQueueStatus returns the current queue length and capacity
func (p *WebSocketPipe) GetQueueStatus() (length int, capacity int) {
    p.queueMu.Lock()
    defer p.queueMu.Unlock()
    return len(p.messageQueue), p.maxQueueLength
}
```

## Integration Changes

### Host WebSocket Integration

Update `internal/host/websocket.go`:

```go
func (wh *WebSocketHost) Connect(ctx context.Context, config *types.WebSocketConfig) error {
    if wh.pipe != nil && wh.pipe.IsConnected() {
        return nil
    }
    
    wsURL := wh.url + "/ws/" + wh.sessionID
    if wh.clientID != "" {
        wsURL += "?client_id=" + wh.clientID
    }
    
    pipe, err := ws.Dial(ctx, wsURL, wh.tlsConfig, config)
    if err != nil {
        return err
    }
    
    wh.pipe = pipe
    pipe.OnMessage(wh.handleMessage)
    pipe.OnDisconnect(func() {
        wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket disconnected")
    })
    pipe.Start(ctx)
    
    wsHostCh.Log(alog.INFO, "[remote-control] Host WebSocket connected to %s", wh.url)
    return nil
}
```

### Client WebSocket Integration

Update `internal/client/websocket.go`:

```go
func (c *WebSocketConnection) Connect(ctx context.Context, config *types.WebSocketConfig) error {
    if c.pipe != nil && c.pipe.IsConnected() {
        return nil
    }
    
    wsURL := c.url + "/ws/" + c.sessionID
    wsCh.Log(alog.DEBUG, "Dialing WebSocket at [%s]", wsURL)
    
    pipe, err := ws.Dial(ctx, wsURL, c.tlsConfig, config)
    if err != nil {
        return err
    }
    
    c.pipe = pipe
    pipe.OnMessage(c.handleMessage)
    pipe.OnDisconnect(func() {
        wsCh.Log(alog.DEBUG, "WebSocket disconnected")
    })
    pipe.Start(ctx)
    
    wsCh.Log(alog.DEBUG, "[remote-control] WebSocket connected to %s", c.url)
    return nil
}
```

## Implementation Steps

### Phase 1: Configuration Setup
1. Add `WebSocketConfig` struct to `internal/common/config/config.go`
2. Add default values in `DefaultConfig()`
3. Update config loading and validation
4. Add config tests

**Files to modify**:
- `internal/common/config/config.go`
- `internal/common/config/config_test.go`

### Phase 2: Core Recovery Infrastructure
1. Extend `WebSocketPipe` struct with recovery fields
2. Implement `queueMessage()` method
3. Modify `Send()` and `SendMessage()` to queue on failure
4. Add `GetQueueStatus()` monitoring method

**Files to modify**:
- `internal/common/websocket/websocket.go`

### Phase 3: Reconnection Logic
1. Implement `startReconnectLoop()` with idempotency check
2. Implement `attemptReconnect()` method
3. Modify `handleDisconnect()` to trigger reconnection
4. Update `Close()` to cancel reconnection loop

**Files to modify**:
- `internal/common/websocket/websocket.go`

### Phase 4: Queue Flushing
1. Implement `flushQueue()` method with blocking send
2. Integrate flush call after successful reconnection
3. Add proper error handling and logging

**Files to modify**:
- `internal/common/websocket/websocket.go`

### Phase 5: Dial and Start Updates
1. Modify `Dial()` to accept and store config
2. Update `Start()` to store context for reconnection
3. Ensure pump restart works correctly after reconnection

**Files to modify**:
- `internal/common/websocket/websocket.go`

### Phase 6: Integration Updates
1. Update `internal/host/websocket.go` to pass config to `Dial()`
2. Update `internal/client/websocket.go` to pass config to `Dial()`
3. Update host and client initialization to load config

**Files to modify**:
- `internal/host/websocket.go`
- `internal/host/host.go`
- `internal/client/websocket.go`
- `internal/client/client.go`

### Phase 7: Testing
1. Add unit tests for queueing logic
2. Add unit tests for reconnection loop
3. Add integration tests with mock server
4. Add end-to-end tests with simulated failures
5. Test graceful shutdown during reconnection

**Files to create/modify**:
- `internal/common/websocket/websocket_test.go`
- `test/integration/websocket_recovery_test.go`
- `test/e2e/websocket_recovery_test.go`

## Testing Strategy

### Unit Tests

1. **Queue Management**:
   - Test adding messages to queue
   - Test queue overflow (oldest dropped)
   - Test queue flushing
   - Test GetQueueStatus()

2. **Reconnection Logic**:
   - Test idempotent reconnection start
   - Test reconnection cancellation on Close()
   - Test context propagation

3. **Send Behavior**:
   - Test queueing on send failure
   - Test normal send when connected

### Integration Tests

1. **Mock Server Tests**:
   - Start mock WebSocket server
   - Connect client
   - Kill server
   - Verify messages are queued
   - Restart server
   - Verify reconnection and queue flush

2. **Network Failure Simulation**:
   - Use network simulation tools
   - Test various failure scenarios
   - Verify recovery behavior

### End-to-End Tests

1. **Full System Tests**:
   - Run actual host and client
   - Simulate server restarts
   - Verify session continuity
   - Verify message delivery

2. **Load Tests**:
   - High message volume during disconnection
   - Verify queue behavior under load
   - Verify no message loss

3. **Chaos Tests**:
   - Random disconnections
   - Random delays
   - Verify system stability

## Error Scenarios and Handling

### Scenario 1: Network Interruption
- **Behavior**: Messages queued, reconnection starts
- **Recovery**: Automatic reconnection, queue flushed
- **User Impact**: Transparent, no action needed

### Scenario 2: Server Restart
- **Behavior**: Connection drops, reconnection attempts
- **Recovery**: Reconnects when server is back, queue flushed
- **User Impact**: Brief delay, automatic recovery

### Scenario 3: Queue Overflow
- **Behavior**: Oldest messages dropped
- **Recovery**: Continues with newer messages
- **User Impact**: Potential data loss (logged)

### Scenario 4: Process Exit During Reconnection
- **Behavior**: Context canceled, reconnection stops
- **Recovery**: Clean shutdown
- **User Impact**: Expected behavior

### Scenario 5: Permanent Network Failure
- **Behavior**: Continuous reconnection attempts
- **Recovery**: Retries indefinitely until network restored
- **User Impact**: Process continues, reconnects when possible

## Performance Considerations

### Memory Usage
- Queue size limited to 100 messages by default
- Each message is raw bytes (already serialized)
- Estimated max memory: ~100KB per connection (assuming 1KB avg message)

### CPU Usage
- Reconnection ticker runs every 5 seconds
- Minimal CPU impact during normal operation
- Reconnection attempts are lightweight (single dial)

### Network Usage
- Reconnection attempts are infrequent (5s interval)
- No additional network overhead during normal operation
- Queue flush is one-time burst after reconnection

## Future Enhancements

### Phase 2 Features (Post-Initial Implementation)

1. **Size-based Queue Limits**
   - Replace message count with byte-based limits
   - More accurate memory management
   - Configurable max queue size in bytes

2. **Message Prioritization**
   - Different queues for different message types
   - Priority-based flushing
   - Critical messages never dropped

3. **Exponential Backoff (Optional)**
   - Add optional exponential backoff strategy
   - Configurable via config
   - Reduces server load during extended outages

4. **Connection Health Monitoring**
   - Proactive health checks
   - Detect degraded connections
   - Preemptive reconnection

5. **Enhanced Monitoring**
   - Metrics for reconnection attempts
   - Queue depth metrics
   - Message drop metrics
   - Integration with monitoring systems

6. **Callback Enhancements**
   - Optional callbacks for reconnection events
   - Configurable notification levels
   - Integration with alerting systems

## Success Criteria

The implementation will be considered successful when:

1. ✅ All WebSocket connections automatically recover from disconnections
2. ✅ No messages are lost during brief disconnections (within queue limits)
3. ✅ Reconnection is transparent to callers (no API changes)
4. ✅ Configuration is flexible and well-documented
5. ✅ All tests pass (unit, integration, e2e)
6. ✅ Performance impact is minimal
7. ✅ Code is well-documented and maintainable
8. ✅ Graceful shutdown works correctly during reconnection

## Documentation Updates

After implementation, update:

1. **README.md**: Add section on WebSocket recovery
2. **Configuration docs**: Document new WebSocket config options
3. **Architecture docs**: Update WebSocket architecture documentation
4. **API docs**: Document new `GetQueueStatus()` method
5. **Troubleshooting guide**: Add recovery-related troubleshooting

## Rollout Plan

1. **Development**: Implement in feature branch
2. **Testing**: Comprehensive test suite
3. **Code Review**: Team review of implementation
4. **Staging**: Deploy to staging environment
5. **Monitoring**: Monitor behavior in staging
6. **Production**: Gradual rollout to production
7. **Observation**: Monitor metrics and logs
8. **Iteration**: Adjust configuration based on real-world behavior
