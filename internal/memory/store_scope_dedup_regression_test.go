package memory

import (
	"path/filepath"
	"testing"
)

func TestProjectMemoryDoesNotDeduplicateIntoSessionScope(t *testing.T) {
	configDir := t.TempDir()
	projectPath := filepath.Join(t.TempDir(), "project")

	store, err := NewStore(configDir, projectPath, 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const fact = "Integration tests require REDIS_URL to point at the local fixture"
	session := NewEntry(fact, MemorySession).WithKey("current-investigation")
	if err := store.Add(session); err != nil {
		t.Fatalf("Add(session): %v", err)
	}

	durable, err := store.AddResolved(
		NewEntry(fact, MemoryProject).WithKey("integration-test-redis"),
	)
	if err != nil {
		t.Fatalf("AddResolved(project): %v", err)
	}
	if durable.Type != MemoryProject || durable.ID == session.ID {
		t.Fatalf("project fact collapsed into ephemeral session entry: %#v", durable)
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	reloaded, err := NewStore(configDir, projectPath, 100)
	if err != nil {
		t.Fatalf("NewStore(reload): %v", err)
	}
	got, ok := reloaded.Get("integration-test-redis")
	if !ok {
		t.Fatal("project fact was lost after reload")
	}
	if got.Type != MemoryProject || got.Content != fact {
		t.Fatalf("reloaded project fact = %#v", got)
	}
	if _, ok := reloaded.Get("current-investigation"); ok {
		t.Fatal("session memory was unexpectedly persisted")
	}
}
