package context

import (
	"strings"
	"testing"
	"time"

	memorystore "gokin/internal/memory"
)

func TestPromptBuilderRefreshesCachedMemoryAfterTTL(t *testing.T) {
	store, err := memorystore.NewStore(t.TempDir(), t.TempDir(), 10)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	project := memorystore.NewEntry("durable prompt fallback", memorystore.MemoryProject).WithKey("prompt-shadow")
	if err := store.Add(project); err != nil {
		t.Fatalf("Add(project): %v", err)
	}
	session := memorystore.NewEntry("temporary prompt override", memorystore.MemorySession).WithKey("prompt-shadow")
	session.ExpiresAt = time.Now().Add(100 * time.Millisecond)
	if err := store.Add(session); err != nil {
		t.Fatalf("Add(session): %v", err)
	}

	builder := NewPromptBuilder(t.TempDir(), &ProjectInfo{})
	builder.SetMemoryStore(store)
	first := builder.Build()
	if !strings.Contains(first, session.Content) || strings.Contains(first, project.Content) {
		t.Fatalf("initial prompt memory shadow = session:%v project:%v", strings.Contains(first, session.Content), strings.Contains(first, project.Content))
	}
	if builder.promptDirty {
		t.Fatal("test did not prime PromptBuilder cache")
	}

	time.Sleep(160 * time.Millisecond)
	second := builder.Build()
	if !strings.Contains(second, project.Content) || strings.Contains(second, session.Content) {
		t.Fatalf("prompt after TTL = session:%v project:%v", strings.Contains(second, session.Content), strings.Contains(second, project.Content))
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}

func TestPromptBuilderRefreshesCachedMemoryAfterStoreRevision(t *testing.T) {
	store, err := memorystore.NewStore(t.TempDir(), t.TempDir(), 10)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	entry := memorystore.NewEntry("memory before revision", memorystore.MemoryProject).WithKey("revision")
	if err := store.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}
	builder := NewPromptBuilder(t.TempDir(), &ProjectInfo{})
	builder.SetMemoryStore(store)
	if first := builder.Build(); !strings.Contains(first, "memory before revision") {
		t.Fatalf("initial prompt did not contain memory: %q", first)
	}
	if err := store.Edit(entry.ID, "memory after revision"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	second := builder.Build()
	if !strings.Contains(second, "memory after revision") || strings.Contains(second, "memory before revision") {
		t.Fatalf("prompt ignored Store revision: %q", second)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
}
