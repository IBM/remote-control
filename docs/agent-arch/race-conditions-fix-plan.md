# Race Conditions Fix Plan

## Overview

Two critical race conditions have been identified that cause "write to closed pipe" panics in the remote-control server. Both must be fixed to eliminate the panics.

---

## Race Condition #1: Goroutine Closure Bug

### Location
`internal/server/session/session.go`, lines 184-194

### Current Code
```go
// Send the chunk to all clients
// NOTE: No need to send to host since output always comes from host
var wg sync.WaitGroup
for clientID, client := range s.clients {
    if client.Info.Approval == types.ApprovalApproved {
        sessCh.Log(alog.DEBUG4, "Sending chunk to %s", clientID)
        wg.Add(1)
        go func() {
            defer wg.Done()
            Send(client, types.WSMessageOutput, &chunk)
        }()
    }
}
wg.Wait()
```

### Problem
The goroutine closure captures the loop variable `client` by reference. All goroutines share the same variable, causing messages to be sent to the wrong clients.

### Fix
```go
// Send the chunk to all clients
// NOTE: No need to send to host since output always comes from host
var wg sync.WaitGroup
for clientID, client := range s.clients {
    if client.Info.Approval == types.ApprovalApproved {
        sessCh.Log(alog.DEBUG4, "Sending chunk to %s", clientID)
        wg.Add(1)
        go func(c *SessionClient) {
            defer wg.Done()
            Send(c, types.WSMessageOutput, &chunk)
        }(client)
    }
}
wg.Wait()
```

### Changes Required
- Add parameter `c *SessionClient` to the goroutine function
- Pass `client` as argument when invoking the goroutine

---

## Race Condition #2: Concurrent WebSocket Write

### Location
`internal/common/websocket/websocket.go`

### Current Code (Close method, line 290-307)
```go
func (p *WebSocketPipe) Close() error {
    p.mu.Lock()
    defer p.mu.Unlock()

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

### Problem
Both `Close()` and `writePump()` call `conn.WriteMessage()` simultaneously, violating gorilla/websocket's thread-safety requirement. This causes the panic:
```
panic: concurrent write to websocket connection
```

### Fix Strategy
Signal `writePump` to send the close frame instead of writing directly from `Close()`. The `writePump` already has exclusive write access.

### Updated Close Method
```go
func (p *WebSocketPipe) Close() error {
    p.mu.Lock()
    defer p.mu.Unlock()

    if p.reconnectCancel != nil {
        p.reconnectCancel()
    }

    select {
    case <-p.done:
        return nil
    default:
        // Signal writePump to send close frame by closing send channel
        // The writePump will detect this and send the close message
        close(p.send)
        close(p.done)
    }

    // Don't write directly to conn - let writePump handle it
    // The connection will be closed when writePump exits

    p.connected.Store(false)
    return nil
}
```

### Verify writePump Handles Closed Send Channel
The `writePump` method (line 390-410) already correctly handles a closed send channel:

```go
case message, ok := <-p.send:
    // ... get conn ...
    
    conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
    if !ok {
        conn.WriteMessage(websocket.CloseMessage, []byte{})  // ✅ Sends close frame
        return
    }
    // ... write message ...
