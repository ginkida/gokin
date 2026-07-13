package app

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

// OrchestratorTaskStatus represents the state of a task.
type OrchestratorTaskStatus int

const (
	OrchStatusPending OrchestratorTaskStatus = iota
	OrchStatusReady
	OrchStatusRunning
	OrchStatusCompleted
	OrchStatusFailed
	OrchStatusSkipped
)

// OrchestratorTask priority.
type OrchPriority int

const (
	OrchPriorityLow OrchPriority = iota
	OrchPriorityNormal
	OrchPriorityHigh
)

// OrchestratorTask represents a unified unit of work.
type OrchestratorTask struct {
	ID           string
	Name         string
	Priority     OrchPriority
	Dependencies []string
	Execute      func(ctx context.Context) error
	OnComplete   func(err error)

	Status      OrchestratorTaskStatus
	Error       error
	CreatedAt   time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time

	mu sync.RWMutex
}

// TaskOrchestrator manages the execution of tasks with priorities and dependencies.
type TaskOrchestrator struct {
	tasks         map[string]*OrchestratorTask
	maxConcurrent int
	timeout       time.Duration

	activeCount int
	taskChan    chan *OrchestratorTask

	onStatusChange func(taskID string, status OrchestratorTaskStatus)

	mu sync.RWMutex
	wg sync.WaitGroup
}

// NewTaskOrchestrator creates a new orchestrator.
func NewTaskOrchestrator(maxConcurrent int, timeout time.Duration) *TaskOrchestrator {
	if maxConcurrent <= 0 {
		maxConcurrent = 5
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &TaskOrchestrator{
		tasks:         make(map[string]*OrchestratorTask),
		maxConcurrent: maxConcurrent,
		timeout:       timeout,
		taskChan:      make(chan *OrchestratorTask, maxConcurrent),
	}
}

// Submit adds a single task.
func (o *TaskOrchestrator) Submit(task *OrchestratorTask) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		task.ID = fmt.Sprintf("task_%d", time.Now().UnixNano())
	}
	if _, exists := o.tasks[task.ID]; exists {
		return fmt.Errorf("task %s already exists", task.ID)
	}

	stored := cloneOrchestratorTask(task)
	stored.Status = OrchStatusPending
	stored.CreatedAt = time.Now()
	proposed := make(map[string]*OrchestratorTask, len(o.tasks)+1)
	for id, existing := range o.tasks {
		proposed[id] = existing
	}
	proposed[stored.ID] = stored
	if err := validateOrchestratorGraph(proposed); err != nil {
		return err
	}
	o.tasks[stored.ID] = stored
	// Count every accepted task immediately. Pending tasks are work too: if only
	// scheduled tasks are counted, Wait can return while a dependency is still
	// pending, and cancelling a queued task has to perform a fragile Add-before-
	// Done handoff to whatever becomes runnable next.
	o.wg.Add(1)
	o.scheduleLocked()
	return nil
}

// SubmitGroup adds multiple tasks that might have inter-dependencies.
func (o *TaskOrchestrator) SubmitGroup(tasks []*OrchestratorTask) error {
	o.mu.Lock()
	defer o.mu.Unlock()

	proposed := make(map[string]*OrchestratorTask, len(o.tasks)+len(tasks))
	for id, task := range o.tasks {
		proposed[id] = task
	}
	storedTasks := make([]*OrchestratorTask, 0, len(tasks))
	createdAt := time.Now()
	for i, task := range tasks {
		if task == nil {
			return fmt.Errorf("task %d cannot be nil", i)
		}
		if task.ID == "" {
			task.ID = fmt.Sprintf("task_%d_%d", createdAt.UnixNano(), i)
		}
		if _, exists := proposed[task.ID]; exists {
			return fmt.Errorf("task %s already exists", task.ID)
		}
		stored := cloneOrchestratorTask(task)
		stored.Status = OrchStatusPending
		stored.CreatedAt = createdAt
		storedTasks = append(storedTasks, stored)
		proposed[stored.ID] = stored
	}
	if err := validateOrchestratorGraph(proposed); err != nil {
		return err
	}
	for _, task := range storedTasks {
		o.tasks[task.ID] = task
	}
	o.wg.Add(len(storedTasks))
	o.scheduleLocked()
	return nil
}

