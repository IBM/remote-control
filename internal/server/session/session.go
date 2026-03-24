package session

import (
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	types "github.com/gabe-l-hart/remote-control/internal/common"
	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var sessCh = alog.UseChannel("SESSION")

type queuedMessage struct {
	data   interface{}
	peeked bool
}

type SessionClient struct {
	Info types.ClientInfo

	mu    sync.RWMutex
	conn  *Connection
	msgQs map[types.WSMessageType][]queuedMessage
}

func newSessionClient(clientID string, approval types.ApprovalStatus, conn *websocket.Conn) *SessionClient {
	now := time.Now()
	client := &SessionClient{
		Info: types.ClientInfo{
			ClientID:   clientID,
			JoinedAt:   now,
			Approval:   approval,
			LastPollAt: now,
		},
		conn:  newConnection(conn),
		msgQs: make(map[types.WSMessageType][]queuedMessage),
	}
	return client
}

// Get the connection's send channel
func (c *SessionClient) GetSendChan() chan []byte {
	return c.conn.send
}

// Get the connection's done channel
func (c *SessionClient) GetDoneChan() chan struct{} {
	return c.conn.done
}

// Get all elements from a queue, marking them as peeked but don't remove them
func (c *SessionClient) GetAllQueue(mType types.WSMessageType) []interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	q, ok := c.msgQs[mType]
	if !ok {
		return make([]interface{}, 0)
	}

	result := make([]interface{}, len(q))
	for i := range q {
		q[i].peeked = true
		result[i] = q[i].data
	}
	return result
}

// Get all elements off the queue and remove them (only removes peeked messages)
func (c *SessionClient) ClearAllQueue(mType types.WSMessageType) {
	c.mu.Lock()
	defer c.mu.Unlock()

	q, ok := c.msgQs[mType]
	if !ok {
		return
	}

	// Only remove peeked messages, keep unpeeked ones
	var remaining []queuedMessage
	for _, mq := range q {
		if !mq.peeked {
			remaining = append(remaining, mq)
		}
	}

	c.msgQs[mType] = remaining
}

// Close the underlying connection
func (c *SessionClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn.Close()
}

// Queue an output chunk and if possible send it to the client
// NOTE: Implemented as a free-function to support generic message type
func Send[T any](c *SessionClient, mType types.WSMessageType, message T) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Get the right queue
	q, ok := c.msgQs[mType]
	if !ok {
		q = make([]queuedMessage, 0)
	}

	// Add to the queue
	q = append(q, queuedMessage{data: message, peeked: false})

	// Build payload from queue data
	payload := make([]interface{}, len(q))
	for i, msg := range q {
		payload[i] = msg.data
	}

	// Attempt to send to the connection and clear the queue if successful
	if nil == SendConnectionMessage(c.conn, mType, payload) {
		q = make([]queuedMessage, 0)
	}
	c.msgQs[mType] = q
}

/* -- Session --------------------------------------------------------------- */

// Session holds all state for a single remote-control session.
type Session struct {
	Info types.SessionInfo

	mu sync.RWMutex

	// buffer for output chunks held for new clients that join
	outputBuffer    []*types.OutputChunk
	maxOutputBuffer int

	// host connection
	hostConn *SessionClient

	// client connections
	clients map[string]*SessionClient

	// whether or not clients need to be approved explicitly
	approvalRequired bool
}

func newSession(id string, hostConn *websocket.Conn, cfg *config.Config) *Session {
	return &Session{
		Info: types.SessionInfo{
			ID:        id,
			Status:    types.SessionStatusActive,
			CreatedAt: time.Now(),
		},
		outputBuffer:     make([]*types.OutputChunk, 0),
		maxOutputBuffer:  cfg.MaxOutputBuffer,
		hostConn:         newSessionClient(types.HostClientID, types.ApprovalApproved, hostConn),
		clients:          make(map[string]*SessionClient),
		approvalRequired: cfg.RequireApproval,
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
				Send(client, types.WSMessageOutput, &chunk)
			}()
		}
	}
	wg.Wait()

	// Add to the outputBuffer and truncate if needed
	s.outputBuffer = append(s.outputBuffer, &chunk)
	sessCh.Log(alog.DEBUG3, "Appended to output buffer. Current length: %d", len(s.outputBuffer))
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

	// Copy data to avoid external mutations
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	entry := types.StdinEntry{
		Data: dataCopy,
	}

	// Send to the host (enqueue if WS not connected)
	Send(s.hostConn, types.WSMessageStdin, entry)
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
// If clientID is HostClientID, updates the host connection instead.
func (s *Session) RegisterClient(clientID string, conn *websocket.Conn) (string, *SessionClient) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If the client identifies itself as the host, update the host connection
	if clientID == types.HostClientID {
		s.hostConn.conn = newConnection(conn)
		s.hostConn.Info.JoinedAt = time.Now()
		sessCh.Log(alog.DEBUG, "Updated host websocket connection")
		return types.HostClientID, s.hostConn
	}

	client := uuid.New().String()
	clientRec := newSessionClient(client, types.ApprovalPending, conn)
	s.clients[client] = clientRec

	// If client approval required, notify the host of the pending client
	if s.approvalRequired {
		sessCh.Log(alog.DEBUG, "Sending approval request to host for client %s", client)
		Send(s.hostConn, types.WSMessagePendingClient, client)
	}

	return client, clientRec
}

// GetClient gets the client if available
func (s *Session) GetClient(clientID string) *SessionClient {
	if clientID == types.HostClientID {
		return s.hostConn
	}
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

	// Send the output buffer to the client
	sessCh.Log(alog.DEBUG3, "Sending queued output buffer to client %s of length %d", clientID, len(s.outputBuffer))
	sessCh.Log(alog.DEBUG4, "%s", s.outputBuffer)
	for _, chunk := range s.outputBuffer {
		Send(rec, types.WSMessageOutput, chunk)
	}

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

// PeekClientQueue peeks at a given message type's queue for a given client
func (s *Session) PeekClientQueue(clientID string, mType types.WSMessageType) []interface{} {
	client := s.GetClient(clientID)
	if nil == client {
		return make([]interface{}, 0)
	}
	return client.GetAllQueue(mType)
}

// ClearClientQueue clears all queued messages of a given type for a given
// client
func (s *Session) ClearClientQueue(clientID string, mType types.WSMessageType) {
	if client := s.GetClient(clientID); nil != client {
		client.ClearAllQueue(mType)
	}
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
