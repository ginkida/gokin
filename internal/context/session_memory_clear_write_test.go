package context

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestSessionMemoryClearSerializesWithInFlightDiskWrite(t *testing.T) {
	sm := NewSessionMemoryManager(t.TempDir(), DefaultSessionMemoryConfig())
	sm.mu.Lock()
	sm.content = "stale session content"
	sm.contentGeneration++
	sm.mu.Unlock()

	writerPaused := make(chan struct{})
	releaseWriter := make(chan struct{})
	var once sync.Once
	sessionMemoryBeforeDiskWriteForTest = func() {
		once.Do(func() { close(writerPaused) })
		<-releaseWriter
	}
	defer func() { sessionMemoryBeforeDiskWriteForTest = nil }()

	writerDone := make(chan struct{})
	go func() {
		sm.writeToDisk()
		close(writerDone)
	}()
	<-writerPaused

	clearStarted := make(chan struct{})
	clearDone := make(chan struct{})
	go func() {
		close(clearStarted)
		sm.Clear()
		close(clearDone)
	}()
	<-clearStarted

	select {
	case <-clearDone:
		close(releaseWriter)
		<-writerDone
		t.Fatal("Clear completed while an older disk writer was still paused")
	case <-time.After(50 * time.Millisecond):
		// Expected: Clear waits, then removes the file after the writer exits.
	}

	close(releaseWriter)
	<-writerDone
	<-clearDone

	// Model a writer that was scheduled before Clear but did not enter the
	// serialized disk phase until afterwards. It sees the cleared snapshot and
	// must not recreate even an empty session-memory file.
	sm.writeToDisk()
	if _, err := os.Stat(sm.filePath()); !os.IsNotExist(err) {
		t.Fatalf("stale writer recreated session memory after Clear: err=%v", err)
	}
	if got := sm.GetContent(); got != "" {
		t.Fatalf("session content survived Clear: %q", got)
	}
}
