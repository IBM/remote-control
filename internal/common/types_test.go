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

	// Should be struct format with stream and base64 encoded data
	expected := `{"stream":1,"data":"aGVsbG8gd29ybGQ="}`
	if string(data) != expected {
		t.Errorf("expected %s, got %s", expected, string(data))
	}
}

func TestOutputChunkUnmarshalJSON(t *testing.T) {
	jsonData := []byte(`{"stream":1,"data":"aGVsbG8gd29ybGQ="}`)

	var chunk OutputChunk
	err := json.Unmarshal(jsonData, &chunk)
	if err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	expected := "hello world"
	if string(chunk.Data) != expected {
		t.Errorf("expected %s, got %s", expected, string(chunk.Data))
	}
	if chunk.Stream != StreamStdout {
		t.Errorf("expected stream %d, got %d", StreamStdout, chunk.Stream)
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

func TestPollResponse(t *testing.T) {
	elements := []json.RawMessage{
		json.RawMessage(`"item1"`),
		json.RawMessage(`"item2"`),
		json.RawMessage(`"item3"`),
	}
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

	if len(decoded.Elements) != 3 {
		t.Errorf("expected 3 elements, got %d", len(decoded.Elements))
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

// ============================================================================
// WSMessage Tests
// ============================================================================

func TestWSMessageCreation(t *testing.T) {
	msg := WSMessage{
		Type:    WSMessageOutput,
		Message: json.RawMessage(`"test message"`),
	}

	if msg.Type != WSMessageOutput {
		t.Errorf("Type mismatch")
	}
	if string(msg.Message) != `"test message"` {
		t.Errorf("Message mismatch")
	}
}

func TestWSMessageJSONMarshaling(t *testing.T) {
	msg := WSMessage{
		Type:    WSMessageStdin,
		Message: json.RawMessage(`"stdin data"`),
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
	if string(decoded.Message) != `"stdin data"` {
		t.Errorf("Message mismatch")
	}
}

func TestWSMessageWithOutputChunk(t *testing.T) {
	chunk := OutputChunk{
		Stream: StreamStdout,
		Data:   []byte("chunk data"),
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("Marshal chunk failed: %v", err)
	}

	msg := WSMessage{
		Type:    WSMessageOutput,
		Message: data,
	}

	data, err = json.Marshal(msg)
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

	var decodedChunk OutputChunk
	err = json.Unmarshal(decoded.Message, &decodedChunk)
	if err != nil {
		t.Fatalf("Unmarshal message failed: %v", err)
	}

	if string(decodedChunk.Data) != "chunk data" {
		t.Errorf("Data mismatch")
	}
}

func TestWSMessageWithStdinEntry(t *testing.T) {
	entry := StdinEntry{
		Data: []byte("stdin input"),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal entry failed: %v", err)
	}

	msg := WSMessage{
		Type:    WSMessageStdin,
		Message: data,
	}

	data, err = json.Marshal(msg)
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

	var decodedEntry StdinEntry
	err = json.Unmarshal(decoded.Message, &decodedEntry)
	if err != nil {
		t.Fatalf("Unmarshal message failed: %v", err)
	}

	if string(decodedEntry.Data) != "stdin input" {
		t.Errorf("Data mismatch")
	}
}

func TestWSMessageWithDifferentTypes(t *testing.T) {
	tests := []struct {
		name    string
		msgType WSMessageType
		message json.RawMessage
	}{
		{"String", WSMessagePendingClient, json.RawMessage(`"client-id-123"`)},
		{"Number", WSMessageError, json.RawMessage(`404`)},
		{"Struct", WSMessageOutput, json.RawMessage(`{"stream":2,"data":"ZXJyb3I="}`)},
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

// ============================================================================
// Additional WebSocket Message Tests (Phase 1.4)
// ============================================================================

func TestWSMessageUnmarshalMessageOutputChunk(t *testing.T) {
	chunk := OutputChunk{
		Stream: StreamStderr,
		Data:   []byte("unmarshaled data"),
	}

	data, err := json.Marshal(chunk)
	if err != nil {
		t.Fatalf("Marshal chunk failed: %v", err)
	}

	msg := WSMessage{
		Type:    WSMessageOutput,
		Message: data,
	}

	var unmarshaled OutputChunk
	err = msg.UnmarshalMessage(&unmarshaled)
	if err != nil {
		t.Fatalf("UnmarshalMessage failed: %v", err)
	}

	if unmarshaled.Stream != StreamStderr {
		t.Errorf("Stream mismatch")
	}
	if string(unmarshaled.Data) != "unmarshaled data" {
		t.Errorf("Data mismatch")
	}
}

func TestWSMessageUnmarshalMessageStdinEntry(t *testing.T) {
	entry := StdinEntry{
		Data: []byte("stdin unmarshaled"),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal entry failed: %v", err)
	}

	msg := WSMessage{
		Type:    WSMessageStdin,
		Message: data,
	}

	var unmarshaled StdinEntry
	err = msg.UnmarshalMessage(&unmarshaled)
	if err != nil {
		t.Fatalf("UnmarshalMessage failed: %v", err)
	}

	if string(unmarshaled.Data) != "stdin unmarshaled" {
		t.Errorf("Data mismatch")
	}
}

func TestWSMessageUnmarshalMessageError(t *testing.T) {
	errResp := ErrorResponse{Error: "test error"}

	data, err := json.Marshal(errResp)
	if err != nil {
		t.Fatalf("Marshal error failed: %v", err)
	}

	msg := WSMessage{
		Type:    WSMessageError,
		Message: data,
	}

	var unmarshaled ErrorResponse
	err = msg.UnmarshalMessage(&unmarshaled)
	if err != nil {
		t.Fatalf("UnmarshalMessage failed: %v", err)
	}

	if unmarshaled.Error != "test error" {
		t.Errorf("Error mismatch")
	}
}

func TestWSMessageUnmarshalMessageInvalidType(t *testing.T) {
	msg := WSMessage{
		Type:    WSMessageUnknown,
		Message: json.RawMessage(`"unknown"`),
	}

	var output OutputChunk
	err := msg.UnmarshalMessage(&output)
	if err != nil {
		return
	}
}

func TestGetWSMessageTypeAdditionalInvalid(t *testing.T) {
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

// =========================================================================================
// Output/Stdin Round-trip Edge Cases (Phase 1.4)
// =========================================================================================

func TestOutputChunkMarshalUnmarshalRoundTripWithBinaryData(t *testing.T) {
	binaryData := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0xfd}

	original := OutputChunk{
		Stream: StreamStdout,
		Data:   binaryData,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded OutputChunk
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Data) != len(original.Data) {
		t.Errorf("Data length mismatch")
	}
	for i := range original.Data {
		if decoded.Data[i] != original.Data[i] {
			t.Errorf("Data mismatch at index %d", i)
		}
	}
}

func TestOutputChunkMarshalUnmarshalRoundTripWithNewlines(t *testing.T) {
	newlineData := []byte("line1\nline2\r\nline3\r\n")

	original := OutputChunk{
		Stream: StreamStderr,
		Data:   newlineData,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded OutputChunk
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if string(decoded.Data) != string(original.Data) {
		t.Errorf("Data mismatch")
	}
}

func TestOutputChunkUnmarshalInvalidBase64(t *testing.T) {
	jsonData := []byte(`{"stream":1,"data":"not-valid-base64!@#$"}`)

	var chunk OutputChunk
	err := json.Unmarshal(jsonData, &chunk)
	if err == nil {
		t.Error("expected error for invalid base64, got nil")
	}
}

func TestStdinEntryMarshalUnmarshalRoundTripWithEscapeSequences(t *testing.T) {
	escapeData := []byte("tab:\there\nnewline\ttabs\tand\tmore")

	original := StdinEntry{
		Data: escapeData,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded StdinEntry
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if string(decoded.Data) != string(original.Data) {
		t.Errorf("Data mismatch: expected %q, got %q", original.Data, decoded.Data)
	}
}

func TestStdinEntryMarshalUnmarshalRoundTripWithUnicode(t *testing.T) {
	unicodeData := []byte("Hello \u4e16界 \u00e9\u00e8\u00e2 \U0001F600")

	original := StdinEntry{
		Data: unicodeData,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded StdinEntry
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if string(decoded.Data) != string(original.Data) {
		t.Errorf("Data mismatch")
	}
}

func TestStdinEntryMarshalUnmarshalRoundTripLargeData(t *testing.T) {
	largeData := make([]byte, 10*1024+500)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	original := StdinEntry{
		Data: largeData,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded StdinEntry
	err = json.Unmarshal(data, &decoded)
	if err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if len(decoded.Data) != len(original.Data) {
		t.Errorf("Data length mismatch: expected %d, got %d", len(original.Data), len(decoded.Data))
	}
}

// ============================================================================
// Enum Boundary Tests (Phase 1.4)
// ============================================================================

func TestWSMessageTypeBounds(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  WSMessageType
	}{
		{"MinBound", 0, WSMessageUnknown},
		{"LowerValid", 10, WSMessageOutput},
		{"UpperValid", 40, WSMessageError},
		{"MaxUint8", 255, WSMessageUnknown},
		{"OutOfBounds", 256, WSMessageUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetWSMessageType(tt.value)
			if result != tt.want {
				t.Errorf("GetWSMessageType(%d) = %d, want %d", tt.value, result, tt.want)
			}
		})
	}
}

func TestStreamUnknownIsZero(t *testing.T) {
	var unknown Stream
	if unknown != StreamUnknown {
		t.Errorf("zero value of Stream should be StreamUnknown")
	}

	unknownMsg := WSMessage{
		Type:    0,
		Message: json.RawMessage(`"test"`),
	}

	if unknownMsg.Type != WSMessageUnknown {
		t.Errorf("zero value of WSMessageType should be WSMessageUnknown")
	}
}

func TestPermissionReadWriteEnumValues(t *testing.T) {
	if string(PermissionReadOnly) != "read-only" {
		t.Errorf("PermissionReadOnly = %q, want %q", PermissionReadOnly, "read-only")
	}
	if string(PermissionReadWrite) != "read-write" {
		t.Errorf("PermissionReadWrite = %q, want %q", PermissionReadWrite, "read-write")
	}
}
