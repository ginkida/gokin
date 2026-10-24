package context

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkingMemoryClearSerializesWithInFlightDiskWrite(t *testing.T) {
	manager := NewWorkingMemoryManager(t.TempDir())
	writerPaused := make(chan struct{})
	releaseWriter := make(chan struct{})
	var once sync.Once
	manager.beforeDiskWriteForTest = func() {
		once.Do(func() { close(writerPaused) })
		<-releaseWriter
	}

	writerDone := make(chan struct{})
	go func() {
		manager.UpdateFromTurn(WorkingMemoryTurn{Response: "persist this working state"})
		close(writerDone)
	}()
	<-writerPaused

	clearDone := make(chan struct{})
	go func() {
		manager.Clear()
		close(clearDone)
	}()
	select {
	case <-clearDone:
		close(releaseWriter)
		<-writerDone
		t.Fatal("Clear completed while a working-memory writer was paused")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseWriter)
	<-writerDone
	<-clearDone
	if got := manager.GetContent(); got != "" {
		t.Fatalf("working memory survived Clear: %q", got)
	}
	if _, err := os.Stat(manager.filePath()); !os.IsNotExist(err) {
		t.Fatalf("working memory file survived Clear: %v", err)
	}
}

func TestWorkingMemoryConcurrentUpdatesPersistInSerializedOrder(t *testing.T) {
	manager := NewWorkingMemoryManager(t.TempDir())
	firstPaused := make(chan struct{})
	releaseFirst := make(chan struct{})
	var once sync.Once
	manager.beforeDiskWriteForTest = func() {
		once.Do(func() {
			close(firstPaused)
			<-releaseFirst
		})
	}

	firstDone := make(chan struct{})
	go func() {
		manager.UpdateFromTurn(WorkingMemoryTurn{Response: "first durable result"})
		close(firstDone)
	}()
	<-firstPaused
	secondDone := make(chan struct{})
	go func() {
		manager.UpdateFromTurn(WorkingMemoryTurn{Response: "second durable result"})
		close(secondDone)
	}()

	close(releaseFirst)
	<-firstDone
	<-secondDone
	data, err := os.ReadFile(manager.filePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "second durable result") {
		t.Fatalf("latest update was not persisted: %s", data)
	}
}

func TestWorkingMemoryDiskWriteHookIsManagerScoped(t *testing.T) {
	first := NewWorkingMemoryManager(t.TempDir())
	second := NewWorkingMemoryManager(t.TempDir())
	calls := 0
	first.beforeDiskWriteForTest = func() { calls++ }
	first.UpdateFromTurn(WorkingMemoryTurn{Response: "first result"})
	second.UpdateFromTurn(WorkingMemoryTurn{Response: "second result"})
	if calls != 1 {
		t.Fatalf("another manager invoked the disk-write seam: calls=%d", calls)
	}
}
