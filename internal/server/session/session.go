package session

import (
	"sync"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
	"github.com/IBM/remote-control/internal/common/config"
	"github.com/IBM/remote-control/internal/common/types"
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

	// maxQueueLength bounds how many not-yet-delivered messages Send will hold
	// for this client (per message type). Without a bound, a client whose
	// connection never succeeds (e.g. an abandoned or long-disconnected
	// client) accumulates every message ever sent to the session forever, and
	// every failed Send call re-marshals the entire backlog. Zero or negative
	// means unbounded; see config.Config.MaxClientQueueLength.
	maxQueueLength int
}

func newSessionClient(clientID string, approval types.ApprovalStatus, conn *websocket.Conn, maxQueueLength int) *SessionClient {
	now := time.Now()
	client := &SessionClient{
		Info: types.ClientInfo{
			ClientID:   clientID,
			JoinedAt:   now,
			Approval:   approval,
			LastPollAt: now,
		},
		conn:           newConnection(conn),
		msgQs:          make(map[types.WSMessageType][]queuedMessage),
		maxQueueLength: maxQueueLength,
	}
	return client
}

// Get the connection's send channel
func (c *SessionClient) GetSendChan() chan []byte {
	return c.conn.GetSendChan()
}

// Get the connection's done channel
func (c *SessionClient) GetDoneChan() chan struct{} {
	return c.conn.GetDoneChan()
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

	// Add to the queue, dropping the oldest entries once the queue exceeds
	// its bound so a permanently-failing client can't grow it unboundedly
	q = append(q, queuedMessage{data: message, peeked: false})
	if c.maxQueueLength > 0 && len(q) > c.maxQueueLength {
		q = q[len(q)-c.maxQueueLength:]
	}

	// Build payload from queue data
	payload := make([]interface{}, len(q))
	for i, msg := range q {
		payload[i] = msg.data
	}

	// Attempt to send to the connection and clear the queue if successful
	if nil == c.conn.SendMessage(mType, payload) {
		q = make([]queuedMessage, 0)
	}
	c.msgQs[mType] = q
}

/* -- Session --------------------------------------------------------------- */

// Session holds all state for a single remote-control session.
type Session struct {
	Info types.SessionInfo

	mu sync.RWMutex

	// buffer for output chunks held for new clients that join. maxOutputBuffer
	// bounds the total bytes (sum of chunk data lengths) held in outputBuffer,
	// not the number of chunks.
	outputBuffer      []*types.OutputChunk
	outputBufferBytes int
	maxOutputBuffer   int

	// maxClientQueueLength bounds each client's per-message-type undelivered
	// backlog; see config.Config.MaxClientQueueLength and SessionClient.maxQueueLength.
	maxClientQueueLength int

	// host connection
	hostConn *SessionClient

	// client connections
	clients map[string]*SessionClient

	// whether or not clients need to be approved explicitly
	approvalRequired bool
}

func newSession(id string, hostConn *websocket.Conn, cfg *config.Config, name string) *Session {
	return &Session{
		Info: types.SessionInfo{
			ID:        id,
			Status:    types.SessionStatusActive,
			CreatedAt: time.Now(),
			Name:      name,
		},
		outputBuffer:         make([]*types.OutputChunk, 0),
		maxOutputBuffer:      cfg.MaxOutputBuffer,
		maxClientQueueLength: cfg.MaxClientQueueLength,
		hostConn:             newSessionClient(types.HostClientID, types.ApprovalApproved, hostConn, cfg.MaxClientQueueLength),
		clients:              make(map[string]*SessionClient),
		approvalRequired:     cfg.RequireApproval,
	}
}

