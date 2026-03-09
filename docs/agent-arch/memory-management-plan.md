# Memory Management Implementation Plan

## Overview

This plan addresses memory buildup in the remote-control system by implementing two key features:
1. **Session lifecycle management**: Flush completed sessions from memory
2. **Consumption-based data purging**: Remove OutputChunks and StdinEntries once consumed by all active clients

## Current Architecture Analysis

### Data Flow
- **Host → Server**: Appends OutputChunks to session via `POST /sessions/{id}/output`
- **Client → Server**: Polls for new chunks via `GET /sessions/{id}/output?stdout_offset=X&stderr_offset=Y`
- **Client → Server**: Submits stdin via `POST /sessions/{id}/stdin`
- **Host → Server**: Polls for pending stdin via `GET /sessions/{id}/stdin`

### Current Memory Model
- `Session` struct holds all data in memory:
  - `stdoutChunks []OutputChunk` - grows unbounded
  - `stderrChunks []OutputChunk` - grows unbounded
  - `stdin []*StdinEntry` - grows unbounded
  - `clients map[string]*ClientRecord` - tracks connected clients

### Key Files
- `internal/session/session.go` - Session data structure and methods
- `internal/session/store.go` - MemoryStore implementation
- `internal/session/types.go` - Type definitions
- `internal/api/handlers.go` - HTTP handlers for I/O operations
- `internal/host/proxy.go` - Host-side I/O proxying
- `internal/client/poller.go` - Client-side polling logic

## Design Decisions

### 1. Session Retention After Completion
- **Decision**: Immediate flush from MemoryStore when session completes
- **Rationale**: Completed sessions have no active subprocess, so no new data will be generated
- **Implementation**: `MemoryStore.Delete()` called automatically when session status becomes `StatusCompleted`

### 2. Client Disconnection Detection
- **Decision**: Timeout-based detection using last poll timestamp
- **Timeout**: 60 seconds of inactivity (configurable)
- **Rationale**: No explicit disconnect API, so must infer from polling behavior
- **Implementation**: Background goroutine periodically checks client activity

### 3. Data Purging Granularity
- **Decision**: Per-chunk purging
- **Rationale**: Simple to implement, provides fine-grained memory reclamation
- **Alternative considered**: Batch purging by offset (deferred for performance optimization if needed)

### 4. API Compatibility
- **Decision**: Return error if client requests purged data
- **Rationale**: Pre-release project, no backward compatibility required
- **Error response**: HTTP 410 Gone with descriptive message

### 5. TUI Visual State Issues
- **Status**: Noted as TODO, not implemented in this phase
- **Issue**: Clients joining mid-stream may see broken TUI state if chunks were purged
- **Potential solution**: Intelligent chunk segmentation along terminal boundaries (newlines, clear sequences)
- **Decision**: Monitor in production; implement only if it becomes a real problem

## Implementation Plan

### Phase 1: Client Activity Tracking

#### 1.1 Add Client Activity Fields
**File**: `internal/session/approval.go`

Add to `ClientRecord`:
```go
LastPollAt    time.Time      `json:"last_poll_at"`
StdoutOffset  int64          `json:"stdout_offset"`
StderrOffset  int64          `json:"stderr_offset"`
```

#### 1.2 Update Client Registration
**File**: `internal/session/approval.go`

Modify `RegisterClient()` to initialize:
```go
LastPollAt:   time.Now(),
StdoutOffset: 0,
StderrOffset: 0,
```

#### 1.3 Track Poll Activity
**File**: `internal/session/session.go`

Add new method:
```go
func (s *Session) UpdateClientActivity(clientID string, stdoutOffset, stderrOffset int64) error
```

Updates `LastPollAt`, `StdoutOffset`, `StderrOffset` for the client.

#### 1.4 Add Client ID to Poll Request
**File**: `internal/api/types.go`

Modify poll request to accept client_id as query parameter:
- Add `client_id` to query string: `GET /sessions/{id}/output?client_id=X&stdout_offset=Y&stderr_offset=Z`