```

This is already correct and doesn't need changes.

### Additional Cleanup Required
Ensure `p.conn.Close()` is called after writePump exits. Check if `handleDisconnect()` already does this:

```go
func (p *WebSocketPipe) handleDisconnect() {
    p.mu.Lock()
    p.connected.Store(false)
    if p.conn != nil {
        p.conn.Close()  // ✅ Already closes connection
        p.conn = nil
    }
    onDisconnect := p.onDisconnect
    p.mu.Unlock()
    // ...
}
```

Good - `handleDisconnect()` already closes the connection, so no additional changes needed.

---

## Implementation Steps

### Step 1: Fix Goroutine Closure Bug
1. Open `internal/server/session/session.go`
2. Locate the `AppendOutput` method (around line 184)
3. Find the goroutine spawn inside the `for` loop
4. Add parameter `c *SessionClient` to the goroutine function
5. Pass `client` as argument: `}(client)`

### Step 2: Fix Concurrent WebSocket Write
1. Open `internal/common/websocket/websocket.go`
2. Locate the `Close()` method (around line 290)
3. Replace the direct `conn.WriteMessage()` call with `close(p.send)`
4. Remove the `p.conn.Close()` and `p.conn = nil` lines (handled by handleDisconnect)
5. Verify `writePump` correctly handles closed send channel (should already be correct)

### Step 3: Test with Race Detector
```bash
make test  # Already runs with -race flag
```

### Step 4: Verify Fixes
1. Run full test suite
2. Check for any race detector warnings
3. Verify no "concurrent write" panics
4. Confirm no "write to closed pipe" panics

---

## Testing Strategy

### Automated Tests

**Test 1: Goroutine Closure Bug**
```go
func TestAppendOutputConcurrentClients(t *testing.T) {
    // Create session with 10 clients
    // Rapidly send output chunks
    // Verify each client receives correct messages
    // Run with -race flag
}
```

**Test 2: Concurrent WebSocket Write**
```go
func TestWebSocketConcurrentClose(t *testing.T) {
    // Create WebSocketPipe
    // Start continuous message sending
    // Call Close() while messages are being sent
    // Verify no panic occurs
    // Run with -race flag
}
```

**Test 3: Client Removal During Output**
```go
func TestRemoveClientsDuringOutput(t *testing.T) {
    // Create session with multiple clients
    // Start sending continuous output
    // Remove clients while output is being sent
    // Verify no panics and remaining clients receive messages
}
```

### Manual Testing

1. **Load Test**: Run with multiple concurrent clients connecting/disconnecting
2. **Stress Test**: Send high-volume output while clients join/leave
3. **Race Detector**: Run all tests with `-race` flag
4. **Production Simulation**: Replicate the conditions that triggered the original panic

### CI/CD Verification

Ensure GitHub Actions workflow (`.github/workflows/test.yml`) runs tests with race detector enabled (already configured).

---

## Risk Assessment

### Low Risk Changes
- **Goroutine closure fix**: Simple parameter passing, no logic changes
- **WebSocket close fix**: Uses existing mechanisms, no new code paths

### Potential Issues
- **Send channel closure**: Ensure no other code tries to send after close
  - Mitigation: The `Send()` method already checks `p.done` before sending
- **Connection cleanup**: Verify `handleDisconnect()` is always called
  - Mitigation: Already called by `readPump` defer statement

### Rollback Plan
If issues arise:
1. Revert both changes
2. Add write mutex as alternative fix for websocket issue
3. Keep goroutine closure fix (very low risk)

---

## Success Criteria

- [ ] No race detector warnings in test suite
- [ ] No "concurrent write to websocket connection" panics
- [ ] No "write to closed pipe" panics
- [ ] All existing tests pass
- [ ] Messages reach correct recipients
- [ ] Clean connection shutdown under load

---

## Next Steps

1. **Switch to Code Mode**: Implement the fixes
2. **Run Tests**: Execute `make test` with race detector
3. **Manual Testing**: Verify under load conditions
4. **Code Review**: Have another developer review the changes
5. **Deploy**: Roll out to staging environment first
6. **Monitor**: Watch for any panics or race conditions in production

---

## Additional Notes

### Why These Fixes Work

**Goroutine Closure Fix:**
- Each goroutine gets its own copy of the client pointer
- No shared variable between goroutines
- Go's garbage collector keeps the client alive until all goroutines complete

**WebSocket Write Fix:**
- Single-writer pattern: only `writePump` writes to the connection
- `Close()` signals intent via channel closure
- No concurrent writes to the underlying websocket

### Prevention for Future

1. **Code Review Checklist**: Check all goroutines spawned in loops
2. **Linting**: Use tools that detect closure variable capture
3. **Testing**: Always run tests with `-race` flag in CI/CD
4. **Documentation**: Add to AGENTS.md development guidelines

### Related Documentation

- [Go FAQ: Closures and goroutines](https://go.dev/doc/faq#closures_and_goroutines)
- [Gorilla WebSocket: Concurrency](https://pkg.go.dev/github.com/gorilla/websocket#hdr-Concurrency)
- [Go Race Detector](https://go.dev/doc/articles/race_detector)
