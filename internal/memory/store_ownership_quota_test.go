package memory

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAddTakesOwnershipOfEntryAndTags(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Flush() })
	input := NewEntry("canonical content", MemoryProject).WithKey("canonical-key").WithTags([]string{"original-tag"})
	id := input.ID
	if err := store.Add(input); err != nil {
		t.Fatalf("Add: %v", err)
	}

	input.Content = "caller changed content"
	input.Key = "caller-key"
	input.Tags[0] = "caller-tag"
	input.Tags = append(input.Tags, "caller-extra")

	got, ok := store.GetByID(id)
	if !ok {
		t.Fatal("stored entry disappeared after caller mutation")
	}
	if got.Content != "canonical content" || got.Key != "canonical-key" {
		t.Fatalf("stored entry followed caller mutation: %#v", got)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "original-tag" {
		t.Fatalf("stored tags followed caller backing array: %v", got.Tags)
	}
}

func TestCallerMutationAfterAddDoesNotRaceStoreReads(t *testing.T) {
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Flush() })
	input := NewEntry("owned content", MemoryProject).WithKey("owned-key").WithTags([]string{"owned-tag"})
	id := input.ID
	if err := store.Add(input); err != nil {
		t.Fatalf("Add: %v", err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				input.Content = fmt.Sprintf("caller-%d", i)
				input.Key = fmt.Sprintf("caller-key-%d", i)
				input.Tags[0] = fmt.Sprintf("caller-tag-%d", i)
			}
		}
	}()
	for range 300 {
		if _, err := store.Export(); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("Export: %v", err)
		}
		_ = store.ListAll()
	}
	close(stop)
	wg.Wait()

	got, ok := store.GetByID(id)
	if !ok || got.Content != "owned content" || got.Key != "owned-key" || len(got.Tags) != 1 || got.Tags[0] != "owned-tag" {
		t.Fatalf("stored entry was not isolated from caller: got=%#v ok=%v", got, ok)
	}
}

func TestMaxEntriesIsEnforcedIndependentlyPerScope(t *testing.T) {
	store, err := NewStore(t.TempDir(), t.TempDir(), 2)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Flush() })
	base := time.Now()
	add := func(memType MemoryType, key string, offset time.Duration) {
		t.Helper()
		entry := NewEntry("content for "+key, memType).WithKey(key)
		entry.Timestamp = base.Add(offset)
		if err := store.Add(entry); err != nil {
			t.Fatalf("Add(%s): %v", key, err)
		}
	}

	add(MemoryGlobal, "global-1", time.Millisecond)
	add(MemoryGlobal, "global-2", 2*time.Millisecond)
	add(MemoryProject, "project-1", 3*time.Millisecond)
	add(MemoryProject, "project-2", 4*time.Millisecond)
	add(MemoryProject, "project-3", 5*time.Millisecond)
	for _, key := range []string{"global-1", "global-2", "project-2", "project-3"} {
		if _, ok := store.Get(key); !ok {
			t.Errorf("%s was evicted by another scope", key)
		}
	}
	if _, ok := store.Get("project-1"); ok {
		t.Error("oldest project entry survived its own scope cap")
	}

	add(MemorySession, "session-1", 6*time.Millisecond)
	add(MemorySession, "session-2", 7*time.Millisecond)
	add(MemorySession, "session-3", 8*time.Millisecond)
	for _, key := range []string{"global-1", "global-2", "project-2", "project-3", "session-2", "session-3"} {
		if _, ok := store.Get(key); !ok {
			t.Errorf("%s was evicted by another lifecycle scope", key)
		}
	}
	if _, ok := store.Get("session-1"); ok {
		t.Error("oldest session entry survived its own scope cap")
	}

	second, err := NewStore(t.TempDir(), t.TempDir(), 2)
	if err != nil {
		t.Fatalf("NewStore(second): %v", err)
	}
	t.Cleanup(func() { _ = second.Flush() })
	projectFacts := []string{"postgres transaction isolation", "frontend accessibility palette"}
	for i, fact := range projectFacts {
		if err := second.Add(NewEntry(fact, MemoryProject).WithKey(fmt.Sprintf("p-%d", i+1))); err != nil {
			t.Fatalf("Add(second project): %v", err)
		}
	}
	globalFacts := []string{"prefer concise answers", "timezone Asia Almaty", "editor uses vim keybindings"}
	for i, fact := range globalFacts {
		if err := second.Add(NewEntry(fact, MemoryGlobal).WithKey(fmt.Sprintf("g-%d", i+1))); err != nil {
			t.Fatalf("Add(second global): %v", err)
		}
	}
	for _, key := range []string{"p-1", "p-2", "g-2", "g-3"} {
		if _, ok := second.Get(key); !ok {
			t.Errorf("second store lost %s", key)
		}
	}
}
