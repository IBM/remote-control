package types

import (
	"encoding/json"
	"testing"
	"time"
)

// ============================================================================
// Stream Enum Tests
// ============================================================================

func TestStreamConstants(t *testing.T) {
	tests := []struct {
		name     string
		stream   Stream
		expected uint8
	}{
		{"StreamUnknown", StreamUnknown, 0},
		{"StreamStdout", StreamStdout, 1},
		{"StreamStderr", StreamStderr, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if uint8(tt.stream) != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, uint8(tt.stream))
			}
		})
	}
}

// ============================================================================
// SessionStatus Enum Tests
// ============================================================================

func TestSessionStatusConstants(t *testing.T) {
	tests := []struct {
		name     string
		status   SessionStatus
		expected uint8
	}{
		{"SessionStatusUnknown", SessionStatusUnknown, 0},
		{"SessionStatusActive", SessionStatusActive, 1},
		{"SessionStatusCompleted", SessionStatusCompleted, 2},
		{"SessionStatusError", SessionStatusError, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if uint8(tt.status) != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, uint8(tt.status))
			}
		})
	}
}

// ============================================================================
// OutputChunk Tests
// ============================================================================

func TestOutputChunkMarshalJSON(t *testing.T) {
	chunk := OutputChunk{
		Stream: StreamStdout,
		Data:   []byte("hello world"),
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	// Should be base64 encoded string
	expected := `"aGVsbG8gd29ybGQ="`
	if string(data) != expected {
		t.Errorf("expected %s, got %s", expected, string(data))
	}
}

func TestOutputChunkUnmarshalJSON(t *testing.T) {
	jsonData := []byte(`"aGVsbG8gd29ybGQ="`)

	var chunk OutputChunk
	err := json.Unmarshal(jsonData, &chunk)
	if err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	expected := "hello world"
	if string(chunk.Data) != expected {
		t.Errorf("expected %s, got %s", expected, string(chunk.Data))
	}
}

func TestOutputChunkMarshalUnmarshalRoundTrip(t *testing.T) {
	original := OutputChunk{
		Stream: StreamStderr,
		Data:   []byte("test data with special chars: \n\t\r"),
	}

	// Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Unmarshal
	var decoded OutputChunk
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	// Verify data integrity
	if string(decoded.Data) != string(original.Data) {
		t.Errorf("data mismatch: expected %q, got %q", original.Data, decoded.Data)
	}
}

func TestOutputChunkUnmarshalInvalidBase64(t *testing.T) {
	jsonData := []byte(`"not-valid-base64!@#$"`)

	var chunk OutputChunk
	err := json.Unmarshal(jsonData, &chunk)
	if err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}

func TestOutputChunkEmptyData(t *testing.T) {
	chunk := OutputChunk{
		Stream: StreamStdout,
		Data:   []byte{},
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded OutputChunk
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Data) != 0 {
		t.Errorf("expected empty data, got %d bytes", len(decoded.Data))
	}
}

// ============================================================================
// Permission Tests
// ============================================================================

func TestPermissionConstants(t *testing.T) {
	tests := []struct {
		name     string
		perm     Permission
		expected string
	}{
		{"PermissionReadOnly", PermissionReadOnly, "read-only"},
		{"PermissionReadWrite", PermissionReadWrite, "read-write"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.perm) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, string(tt.perm))
			}
		})
	}
}

// ============================================================================
// ApprovalStatus Tests
// ============================================================================

func TestApprovalStatusConstants(t *testing.T) {
	tests := []struct {
		name     string
		status   ApprovalStatus
		expected string
	}{
		{"ApprovalPending", ApprovalPending, "pending"},
		{"ApprovalApproved", ApprovalApproved, "approved"},
		{"ApprovalDenied", ApprovalDenied, "denied"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.status) != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, string(tt.status))
			}
		})
	}
}

// ============================================================================
// ClientInfo Tests
// ============================================================================

