package session

import (
	"fmt"
	"sync"

	"github.com/google/uuid"
)

// StoreOptions holds optional configuration for a Store implementation.
// Future fields: DSN for SQL backends, path for file-backed store, etc.
type StoreOptions struct{}

// Store is the interface for session persistence.
type Store interface {
	// Create creates a new session running the given command.
	Create(command []string) (*Session, error)
	// Get returns the session with the given ID.
	Get(id string) (*Session, error)
	// List returns all sessions.
	List() ([]*Session, error)
	// Delete removes the session with the given ID.
	Delete(id string) error
}

// NewStore creates a Store of the requested type.
// storeType "memory" → MemoryStore; future: "sqlite", "postgres", etc.
func NewStore(storeType string, opts StoreOptions) (Store, error) {
	switch storeType {
	case "memory":
		return &MemoryStore{
			sessions: make(map[string]*Session),
		}, nil
	default:
		return nil, fmt.Errorf("unknown store type: %q", storeType)
	}
}

// MemoryStore is an in-memory implementation of Store.
type MemoryStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// Create creates a new session and stores it in memory.
func (m *MemoryStore) Create(command []string) (*Session, error) {
	id := uuid.New().String()
	sess := newSession(id, command)

	m.mu.Lock()
	m.sessions[id] = sess
	m.mu.Unlock()

	return sess, nil
}

// Get returns the session with the given ID, or an error if not found.
func (m *MemoryStore) Get(id string) (*Session, error) {
	m.mu.RLock()
	sess, ok := m.sessions[id]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("session not found: %s", id)
	}
	return sess, nil
}

// List returns all sessions in the store.
func (m *MemoryStore) List() ([]*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Session, 0, len(m.sessions))
	for _, sess := range m.sessions {
		result = append(result, sess)
	}
	return result, nil
}

// Delete removes the session with the given ID.
func (m *MemoryStore) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[id]; !ok {
		return fmt.Errorf("session not found: %s", id)
	}
	delete(m.sessions, id)
	return nil
}
