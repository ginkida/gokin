package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQueueManager_PreservesMetadataOrderAndSnapshotOwnership(t *testing.T) {
	qm := NewQueueManager(3)
	type contextKey string
	ctx := context.WithValue(t.Context(), contextKey("key"), "value")
	for _, input := range []struct {
		message  string
		priority Priority
	}{
		{message: "low", priority: PriorityLow},
		{message: "normal", priority: PriorityNormal},
		{message: "high", priority: PriorityHigh},
	} {
		if _, err := qm.Enqueue(ctx, input.message, input.priority, func(context.Context) error { return nil }, nil); err != nil {
			t.Fatalf("Enqueue %s: %v", input.message, err)
		}
	}

	tasks := qm.ListTasks()
	if len(tasks) != 3 || tasks[0].Message != "high" || tasks[1].Message != "normal" || tasks[2].Message != "low" {
		t.Fatalf("priority order or messages lost: %+v", tasks)
	}
	if tasks[0].CreatedAt.IsZero() || tasks[0].Context.Value(contextKey("key")) != "value" {
		t.Fatalf("task metadata not preserved: %+v", tasks[0])
	}
	id := tasks[0].ID
	tasks[0].Message = "caller mutation"
	tasks[0].Priority = PriorityLow
	fresh, ok := qm.GetTask(id)
	if !ok || fresh.Message != "high" || fresh.Priority != PriorityHigh {
		t.Fatalf("queue state aliased through ListTasks snapshot: %+v", fresh)
	}
	peek := qm.Peek()
	peek.Message = "peek mutation"
	if next := qm.Peek(); next.Message != "high" {
		t.Fatalf("queue state aliased through Peek snapshot: %+v", next)
	}
}

