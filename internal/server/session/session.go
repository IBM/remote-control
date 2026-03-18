package session

import (
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var sessCh = alog.UseChannel("SESSION")

type SessionClient struct {
	Info types.ClientInfo

	mu    sync.RWMutex
	conn  *Connection
	msgQs map[types.WSMessageType][]interface{}
}

// Get the connection's send channel
func (c *SessionClient) GetSendChan() chan []byte {
	return c.conn.send
}

// Get the connection's done channel
func (c *SessionClient) GetDoneChan() chan struct{} {
	return c.conn.done
}

// Queue an output chunk and if possible send it to the client
func (c *SessionClient) Send(mType types.WSMessageType, message interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Get the right queue
	q, ok := c.msgQs[mType]
	if !ok {
		q = make([]interface{}, 0)
	}

	// Add to the queue
	q = append(q, message)

	// Attempt to send to the connection and clear the queue if successful
	if nil == c.conn.SendMessage(mType, q) {
		q = make([]interface{}, 0)
	}
	c.msgQs[mType] = q
}

// Get all chunks off the queue and remove them
func (c *SessionClient) PopAllQueue(mType types.WSMessageType) []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()

	q, ok := c.msgQs[types.WSMessageOutput]
	if !ok {
		return make([]interface{}, 0)
	}
	c.msgQs[types.WSMessageOutput] = make([]interface{}, 0)
	return q
}

// Close the underlying connection
func (c *SessionClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn.Close()
}

/* -- Session --------------------------------------------------------------- */

// Session holds all state for a single remote-control session.
type Session struct {
	Info types.SessionInfo

	mu sync.RWMutex

	// buffer for output chunks held for new clients that join
	outputBuffer    []*types.OutputChunk
	maxOutputBuffer int

	// stdin is the ordered queue of all pending stdin entries
	stdin []*types.StdinEntry

	// host connection
	hostConn *Connection

	// client connections
	clients map[string]*SessionClient
}

func newSession(id string, maxOutputBuffer int, hostConn *websocket.Conn) *Session {
	return &Session{
		Info: types.SessionInfo{
			ID:        id,
			Status:    types.SessionStatusActive,
			CreatedAt: time.Now(),
		},
		outputBuffer:    make([]*types.OutputChunk, 0),
		maxOutputBuffer: maxOutputBuffer,
		stdin:           make([]*types.StdinEntry, 0),
		hostConn:        newConnection(hostConn),
		clients:         make(map[string]*SessionClient),
	}
}

// AppendOutput adds a new chunk to the specified stream's buffer.
// The chunk's Offset is set to the current total bytes for that stream.
// timestamp is provided by the caller (host-grounded).
func (s *Session) AppendOutput(stream types.Stream, data []byte) {
	if len(data) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Create the output chunk
	chunk := types.OutputChunk{
		Stream: stream,
		Data:   make([]byte, len(data)),
	}
	copy(chunk.Data, data)

	// Send the chunk to all clients
	// NOTE: No need to send to host since output always comes from host
	var wg sync.WaitGroup
	for clientID, client := range s.clients {
		if client.Info.Approval == types.ApprovalApproved {
			sessCh.Log(alog.DEBUG4, "Sending chunk to %s", clientID)
			wg.Add(1)
			go func() {
				client.Send(types.WSMessageOutput, &chunk)
			}()
		}
	}
	wg.Wait()

	// Add to the outputBuffer and truncate if needed
	s.outputBuffer = append(s.outputBuffer, &chunk)
	if s.maxOutputBuffer > 0 && len(s.outputBuffer) > s.maxOutputBuffer {
		trimLen := len(s.outputBuffer) - s.maxOutputBuffer
		sessCh.Log(alog.DEBUG3, "Trimming %d chunks from output buffer", trimLen)
		s.outputBuffer = s.outputBuffer[trimLen:]
	}
}

// EnqueueStdin appends a new stdin entry to the session's STDIN queue.
func (s *Session) EnqueueStdin(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := types.StdinEntry{
		Data: data,
	}
	s.stdin = append(s.stdin, &entry)

	// Attempt to send to the host connection and clear if successful
	if err := s.hostConn.SendMessage(types.WSMessageStdin, s.stdin); nil == err {
		sessCh.Log(alog.DEBUG2, "Stdin sent to host. Purging from queue.")
		s.stdin = make([]*types.StdinEntry, 0)
	}
}

// Complete marks the session as completed with the given exit code.
func (s *Session) Complete(exitCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.Info.Status = types.SessionStatusCompleted
	s.Info.CompletedAt = &now
	s.Info.ExitCode = &exitCode
}

// RegisterClient adds a new client to the session in pending state.
func (s *Session) RegisterClient(conn *websocket.Conn) (string, *SessionClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	clientID := uuid.New().String()
	client := &SessionClient{
		Info: types.ClientInfo{
			ClientID:   clientID,
			JoinedAt:   now,
			Approval:   types.ApprovalPending,
			LastPollAt: now,
		},
		conn:  newConnection(conn),
		msgQs: make(map[types.WSMessageType][]interface{}),
	}
	s.clients[clientID] = client

	// Notify the host of the pending client
	s.hostConn.SendMessage(types.WSMessagePendingClient, clientID)

	return clientID, client
}

// GetClient gets the client if available
func (s *Session) GetClient(clientID string) *SessionClient {
	if client, ok := s.clients[clientID]; ok {
		return client
	}
	return nil
}

// ApproveClient approves a client with the given permission level.
func (s *Session) ApproveClient(clientID string, perm types.Permission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.clients[clientID]
	if !ok {
		return errNotFound(clientID)
	}
	rec.Info.Approval = types.ApprovalApproved
	rec.Info.Permission = perm
	return nil
}

// DenyClient denies a client.
func (s *Session) DenyClient(clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.clients[clientID]
	if !ok {
		return errNotFound(clientID)
	}
	rec.Info.Approval = types.ApprovalDenied
	return nil
}

// ListPendingClients returns all clients in pending approval state.
func (s *Session) ListPendingClients() []*types.ClientInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*types.ClientInfo
	for _, rec := range s.clients {
		if rec.Info.Approval == types.ApprovalPending {
			cp := rec.Info
			result = append(result, &cp)
		}
	}
	return result
}

// ListClients returns all client records.
func (s *Session) ListClients() []*types.ClientInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]*types.ClientInfo, 0, len(s.clients))
	for _, rec := range s.clients {
		cp := rec.Info
		result = append(result, &cp)
	}
	return result
}

// UpdateClientActivity updates the last poll timestamp for a client.
func (s *Session) UpdateClientActivity(clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.clients[clientID]
	if !ok {
		// TODO: Auto re-register this client. Needs the ws connection!
		return errNotFound(clientID)
	}
	rec.Info.LastPollAt = time.Now()
	return nil
}

// RemoveInactiveClients removes clients that haven't polled within the timeout
// period. Returns the list of removed client IDs.
func (s *Session) RemoveInactiveClients(timeout time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var removed []string

	for clientID, client := range s.clients {
		if now.Sub(client.Info.LastPollAt) > timeout {
			client.Close()
			delete(s.clients, clientID)
			removed = append(removed, clientID)
		}
	}

	return removed
}
