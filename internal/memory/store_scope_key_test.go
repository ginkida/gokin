package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMemoryKeysAreNamespacedByScopeWithDeterministicShadowing(t *testing.T) {
	configDir := t.TempDir()
	projectDir := t.TempDir()
	store, err := NewStore(configDir, projectDir, 100)
	if err != nil {
		t.Fatal(err)
	}

	global, err := store.AddResolved(NewEntry("global verification preference", MemoryGlobal).WithKey("verification"))
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.AddResolved(NewEntry("project verification command", MemoryProject).WithKey("verification"))
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.AddResolved(NewEntry("temporary verification experiment", MemorySession).WithKey("verification"))
	if err != nil {
		t.Fatal(err)
	}

	for _, entry := range []*Entry{global, project, session} {
		if got, ok := store.GetByID(entry.ID); !ok || got.Content != entry.Content {
			t.Fatalf("adding same key in another scope deleted %+v; got=%+v ok=%v", entry, got, ok)
		}
	}
	if got, ok := store.Get("verification"); !ok || got.ID != session.ID {
		t.Fatalf("key lookup = %+v, %v; want session shadow", got, ok)
	}
	assertOnlyVisibleMemory(t, store.GetForContext(false), session.Content, project.Content, global.Content)
	assertOnlyVisibleMemory(t, store.GetRelevantForContext("verification", false, 0), session.Content, project.Content, global.Content)

	if removed := store.ClearSession(); removed != 1 {
		t.Fatalf("ClearSession removed %d entries, want 1", removed)
	}
	assertOnlyVisibleMemory(t, store.GetForContext(false), project.Content, session.Content, global.Content)
	assertOnlyVisibleMemory(t, store.GetRelevantForContext("verification", false, 0), project.Content, session.Content, global.Content)

	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewStore(configDir, projectDir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.GetByID(session.ID); ok {
		t.Fatal("session-scoped memory was persisted")
	}
	if got, ok := reopened.Get("verification"); !ok || got.ID != project.ID {
		t.Fatalf("reopened key lookup = %+v, %v; want project shadow", got, ok)
	}
	if got, ok := reopened.GetByID(global.ID); !ok || got.Content != global.Content {
		t.Fatalf("project shadow physically removed global memory: %+v, %v", got, ok)
	}

	if !reopened.Remove("verification") {
		t.Fatal("failed to remove project shadow by key")
	}
	if got, ok := reopened.Get("verification"); !ok || got.ID != global.ID {
		t.Fatalf("removing project shadow did not reveal global fallback: %+v, %v", got, ok)
	}
	assertOnlyVisibleMemory(t, reopened.GetForContext(false), global.Content, project.Content, session.Content)
}

func TestExpiredSessionKeyRevealsDurableMemory(t *testing.T) {
	store, err := NewStore(t.TempDir(), t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	project := NewEntry("durable auth verification", MemoryProject).WithKey("verification")
	if err := store.Add(project); err != nil {
		t.Fatal(err)
	}
	session := NewEntry("expired auth experiment", MemorySession).WithKey("verification")
	session.ExpiresAt = time.Now().Add(-time.Minute)
	if err := store.Add(session); err != nil {
		t.Fatal(err)
	}

	if got, ok := store.Get("verification"); !ok || got.ID != project.ID {
		t.Fatalf("expired session shadow hid durable memory: got=%+v ok=%v", got, ok)
	}
	assertOnlyVisibleMemory(t, store.GetForContext(false), project.Content, session.Content)
}

func TestImportRebindsProjectScopeAndRejectsNullEntries(t *testing.T) {
	store, err := NewStore(t.TempDir(), t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	foreign := NewEntry("imported verification command", MemoryProject).
		WithKey("imported-verification").
		WithProject("another-project-hash")
	data, err := json.Marshal([]*Entry{foreign})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Import(data); err != nil {
		t.Fatal(err)
	}
	got, ok := store.Get("imported-verification")
	if !ok || got.Project != store.projectHash {
		t.Fatalf("imported project scope = %+v, want destination %q", got, store.projectHash)
	}
	if context := store.GetForContext(false); !strings.Contains(context, foreign.Content) {
		t.Fatalf("rebound import missing from destination context:\n%s", context)
	}

	if err := store.Import([]byte(`[null]`)); err == nil || !strings.Contains(err.Error(), "entry is null") {
		t.Fatalf("null import error = %v", err)
	}
}

func assertOnlyVisibleMemory(t *testing.T, context, want string, hidden ...string) {
	t.Helper()
	if !strings.Contains(context, want) {
		t.Fatalf("visible memory %q missing from context:\n%s", want, context)
	}
	for _, content := range hidden {
		if strings.Contains(context, content) {
			t.Fatalf("shadowed memory %q leaked into context:\n%s", content, context)
		}
	}
}