**File**: `internal/api/handlers.go`

Modify `handlePollOutput()`:
- Extract `client_id` from query parameter: `r.URL.Query().Get("client_id")`
- If `client_id` is provided, call `UpdateClientActivity()` with requested offsets
- If client not registered, register them automatically (or return error if approval required)
- Host polls without client_id (doesn't need tracking)

### Phase 2: Client Timeout and Cleanup

#### 2.1 Add Configuration
**File**: `internal/config/config.go`

Add to `Config`:
```go
ClientTimeoutSeconds int `json:"client_timeout_seconds"`
```

Default: 60 seconds

#### 2.2 Implement Client Cleanup
**File**: `internal/session/session.go`

Add new method:
```go
func (s *Session) RemoveInactiveClients(timeout time.Duration) []string
```

Returns list of removed client IDs. Removes clients where `time.Since(LastPollAt) > timeout`.

#### 2.3 Event-Driven Client Cleanup
**File**: `internal/api/handlers.go`

Integrate cleanup into existing operations (event-driven approach):

**In `handlePollOutput()`**:
- After updating client activity, call `session.RemoveInactiveClients(timeout)`
- This piggybacks on existing poll traffic

**In `handleAppendOutput()`**:
- After appending output, call `session.RemoveInactiveClients(timeout)`
- This triggers cleanup when host sends new data

**Rationale**: 
- Avoids dedicated background goroutine
- Cleanup happens naturally during normal operations
- More efficient than periodic scanning
- Cleanup frequency scales with activity level

**Alternative considered**: Periodic goroutine (simpler but less efficient)

### Phase 3: Consumption-Based Data Purging

#### 3.1 Add Purging Methods to Session
**File**: `internal/session/session.go`

Add methods:
```go
// PurgeConsumedOutput removes OutputChunks that all active clients have consumed
func (s *Session) PurgeConsumedOutput() (purgedStdout, purgedStderr int)

// PurgeConsumedStdin removes StdinEntries that have been accepted by the host
func (s *Session) PurgeConsumedStdin() int
```

**PurgeConsumedOutput logic**:
1. Find minimum `StdoutOffset` across all active (approved) clients
2. Remove all `stdoutChunks` where `chunk.Offset + len(chunk.Data) <= minOffset`
3. Repeat for stderr
4. Return count of purged chunks

**PurgeConsumedStdin logic**:
1. Remove all `StdinEntry` where `Status == StdinAccepted`
2. Return count of purged entries

#### 3.2 Integrate Purging into Poll Handler
**File**: `internal/api/handlers.go`

Modify `handlePollOutput()`:
- After updating client activity, call `session.PurgeConsumedOutput()`
- Log purge statistics at DEBUG level

#### 3.3 Integrate Purging into Stdin Accept Handler
**File**: `internal/api/handlers.go`

Modify `handleAcceptStdin()`:
- After accepting stdin, call `session.PurgeConsumedStdin()`
- Log purge statistics at DEBUG level

#### 3.4 Handle Purged Data Requests
**File**: `internal/session/session.go`

Modify `ReadOutput()`:
- Track earliest available offset (first chunk's offset, or current stream offset if no chunks)
- If `fromOffset < earliestOffset`, adjust to start from `earliestOffset`
- Return chunks starting from the adjusted offset
- Return the actual starting offset used (may differ from requested)

**File**: `internal/api/types.go`

Modify `PollOutputResponse`:
```go
type PollOutputResponse struct {
    Chunks         []OutputChunkResponse `json:"chunks"`
    NextOffsets    map[string]int64      `json:"next_offsets"`
    ActualOffsets  map[string]int64      `json:"actual_offsets"` // NEW: actual starting offsets used
}
```

**File**: `internal/api/handlers.go`

Modify `handlePollOutput()`:
- Return `ActualOffsets` showing the actual starting offset for each stream
- Clients can detect data loss by comparing requested vs actual offsets
- No error returned - just return available data

### Phase 4: Session Lifecycle Management

#### 4.1 Add Auto-Delete on Completion
**File**: `internal/api/handlers.go`

Modify `handlePatchSession()`:
- After calling `session.Complete()`, immediately delete the session
- Call `s.store.Delete(id)` directly (no scheduling needed)
- Return success response before deletion completes

```go
func (s *Server) handlePatchSession(w http.ResponseWriter, r *http.Request) {
    // ... existing code ...
    sess.Complete(req.ExitCode)
    
    // Immediately delete completed session from memory
    if err := s.store.Delete(id); err != nil {
        ch.Log(alog.WARNING, "[remote-control] failed to delete completed session: %v", err)
    }
    
    writeJSON(w, http.StatusOK, sessionToResponse(sess))
}
```

#### 4.2 Handle Requests to Deleted Sessions
**File**: `internal/api/handlers.go`

All handlers already return 404 if `store.Get()` fails. No changes needed.

### Phase 5: Testing and Validation

#### 5.1 Unit Tests
**File**: `internal/session/session_test.go`

Add tests:
- `TestUpdateClientActivity`
- `TestRemoveInactiveClients`
- `TestPurgeConsumedOutput`
- `TestPurgeConsumedStdin`
- `TestReadOutputPurgedData`

#### 5.2 Integration Tests
**File**: `test/integration/memory_test.go`

Add tests:
- Session deleted after completion
- Client timeout and removal
- Output purging with multiple clients
- Stdin purging after acceptance
- 410 Gone response for purged data

#### 5.3 End-to-End Tests
**File**: `test/e2e/memory_test.go`

Add tests:
- Full session lifecycle with memory cleanup
- Multiple clients with different consumption rates
- Client reconnection after timeout

## Implementation Order

1. **Phase 1**: Client Activity Tracking (foundation for all other features)
2. **Phase 2**: Client Timeout and Cleanup (enables accurate purging)
3. **Phase 3**: Consumption-Based Data Purging (core memory management)
4. **Phase 4**: Session Lifecycle Management (cleanup completed sessions)
5. **Phase 5**: Testing and Validation (ensure correctness)

## Metrics and Monitoring

Add logging for:
- Session deletion events
- Client timeout events
- Purge operations (chunks/entries removed)
- Memory usage before/after purge (optional, for debugging)

Use `alog.DEBUG` level for detailed purge statistics.

## Future Enhancements (Out of Scope)

1. **Persistent storage**: Move old session data to disk/database
2. **Configurable retention policies**: Keep last N chunks, or last X minutes
3. **Smart chunk segmentation**: Preserve terminal state boundaries
4. **Client reconnection grace period**: Allow clients to resume after brief disconnection
5. **Compression**: Compress old chunks before purging
6. **Metrics endpoint**: Expose memory usage statistics via HTTP

## TODO Items

- [ ] Monitor TUI visual state issues in production
- [ ] Consider implementing smart chunk segmentation if TUI issues arise
- [ ] Evaluate performance of per-chunk purging vs batch purging
- [ ] Add Prometheus metrics for memory usage tracking

## Risk Assessment

### Low Risk
- Session deletion after completion (well-defined lifecycle)
- Stdin purging (host is single consumer)

### Medium Risk
- Client timeout detection (may disconnect active clients if network is slow)
- Output purging (clients may request purged data)

### Mitigation
- Make timeout configurable (default 60s, can increase for slow networks)
- Return clear error message (410 Gone) when data is purged
- Log all purge operations for debugging

## Success Criteria

1. ✅ Completed sessions are removed from memory immediately
2. ✅ Inactive clients are detected and removed after timeout
3. ✅ OutputChunks are purged once all active clients have consumed them
4. ✅ StdinEntries are purged once accepted by host
5. ✅ Clients receive clear error when requesting purged data
6. ✅ Memory usage remains bounded during long-running sessions
7. ✅ All tests pass (unit, integration, e2e)