func TestClientInfoCreation(t *testing.T) {
	now := time.Now()
	info := ClientInfo{
		ClientID:     "test-client-123",
		JoinedAt:     now,
		Approval:     ApprovalPending,
		Permission:   PermissionReadOnly,
		LastPollAt:   now,
		StdoutOffset: 100,
		StderrOffset: 50,
	}

	if info.ClientID != "test-client-123" {
		t.Errorf("ClientID mismatch")
	}
	if info.Approval != ApprovalPending {
		t.Errorf("Approval mismatch")
	}
	if info.Permission != PermissionReadOnly {
		t.Errorf("Permission mismatch")
	}
	if info.StdoutOffset != 100 {
		t.Errorf("StdoutOffset mismatch")
	}
	if info.StderrOffset != 50 {
		t.Errorf("StderrOffset mismatch")
	}
}

func TestClientInfoJSONMarshaling(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	info := ClientInfo{
		ClientID:     "client-456",
		JoinedAt:     now,
		Approval:     ApprovalApproved,
		Permission:   PermissionReadWrite,
		LastPollAt:   now,
		StdoutOffset: 200,
		StderrOffset: 100,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded ClientInfo
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ClientID != info.ClientID {
		t.Errorf("ClientID mismatch")
	}
	if decoded.Approval != info.Approval {
		t.Errorf("Approval mismatch")
	}
	if decoded.Permission != info.Permission {
		t.Errorf("Permission mismatch")
	}
	if decoded.StdoutOffset != info.StdoutOffset {
		t.Errorf("StdoutOffset mismatch")
	}
	if decoded.StderrOffset != info.StderrOffset {
		t.Errorf("StderrOffset mismatch")
	}
}

// ============================================================================
// SessionInfo Tests
// ============================================================================

func TestSessionInfoCreation(t *testing.T) {
	now := time.Now()
	exitCode := 0
	info := SessionInfo{
		ID:          "session-789",
		Status:      SessionStatusActive,
		CreatedAt:   now,
		CompletedAt: &now,
		ExitCode:    &exitCode,
	}

	if info.ID != "session-789" {
		t.Errorf("ID mismatch")
	}
	if info.Status != SessionStatusActive {
		t.Errorf("Status mismatch")
	}
	if info.CompletedAt == nil {
		t.Errorf("CompletedAt should not be nil")
	}
	if info.ExitCode == nil || *info.ExitCode != 0 {
		t.Errorf("ExitCode mismatch")
	}
}

func TestSessionInfoJSONMarshalingWithOptionalFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	exitCode := 1
	info := SessionInfo{
		ID:          "session-complete",
		Status:      SessionStatusCompleted,
		CreatedAt:   now,
		CompletedAt: &now,
		ExitCode:    &exitCode,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded SessionInfo
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != info.ID {
		t.Errorf("ID mismatch")
	}
	if decoded.Status != info.Status {
		t.Errorf("Status mismatch")
	}
	if decoded.CompletedAt == nil {
		t.Errorf("CompletedAt should not be nil")
	}
	if decoded.ExitCode == nil || *decoded.ExitCode != 1 {
		t.Errorf("ExitCode mismatch")
	}
}

func TestSessionInfoJSONMarshalingWithoutOptionalFields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	info := SessionInfo{
		ID:        "session-active",
		Status:    SessionStatusActive,
		CreatedAt: now,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded SessionInfo
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.CompletedAt != nil {
		t.Errorf("CompletedAt should be nil")
	}
	if decoded.ExitCode != nil {
		t.Errorf("ExitCode should be nil")
	}
}

// ============================================================================
// HostClientID Constant Test
// ============================================================================

func TestHostClientIDConstant(t *testing.T) {
	if HostClientID != "host" {
		t.Errorf("expected HostClientID to be 'host', got %s", HostClientID)
	}
}

// ============================================================================
// Request/Response Types Tests
// ============================================================================

func TestCreateSessionRequest(t *testing.T) {
	tests := []struct {
		name string
		req  CreateSessionRequest
	}{
		{"WithID", CreateSessionRequest{ID: "custom-id"}},
		{"WithoutID", CreateSessionRequest{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			var decoded CreateSessionRequest
			err = json.Unmarshal(data, &decoded)
			if err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			if decoded.ID != tt.req.ID {
				t.Errorf("ID mismatch: expected %s, got %s", tt.req.ID, decoded.ID)
			}
		})
	}
}

func TestAppendOutputRequestMarshalJSON(t *testing.T) {
	req := AppendOutputRequest{
		Stream: StreamStdout,
		Data:   []byte("output data"),
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Should be base64 encoded
	expected := `"b3V0cHV0IGRhdGE="`
	if string(data) != expected {
		t.Errorf("expected %s, got %s", expected, string(data))
	}
}

func TestAppendOutputRequestUnmarshalJSON(t *testing.T) {
	jsonData := []byte(`"b3V0cHV0IGRhdGE="`)

	var req AppendOutputRequest
	err := json.Unmarshal(jsonData, &req)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	expected := "output data"
	if string(req.Data) != expected {
		t.Errorf("expected %s, got %s", expected, string(req.Data))
	}
}

func TestAppendOutputRequestInvalidBase64(t *testing.T) {
	jsonData := []byte(`"invalid-base64!@#"`)

	var req AppendOutputRequest
	err := json.Unmarshal(jsonData, &req)
	if err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}

func TestPollResponse(t *testing.T) {
	elements := []interface{}{"item1", "item2", "item3"}
	resp := PollResponse{Elements: elements}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded PollResponse
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	decodedSlice, ok := decoded.Elements.([]interface{})
	if !ok {
		t.Fatalf("Elements is not []interface{}")
	}

	if len(decodedSlice) != 3 {
		t.Errorf("expected 3 elements, got %d", len(decodedSlice))
	}
}

func TestPatchSessionRequest(t *testing.T) {
	req := PatchSessionRequest{ExitCode: 42}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded PatchSessionRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ExitCode != 42 {
		t.Errorf("expected exit code 42, got %d", decoded.ExitCode)
	}
}

func TestStdinRequest(t *testing.T) {
	req := StdinRequest{Data: "dGVzdCBzdGRpbg=="}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded StdinRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Data != req.Data {
		t.Errorf("Data mismatch")
	}
}

func TestAckStdinRequest(t *testing.T) {
	req := AckStdinRequest{ID: 12345}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded AckStdinRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != 12345 {
		t.Errorf("expected ID 12345, got %d", decoded.ID)
	}
}

func TestStdinStatusResponse(t *testing.T) {
	resp := StdinStatusResponse{Status: "processed"}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded StdinStatusResponse
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Status != "processed" {
		t.Errorf("Status mismatch")
	}
}

func TestErrorResponse(t *testing.T) {
	resp := ErrorResponse{Error: "something went wrong"}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded ErrorResponse
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Error != "something went wrong" {
		t.Errorf("Error message mismatch")
	}
}

func TestRegisterClientRequest(t *testing.T) {
	req := RegisterClientRequest{}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded RegisterClientRequest
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
}

func TestRegisterClientResponse(t *testing.T) {
	resp := RegisterClientResponse{
		ClientID: "client-999",
		Status:   ApprovalPending,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded RegisterClientResponse
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ClientID != "client-999" {
		t.Errorf("ClientID mismatch")
	}
	if decoded.Status != ApprovalPending {
		t.Errorf("Status mismatch")
	}
}

func TestApproveClientRequest(t *testing.T) {
	tests := []struct {
		name string
		req  ApproveClientRequest
	}{
		{"WithPermission", ApproveClientRequest{Permission: PermissionReadWrite}},
		{"WithoutPermission", ApproveClientRequest{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.req)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			var decoded ApproveClientRequest
			err = json.Unmarshal(data, &decoded)
			if err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			if decoded.Permission != tt.req.Permission {
				t.Errorf("Permission mismatch")
			}
		})
	}
}

// ============================================================================
// WSMessageType Tests
// ============================================================================

func TestWSMessageTypeConstants(t *testing.T) {
	tests := []struct {
		name     string
		msgType  WSMessageType
		expected uint8
	}{
		{"WSMessageUnknown", WSMessageUnknown, 0},
		{"WSMessageOutput", WSMessageOutput, 10},
		{"WSMessageStdin", WSMessageStdin, 20},
		{"WSMessagePendingClient", WSMessagePendingClient, 30},
		{"WSMessageError", WSMessageError, 40},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if uint8(tt.msgType) != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, uint8(tt.msgType))
			}
		})
	}
}

