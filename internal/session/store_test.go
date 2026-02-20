package session

import (
	"testing"
)

func TestNewStoreMemory(t *testing.T) {
	store, err := NewStore("memory", StoreOptions{})
	if err != nil {
		t.Fatalf("NewStore error: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestNewStoreUnknownType(t *testing.T) {
	_, err := NewStore("unknown", StoreOptions{})
	if err == nil {
		t.Fatal("expected error for unknown store type")
	}
}

func TestMemoryStoreCreate(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	sess, err := store.Create([]string{"bash", "-c", "echo hello"})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	if sess.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if len(sess.Command) < 1 || sess.Command[0] != "bash" {
		t.Errorf("expected bash command, got %v", sess.Command)
	}
	if sess.Status != StatusActive {
		t.Errorf("expected active status, got %s", sess.Status)
	}
}

func TestMemoryStoreGet(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	created, _ := store.Create([]string{"bash"})
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("IDs don't match: %s vs %s", got.ID, created.ID)
	}
}

func TestMemoryStoreGetNotFound(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	_, err := store.Get("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestMemoryStoreListEmpty(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions initially, got %d", len(sessions))
	}
}

func TestMemoryStoreList(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	store.Create([]string{"bash"})  //nolint:errcheck
	store.Create([]string{"sh"})    //nolint:errcheck

	sessions, err := store.List()
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

func TestMemoryStoreDelete(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	sess, _ := store.Create([]string{"bash"})
	if err := store.Delete(sess.ID); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	_, err := store.Get(sess.ID)
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestMemoryStoreDeleteNotFound(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	err := store.Delete("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent session")
	}
}

func TestMemoryStoreCreateUniqueIDs(t *testing.T) {
	store, _ := NewStore("memory", StoreOptions{})

	sess1, _ := store.Create([]string{"bash"})
	sess2, _ := store.Create([]string{"sh"})

	if sess1.ID == sess2.ID {
		t.Error("expected unique IDs for different sessions")
	}
}