// AppendOutput adds a new chunk to the specified stream's buffer.
// The chunk's Offset is set to the current total bytes for that stream.
// timestamp is provided by the caller (host-grounded).
func (s *Session) AppendOutput(stream types.Stream, data []byte) {
	if len(data) == 0 {
		return
	}

	// Create the output chunk
	chunk := types.OutputChunk{
		Stream: stream,
		Data:   make([]byte, len(data)),
	}
	copy(chunk.Data, data)

	s.mu.Lock()

	// Snapshot the approved clients while holding the lock; the actual sends
	// happen outside the lock so a slow/stuck client doesn't serialize the
	// rest of the session.
	approved := make([]*SessionClient, 0, len(s.clients))
	for _, client := range s.clients {
		if client.Info.Approval == types.ApprovalApproved {
			approved = append(approved, client)
		}
	}

	// Add to the outputBuffer and truncate by total bytes if needed
	s.outputBuffer = append(s.outputBuffer, &chunk)
	s.outputBufferBytes += len(chunk.Data)
	sessCh.Log(alog.DEBUG3, "Appended to output buffer. Current length: %d, bytes: %d", len(s.outputBuffer), s.outputBufferBytes)
	if s.maxOutputBuffer > 0 {
		trimmed := 0
		for s.outputBufferBytes > s.maxOutputBuffer && len(s.outputBuffer) > 0 {
			s.outputBufferBytes -= len(s.outputBuffer[0].Data)
			s.outputBuffer = s.outputBuffer[1:]
			trimmed++
		}
		if trimmed > 0 {
			sessCh.Log(alog.DEBUG3, "Trimmed %d chunks from output buffer", trimmed)
		}
	}
	s.mu.Unlock()

	// Send the chunk to all approved clients
	// NOTE: No need to send to host since output always comes from host
	var wg sync.WaitGroup
	for _, client := range approved {
		sessCh.Log(alog.DEBUG4, "Sending chunk to %s", client.Info.ClientID)
		wg.Add(1)
		go func(c *SessionClient) {
			defer wg.Done()
			Send(c, types.WSMessageOutput, &chunk)
		}(client)
	}
	wg.Wait()
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

// RegisterClient adds a new client to the session, or reuses an existing
// client record if clientID matches one already known to the session (e.g. a
// client that pre-registered over HTTP before upgrading to a WebSocket, or a
// client reconnecting after a disconnect). If clientID is HostClientID,
// updates the host connection instead. Approval/permission state is preserved
// across reuse.
func (s *Session) RegisterClient(clientID string, conn *websocket.Conn) (string, *SessionClient) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// If the client identifies itself as the host, update the host connection
	if clientID == types.HostClientID {
		if s.hostConn.conn != nil {
			s.hostConn.conn.Close()
		}
		s.hostConn.conn = newConnection(conn)
		s.hostConn.Info.JoinedAt = time.Now()
		sessCh.Log(alog.DEBUG, "Updated host websocket connection")
		return types.HostClientID, s.hostConn
	}

	// Reuse an existing client record if the caller identified a known client
	if clientID != "" {
		if existing, ok := s.clients[clientID]; ok {
			existing.mu.Lock()
			existing.conn.Reconnect(conn)
			existing.mu.Unlock()
			existing.Info.JoinedAt = time.Now()
			sessCh.Log(alog.DEBUG, "Reusing existing client record for %s", clientID)
			return clientID, existing
		}
	}

	client := uuid.New().String()
	clientRec := newSessionClient(client, types.ApprovalPending, conn, s.maxClientQueueLength)
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
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.hostConn
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	client, ok := s.clients[clientID]
	if !ok {
		return nil
	}
	return client
}

// ApproveClient approves a client with the given permission level.
func (s *Session) ApproveClient(clientID string, perm types.Permission) error {
	s.mu.Lock()
	rec, ok := s.clients[clientID]
	if !ok {
		s.mu.Unlock()
		return errNotFound(clientID)
	}
	rec.Info.Approval = types.ApprovalApproved
	rec.Info.Permission = perm
	// Snapshot the buffer slice; chunks are never mutated after being
	// appended, so replaying against this snapshot outside the lock is safe.
	bufferSnapshot := s.outputBuffer
	s.mu.Unlock()

	// Send the output buffer to the client outside the lock so a slow/stuck
	// client doesn't serialize the rest of the session.
	sessCh.Log(alog.DEBUG3, "Sending queued output buffer to client %s of length %d", clientID, len(bufferSnapshot))
	sessCh.Log(alog.DEBUG4, "%s", bufferSnapshot)
	for _, chunk := range bufferSnapshot {
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