func TestGetWSMessageTypeValid(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected WSMessageType
	}{
		{"Output", 10, WSMessageOutput},
		{"Stdin", 20, WSMessageStdin},
		{"PendingClient", 30, WSMessagePendingClient},
		{"Error", 40, WSMessageError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetWSMessageType(tt.input)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestGetWSMessageTypeInvalid(t *testing.T) {
	tests := []struct {
		name  string
		input int
	}{
		{"Zero", 0},
		{"One", 1},
		{"Five", 5},
		{"Fifteen", 15},
		{"TwentyFive", 25},
		{"ThirtyFive", 35},
		{"FortyFive", 45},
		{"Hundred", 100},
		{"Negative", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetWSMessageType(tt.input)
			if result != WSMessageUnknown {
				t.Errorf("expected WSMessageUnknown for input %d, got %d", tt.input, result)
			}
		})
	}
}

func TestGetWSMessageTypeEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected WSMessageType
	}{
		{"Zero", 0, WSMessageUnknown},
		{"MaxUint8", 255, WSMessageUnknown},
		{"Negative", -1, WSMessageUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetWSMessageType(tt.input)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

// ============================================================================
// WSMessage Tests
// ============================================================================

func TestWSMessageCreation(t *testing.T) {
	msg := WSMessage{
		Type:    WSMessageOutput,
		Message: "test message",
	}

	if msg.Type != WSMessageOutput {
		t.Errorf("Type mismatch")
	}
	if msg.Message != "test message" {
		t.Errorf("Message mismatch")
	}
}

func TestWSMessageJSONMarshaling(t *testing.T) {
	msg := WSMessage{
		Type:    WSMessageStdin,
		Message: "stdin data",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded WSMessage
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Type != WSMessageStdin {
		t.Errorf("Type mismatch")
	}
	if decoded.Message != "stdin data" {
		t.Errorf("Message mismatch")
	}
}

func TestWSMessageWithOutputChunk(t *testing.T) {
	chunk := OutputChunk{
		Stream: StreamStdout,
		Data:   []byte("chunk data"),
	}

	msg := WSMessage{
		Type:    WSMessageOutput,
		Message: chunk,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded WSMessage
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Type != WSMessageOutput {
		t.Errorf("Type mismatch")
	}

	// Message will be decoded as a string (base64) due to OutputChunk's custom marshaling
	// This is expected behavior - the actual unmarshaling would need type-specific handling
	if decoded.Message == nil {
		t.Error("Message should not be nil")
	}
}

func TestWSMessageWithStdinEntry(t *testing.T) {
	entry := StdinEntry{
		Data: []byte("stdin input"),
	}

	msg := WSMessage{
		Type:    WSMessageStdin,
		Message: entry,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded WSMessage
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Type != WSMessageStdin {
		t.Errorf("Type mismatch")
	}
}

func TestWSMessageWithDifferentTypes(t *testing.T) {
	tests := []struct {
		name    string
		msgType WSMessageType
		message interface{}
	}{
		{"String", WSMessagePendingClient, "client-id-123"},
		{"Number", WSMessageError, 404},
		{"Struct", WSMessageOutput, OutputChunk{Stream: StreamStderr, Data: []byte("error")}},
		{"Nil", WSMessageUnknown, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := WSMessage{
				Type:    tt.msgType,
				Message: tt.message,
			}

			data, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			var decoded WSMessage
			err = json.Unmarshal(data, &decoded)
			if err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}

			if decoded.Type != tt.msgType {
				t.Errorf("Type mismatch: expected %d, got %d", tt.msgType, decoded.Type)
			}
		})
	}
}

// ============================================================================
// StdinEntry Tests
// ============================================================================

func TestStdinEntryCreation(t *testing.T) {
	entry := StdinEntry{
		Data: []byte("test stdin"),
	}

	if string(entry.Data) != "test stdin" {
		t.Errorf("Data mismatch")
	}
}

func TestStdinEntryJSONMarshaling(t *testing.T) {
	entry := StdinEntry{
		Data: []byte("stdin content"),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded StdinEntry
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if string(decoded.Data) != "stdin content" {
		t.Errorf("Data mismatch")
	}
}
