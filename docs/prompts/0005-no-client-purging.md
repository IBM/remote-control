# No-Client Memory Purging Implementation Plan

## Problem Statement

Currently, when a host outputs data before any clients connect, chunks accumulate in memory without being purged. The `PurgeConsumedOutput()` method at line 276 in `session.go` has this logic:

```go
// If no approved clients, don't purge anything
if minStdoutOffset == -1 {
    return 0, 0
}
```

This causes memory buildup during initialization phases when the host is actively producing output but no clients have connected yet.

## Proposed Solution

**Invert the purging logic**: When there are NO approved clients, purge ALL chunks. When there ARE approved clients, use the existing consumption-based purging logic.

### Rationale

1. **No clients = no consumers**: If no one is reading the data, there's no reason to keep it in memory
2. **Prevents initialization buildup**: Long-running hosts that output data before clients connect won't accumulate unbounded memory
3. **Maintains existing behavior for active sessions**: Once clients connect, the consumption-based purging continues to work as designed
4. **Simple and safe**: Clear logic with no edge cases

## Implementation Details

### File: `internal/session/session.go`

**Current Code (lines ~270-280):**
```go
// Find minimum offset across all active approved clients
var minStdoutOffset, minStderrOffset int64 = -1, -1

for _, client := range s.clients {
    // Only consider approved clients
    if client.Approval != ApprovalApproved {
        continue
    }

    if minStdoutOffset == -1 || client.StdoutOffset < minStdoutOffset {
        minStdoutOffset = client.StdoutOffset
    }
    if minStderrOffset == -1 || client.StderrOffset < minStderrOffset {
        minStderrOffset = client.StderrOffset
    }
}

// If no approved clients, don't purge anything
if minStdoutOffset == -1 {
    return 0, 0
}
```

**Proposed Change:**
```go
// Find minimum offset across all active approved clients
var minStdoutOffset, minStderrOffset int64 = -1, -1

for _, client := range s.clients {
    // Only consider approved clients
    if client.Approval != ApprovalApproved {
        continue
    }

    if minStdoutOffset == -1 || client.StdoutOffset < minStdoutOffset {
        minStdoutOffset = client.StdoutOffset
    }
    if minStderrOffset == -1 || client.StderrOffset < minStderrOffset {
        minStderrOffset = client.StderrOffset
    }
}

// If no approved clients, purge ALL chunks (no one is consuming)
if minStdoutOffset == -1 {
    purgedStdout = len(s.stdoutChunks)
    purgedStderr = len(s.stderrChunks)
    s.stdoutChunks = nil
    s.stderrChunks = nil
    return purgedStdout, purgedStderr
}
```

### Key Changes

1. **When no clients exist**: Set purge counts to the total number of chunks and clear both chunk slices
2. **When clients exist**: Continue with existing consumption-based purging logic
3. **Return values**: Accurately report the number of chunks purged in both cases

## Testing Strategy

### Unit Tests (`internal/session/session_test.go`)

Add test case: `TestPurgeConsumedOutput_NoClients`

```go
func TestPurgeConsumedOutput_NoClients(t *testing.T) {
    sess := newSession("test-session", []string{"echo", "test"})
    
    // Append some output chunks
    sess.AppendOutput(StreamStdout, []byte("line 1\n"), time.Now())
    sess.AppendOutput(StreamStdout, []byte("line 2\n"), time.Now())
    sess.AppendOutput(StreamStderr, []byte("error 1\n"), time.Now())
    
    // Verify chunks exist
    assert.Equal(t, 2, len(sess.stdoutChunks))
    assert.Equal(t, 1, len(sess.stderrChunks))
    
    // Purge with no clients
    purgedStdout, purgedStderr := sess.PurgeConsumedOutput()
    
    // All chunks should be purged
    assert.Equal(t, 2, purgedStdout)
    assert.Equal(t, 1, purgedStderr)
    assert.Equal(t, 0, len(sess.stdoutChunks))
    assert.Equal(t, 0, len(sess.stderrChunks))
}
```

### Integration Tests (`test/integration/memory_test.go`)

Add test case: `TestNoClientMemoryPurging`

