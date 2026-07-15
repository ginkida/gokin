package ui

import (
	"time"
)

// CloseOverlayMsg completes an asynchronous batch-progress close request.
// It is intentionally harmless once that progress surface is no longer active.
// CloseOverlayMsg is asynchronous, so it carries the exact key that owned the
// transition. The parent can suppress only that physical auto-repeat after the
// composer or active turn is revealed. The owner generation prevents a delayed
// close from batch A from dismissing a newer batch B. Empty ownership preserves
// compatibility for synthetic close messages.
type CloseOverlayMsg struct {
	triggerKey      string
	ownerGeneration uint64
}

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
