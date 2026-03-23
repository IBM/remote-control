package session

import (
	"fmt"
	"sync"

	"github.com/gabe-l-hart/remote-control/internal/common/config"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Store is an in-memory implementation of Store.
type Store struct {
	mu              sync.RWMutex
	sessions        map[string]*Session
	maxOutputBuffer int
}

// NewStore creates a Store of the requested type.
func NewStore(maxOutputBuffer int) *Store {
	return &Store{
		sessions:        make(map[string]*Session),
		maxOutputBuffer: maxOutputBuffer,
	}
}

// Create creates a new session and stores it in memory.
func (s *Store) Create(id *string, conn *websocket.Conn, cfg *config.Config) (*Session, error) {
	if nil == id {
		newId := uuid.New().String()
		id = &newId
	}
	sess := newSession(*id, conn, cfg)

	s.mu.Lock()
	s.sessions[*id] = sess
	s.mu.Unlock()

	return sess, nil
}

// Get returns the session with the given ID, or an error if not found.
func (s *Store) Get(id string) (*Session, error) {
	s.mu.RLock()
	sess, ok := s.sessions[id]
	s.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	return sess, nil
}

// List returns all sessions in the store.
func (s *Store) List() ([]*Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		result = append(result, sess)
	}
	return result, nil
}

// Delete removes the session with the given ID.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	delete(s.sessions, id)
	return nil
}