func validateOrchestratorGraph(tasks map[string]*OrchestratorTask) error {
	indegree := make(map[string]int, len(tasks))
	dependents := make(map[string][]string, len(tasks))
	for id := range tasks {
		indegree[id] = 0
	}
	for id, task := range tasks {
		seen := make(map[string]struct{}, len(task.Dependencies))
		for _, dependency := range task.Dependencies {
			if _, exists := tasks[dependency]; !exists {
				return fmt.Errorf("task %s depends on missing task %s", id, dependency)
			}
			if _, duplicate := seen[dependency]; duplicate {
				return fmt.Errorf("task %s has duplicate dependency %s", id, dependency)
			}
			seen[dependency] = struct{}{}
			indegree[id]++
			dependents[dependency] = append(dependents[dependency], id)
		}
	}

	ready := make([]string, 0, len(tasks))
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	visited := 0
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		visited++
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if visited != len(tasks) {
		return fmt.Errorf("orchestrator task graph contains a dependency cycle")
	}
	return nil
}

// SetOnStatusChange sets the callback for UI updates.
func (o *TaskOrchestrator) SetOnStatusChange(fn func(string, OrchestratorTaskStatus)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.onStatusChange = fn
}

func cloneOrchestratorTask(task *OrchestratorTask) *OrchestratorTask {
	if task == nil {
		return nil
	}
	task.mu.RLock()
	defer task.mu.RUnlock()
	clone := &OrchestratorTask{
		ID:           task.ID,
		Name:         task.Name,
		Priority:     task.Priority,
		Dependencies: append([]string(nil), task.Dependencies...),
		Execute:      task.Execute,
		OnComplete:   task.OnComplete,
		Status:       task.Status,
		Error:        task.Error,
		CreatedAt:    task.CreatedAt,
	}
	if task.StartedAt != nil {
		startedAt := *task.StartedAt
		clone.StartedAt = &startedAt
	}
	if task.CompletedAt != nil {
		completedAt := *task.CompletedAt
		clone.CompletedAt = &completedAt
	}
	return clone
}

func invokeOrchestratorStatusChange(callback func(string, OrchestratorTaskStatus), taskID string, status OrchestratorTaskStatus) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("orchestrator status callback panicked",
				"task_id", taskID, "status", status,
				"panic", recovered, "stack", logging.PanicStack())
		}
	}()
	callback(taskID, status)
}

func invokeOrchestratorComplete(callback func(error), taskID string, err error) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("orchestrator completion callback panicked",
				"task_id", taskID, "panic", recovered, "stack", logging.PanicStack())
		}
	}()
	callback(err)
}

// Start begins processing the queue.
func (o *TaskOrchestrator) Start(ctx context.Context) {
	logging.Info("TaskOrchestrator started", "max_concurrent", o.maxConcurrent)
	for {
		if err := ctx.Err(); err != nil {
			o.cancelUnfinished(err)
			return
		}
		select {
		case <-ctx.Done():
			o.cancelUnfinished(ctx.Err())
			return
		case task := <-o.taskChan:
			o.executeTask(ctx, task)
		}
	}
}

// schedule checks for ready tasks and sends them to taskChan.
func (o *TaskOrchestrator) schedule() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.scheduleLocked()
}

func (o *TaskOrchestrator) scheduleLocked() {
	if o.activeCount >= o.maxConcurrent {
		return
	}

	// Find all ready tasks
	var readyTasks []*OrchestratorTask
	for _, t := range o.tasks {
		t.mu.RLock()
		if t.Status == OrchStatusPending && o.isReady(t) {
			readyTasks = append(readyTasks, t)
		}
		t.mu.RUnlock()
	}

	// Sort by priority (High first), then by creation time
	sort.Slice(readyTasks, func(i, j int) bool {
		if readyTasks[i].Priority != readyTasks[j].Priority {
			return readyTasks[i].Priority > readyTasks[j].Priority
		}
		return readyTasks[i].CreatedAt.Before(readyTasks[j].CreatedAt)
	})

	// Fill available slots
	for _, t := range readyTasks {
		if o.activeCount >= o.maxConcurrent {
			break
		}

		// A cancelled Ready task can still occupy a buffered channel slot until
		// Start consumes it. Never block Submit/Cancel behind such a stale item;
		// leave this task pending and retry when the stale item is drained.
		t.mu.Lock()
		t.Status = OrchStatusReady
		select {
		case o.taskChan <- t:
			o.activeCount++
			t.mu.Unlock()
		default:
			t.Status = OrchStatusPending
			t.mu.Unlock()
			return
		}
	}
}

