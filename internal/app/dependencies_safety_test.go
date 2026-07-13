package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskDependencies_AddWithInvalidDependencyIsAtomic(t *testing.T) {
	td := NewTaskDependencies()
	err := td.AddTaskWithDependencies(&DependencyTask{ID: "orphan"}, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("AddTaskWithDependencies error = %v, want missing dependency", err)
	}
	if _, exists := td.GetTask("orphan"); exists {
		t.Fatal("invalid AddTaskWithDependencies left a ghost task in the graph")
	}
	if got := td.GetStats().TotalTasks; got != 0 {
		t.Fatalf("task count after rejected add = %d, want 0", got)
	}
}

func TestTaskDependencies_AddTaskValidatesEmbeddedDependenciesAtomically(t *testing.T) {
	td := NewTaskDependencies()
	for _, task := range []*DependencyTask{
		{ID: "missing-edge", Dependencies: []string{"missing"}},
		{ID: "duplicate-edge", Dependencies: []string{"root", "root"}},
	} {
		if task.ID == "duplicate-edge" {
			if err := td.AddTask(&DependencyTask{ID: "root"}); err != nil {
				t.Fatalf("AddTask root: %v", err)
			}
		}
		if err := td.AddTask(task); err == nil {
			t.Fatalf("AddTask %s unexpectedly accepted invalid dependencies", task.ID)
		}
		if _, exists := td.GetTask(task.ID); exists {
			t.Fatalf("rejected task %s remained in graph", task.ID)
		}
	}
	if err := td.AddTask(&DependencyTask{ID: "child", Dependencies: []string{"root"}}); err != nil {
		t.Fatalf("AddTask with valid embedded dependency: %v", err)
	}
	plan, err := td.GetPlan()
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if len(plan.Levels) != 2 || len(plan.Levels[1]) != 1 || plan.Levels[1][0] != "child" {
		t.Fatalf("unexpected plan for embedded dependency: %+v", plan.Levels)
	}
}

func TestTaskDependencies_SnapshotsDoNotAliasCallerOrInternalState(t *testing.T) {
	original := &DependencyTask{
		ID: "task", Message: "original", Dependencies: []string{"input-dependency"},
		Result: map[string]any{"files": []string{"original.go"}},
	}
	td := NewTaskDependencies()
	if err := td.AddTask(&DependencyTask{ID: "input-dependency"}); err != nil {
		t.Fatalf("AddTask dependency: %v", err)
	}
	if err := td.AddTask(original); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	// The graph owns its stored task independently from the caller's input.
	original.Message = "caller mutation"
	original.Dependencies[0] = "caller mutation"
	original.Result.(map[string]any)["files"].([]string)[0] = "caller.go"

	snapshot, exists := td.GetTask("task")
	if !exists {
		t.Fatal("stored task missing")
	}
	snapshot.Message = "snapshot mutation"
	snapshot.Dependencies[0] = "snapshot mutation"
	snapshot.Result.(map[string]any)["files"].([]string)[0] = "snapshot.go"

	fresh, _ := td.GetTask("task")
	if fresh.Message != "original" || fresh.Dependencies[0] != "input-dependency" {
		t.Fatalf("task ownership leaked: %+v", fresh)
	}
	if got := fresh.Result.(map[string]any)["files"].([]string)[0]; got != "original.go" {
		t.Fatalf("nested result ownership leaked: %s", got)
	}
}

func TestTaskDependencies_StatusCallbackIsReentrantAndPanicSafe(t *testing.T) {
	td := NewTaskDependencies()
	if err := td.AddTask(&DependencyTask{ID: "task"}); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	reentered := make(chan TaskStatus, 1)
	td.SetOnStatusChange(func(taskID string, status TaskStatus) {
		// Re-enter the same manager; this deadlocked while callback ran under
		// td.mu. Panic afterward must also stay contained.
		reentered <- td.GetTaskStatus(taskID)
		panic("observer failure")
	})
	done := make(chan struct{})
	go func() {
		td.MarkTaskStatus("task", TaskStatusRunning, nil)
		close(done)
	}()

	select {
	case status := <-reentered:
		if status != TaskStatusRunning {
			t.Fatalf("callback observed status %v, want running", status)
		}
	case <-time.After(time.Second):
		t.Fatal("status callback could not re-enter TaskDependencies")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panic escaped or callback deadlocked MarkTaskStatus")
	}
}

func TestTaskDependencies_SnapshotAndStatsConcurrentMutationAreRaceFree(t *testing.T) {
	td := NewTaskDependencies()
	if err := td.AddTask(&DependencyTask{
		ID: "task", Message: "initial", Result: map[string]any{"counter": 0},
	}); err != nil {
		t.Fatalf("AddTask: %v", err)
	}

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			td.mu.Lock()
			task := td.tasks["task"]
			task.Message = fmt.Sprintf("message-%d", i)
			task.Result.(map[string]any)["counter"] = i
			td.mu.Unlock()
			td.MarkTaskStatus("task", TaskStatus(i%int(TaskStatusSkipped+1)), nil)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			task, ok := td.GetTask("task")
			if !ok {
				t.Error("task disappeared")
				return
			}
			_ = task.Message
			_ = task.Result.(map[string]any)["counter"]
			_ = td.GetStats()
			_, _ = td.GetPlan()
		}
	}()
	wg.Wait()
}