func TestQueueManager_HighPriorityPreemptsNewestLowestPriority(t *testing.T) {
	qm := NewQueueManager(3)
	evicted := make(chan error, 1)
	lowOldID, err := qm.Enqueue(t.Context(), "low-old", PriorityLow, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatalf("Enqueue low-old: %v", err)
	}
	lowNewID, err := qm.Enqueue(t.Context(), "low-new", PriorityLow, func(context.Context) error { return nil }, func(err error) {
		// Re-entering the queue would deadlock if eviction callbacks ran under qm.mu.
		_ = qm.Len()
		evicted <- err
	})
	if err != nil {
		t.Fatalf("Enqueue low-new: %v", err)
	}
	if _, err := qm.Enqueue(t.Context(), "normal", PriorityNormal, func(context.Context) error { return nil }, nil); err != nil {
		t.Fatalf("Enqueue normal: %v", err)
	}
	if _, err := qm.Enqueue(t.Context(), "high", PriorityHigh, func(context.Context) error { return nil }, nil); err != nil {
		t.Fatalf("Enqueue high: %v", err)
	}

	select {
	case err := <-evicted:
		if err == nil || !strings.Contains(err.Error(), "preempted") {
			t.Fatalf("eviction callback error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("evicted task did not receive completion callback")
	}
	if _, exists := qm.GetTask(lowNewID); exists {
		t.Fatal("newest low-priority task was not evicted")
	}
	if _, exists := qm.GetTask(lowOldID); !exists {
		t.Fatal("older low-priority task was evicted instead of newer peer")
	}
	if stats := qm.GetStats(); stats.Length != 3 || stats.TotalDropped != 1 {
		t.Fatalf("wrong stats after preemption: %+v", stats)
	}
}

func TestQueueManager_FullHighPriorityQueueRejectsWithoutEviction(t *testing.T) {
	qm := NewQueueManager(1)
	firstID, err := qm.Enqueue(t.Context(), "first", PriorityHigh, func(context.Context) error { return nil }, nil)
	if err != nil {
		t.Fatalf("Enqueue first: %v", err)
	}
	if _, err := qm.Enqueue(t.Context(), "second", PriorityHigh, func(context.Context) error { return nil }, nil); err == nil {
		t.Fatal("full high-priority queue accepted task without a preemptible item")
	}
	if task, exists := qm.GetTask(firstID); !exists || task.Message != "first" {
		t.Fatalf("existing high-priority task was displaced: %+v", task)
	}
}

func TestQueueManager_ProcessQueueRecoversPanicsAndContinues(t *testing.T) {
	qm := NewQueueManager(4)
	qm.SetCallbacks(
		func(task *QueueTask) {
			task.ID = "callback mutation"
			panic("start callback")
		},
		func(task *QueueTask, err error) {
			if task.ID == "callback mutation" {
				t.Error("completion callback received aliased start snapshot")
			}
			panic("completion callback")
		},
	)
	panicResult := make(chan error, 1)
	if _, err := qm.Enqueue(t.Context(), "panics", PriorityHigh, func(context.Context) error {
		panic("execute failure")
	}, func(err error) {
		panicResult <- err
		panic("task callback")
	}); err != nil {
		t.Fatalf("Enqueue panics: %v", err)
	}
	secondRan := make(chan struct{}, 1)
	if _, err := qm.Enqueue(t.Context(), "second", PriorityNormal, func(context.Context) error {
		secondRan <- struct{}{}
		return nil
	}, nil); err != nil {
		t.Fatalf("Enqueue second: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go qm.ProcessQueue(ctx)
	select {
	case err := <-panicResult:
		if err == nil || !strings.Contains(err.Error(), "panicked") {
			t.Fatalf("panic result = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("panicking task did not complete")
	}
	select {
	case <-secondRan:
	case <-time.After(2 * time.Second):
		t.Fatal("queue did not continue after task/callback panics")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && qm.GetStats().TotalProcessed != 2 {
		time.Sleep(time.Millisecond)
	}
	if qm.IsProcessing() || qm.GetCurrentTask() != nil || qm.GetStats().TotalProcessed != 2 {
		t.Fatalf("processing lifecycle not restored: current=%+v stats=%+v", qm.GetCurrentTask(), qm.GetStats())
	}
}

func TestQueueManager_QueuedTaskHonorsTaskAndProcessorCancellation(t *testing.T) {
	t.Run("task context already cancelled", func(t *testing.T) {
		qm := NewQueueManager(1)
		taskCtx, cancelTask := context.WithCancel(t.Context())
		cancelTask()
		ran := false
		completed := make(chan error, 1)
		if _, err := qm.Enqueue(taskCtx, "cancelled", PriorityNormal, func(context.Context) error {
			ran = true
			return nil
		}, func(err error) { completed <- err }); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		processCtx, cancelProcess := context.WithCancel(t.Context())
		defer cancelProcess()
		go qm.ProcessQueue(processCtx)
		select {
		case err := <-completed:
			if !errors.Is(err, context.Canceled) || ran {
				t.Fatalf("completion err=%v ran=%v", err, ran)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled queued task did not complete")
		}
	})

	t.Run("processor context cancels running task", func(t *testing.T) {
		qm := NewQueueManager(1)
		started := make(chan struct{})
		completed := make(chan error, 1)
		if _, err := qm.Enqueue(t.Context(), "running", PriorityNormal, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}, func(err error) { completed <- err }); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		processCtx, cancelProcess := context.WithCancel(t.Context())
		go qm.ProcessQueue(processCtx)
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("task did not start")
		}
		cancelProcess()
		select {
		case err := <-completed:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("completion error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("processor cancellation did not reach task")
		}
	})
}

func TestQueueManager_SerializesProcessorsAndTransfersOwnership(t *testing.T) {
	qm := NewQueueManager(2)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	if _, err := qm.Enqueue(t.Context(), "first", PriorityHigh, func(context.Context) error {
		close(firstStarted)
		<-releaseFirst
		return nil
	}, nil); err != nil {
		t.Fatalf("Enqueue first: %v", err)
	}
	if _, err := qm.Enqueue(t.Context(), "second", PriorityNormal, func(context.Context) error {
		secondStarted <- struct{}{}
		return nil
	}, nil); err != nil {
		t.Fatalf("Enqueue second: %v", err)
	}

	firstCtx, cancelFirst := context.WithCancel(t.Context())
	go qm.ProcessQueue(firstCtx)
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first processor did not start task")
	}
	secondCtx, cancelSecond := context.WithCancel(t.Context())
	defer cancelSecond()
	go qm.ProcessQueue(secondCtx)
	select {
	case <-secondStarted:
		t.Fatal("second processor ran concurrently with current task")
	case <-time.After(50 * time.Millisecond):
	}

	// The waiting processor must take ownership after the first context ends,
	// rather than returning early and leaving queued work stranded.
	cancelFirst()
	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("waiting processor did not take ownership")
	}
}

func TestQueueManager_IdleProcessorWakesOnEnqueue(t *testing.T) {
	qm := NewQueueManager(1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go qm.ProcessQueue(ctx)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && len(qm.processorToken) != 0 {
		time.Sleep(time.Millisecond)
	}
	if len(qm.processorToken) != 0 {
		t.Fatal("processor did not acquire ownership token")
	}

	ran := make(chan struct{}, 1)
	if _, err := qm.Enqueue(t.Context(), "wake", PriorityNormal, func(context.Context) error {
		ran <- struct{}{}
		return nil
	}, nil); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("idle processor did not wake after enqueue")
	}
}