func (o *TaskOrchestrator) isReady(t *OrchestratorTask) bool {
	for _, depID := range t.Dependencies {
		dep, exists := o.tasks[depID]
		if !exists {
			// Missing dependency - treat as failed, task cannot proceed
			logging.Warn("task has missing dependency", "task_id", t.ID, "missing_dep", depID)
			return false
		}
		dep.mu.RLock()
		status := dep.Status
		dep.mu.RUnlock()
		if status != OrchStatusCompleted {
			return false
		}
	}
	return true
}

func (o *TaskOrchestrator) executeTask(ctx context.Context, t *OrchestratorTask) {
	// Claim the scheduled slot synchronously. Cancellation can now distinguish
	// an unclaimed Ready task (safe to skip and wg.Done itself) from a Running
	// task whose goroutine owns the terminal transition.
	now := time.Now()
	t.mu.Lock()
	if t.Status != OrchStatusReady {
		t.mu.Unlock()
		// This is normally a queued task cancelled before Start received it.
		// Consuming its stale channel item frees capacity for pending work.
		o.schedule()
		return
	}
	t.Status = OrchStatusRunning
	t.StartedAt = &now
	t.mu.Unlock()

	go func() {
		defer o.wg.Done()
		finished := false
		defer func() {
			if recovered := recover(); recovered != nil {
				err := fmt.Errorf("task %s panicked: %v", t.ID, recovered)
				logging.Error("orchestrator task panic",
					"task_id", t.ID,
					"panic", recovered,
					"stack", logging.PanicStack())
				if !finished {
					o.finishTask(t, err)
				}
			}
			o.mu.Lock()
			o.activeCount--
			o.mu.Unlock()
			o.schedule() // Check for new ready tasks
		}()

		// Snapshot callback under lock to avoid a race with SetOnStatusChange.
		o.mu.Lock()
		onStatusChange := o.onStatusChange
		o.mu.Unlock()

		invokeOrchestratorStatusChange(onStatusChange, t.ID, OrchStatusRunning)

		// Execute with timeout
		taskCtx, cancel := context.WithTimeout(ctx, o.timeout)
		defer cancel()

		var err error
		if t.Execute != nil {
			err = t.Execute(taskCtx)
		}

		o.finishTask(t, err)
		finished = true
	}()
}

type orchestratorTerminalNotification struct {
	id         string
	err        error
	onComplete func(error)
	counted    bool
}

func (o *TaskOrchestrator) cancelUnfinished(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	o.mu.Lock()
	onStatusChange := o.onStatusChange
	notifications := make([]orchestratorTerminalNotification, 0)
	for _, task := range o.tasks {
		task.mu.Lock()
		counted := task.Status == OrchStatusReady
		if task.Status == OrchStatusPending || counted {
			err := fmt.Errorf("orchestrator stopped: %w", cause)
			task.Status = OrchStatusSkipped
			task.Error = err
			now := time.Now()
			task.CompletedAt = &now
			notifications = append(notifications, orchestratorTerminalNotification{
				id: task.ID, err: err, onComplete: task.OnComplete, counted: counted,
			})
			if counted && o.activeCount > 0 {
				o.activeCount--
			}
		}
		task.mu.Unlock()
	}
	o.mu.Unlock()

	for _, notification := range notifications {
		invokeOrchestratorStatusChange(onStatusChange, notification.id, OrchStatusSkipped)
		invokeOrchestratorComplete(notification.onComplete, notification.id, notification.err)
		o.wg.Done()
	}
}

func (o *TaskOrchestrator) finishTask(t *OrchestratorTask, err error) {
	t.mu.Lock()
	compNow := time.Now()
	t.CompletedAt = &compNow
	status := OrchStatusCompleted
	if err != nil {
		status = OrchStatusFailed
		t.Error = err
	} else {
		t.Error = nil
	}
	t.Status = status
	onComplete := t.OnComplete
	t.mu.Unlock()

	if err != nil {
		logging.Error("task failed", "id", t.ID, "error", err)
	}
	o.mu.RLock()
	onStatusChange := o.onStatusChange
	o.mu.RUnlock()
	invokeOrchestratorStatusChange(onStatusChange, t.ID, status)
	invokeOrchestratorComplete(onComplete, t.ID, err)
	if err != nil {
		o.skipDependents(t.ID)
	}
}

