package app

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitForOrchestratorStatus(t *testing.T, orchestrator *TaskOrchestrator, taskID string, want OrchestratorTaskStatus) *OrchestratorTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, ok := orchestrator.GetTask(taskID)
		if ok && task.Status == want {
			return task
		}
		time.Sleep(time.Millisecond)
	}
	task, _ := orchestrator.GetTask(taskID)
	t.Fatalf("task %q did not reach status %d; latest=%+v", taskID, want, task)
	return nil
}

func TestTaskOrchestrator_PanickingTaskTerminatesAndSkipsDependents(t *testing.T) {
	orchestrator := NewTaskOrchestrator(2, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	completionCalled := make(chan error, 1)
	root := &OrchestratorTask{
		ID: "root",
		Execute: func(context.Context) error {
			panic("execution exploded")
		},
		OnComplete: func(err error) {
			completionCalled <- err
			panic("completion observer exploded")
		},
	}
	dependent := &OrchestratorTask{
		ID: "dependent", Dependencies: []string{"root"},
		Execute: func(context.Context) error {
			t.Error("dependent executed after prerequisite panic")
			return nil
		},
	}
	if err := orchestrator.Submit(root); err != nil {
		t.Fatalf("Submit root: %v", err)
	}
	if err := orchestrator.Submit(dependent); err != nil {
		t.Fatalf("Submit dependent: %v", err)
	}
	go orchestrator.Start(ctx)

	failed := waitForOrchestratorStatus(t, orchestrator, "root", OrchStatusFailed)
	if failed.Error == nil || !strings.Contains(failed.Error.Error(), "panicked") {
		t.Fatalf("panic failure not recorded: %+v", failed)
	}
	waitForOrchestratorStatus(t, orchestrator, "dependent", OrchStatusSkipped)
	select {
	case err := <-completionCalled:
		if err == nil || !strings.Contains(err.Error(), "panicked") {
			t.Fatalf("OnComplete received wrong error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("OnComplete was not called for panicking task")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if orchestrator.GetStats()["active_count"] == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("active_count was not released after task panic")
}

func TestTaskOrchestrator_StatusCallbackIsReentrantAndPanicSafe(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runningObserved := make(chan struct{}, 1)
	orchestrator.SetOnStatusChange(func(taskID string, status OrchestratorTaskStatus) {
		// Re-enter GetTask. skipDependents/CancelTask used to call this while
		// holding o.mu, which self-deadlocked; every status path now shares the
		// same outside-lock, panic-safe invocation boundary.
		if task, ok := orchestrator.GetTask(taskID); ok && task.Status == status && status == OrchStatusRunning {
			runningObserved <- struct{}{}
		}
		panic("observer failure")
	})
	if err := orchestrator.Submit(&OrchestratorTask{ID: "task", Execute: func(context.Context) error { return nil }}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	go orchestrator.Start(ctx)

	select {
	case <-runningObserved:
	case <-time.After(time.Second):
		t.Fatal("status callback could not re-enter orchestrator")
	}
	waitForOrchestratorStatus(t, orchestrator, "task", OrchStatusCompleted)
}

func TestTaskOrchestrator_TaskSnapshotsDoNotAliasState(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	if err := orchestrator.Submit(&OrchestratorTask{ID: "dependency"}); err != nil {
		t.Fatalf("Submit dependency: %v", err)
	}
	original := &OrchestratorTask{ID: "task", Name: "original", Dependencies: []string{"dependency"}}
	if err := orchestrator.Submit(original); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	original.Name = "caller mutation"
	original.Dependencies[0] = "caller mutation"
	snapshot, ok := orchestrator.GetTask("task")
	if !ok {
		t.Fatal("task missing")
	}
	snapshot.Name = "snapshot mutation"
	snapshot.Dependencies[0] = "snapshot mutation"

	fresh, _ := orchestrator.GetTask("task")
	if fresh.Name != "original" || fresh.Dependencies[0] != "dependency" {
		t.Fatalf("task snapshot/input aliases orchestrator state: %+v", fresh)
	}
}

func TestTaskOrchestrator_GetTaskConcurrentMutationIsRaceFree(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	internal := &OrchestratorTask{ID: "task", Name: "initial", Dependencies: []string{"dependency"}}
	orchestrator.tasks[internal.ID] = internal

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			internal.mu.Lock()
			internal.Name = fmt.Sprintf("name-%d", i)
			internal.Dependencies[0] = fmt.Sprintf("dependency-%d", i)
			internal.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			task, ok := orchestrator.GetTask("task")
			if !ok {
				t.Error("task disappeared")
				return
			}
			_ = task.Name
			_ = task.Dependencies[0]
		}
	}()
	wg.Wait()
}

func TestTaskOrchestrator_SubmitRejectsNil(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	if err := orchestrator.Submit(nil); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("Submit(nil) error = %v, want validation error", err)
	}
}

func TestTaskOrchestrator_SubmitGroupIsAtomicAndValidatesGraph(t *testing.T) {
	for _, test := range []struct {
		name  string
		tasks []*OrchestratorTask
		want  string
	}{
		{
			name: "missing dependency",
			tasks: []*OrchestratorTask{
				{ID: "task", Dependencies: []string{"missing"}},
			},
			want: "missing",
		},
		{
			name: "cycle",
			tasks: []*OrchestratorTask{
				{ID: "a", Dependencies: []string{"b"}},
				{ID: "b", Dependencies: []string{"a"}},
			},
			want: "cycle",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			orchestrator := NewTaskOrchestrator(1, time.Second)
			err := orchestrator.SubmitGroup(test.tasks)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("SubmitGroup error = %v, want %q", err, test.want)
			}
			if stats := orchestrator.GetStats(); stats["pending"]+stats["running"]+stats["completed"]+stats["failed"]+stats["skipped"] != 0 {
				t.Fatalf("rejected group partially mutated orchestrator: %+v", stats)
			}
		})
	}

	orchestrator := NewTaskOrchestrator(2, time.Second)
	if err := orchestrator.SubmitGroup([]*OrchestratorTask{
		{ID: "dependent", Dependencies: []string{"root"}}, // forward edge
		{ID: "root"},
	}); err != nil {
		t.Fatalf("valid forward dependency group rejected: %v", err)
	}
	if _, ok := orchestrator.GetTask("dependent"); !ok {
		t.Fatal("valid group was not committed")
	}
}

