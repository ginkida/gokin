package ui

import (
	"time"
)

// CloseOverlayMsg closes any open overlay
type CloseOverlayMsg struct{}

// ========== Task Execution Events ==========

// TaskStartedEvent is fired when a task starts execution
type TaskStartedEvent struct {
	TaskID   string
	Message  string
	PlanType string
}

// TaskCompletedEvent is fired when a task completes
type TaskCompletedEvent struct {
	TaskID   string
	Success  bool
	Duration time.Duration
	Error    error
	PlanType string
}

// TaskProgressEvent is fired for task progress updates
type TaskProgressEvent struct {
	TaskID   string
	Progress float64
	Message  string
}

// CoordinatedTaskCleanupMsg is sent by handleTaskCompleted after a delay to
// auto-remove a completed/failed task from the display.
type CoordinatedTaskCleanupMsg struct {
	ID string
}
