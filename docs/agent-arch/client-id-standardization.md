# Client ID Standardization Plan

## Current State
- Client generates its own UUID in `NewClient()`
- Server extracts client ID from TLS cert CN or X-Client-ID header
- Registration endpoint returns the extracted/generated ID
- Client doesn't use the returned ID

## Target State
1. Client registers with server (no client ID needed)
2. Server generates and returns a unique client ID
3. Client stores and uses this ID in all subsequent requests
4. Client ID is required in query parameters for all client operations

## Implementation Steps

### 1. Server Changes

#### `internal/api/handlers_approval.go`
- Modify `handleRegisterClient()` to generate a new UUID for each registration
- Remove dependency on `clientIdentity()` for ID generation
- Return the server-generated ID in response

#### `internal/api/server.go`
- Remove or deprecate `clientIdentity()` method
- Update `checkClientApproval()` to work with explicit client IDs only

#### `internal/api/handlers.go`
- Make `client_id` query parameter **required** for:
  - `handlePollOutput()`
  - `handleEnqueueStdin()`
- Return 400 Bad Request if client_id is missing

### 2. Client Changes

#### `internal/client/client.go`
- Remove `clientID` field initialization in `NewClient()`
- Store server-assigned ID after registration
- Pass client ID to all API calls

#### `internal/client/apiclient.go`
- Update `PollOutput()` to require and include client_id parameter
- Update `EnqueueStdin()` to require and include client_id parameter
- Update `RegisterClient()` to not send a client_id (server generates it)

### 3. API Contract Changes

#### Registration Endpoint
**Before:**
```
POST /sessions/{id}/clients
Body: { "client_id": "uuid" }
Response: { "client_id": "uuid", "status": "pending" }
```

**After:**
```
POST /sessions/{id}/clients
Body: { "common_name": "optional-display-name" }
Response: { "client_id": "server-generated-uuid", "status": "pending" }
```

#### Poll Endpoint
**Before:**
```
GET /sessions/{id}/output?stdout_offset=X&stderr_offset=Y&client_id=Z (optional)
```

**After:**
```
GET /sessions/{id}/output?stdout_offset=X&stderr_offset=Y&client_id=Z (required)
```

#### Stdin Endpoint
**Before:**
```
POST /sessions/{id}/stdin
Body: { "source": "client-id", "data": "base64" }
```

**After:**
```
POST /sessions/{id}/stdin?client_id=Z
Body: { "data": "base64" }
```

## Migration Notes

- This is a breaking change for existing clients
- Host operations (no client_id) remain unchanged
- TLS cert CN can still be used for display purposes (common_name)
- Old clients will receive 400 Bad Request until updated

## Benefits

1. **Centralized ID Management**: Server controls all client IDs
2. **Simplified Client Logic**: No UUID generation needed
3. **Explicit Client Tracking**: All operations clearly identify the client
4. **Better Security**: Server can enforce ID uniqueness and validity
5. **Audit Trail**: Clear mapping of operations to registered clients
