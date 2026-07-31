package tasks

import (
	"context"
	"testing"
	"time"
)

func TestManagerWaitAllJoinsCancelledTaskAndClosesOutput(t *testing.T) {
	manager := NewManager(t.TempDir())
	id, err := manager.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("started task missing")
	}

	manager.CancelAll()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.WaitAll(ctx); err != nil {
		t.Fatalf("WaitAll: %v", err)
	}

	select {
	case <-task.Done():
	default:
		t.Fatal("WaitAll returned before task.Done closed")
	}
	if got := task.GetStatus(); got != StatusCancelled {
		t.Fatalf("status = %s, want cancelled", got)
	}
	task.Output.mu.Lock()
	fileStillOpen := task.Output.file != nil
	task.Output.mu.Unlock()
	if fileStillOpen {
		t.Fatal("WaitAll returned before output file closed")
	}
}

func TestManagerWaitAllHonorsContext(t *testing.T) {
	manager := NewManager(t.TempDir())
	task := NewTask("blocked", "never started", t.TempDir())
	manager.mu.Lock()
	manager.tasks[task.ID] = task
	manager.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.WaitAll(ctx); err == nil {
		t.Fatal("WaitAll unexpectedly ignored context deadline")
	}
}