func (o *TaskOrchestrator) skipDependents(failedID string) {
	// Collect tasks to skip while holding the lock
	var toSkip []orchestratorTerminalNotification

	o.mu.Lock()
	onStatusChange := o.onStatusChange
	for _, t := range o.tasks {
		t.mu.Lock()
		if t.Status == OrchStatusPending {
			for _, depID := range t.Dependencies {
				if depID == failedID {
					t.Status = OrchStatusSkipped
					t.Error = fmt.Errorf("dependency %s failed", failedID)
					now := time.Now()
					t.CompletedAt = &now
					toSkip = append(toSkip, orchestratorTerminalNotification{
						id: t.ID, err: t.Error, onComplete: t.OnComplete,
					})
					break
				}
			}
		}
		t.mu.Unlock()
	}
	o.mu.Unlock()
	for _, notification := range toSkip {
		invokeOrchestratorStatusChange(onStatusChange, notification.id, OrchStatusSkipped)
		invokeOrchestratorComplete(notification.onComplete, notification.id, notification.err)
		o.wg.Done()
	}

	// Recursively skip dependents without holding the lock
	for _, notification := range toSkip {
		o.skipDependents(notification.id)
	}
}

// GetStats returns current orchestrator metrics.
func (o *TaskOrchestrator) GetStats() map[string]int {
	o.mu.RLock()
	defer o.mu.RUnlock()

	stats := make(map[string]int)
	for _, t := range o.tasks {
		t.mu.RLock()
		switch t.Status {
		case OrchStatusPending:
			stats["pending"]++
		case OrchStatusReady:
			stats["ready"]++
		case OrchStatusRunning:
			stats["running"]++
		case OrchStatusCompleted:
			stats["completed"]++
		case OrchStatusFailed:
			stats["failed"]++
		case OrchStatusSkipped:
			stats["skipped"]++
		}
		t.mu.RUnlock()
	}
	stats["active_count"] = o.activeCount
	return stats
}

// Wait blocks until all submitted tasks have completed.
func (o *TaskOrchestrator) Wait() {
	o.wg.Wait()
}

// Cleanup removes terminal tasks that are no longer prerequisites of pending
// work. A completed task can be briefly terminal before the execution
// goroutine's deferred scheduler promotes its dependents; deleting it in that
// window makes isReady observe a missing dependency and strands the DAG.
// Returns the number of tasks removed.
func (o *TaskOrchestrator) Cleanup() int {
	o.mu.Lock()
	defer o.mu.Unlock()

	requiredByPending := make(map[string]struct{})
	for _, task := range o.tasks {
		task.mu.RLock()
		if task.Status == OrchStatusPending {
			for _, dependency := range task.Dependencies {
				requiredByPending[dependency] = struct{}{}
			}
		}
		task.mu.RUnlock()
	}

	var toRemove []string
	for id, t := range o.tasks {
		t.mu.RLock()
		status := t.Status
		t.mu.RUnlock()

		_, required := requiredByPending[id]
		if !required && (status == OrchStatusCompleted || status == OrchStatusFailed || status == OrchStatusSkipped) {
			toRemove = append(toRemove, id)
		}
	}

	for _, id := range toRemove {
		delete(o.tasks, id)
	}

	return len(toRemove)
}

// GetTask returns a caller-owned task snapshot by ID.
func (o *TaskOrchestrator) GetTask(id string) (*OrchestratorTask, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	t, ok := o.tasks[id]
	return cloneOrchestratorTask(t), ok
}

// CancelTask marks a pending or queued task as skipped.
func (o *TaskOrchestrator) CancelTask(id string) error {
	o.mu.Lock()

	t, exists := o.tasks[id]
	if !exists {
		o.mu.Unlock()
		return fmt.Errorf("task %s not found", id)
	}

	t.mu.Lock()

	counted := t.Status == OrchStatusReady
	if t.Status != OrchStatusPending && !counted {
		status := t.Status
		t.mu.Unlock()
		o.mu.Unlock()
		return fmt.Errorf("task %s is not pending (status: %d)", id, status)
	}

	t.Status = OrchStatusSkipped
	t.Error = fmt.Errorf("cancelled by user")
	now := time.Now()
	t.CompletedAt = &now
	onStatusChange := o.onStatusChange
	onComplete := t.OnComplete
	err := t.Error
	if counted && o.activeCount > 0 {
		o.activeCount--
	}
	t.mu.Unlock()
	o.mu.Unlock()

	invokeOrchestratorStatusChange(onStatusChange, id, OrchStatusSkipped)
	invokeOrchestratorComplete(onComplete, id, err)
	o.skipDependents(id)
	// A Ready task's channel item is still present (or has just been received by
	// Start); its consumer will schedule replacement work after draining it.
	// Pending cancellation does not release a scheduled slot.
	o.wg.Done()

	return nil
}