func TestTaskOrchestrator_WaitIncludesQueuedAndLateDependentWork(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	releaseRoot := make(chan struct{})
	dependentRan := make(chan struct{}, 1)
	if err := orchestrator.SubmitGroup([]*OrchestratorTask{
		{
			ID: "root",
			Execute: func(context.Context) error {
				<-releaseRoot
				return nil
			},
		},
		{
			ID: "dependent", Dependencies: []string{"root"},
			Execute: func(context.Context) error {
				dependentRan <- struct{}{}
				return nil
			},
		},
	}); err != nil {
		t.Fatalf("SubmitGroup: %v", err)
	}

	waitDone := make(chan struct{})
	go func() {
		orchestrator.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
		t.Fatal("Wait returned while root was only queued and Start had not run")
	case <-time.After(30 * time.Millisecond):
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go orchestrator.Start(ctx)
	close(releaseRoot)

	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not include the dependent scheduled by root completion")
	}
	select {
	case <-dependentRan:
	default:
		t.Fatal("Wait returned before dependent execution")
	}
}

func TestTaskOrchestrator_CancelQueuedTaskReleasesWaitExactlyOnce(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	completed := make(chan error, 1)
	if err := orchestrator.Submit(&OrchestratorTask{
		ID: "queued",
		Execute: func(context.Context) error {
			t.Error("cancelled queued task executed")
			return nil
		},
		OnComplete: func(err error) { completed <- err },
	}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if status := waitForOrchestratorStatus(t, orchestrator, "queued", OrchStatusReady).Status; status != OrchStatusReady {
		t.Fatalf("queued status = %d, want ready", status)
	}
	if err := orchestrator.CancelTask("queued"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	waitDone := make(chan struct{})
	go func() {
		orchestrator.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("Wait remained blocked after queued task cancellation")
	}
	select {
	case err := <-completed:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Fatalf("OnComplete cancellation error = %v", err)
		}
	default:
		t.Fatal("queued cancellation did not invoke OnComplete")
	}
	if stats := orchestrator.GetStats(); stats["active_count"] != 0 || stats["skipped"] != 1 {
		t.Fatalf("wrong stats after queued cancellation: %+v", stats)
	}

	// Starting afterward drains the stale channel entry. executeTask must see
	// Skipped and return without a second wg.Done (negative-counter panic).
	ctx, cancel := context.WithCancel(context.Background())
	go orchestrator.Start(ctx)
	time.Sleep(10 * time.Millisecond)
	cancel()
}

func TestTaskOrchestrator_CancelQueuedTaskDoesNotBlockBehindStaleChannelItem(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	secondRan := make(chan struct{}, 1)
	if err := orchestrator.SubmitGroup([]*OrchestratorTask{
		{
			ID:       "queued",
			Priority: OrchPriorityHigh,
			Execute: func(context.Context) error {
				t.Error("cancelled queued task executed")
				return nil
			},
		},
		{
			ID:       "second",
			Priority: OrchPriorityLow,
			Execute: func(context.Context) error {
				secondRan <- struct{}{}
				return nil
			},
		},
	}); err != nil {
		t.Fatalf("SubmitGroup: %v", err)
	}
	waitForOrchestratorStatus(t, orchestrator, "queued", OrchStatusReady)
	waitForOrchestratorStatus(t, orchestrator, "second", OrchStatusPending)

	cancelDone := make(chan error, 1)
	go func() { cancelDone <- orchestrator.CancelTask("queued") }()
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatalf("CancelTask: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("CancelTask blocked while stale queued item occupied taskChan")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go orchestrator.Start(ctx)
	waitDone := make(chan struct{})
	go func() {
		orchestrator.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not include pending task after queued cancellation")
	}
	select {
	case <-secondRan:
	default:
		t.Fatal("pending replacement did not execute after stale item was drained")
	}
}

func TestTaskOrchestrator_SubmitAfterQueuedCancellationDoesNotBlock(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	if err := orchestrator.Submit(&OrchestratorTask{ID: "cancelled"}); err != nil {
		t.Fatalf("Submit cancelled: %v", err)
	}
	if err := orchestrator.CancelTask("cancelled"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	ran := make(chan struct{}, 1)
	submitDone := make(chan error, 1)
	go func() {
		submitDone <- orchestrator.Submit(&OrchestratorTask{
			ID: "replacement",
			Execute: func(context.Context) error {
				ran <- struct{}{}
				return nil
			},
		})
	}()
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("Submit replacement: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit blocked behind cancelled task's stale channel item")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go orchestrator.Start(ctx)
	waitDone := make(chan struct{})
	go func() {
		orchestrator.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement remained pending after stale item was drained")
	}
	select {
	case <-ran:
	default:
		t.Fatal("replacement did not execute")
	}
}

func TestTaskOrchestrator_StartCancellationTerminatesQueuedAndPendingTasks(t *testing.T) {
	orchestrator := NewTaskOrchestrator(2, time.Second)
	var completionMu sync.Mutex
	completionCount := 0
	tasks := make([]*OrchestratorTask, 0, 3)
	for _, id := range []string{"a", "b", "c"} {
		tasks = append(tasks, &OrchestratorTask{
			ID: id,
			Execute: func(context.Context) error {
				t.Error("task executed after Start received an already-cancelled context")
				return nil
			},
			OnComplete: func(err error) {
				if err == nil || !strings.Contains(err.Error(), "orchestrator stopped") {
					t.Errorf("shutdown OnComplete error = %v", err)
				}
				completionMu.Lock()
				completionCount++
				completionMu.Unlock()
			},
		})
	}
	if err := orchestrator.SubmitGroup(tasks); err != nil {
		t.Fatalf("SubmitGroup: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	orchestrator.Start(ctx)

	waitDone := make(chan struct{})
	go func() {
		orchestrator.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("Wait remained blocked after Start cancellation")
	}
	stats := orchestrator.GetStats()
	if stats["skipped"] != len(tasks) || stats["active_count"] != 0 || stats["ready"] != 0 || stats["pending"] != 0 {
		t.Fatalf("wrong stats after Start cancellation: %+v", stats)
	}
	completionMu.Lock()
	gotCompletions := completionCount
	completionMu.Unlock()
	if gotCompletions != len(tasks) {
		t.Fatalf("shutdown completion callbacks = %d, want %d", gotCompletions, len(tasks))
	}
}

func TestTaskOrchestrator_CleanupRetainsPrerequisiteUntilScheduling(t *testing.T) {
	orchestrator := NewTaskOrchestrator(1, time.Second)
	rootCompletedCallback := make(chan struct{})
	releaseCallback := make(chan struct{})
	dependentRan := make(chan struct{}, 1)
	orchestrator.SetOnStatusChange(func(taskID string, status OrchestratorTaskStatus) {
		if taskID == "root" && status == OrchStatusCompleted {
			close(rootCompletedCallback)
			<-releaseCallback
		}
	})
	if err := orchestrator.SubmitGroup([]*OrchestratorTask{
		{ID: "root", Execute: func(context.Context) error { return nil }},
		{
			ID: "dependent", Dependencies: []string{"root"},
			Execute: func(context.Context) error {
				dependentRan <- struct{}{}
				return nil
			},
		},
	}); err != nil {
		t.Fatalf("SubmitGroup: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go orchestrator.Start(ctx)

	select {
	case <-rootCompletedCallback:
	case <-time.After(time.Second):
		t.Fatal("root did not reach completion callback")
	}
	// Root is terminal, but its goroutine cannot run deferred schedule until the
	// callback returns. Old Cleanup deleted it here and stranded dependent.
	if removed := orchestrator.Cleanup(); removed != 0 {
		t.Fatalf("Cleanup removed %d task(s) still required by pending dependent", removed)
	}
	if _, exists := orchestrator.GetTask("root"); !exists {
		t.Fatal("Cleanup removed live prerequisite")
	}
	close(releaseCallback)

	waitDone := make(chan struct{})
	go func() {
		orchestrator.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("dependent remained stranded after prerequisite callback")
	}
	select {
	case <-dependentRan:
	default:
		t.Fatal("dependent did not execute")
	}
	if removed := orchestrator.Cleanup(); removed != 2 {
		t.Fatalf("Cleanup after complete batch removed %d tasks, want 2", removed)
	}
}