```go
func TestNoClientMemoryPurging(t *testing.T) {
    // Start a session with host outputting data
    // Verify chunks accumulate initially
    // Trigger purge (via poll handler or explicit call)
    // Verify all chunks are purged when no clients exist
    // Connect a client
    // Verify chunks start accumulating again
    // Verify consumption-based purging works with client
}
```

### End-to-End Tests (`test/e2e/memory_test.go`)

Add test case: `TestInitializationMemoryManagement`

```go
func TestInitializationMemoryManagement(t *testing.T) {
    // Start host that outputs continuously
    // Wait for significant output (e.g., 1000 chunks)
    // Verify memory doesn't grow unbounded
    // Connect client mid-stream
    // Verify client can still read available data
    // Verify consumption-based purging takes over
}
```

## Edge Cases and Considerations

### 1. Client Connects Mid-Stream
**Scenario**: Host outputs data → chunks purged → client connects

**Behavior**: 
- Client will request data from offset 0
- `ReadOutput()` will adjust to earliest available offset (which may be > 0)
- Client receives `ActualOffsets` showing data loss
- This is expected and already handled by existing code

### 2. All Clients Disconnect
**Scenario**: Multiple clients connected → all disconnect → host continues outputting

**Behavior**:
- Client timeout mechanism removes inactive clients
- Once all clients removed, purging switches to "no clients" mode
- All subsequent chunks are purged immediately
- Memory remains bounded

### 3. Rapid Client Connect/Disconnect
**Scenario**: Client connects briefly, disconnects, repeats

**Behavior**:
- During connection: consumption-based purging
- During disconnection: full purging
- No memory leak as chunks are purged in both states

### 4. Approved vs Pending Clients
**Scenario**: Pending clients exist but no approved clients

**Behavior**:
- Only approved clients count for consumption tracking
- Pending clients are ignored (existing behavior)
- With no approved clients, full purging occurs
- This is correct: pending clients shouldn't prevent purging

## Performance Impact

### Memory
- **Before**: Unbounded growth when no clients connected
- **After**: Constant memory usage (only recent chunks retained)
- **Improvement**: O(n) → O(1) memory for no-client case

### CPU
- **Purging overhead**: Minimal - just clearing slice references
- **GC pressure**: Reduced - fewer long-lived objects
- **Overall**: Net positive performance impact

## Rollout Plan

1. **Implement change** in `session.go`
2. **Add unit tests** to verify behavior
3. **Run existing test suite** to ensure no regressions
4. **Add integration tests** for no-client scenarios
5. **Manual testing** with long-running host before client connection
6. **Monitor** memory usage in production

## Success Criteria

- ✅ No memory buildup when host outputs data before clients connect
- ✅ Existing consumption-based purging still works with connected clients
- ✅ All unit tests pass
- ✅ All integration tests pass
- ✅ All e2e tests pass
- ✅ No regressions in existing functionality

## Risks and Mitigation

### Risk: Data Loss for Late-Joining Clients
**Mitigation**: This is expected behavior. Clients joining mid-stream already handle data loss via `ActualOffsets`. Documentation should clarify that early output may not be available.

### Risk: Unexpected Behavior Change
**Mitigation**: The change is intuitive - "no consumers = no retention". Existing tests will catch any regressions.

### Risk: Race Conditions
**Mitigation**: All operations are protected by `s.mu` mutex. No new concurrency issues introduced.

## Documentation Updates

### Update: `docs/memory-management-plan.md`

Add section under "Design Decisions":

```markdown
### 6. No-Client Purging Strategy
- **Decision**: Purge all chunks when no approved clients exist
- **Rationale**: No consumers means no reason to retain data in memory
- **Implementation**: Invert purging logic in `PurgeConsumedOutput()`
- **Impact**: Prevents memory buildup during initialization phase
```

## Future Enhancements

1. **Configurable retention**: Keep last N chunks even with no clients (for debugging)
2. **Metrics**: Track purge events and reasons (no-clients vs consumed)
3. **Logging**: Add debug logs when switching between purge modes

## Related Issues

- Addresses the last remaining memory buildup case mentioned in memory management plan
- Complements existing consumption-based purging for active sessions
- Works with client timeout mechanism to handle disconnections
