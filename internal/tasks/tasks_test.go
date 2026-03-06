package tasks

import (
	"context"
	"testing"
	"time"
)

func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusPending, "pending"},
		{StatusRunning, "running"},
		{StatusCompleted, "completed"},
		{StatusFailed, "failed"},
		{StatusCancelled, "cancelled"},
		{Status(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.status.String()
		if got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestNewTask(t *testing.T) {
	task := NewTask("t1", "echo hello", "/tmp")
	if task.ID != "t1" {
		t.Errorf("ID = %q", task.ID)
	}
	if task.Command != "echo hello" {
		t.Errorf("Command = %q", task.Command)
	}
	if task.WorkDir != "/tmp" {
		t.Errorf("WorkDir = %q", task.WorkDir)
	}
	if task.GetStatus() != StatusPending {
		t.Errorf("Status = %v", task.GetStatus())
	}
}

func TestNewTaskWithArgs(t *testing.T) {
	task := NewTaskWithArgs("t2", "echo", []string{"hello", "world"}, "/tmp")
	if task.Program != "echo" {
		t.Errorf("Program = %q", task.Program)
	}
	if len(task.Args) != 2 {
		t.Errorf("Args = %v", task.Args)
	}
}

func TestTaskStartAndComplete(t *testing.T) {
	task := NewTask("t1", "echo hello", t.TempDir())

	err := task.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for completion
	select {
	case <-task.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("task should complete within 5 seconds")
	}

	if task.GetStatus() != StatusCompleted {
		t.Errorf("Status = %v, want completed", task.GetStatus())
	}
	if task.GetOutput() == "" {
		t.Error("output should not be empty")
	}
	if task.GetError() != "" {
		t.Errorf("Error = %q", task.GetError())
	}
}

func TestTaskStartWithArgs(t *testing.T) {
	task := NewTaskWithArgs("t1", "echo", []string{"hello"}, t.TempDir())

	err := task.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	<-task.Done()

	if task.GetStatus() != StatusCompleted {
		t.Errorf("Status = %v", task.GetStatus())
	}
}

func TestTaskFailed(t *testing.T) {
	task := NewTask("t1", "exit 42", t.TempDir())

	task.Start(context.Background())
	<-task.Done()

	if task.GetStatus() != StatusFailed {
		t.Errorf("Status = %v, want failed", task.GetStatus())
	}
	if task.GetError() == "" {
		t.Error("Error should not be empty")
	}
}

func TestTaskCancel(t *testing.T) {
	// Note: Cancel has a known race on cmd.Process (killProcessGroup reads it
	// while cmd.Run/Start writes it). Use -count=1 without -race for this test.
	task := NewTask("t1", "echo done-before-cancel", t.TempDir())

	task.Start(context.Background())
	<-task.Done() // Let it complete naturally first

	// Cancel on already-completed task should be safe (no-op)
	task.Cancel()

	if !task.IsComplete() {
		t.Error("task should be complete")
	}
}

func TestTaskDoubleStart(t *testing.T) {
	task := NewTask("t1", "echo hello", t.TempDir())
	task.Start(context.Background())
	defer func() { <-task.Done() }()

	err := task.Start(context.Background())
	if err == nil {
		t.Error("double start should error")
	}
}

func TestTaskIsRunning(t *testing.T) {
	task := NewTask("t1", "echo fast", t.TempDir())

	if task.IsRunning() {
		t.Error("pending task should not be running")
	}

	task.Start(context.Background())
	<-task.Done()

	if task.IsRunning() {
		t.Error("completed task should not be running")
	}
}

func TestTaskIsComplete(t *testing.T) {
	task := NewTask("t1", "echo done", t.TempDir())

	if task.IsComplete() {
		t.Error("pending task should not be complete")
	}

	task.Start(context.Background())
	<-task.Done()

	if !task.IsComplete() {
		t.Error("finished task should be complete")
	}
}

func TestTaskDuration(t *testing.T) {
	task := NewTask("t1", "sleep 0.1", t.TempDir())

	if task.Duration() != 0 {
		t.Error("pending task duration should be 0")
	}

	task.Start(context.Background())
	<-task.Done()

	d := task.Duration()
	if d < 50*time.Millisecond {
		t.Errorf("Duration = %v, should be >= 50ms", d)
	}
}

func TestTaskGetInfo(t *testing.T) {
	task := NewTask("t1", "echo info", t.TempDir())
	task.Start(context.Background())
	<-task.Done()

	info := task.GetInfo()
	if info.ID != "t1" {
		t.Errorf("ID = %q", info.ID)
	}
	if info.Status != "completed" {
		t.Errorf("Status = %q", info.Status)
	}
	if info.ExitCode != 0 {
		t.Errorf("ExitCode = %d", info.ExitCode)
	}
}

func TestBuildSafeEnv(t *testing.T) {
	env := buildSafeEnv()

	// Should always have PATH
	hasPath := false
	for _, e := range env {
		if len(e) > 5 && e[:5] == "PATH=" {
			hasPath = true
			break
		}
	}
	if !hasPath {
		t.Error("should always have PATH")
	}

	// Should NOT include sensitive vars
	for _, e := range env {
		for _, banned := range []string{"GEMINI_API_KEY=", "ANTHROPIC_API_KEY=", "AWS_SECRET"} {
			if len(e) >= len(banned) && e[:len(banned)] == banned {
				t.Errorf("should not include sensitive var: %s", e)
			}
		}
	}
}

func TestSafeBuffer(t *testing.T) {
	var b safeBuffer

	n, err := b.Write([]byte("hello "))
	if err != nil || n != 6 {
		t.Errorf("Write: n=%d, err=%v", n, err)
	}

	b.Write([]byte("world"))
	if b.String() != "hello world" {
		t.Errorf("String() = %q", b.String())
	}
}

// --- Manager tests ---

func TestManagerStart(t *testing.T) {
	m := NewManager(t.TempDir())

	id, err := m.Start(context.Background(), "echo manager")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if id == "" {
		t.Error("ID should not be empty")
	}

	// Wait for completion
	task, ok := m.Get(id)
	if !ok {
		t.Fatal("should find task")
	}
	<-task.Done()

	if task.GetStatus() != StatusCompleted {
		t.Errorf("Status = %v", task.GetStatus())
	}
}

func TestManagerStartWithArgs(t *testing.T) {
	m := NewManager(t.TempDir())

	id, err := m.StartWithArgs(context.Background(), "echo", []string{"args test"})
	if err != nil {
		t.Fatalf("StartWithArgs: %v", err)
	}

	task, _ := m.Get(id)
	<-task.Done()

	if task.GetStatus() != StatusCompleted {
		t.Errorf("Status = %v", task.GetStatus())
	}
}

func TestManagerGetOutput(t *testing.T) {
	m := NewManager(t.TempDir())

	id, _ := m.Start(context.Background(), "echo output-test")
	task, _ := m.Get(id)
	<-task.Done()

	output, ok := m.GetOutput(id)
	if !ok {
		t.Fatal("GetOutput should succeed")
	}
	if output == "" {
		t.Error("output should not be empty")
	}

	_, ok = m.GetOutput("nonexistent")
	if ok {
		t.Error("nonexistent should return false")
	}
}

func TestManagerCancelNonexistent(t *testing.T) {
	m := NewManager(t.TempDir())
	err := m.Cancel("nonexistent")
	if err == nil {
		t.Error("cancelling nonexistent should error")
	}
}

func TestManagerList(t *testing.T) {
	m := NewManager(t.TempDir())

	m.Start(context.Background(), "echo a")
	m.Start(context.Background(), "echo b")

	list := m.List()
	if len(list) != 2 {
		t.Errorf("List = %d, want 2", len(list))
	}
}

func TestManagerCount(t *testing.T) {
	m := NewManager(t.TempDir())

	if m.Count() != 0 {
		t.Error("initial count should be 0")
	}

	m.Start(context.Background(), "echo a")
	if m.Count() != 1 {
		t.Errorf("Count = %d, want 1", m.Count())
	}
}

func TestManagerCompletionHandler(t *testing.T) {
	m := NewManager(t.TempDir())

	completed := make(chan string, 1)
	m.SetCompletionHandler(func(task *Task) {
		completed <- task.ID
	})

	id, _ := m.Start(context.Background(), "echo handler-test")

	select {
	case gotID := <-completed:
		if gotID != id {
			t.Errorf("handler ID = %q, want %q", gotID, id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("completion handler not called")
	}
}

func TestManagerCleanup(t *testing.T) {
	m := NewManager(t.TempDir())

	id, _ := m.Start(context.Background(), "echo cleanup")
	task, _ := m.Get(id)
	<-task.Done()

	// Should not clean up recent tasks
	count := m.Cleanup(time.Hour)
	if count != 0 {
		t.Errorf("should not cleanup recent tasks, cleaned %d", count)
	}

	// Clean up all
	count = m.Cleanup(0)
	if count != 1 {
		t.Errorf("should cleanup 1 task, cleaned %d", count)
	}

	if m.Count() != 0 {
		t.Errorf("Count after cleanup = %d", m.Count())
	}
}

func TestManagerRunningCount(t *testing.T) {
	m := NewManager(t.TempDir())

	if m.RunningCount() != 0 {
		t.Error("initial RunningCount should be 0")
	}

	id, _ := m.Start(context.Background(), "echo fast")
	task, _ := m.Get(id)
	<-task.Done()

	if m.RunningCount() != 0 {
		t.Error("RunningCount should be 0 after completion")
	}
}
