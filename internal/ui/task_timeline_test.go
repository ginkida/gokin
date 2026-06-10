package ui

import (
	"strings"
	"testing"
	"time"
)

func TestHandleTaskStarted_RendersTimelineHeader(t *testing.T) {
	m := NewModel()

	m.handleTaskStarted(TaskStartedEvent{
		TaskID:   "task-1",
		Message:  "Inspect retry behavior around Kimi idle phases",
		PlanType: "explorer",
	})

	rendered := stripAnsi(m.output.state.content.String())
	for _, want := range []string{
		"Subtask 1",
		"Explorer",
		"Inspect retry behavior around Kimi idle phases",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("timeline header missing %q:\n%s", want, rendered)
		}
	}
}

func TestHandleTaskProgress_DeduplicatesMinorProgressNoise(t *testing.T) {
	m := NewModel()
	m.handleTaskStarted(TaskStartedEvent{TaskID: "task-1", Message: "Inspect retry behavior", PlanType: "explorer"})

	m.handleTaskProgress(TaskProgressEvent{TaskID: "task-1", Progress: 0.11, Message: "Reading retry.go"})
	m.handleTaskProgress(TaskProgressEvent{TaskID: "task-1", Progress: 0.14, Message: "Reading retry.go"})

	rendered := stripAnsi(m.output.state.content.String())
	if strings.Count(rendered, "Reading retry.go") != 1 {
		t.Fatalf("expected deduplicated progress output, got:\n%s", rendered)
	}
}

func TestSubAgentActivityStart_UsesTrackedDescription(t *testing.T) {
	m := NewModel()
	m.handleBackgroundTask(BackgroundTaskMsg{
		ID:          "agent-1",
		Type:        "agent",
		Description: "Audit failover and recovery path",
		Status:      "running",
	})

	m.handleMessageTypes(SubAgentActivityMsg{
		AgentID:   "agent-1",
		AgentType: "explorer",
		Status:    "start",
	})

	rendered := stripAnsi(m.output.state.content.String())
	for _, want := range []string{
		"Agent Explorer started",
		"Audit failover and recovery path",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("agent start line missing %q:\n%s", want, rendered)
		}
	}
}

// TestBackgroundTaskMsg_DispatchLifecycle drives BackgroundTaskMsg through the
// Update/handleMessageTypes switch (NOT the handler directly) so a dropped
// 'case BackgroundTaskMsg' regression is caught. The handler has its own unit
// test above, but only the dispatch path proves the live wiring from builder.go
// still reaches it.
func TestBackgroundTaskMsg_DispatchLifecycle(t *testing.T) {
	m := NewModel()

	m.handleMessageTypes(BackgroundTaskMsg{
		ID:          "agent-1",
		Type:        "agent",
		Description: "Audit failover path",
		Status:      "running",
	})
	if got := m.GetBackgroundTaskCount(); got != 1 {
		t.Fatalf("running BackgroundTaskMsg must populate tracking via dispatch; count=%d want 1", got)
	}

	m.handleMessageTypes(BackgroundTaskMsg{ID: "agent-1", Type: "agent", Status: "completed"})
	if got := m.GetBackgroundTaskCount(); got != 0 {
		t.Fatalf("completed BackgroundTaskMsg must remove tracking via dispatch; count=%d want 0", got)
	}
}

// TestCleanupStaleCoordinatedTasks_SweepsStaleKeepsRunning pins the new
// auto-cleanup machinery: the EndTime gate (running tasks are never dropped),
// the sweep of stale completed tasks from both the map and order slice, and
// the double-removal safety of removeCoordinatedTask.
func TestCleanupStaleCoordinatedTasks_SweepsStaleKeepsRunning(t *testing.T) {
	m := NewModel()
	m.handleTaskStarted(TaskStartedEvent{TaskID: "done-1", Message: "x", PlanType: "explorer"})
	m.handleTaskStarted(TaskStartedEvent{TaskID: "run-1", Message: "y", PlanType: "explorer"})

	cmd := m.handleTaskCompleted(TaskCompletedEvent{TaskID: "done-1", Success: true})
	if cmd == nil {
		t.Fatal("handleTaskCompleted must return a cleanup tick cmd")
	}
	if m.coordinatedTasks["done-1"].Status != "completed" {
		t.Fatalf("status=%q want completed", m.coordinatedTasks["done-1"].Status)
	}
	// Backdate EndTime past the 5s cleanup delay.
	m.coordinatedTasks["done-1"].EndTime = time.Now().Add(-6 * time.Second)

	m.cleanupStaleCoordinatedTasks()
	if _, ok := m.coordinatedTasks["done-1"]; ok {
		t.Fatal("stale completed task should be swept")
	}
	if _, ok := m.coordinatedTasks["run-1"]; !ok {
		t.Fatal("running task (zero EndTime) must not be swept")
	}
	if len(m.coordinatedTaskOrder) != 1 || m.coordinatedTaskOrder[0] != "run-1" {
		t.Fatalf("order=%v want [run-1]", m.coordinatedTaskOrder)
	}

	// Double-removal must be a no-op (map delete on missing key + slice scan).
	m.removeCoordinatedTask("done-1")
	m.removeCoordinatedTask("done-1")
	if len(m.coordinatedTaskOrder) != 1 {
		t.Fatalf("double-remove mutated order: %v", m.coordinatedTaskOrder)
	}
}

func TestHandleTaskCompleted_RendersTimelineResult(t *testing.T) {
	m := NewModel()
	m.handleTaskStarted(TaskStartedEvent{TaskID: "task-1", Message: "Inspect retry behavior", PlanType: "explorer"})

	m.handleTaskCompleted(TaskCompletedEvent{
		TaskID:   "task-1",
		Success:  true,
		Duration: 2400 * time.Millisecond,
	})

	rendered := stripAnsi(m.output.state.content.String())
	if !strings.Contains(rendered, "Completed 2.4s") {
		t.Fatalf("expected completion line, got:\n%s", rendered)
	}
}
