package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/tasks"
)

func TestGracefulShutdownWaitsForBackgroundShellFinalization(t *testing.T) {
	manager := tasks.NewManager(t.TempDir())
	id, err := manager.Start(context.Background(), "sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("started task missing")
	}

	application := &App{
		ctx:         context.Background(),
		taskManager: manager,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	application.gracefulShutdown(ctx)

	select {
	case <-task.Done():
	default:
		t.Fatal("gracefulShutdown returned before background task finalized")
	}
	if got := task.GetStatus(); got != tasks.StatusCancelled {
		t.Fatalf("task status = %s, want cancelled", got)
	}
}
