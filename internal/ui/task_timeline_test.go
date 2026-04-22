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