func TestDependencyManager_RejectsNonPositiveParallelism(t *testing.T) {
	dm := NewDependencyManager()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, maxParallel := range []int{0, -1} {
		if err := dm.ExecuteDependencies(ctx, maxParallel); err == nil || !strings.Contains(err.Error(), "greater than zero") {
			t.Fatalf("maxParallel=%d error = %v, want validation error", maxParallel, err)
		}
	}
}

func TestDependencyManager_SkipsFailedBranchTransitively(t *testing.T) {
	dm := NewDependencyManager()
	directRan := make(chan struct{}, 1)
	grandchildRan := make(chan struct{}, 1)
	independentRan := make(chan struct{}, 1)

	if err := dm.AddTask(&DependencyTask{
		ID: "failed-root",
		Execute: func(context.Context) error {
			return fmt.Errorf("root failure")
		},
	}); err != nil {
		t.Fatalf("AddTask failed-root: %v", err)
	}
	if err := dm.AddTaskWithDependencies(&DependencyTask{
		ID: "direct",
		Execute: func(context.Context) error {
			directRan <- struct{}{}
			return nil
		},
	}, []string{"failed-root"}); err != nil {
		t.Fatalf("AddTask direct: %v", err)
	}
	if err := dm.AddTaskWithDependencies(&DependencyTask{
		ID: "grandchild",
		Execute: func(context.Context) error {
			grandchildRan <- struct{}{}
			return nil
		},
	}, []string{"direct"}); err != nil {
		t.Fatalf("AddTask grandchild: %v", err)
	}
	if err := dm.AddTask(&DependencyTask{ID: "independent-root"}); err != nil {
		t.Fatalf("AddTask independent-root: %v", err)
	}
	if err := dm.AddTaskWithDependencies(&DependencyTask{
		ID: "independent-child",
		Execute: func(context.Context) error {
			independentRan <- struct{}{}
			return nil
		},
	}, []string{"independent-root"}); err != nil {
		t.Fatalf("AddTask independent-child: %v", err)
	}

	if err := dm.ExecuteDependencies(t.Context(), 3); err == nil || !strings.Contains(err.Error(), "root failure") {
		t.Fatalf("ExecuteDependencies error = %v, want root failure", err)
	}
	for name, ran := range map[string]<-chan struct{}{
		"direct": directRan, "grandchild": grandchildRan,
	} {
		select {
		case <-ran:
			t.Errorf("%s executed despite failed ancestor", name)
		default:
		}
	}
	select {
	case <-independentRan:
	default:
		t.Fatal("independent branch did not execute")
	}
	for _, id := range []string{"direct", "grandchild"} {
		task, ok := dm.GetTask(id)
		if !ok || task.Status != TaskStatusSkipped || task.Error == nil {
			t.Errorf("task %s after ancestor failure = %+v, want skipped with error", id, task)
		}
	}
}

func TestDependencyManager_DoesNotReexecuteTerminalTasks(t *testing.T) {
	dm := NewDependencyManager()
	var executions atomic.Int32
	if err := dm.AddTask(&DependencyTask{
		ID: "once",
		Execute: func(context.Context) error {
			executions.Add(1)
			return nil
		},
	}); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	for run := 1; run <= 2; run++ {
		if err := dm.ExecuteDependencies(t.Context(), 1); err != nil {
			t.Fatalf("ExecuteDependencies run %d: %v", run, err)
		}
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("terminal task executions = %d, want 1", got)
	}
}

func TestDependencyManager_CancellationReleasesSemaphoreWaiters(t *testing.T) {
	const taskCount = 20
	dm := NewDependencyManager()
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	var executions atomic.Int32
	for i := 0; i < taskCount; i++ {
		id := fmt.Sprintf("task-%02d", i)
		if err := dm.AddTask(&DependencyTask{
			ID: id,
			Execute: func(context.Context) error {
				executions.Add(1)
				startOnce.Do(func() { close(started) })
				<-release
				return nil
			},
		}); err != nil {
			t.Fatalf("AddTask %s: %v", id, err)
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- dm.ExecuteDependencies(ctx, 1) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no task acquired the execution slot")
	}
	cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if dm.GetStats().Skipped == taskCount-1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if stats := dm.GetStats(); stats.Skipped != taskCount-1 || stats.Running != 1 {
		t.Fatalf("statuses while one task holds slot after cancellation: %+v", stats)
	}
	if got := executions.Load(); got != 1 {
		t.Fatalf("tasks entering Execute before slot release = %d, want 1", got)
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ExecuteDependencies error = %v, want context cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteDependencies remained blocked after active task returned")
	}
}
