package memory

import (
	"strings"
	"testing"
	"time"
)

func TestContextCacheExpiresSessionShadowAndRevealsProject(t *testing.T) {
	store := newTestStore(t)
	project := NewEntry("durable project fallback", MemoryProject).WithKey("build-command")
	if err := store.Add(project); err != nil {
		t.Fatalf("Add(project): %v", err)
	}
	session := NewEntry("temporary session override", MemorySession).WithKey("build-command")
	session.ExpiresAt = time.Now().Add(100 * time.Millisecond)
	if err := store.Add(session); err != nil {
		t.Fatalf("Add(session): %v", err)
	}

	first, revision, validUntil := store.GetForContextSnapshot(true)
	if !strings.Contains(first, session.Content) || strings.Contains(first, project.Content) {
		t.Fatalf("initial shadowed context = %q", first)
	}
	if validUntil.IsZero() {
		t.Fatal("TTL-bearing context snapshot did not expose a validity deadline")
	}

	time.Sleep(160 * time.Millisecond)
	second, nextRevision, _ := store.GetForContextSnapshot(true)
	if nextRevision != revision {
		t.Fatalf("wall-clock expiry unexpectedly changed mutation revision: before=%d after=%d", revision, nextRevision)
	}
	if !strings.Contains(second, project.Content) || strings.Contains(second, session.Content) {
		t.Fatalf("context after session TTL = %q", second)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	finalRevision, _ := store.MemoryContextState(true)
	if finalRevision <= nextRevision {
		t.Fatalf("TTL housekeeping did not advance context revision: before=%d after=%d", nextRevision, finalRevision)
	}
}
