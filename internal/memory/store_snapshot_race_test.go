package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestDebouncedSaveUsesOwnedImmutableSnapshot(t *testing.T) {
	saveTestSeamMu.Lock()
	origInterval := saveDebounceInterval
	origHook := saveIOHookForTest
	saveDebounceInterval = 10 * time.Millisecond
	saveTestSeamMu.Unlock()
	t.Cleanup(func() {
		saveTestSeamMu.Lock()
		saveDebounceInterval = origInterval
		saveIOHookForTest = origHook
		saveTestSeamMu.Unlock()
	})

	store := newTestStore(t)
	hookEntered := make(chan struct{})
	proceed := make(chan struct{})
	var hookOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(proceed) }) }
	t.Cleanup(release)
	saveTestSeamMu.Lock()
	saveIOHookForTest = func() {
		hookOnce.Do(func() { close(hookEntered) })
		<-proceed
	}
	saveTestSeamMu.Unlock()

	entry := NewEntry("snapshot before edit", MemoryProject).WithKey("snapshot")
	if err := store.Add(entry); err != nil {
		t.Fatalf("Add: %v", err)
	}
	select {
	case <-hookEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("debounced save did not reach snapshot hook")
	}

	// Keep mutations made below dirty without letting their replacement timer
	// fire before the first, paused snapshot has been inspected.
	saveTestSeamMu.Lock()
	saveDebounceInterval = time.Hour
	saveTestSeamMu.Unlock()

	editStarted := make(chan struct{})
	feedbackStarted := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 500 {
			if err := store.Edit(entry.ID, fmt.Sprintf("edited-%d", i)); err != nil {
				return
			}
			if i == 0 {
				close(editStarted)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 500 {
			if !store.RecordFeedback(entry.ID, i%2 == 0) {
				return
			}
			if i == 0 {
				close(feedbackStarted)
			}
		}
	}()
	<-editStarted
	<-feedbackStarted
	release()
	wg.Wait()

	var first []*Entry
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(store.storagePath())
		if err == nil && json.Unmarshal(data, &first) == nil && len(first) == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(first) != 1 {
		t.Fatalf("first debounced snapshot was not persisted: %#v", first)
	}
	if first[0].Content != "snapshot before edit" || first[0].SuccessCount != 0 || first[0].FailureCount != 0 {
		t.Fatalf("in-flight snapshot changed after lock release: %#v", first[0])
	}

	if err := store.Flush(); err != nil {
		t.Fatalf("Flush(latest): %v", err)
	}
	data, err := os.ReadFile(store.storagePath())
	if err != nil {
		t.Fatalf("ReadFile(latest): %v", err)
	}
	var latest []*Entry
	if err := json.Unmarshal(data, &latest); err != nil || len(latest) != 1 {
		t.Fatalf("latest persisted entries = %#v, err=%v", latest, err)
	}
	if latest[0].Content != "edited-499" || latest[0].SuccessCount+latest[0].FailureCount != 500 {
		t.Fatalf("latest mutations were not eventually persisted: %#v", latest[0])
	}
}
