package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"gokin/internal/logging"
)

// Coordinator manages multiple agents with dependencies and parallelism.
type Coordinator struct {
	runner       *Runner
	tasks        map[string]*CoordinatedTask
	dependencies map[string][]string // taskID -> dependent task IDs
	queue        *TaskQueue
	maxParallel  int

	// Tracking running agents
	running   map[string]string // agentID -> taskID
	completed map[string]bool   // completed taskIDs

	// Callbacks
	onTaskStart    func(task *CoordinatedTask)
	onTaskComplete func(task *CoordinatedTask, result *AgentResult)
	onAllComplete  func(results map[string]*AgentResult)

	// Event-driven channels for efficient processing
	taskReadyCh  chan struct{} // Signals when a task becomes ready
	agentDoneCh  chan string   // Signals when an agent completes (carries agentID)
	completionCh chan struct{} // Closed once when the whole coordination completes
	completeOnce sync.Once
	startOnce    sync.Once

	// Reflection for error learning feedback loop
	reflector *Reflector

	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

// CoordinatorConfig holds configuration for the coordinator.
type CoordinatorConfig struct {
	MaxParallel int // Maximum concurrent agents (default: 3)
}

// NewCoordinator creates a new coordinator.
func NewCoordinator(ctx context.Context, runner *Runner, config *CoordinatorConfig) *Coordinator {
	if config == nil {
		config = &CoordinatorConfig{MaxParallel: 3}
	}
	if config.MaxParallel <= 0 {
		config.MaxParallel = 3
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)

	return &Coordinator{
		runner:       runner,
		tasks:        make(map[string]*CoordinatedTask),
		dependencies: make(map[string][]string),
		queue:        NewTaskQueue(),
		maxParallel:  config.MaxParallel,
		running:      make(map[string]string),
		completed:    make(map[string]bool),
		taskReadyCh:  make(chan struct{}, 100), // Buffered to avoid blocking
		agentDoneCh:  make(chan string, 100),   // Buffered for agent completions
		completionCh: make(chan struct{}),
		reflector:    NewReflector(), // Initialize reflector for feedback loop
		ctx:          ctx,
		cancel:       cancel,
	}
}

// SetReflector sets the reflector for error learning feedback loop.
func (c *Coordinator) SetReflector(r *Reflector) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reflector = r
}

// generateTaskID creates a unique task ID.
func generateTaskID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "task_" + hex.EncodeToString(b)
}

// AddTask adds a new task to the coordinator.
func (c *Coordinator) AddTask(prompt string, agentType AgentType, priority TaskPriority, deps []string) string {
	c.mu.Lock()
	defer c.mu.Unlock()

	taskID := generateTaskID()
	task := &CoordinatedTask{
		ID:           taskID,
		Prompt:       prompt,
		AgentType:    agentType,
		Priority:     priority,
		Dependencies: append([]string(nil), deps...),
		Status:       TaskStatusPending,
	}

	c.tasks[taskID] = task

	// Build reverse dependency map
	for _, depID := range deps {
		c.dependencies[depID] = append(c.dependencies[depID], taskID)
	}

	// Check if task is ready
	if c.areDependenciesMet(task) {
		task.Status = TaskStatusReady
		c.queue.PushTask(task)
		// Signal that a task is ready (non-blocking)
		select {
		case c.taskReadyCh <- struct{}{}:
		default:
		}
	} else {
		task.Status = TaskStatusBlocked
	}

	logging.Debug("coordinator: task added",
		"task_id", taskID,
		"agent_type", agentType,
		"priority", priority,
		"dependencies", deps,
		"status", task.Status)

	return taskID
}

// areDependenciesMet checks if all dependencies are completed.
func (c *Coordinator) areDependenciesMet(task *CoordinatedTask) bool {
	for _, depID := range task.Dependencies {
		if !c.completed[depID] {
			return false
		}
	}
	return true
}

// Start begins processing tasks.
func (c *Coordinator) Start() {
	c.startOnce.Do(func() {
		go c.processLoop()
		// AddTask normally leaves a buffered readiness signal, but an empty
		// batch has none and used to wait for the five-second fallback ticker.
		// An initial wake also makes Start's completion behavior deterministic.
		select {
		case c.taskReadyCh <- struct{}{}:
		default:
		}
	})
}

// SetMaxParallel updates the concurrency limit. Reducing the limit does not
// cancel agents that are already running; it only gates subsequent starts.
// The coordinate tool calls this before Start when the caller supplies its
// public max_parallel argument.
func (c *Coordinator) SetMaxParallel(maxParallel int) {
	if maxParallel <= 0 {
		return
	}
	c.mu.Lock()
	c.maxParallel = maxParallel
	c.mu.Unlock()

	// Wake a live process loop when the limit is raised dynamically.
	select {
	case c.taskReadyCh <- struct{}{}:
	default:
	}
}

// Stop stops the coordinator.
func (c *Coordinator) Stop() {
	c.cancel()
}

// processLoop is the main coordination loop.
// Uses event-driven approach with fallback ticker to reduce CPU usage.
//
// Defer-recover guards against a panic in processReadyTasks /
// handleAgentCompletion / checkCompletedAgents — central agent
// orchestration sits inside this loop and a silent crash would stall
// every running agent for the rest of the process lifetime.
func (c *Coordinator) processLoop() {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("coordinator processLoop panicked — agent orchestration stopped", "panic", r)
		}
	}()
	// Fallback ticker for periodic checks (5s safety net — primary notification is event-driven)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return

		case <-c.taskReadyCh:
			// A task became ready - process it
			c.processReadyTasks()

			// Check if all done
			if c.isAllComplete() {
				c.notifyAllComplete()
				return
			}

		case agentID := <-c.agentDoneCh:
			// An agent completed - handle it
			c.handleAgentCompletion(agentID)

			// Check if all done
			if c.isAllComplete() {
				c.notifyAllComplete()
				return
			}

		case <-ticker.C:
			// Fallback: periodic check for any missed events
			c.processReadyTasks()
			c.checkCompletedAgents()

			// Check if all done
			if c.isAllComplete() {
				c.notifyAllComplete()
				return
			}
		}
	}
}

// processReadyTasks starts ready tasks up to maxParallel, then fires
// onTaskStart for each OUTSIDE the lock — the same discipline
// handleAgentCompletion already follows for onTaskComplete. onTaskStart used
// to fire inside startTask while c.mu was held: a callback that re-enters
// the coordinator (the agent-tree snapshot calls GetAllTasks → c.mu.RLock)
// would self-deadlock the scheduling goroutine. The latent bug never
// detonated only because the sole wired coordinator (the boot one) never
// received tasks.
func (c *Coordinator) processReadyTasks() {
	started, failed, onStart, onComplete := c.startReadyTasksLocked()
	for _, task := range started {
		invokeCoordinatorTaskStart(onStart, task)
	}
	for _, notification := range failed {
		invokeCoordinatorTaskComplete(onComplete, notification.task, notification.result)
	}
}

type coordinatorTaskCompletion struct {
	task   *CoordinatedTask
	result *AgentResult
}

// Coordinator callbacks are integration hooks (UI, telemetry, embedding
// applications), not part of the scheduler's correctness path. Keep each
// invocation behind its own panic boundary so one faulty observer cannot stop
// the process loop, strand dependent tasks, or prevent Wait from being
// notified after the coordinator has already committed task state.
func recoverCoordinatorCallback(kind, taskID string) {
	if recovered := recover(); recovered != nil {
		logging.Error("coordinator callback panicked",
			"callback", kind,
			"task_id", taskID,
			"panic", recovered,
			"stack", logging.PanicStack())
	}
}

func invokeCoordinatorTaskStart(callback func(*CoordinatedTask), task *CoordinatedTask) {
	if callback == nil {
		return
	}
	taskID := ""
	if task != nil {
		taskID = task.ID
	}
	defer recoverCoordinatorCallback("task_start", taskID)
	callback(cloneCoordinatedTask(task))
}

func invokeCoordinatorTaskComplete(callback func(*CoordinatedTask, *AgentResult), task *CoordinatedTask, result *AgentResult) {
	if callback == nil {
		return
	}
	taskID := ""
	if task != nil {
		taskID = task.ID
	}
	defer recoverCoordinatorCallback("task_complete", taskID)
	callback(cloneCoordinatedTask(task), cloneAgentResult(result))
}

func invokeCoordinatorAllComplete(callback func(map[string]*AgentResult), results map[string]*AgentResult) {
	if callback == nil {
		return
	}
	defer recoverCoordinatorCallback("all_complete", "")
	callback(cloneCoordinatorResults(results))
}

func cloneCoordinatedTask(task *CoordinatedTask) *CoordinatedTask {
	if task == nil {
		return nil
	}
	clone := *task
	clone.Dependencies = append([]string(nil), task.Dependencies...)
	clone.Result = cloneAgentResult(task.Result)
	clone.index = -1
	return &clone
}

func cloneCoordinatorResults(results map[string]*AgentResult) map[string]*AgentResult {
	if results == nil {
		return nil
	}
	clone := make(map[string]*AgentResult, len(results))
	for taskID, result := range results {
		clone[taskID] = cloneAgentResult(result)
	}
	return clone
}

// startReadyTasksLocked pops and starts ready tasks under c.mu (deferred
// unlock — a panic inside a spawn must not leave the coordinator locked) and
// returns the started tasks plus the callback snapshot for unlocked firing.
func (c *Coordinator) startReadyTasksLocked() (
	[]*CoordinatedTask,
	[]coordinatorTaskCompletion,
	func(*CoordinatedTask),
	func(*CoordinatedTask, *AgentResult),
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	runningCount := len(c.running)
	availableSlots := c.maxParallel - runningCount

	var started []*CoordinatedTask
	var failed []coordinatorTaskCompletion
	for availableSlots > 0 {
		task := c.queue.PopTask()
		if task == nil {
			break
		}

		if task.Status != TaskStatusReady {
			continue
		}

		// Start the task
		task.Status = TaskStatusRunning
		if failure := c.startTask(task); failure != nil {
			failed = append(failed, coordinatorTaskCompletion{
				task: cloneCoordinatedTask(task), result: cloneAgentResult(failure),
			})
			continue
		}
		started = append(started, cloneCoordinatedTask(task))
		availableSlots--
	}
	return started, failed, c.onTaskStart, c.onTaskComplete
}

// startTask spawns an agent for a task.
// The caller holds c.mu. A spawn failure is committed as a terminal task
// result so the popped queue item cannot remain Running without an agent.
func (c *Coordinator) startTask(task *CoordinatedTask) (failure *AgentResult) {
	logging.Info("coordinator: starting task",
		"task_id", task.ID,
		"agent_type", task.AgentType,
		"prompt", truncate(task.Prompt, 100))

	// NOTE: onTaskStart is fired by processReadyTasks AFTER c.mu is released
	// — never here (this function runs under the lock; see processReadyTasks).

	fail := func(err error) *AgentResult {
		result := &AgentResult{
			Type: task.AgentType, Status: AgentStatusFailed,
			Error: err.Error(), Completed: true,
		}
		task.Status = TaskStatusFailed
		task.Result = result
		c.completed[task.ID] = true
		c.unblockDependents(task.ID)
		logging.Error("coordinator: failed to start task", "task_id", task.ID, "error", err)
		return result
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			failure = fail(fmt.Errorf("agent spawn panicked: %v", recovered))
			logging.Error("coordinator agent spawn panic",
				"task_id", task.ID, "panic", recovered, "stack", logging.PanicStack())
		}
	}()
	if c.runner == nil {
		return fail(fmt.Errorf("agent runner is not configured"))
	}

	// Spawn async agent
	agentID := c.runner.SpawnAsync(c.ctx, string(task.AgentType), task.Prompt, 30, "")
	if agentID == "" {
		return fail(fmt.Errorf("agent runner returned an empty agent ID"))
	}
	c.running[agentID] = task.ID

	// Monitor completion and notify coordinator immediately via agentDoneCh
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Warn("panic in coordinator agent monitor goroutine", "agent", agentID, "panic", r)
			}
		}()
		if _, err := c.runner.WaitWithContext(c.ctx, agentID); err == nil || c.ctx.Err() == nil {
			select {
			case c.agentDoneCh <- agentID:
			case <-c.ctx.Done():
			}
		}
	}()
	return nil
}

// checkCompletedAgents checks for completed agents and updates tasks.
func (c *Coordinator) checkCompletedAgents() {
	type callbackInfo struct {
		task   *CoordinatedTask
		result *AgentResult
	}

	c.mu.Lock()

	// Collect completed agents first to avoid modifying map during iteration
	type completedAgent struct {
		agentID string
		taskID  string
		result  *AgentResult
	}
	var completed []completedAgent

	for agentID, taskID := range c.running {
		// completedResultLocked (not GetResult + a raw .Completed read) —
		// GetResult releases r.mu before returning the pointer, so a bare
		// `result.Completed` check afterward races against writers
		// (SpawnAsync's panic-recovery defer, Runner.Cancel — both mutate
		// this same *AgentResult in place under r.mu.Lock(), reachable
		// independently of c.mu via task_stop/shutdown) — the identical
		// unsynchronized-flag-read bug WaitWithContext had.
		result, ok := c.runner.completedResultLocked(agentID)
		if !ok {
			continue
		}
		completed = append(completed, completedAgent{agentID, taskID, result})
	}

	// Snapshot callback for invocation outside lock
	onComplete := c.onTaskComplete
	var callbacks []callbackInfo

	// Now process completed agents
	for _, ca := range completed {
		task := c.tasks[ca.taskID]
		if task == nil {
			delete(c.running, ca.agentID)
			continue
		}

		// Update task status
		if ca.result.Status == AgentStatusCompleted {
			task.Status = TaskStatusCompleted
			// Record success for learned solutions (feedback loop)
			c.recordReflectionFeedback(ca.result, true)
		} else {
			task.Status = TaskStatusFailed
			// Record failure for learned solutions (feedback loop)
			c.recordReflectionFeedback(ca.result, false)
		}
		task.Result = ca.result

		// Mark completed
		c.completed[ca.taskID] = true
		delete(c.running, ca.agentID)

		logging.Info("coordinator: task completed",
			"task_id", ca.taskID,
			"status", task.Status,
			"duration", ca.result.Duration)

		if onComplete != nil {
			callbacks = append(callbacks, callbackInfo{
				task:   cloneCoordinatedTask(task),
				result: cloneAgentResult(ca.result),
			})
		}

		// Unblock dependent tasks (needs lock — accesses c.tasks, c.dependencies, c.queue)
		c.unblockDependents(ca.taskID)
	}

	c.mu.Unlock()

	// Callbacks OUTSIDE lock
	for _, cb := range callbacks {
		invokeCoordinatorTaskComplete(onComplete, cb.task, cb.result)
	}

}

// recordReflectionFeedback records success/failure for learned error solutions.
func (c *Coordinator) recordReflectionFeedback(result *AgentResult, success bool) {
	if c.reflector == nil {
		return
	}

	// Check if this result used a learned solution (has LearnedEntryID in metadata)
	// The agent would have stored this in the result metadata during error recovery
	if result.Metadata != nil {
		if entryID, ok := result.Metadata["learned_entry_id"].(string); ok && entryID != "" {
			var err error
			if success {
				err = c.reflector.RecordSolutionSuccess(entryID)
			} else {
				err = c.reflector.RecordSolutionFailure(entryID)
			}
			if err != nil {
				logging.Warn("coordinator: failed to record reflection feedback",
					"entry_id", entryID,
					"success", success,
					"error", err)
			} else {
				logging.Debug("coordinator: recorded reflection feedback",
					"entry_id", entryID,
					"success", success)
			}
		}
	}
}

// unblockDependents moves blocked tasks to ready if dependencies are met.
func (c *Coordinator) unblockDependents(completedID string) {
	dependents := c.dependencies[completedID]
	for _, depTaskID := range dependents {
		task := c.tasks[depTaskID]
		if task == nil || task.Status != TaskStatusBlocked {
			continue
		}

		if c.areDependenciesMet(task) {
			task.Status = TaskStatusReady
			c.queue.PushTask(task)

			// Signal that a task is ready (non-blocking)
			select {
			case c.taskReadyCh <- struct{}{}:
			default:
			}

			logging.Debug("coordinator: task unblocked",
				"task_id", depTaskID,
				"unblocked_by", completedID)
		}
	}
}

// handleAgentCompletion handles a single agent completion event.
func (c *Coordinator) handleAgentCompletion(agentID string) {
	c.mu.Lock()

	taskID, ok := c.running[agentID]
	if !ok {
		c.mu.Unlock()
		return
	}

	// See the matching comment in checkCompletedAgents — completedResultLocked
	// avoids the unsynchronized .Completed read GetResult's callers used to do.
	result, ok := c.runner.completedResultLocked(agentID)
	if !ok {
		c.mu.Unlock()
		return
	}

	task := c.tasks[taskID]
	if task == nil {
		c.mu.Unlock()
		return
	}

	// Update task status
	if result.Status == AgentStatusCompleted {
		task.Status = TaskStatusCompleted
	} else {
		task.Status = TaskStatusFailed
	}
	task.Result = result

	// Mark completed
	c.completed[taskID] = true
	delete(c.running, agentID)

	logging.Info("coordinator: task completed",
		"task_id", taskID,
		"status", task.Status,
		"duration", result.Duration)

	// Snapshot callback and unblock dependents under lock
	onComplete := c.onTaskComplete
	c.unblockDependents(taskID)
	callbackTask := cloneCoordinatedTask(task)
	callbackResult := cloneAgentResult(result)
	c.mu.Unlock()

	// Callback OUTSIDE lock
	invokeCoordinatorTaskComplete(onComplete, callbackTask, callbackResult)
}

// isAllComplete checks if all tasks are done.
func (c *Coordinator) isAllComplete() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isAllCompleteLocked()
}

// isAllCompleteLocked reports completion while the caller holds c.mu for
// reading or writing.
func (c *Coordinator) isAllCompleteLocked() bool {
	if len(c.running) > 0 {
		return false
	}

	for _, task := range c.tasks {
		if task.Status != TaskStatusCompleted && task.Status != TaskStatusFailed {
			return false
		}
	}

	return true
}

// notifyAllComplete calls the completion callback.
func (c *Coordinator) notifyAllComplete() {
	// Completion is a durable, broadcast event. Closing completionCh wakes any
	// number of current waiters and remains observable by waiters that arrive
	// after the process loop exits. Wait used to install its own function into
	// onAllComplete, which both overwrote the user's callback and allowed two
	// concurrent waiters to overwrite each other (leaving one blocked forever).
	// Keep notification independent from the optional observer callback.
	c.mu.RLock()
	cb := c.onAllComplete
	results := make(map[string]*AgentResult, len(c.tasks))
	for taskID, task := range c.tasks {
		results[taskID] = cloneAgentResult(task.Result)
	}
	c.mu.RUnlock()

	firstNotification := false
	c.completeOnce.Do(func() {
		close(c.completionCh)
		firstNotification = true
	})
	if !firstNotification {
		return
	}
	invokeCoordinatorAllComplete(cb, results)
}

// Wait blocks until all tasks are complete.
func (c *Coordinator) Wait() map[string]*AgentResult {
	select {
	case <-c.completionCh:
		return c.resultsSnapshot()
	case <-c.ctx.Done():
		return nil
	}
}

// resultsSnapshot returns all task results, including nil for a terminal task
// that produced no result. It is used only after completionCh closes, matching
// the historical notifyAllComplete result shape.
func (c *Coordinator) resultsSnapshot() map[string]*AgentResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]*AgentResult, len(c.tasks))
	for taskID, task := range c.tasks {
		results[taskID] = cloneAgentResult(task.Result)
	}
	return results
}

// partialResults snapshots whatever results are available right now — the
// same c.tasks[taskID].Result data notifyAllComplete uses for the
// all-complete case, but callable at any point (including a task still
// in-flight, whose Result is simply not yet set and is omitted here).
func (c *Coordinator) partialResults() map[string]*AgentResult {
	c.mu.RLock()
	defer c.mu.RUnlock()

	results := make(map[string]*AgentResult, len(c.completed))
	for taskID, task := range c.tasks {
		if task.Result != nil {
			results[taskID] = cloneAgentResult(task.Result)
		}
	}
	return results
}

// WaitWithTimeout waits for completion with a timeout.
func (c *Coordinator) WaitWithTimeout(timeout time.Duration) (map[string]*AgentResult, error) {
	return c.WaitWithTimeoutCtx(context.Background(), timeout)
}

// WaitWithTimeoutCtx is WaitWithTimeout that ALSO unblocks when the CALLER's
// context cancels (the coordinate tool passes the turn ctx, so Esc actually
// interrupts the wait — previously the wait selected only on completion, the
// timer, and the coordinator's own app-lifetime ctx, making a coordinate turn
// un-interruptible by any user action; the /loop CancelInFlight bug class).
func (c *Coordinator) WaitWithTimeoutCtx(ctx context.Context, timeout time.Duration) (map[string]*AgentResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-c.completionCh:
		return c.resultsSnapshot(), nil
	case <-timer.C:
		// Surface whatever tasks DID finish before the deadline instead of
		// discarding them — checkCompletedAgents populates task.Result as
		// each agent finishes, independent of whether all tasks are done, so
		// a 4-of-5 partial completion is real data sitting in c.tasks right
		// now, not just an all-or-nothing outcome.
		return c.partialResults(), fmt.Errorf("coordination timed out after %v", timeout)
	case <-c.ctx.Done():
		return c.partialResults(), c.ctx.Err()
	case <-ctx.Done():
		return c.partialResults(), fmt.Errorf("coordination cancelled: %w", ctx.Err())
	}
}

// CancelRunning cancels every task whose agent is still executing, via the
// runner's per-agent cancel (SpawnAsync registers SetCancelFunc, so the
// agents' contexts really die). No-op when nothing is running — the
// coordinate tool calls this unconditionally on teardown so that an Esc'd or
// timed-out coordination stops burning provider quota in the background
// (previously the spawned agents were unreachable by ANY user action: they
// run on WithoutCancel and the only cancel bridge, CancelTask, had zero
// callers). Returns how many agents were cancelled.
func (c *Coordinator) CancelRunning() int {
	c.mu.RLock()
	ids := make([]string, 0, len(c.running))
	for agentID := range c.running {
		ids = append(ids, agentID)
	}
	c.mu.RUnlock()
	n := 0
	for _, id := range ids {
		if err := c.runner.Cancel(id); err == nil {
			n++
		}
	}
	return n
}

// GetTask returns a caller-owned task snapshot by ID.
func (c *Coordinator) GetTask(taskID string) *CoordinatedTask {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return cloneCoordinatedTask(c.tasks[taskID])
}

// GetTaskAgentID returns the agent ID assigned to a task, if it's currently running.
func (c *Coordinator) GetTaskAgentID(taskID string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for agentID, tid := range c.running {
		if tid == taskID {
			return agentID
		}
	}
	return ""
}

// GetAllTasks returns caller-owned snapshots of all tasks.
func (c *Coordinator) GetAllTasks() []*CoordinatedTask {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tasks := make([]*CoordinatedTask, 0, len(c.tasks))
	for _, task := range c.tasks {
		tasks = append(tasks, cloneCoordinatedTask(task))
	}
	return tasks
}

// GetStatus returns the current status summary.
func (c *Coordinator) GetStatus() *CoordinatorStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := &CoordinatorStatus{
		TotalTasks:     len(c.tasks),
		CompletedTasks: len(c.completed),
		RunningTasks:   len(c.running),
	}

	for _, task := range c.tasks {
		switch task.Status {
		case TaskStatusPending:
			status.PendingTasks++
		case TaskStatusBlocked:
			status.BlockedTasks++
		case TaskStatusReady:
			status.ReadyTasks++
		case TaskStatusFailed:
			status.FailedTasks++
		}
	}

	return status
}

// CoordinatorStatus represents the current state of coordination.
type CoordinatorStatus struct {
	TotalTasks     int
	PendingTasks   int
	BlockedTasks   int
	ReadyTasks     int
	RunningTasks   int
	CompletedTasks int
	FailedTasks    int
}

// SetCallbacks sets callback functions.
func (c *Coordinator) SetCallbacks(
	onStart func(*CoordinatedTask),
	onComplete func(*CoordinatedTask, *AgentResult),
	onAllComplete func(map[string]*AgentResult),
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.onTaskStart = onStart
	c.onTaskComplete = onComplete
	c.onAllComplete = onAllComplete
}

// UIBroadcaster interface for sending task events to UI.
type UIBroadcaster interface {
	BroadcastTaskStarted(taskID, message, planType string)
	BroadcastTaskCompleted(taskID string, success bool, duration time.Duration, err error, planType string)
	BroadcastTaskProgress(taskID string, progress float64, message string)
}

// SetUIBroadcaster sets the UI broadcaster for sending task events.
func (c *Coordinator) SetUIBroadcaster(broadcaster UIBroadcaster) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Wire up callbacks to broadcast to UI
	c.onTaskStart = func(task *CoordinatedTask) {
		if broadcaster != nil {
			broadcaster.BroadcastTaskStarted(task.ID, task.Prompt, string(task.AgentType))
		}
	}

	c.onTaskComplete = func(task *CoordinatedTask, result *AgentResult) {
		if broadcaster != nil {
			var err error
			if result != nil && result.Error != "" {
				err = fmt.Errorf("%s", result.Error)
			}
			success := result != nil && result.Status == AgentStatusCompleted
			duration := time.Duration(0)
			if result != nil {
				duration = result.Duration
			}
			broadcaster.BroadcastTaskCompleted(task.ID, success, duration, err, string(task.AgentType))
		}
	}
}

// CancelTask cancels a specific task.
func (c *Coordinator) CancelTask(taskID string) error {
	c.mu.Lock()

	task := c.tasks[taskID]
	if task == nil {
		c.mu.Unlock()
		return fmt.Errorf("task not found: %s", taskID)
	}
	// Cancellation is idempotent and must not replace a real terminal result.
	if task.Status == TaskStatusCompleted || task.Status == TaskStatusFailed {
		c.mu.Unlock()
		return nil
	}

	// If running, cancel the agent
	cancelledAgentID := ""
	for agentID, tid := range c.running {
		if tid == taskID {
			if err := c.runner.Cancel(agentID); err != nil {
				c.mu.Unlock()
				return err
			}
			cancelledAgentID = agentID
			delete(c.running, agentID)
		}
	}

	// Remove from queue if pending/ready
	c.queue.RemoveTask(taskID)
	task.Status = TaskStatusFailed
	task.Result = &AgentResult{
		AgentID:   cancelledAgentID,
		Type:      task.AgentType,
		Status:    AgentStatusCancelled,
		Error:     "cancelled by coordinator",
		Completed: true,
	}
	c.completed[taskID] = true

	// A cancelled prerequisite is terminal, matching the coordinator's existing
	// failed-agent behavior: dependents become eligible once all prerequisites
	// have reached a terminal state. unblockDependents also wakes processLoop.
	c.unblockDependents(taskID)
	onComplete := c.onTaskComplete
	allComplete := c.isAllCompleteLocked()
	callbackTask := cloneCoordinatedTask(task)
	result := cloneAgentResult(task.Result)
	c.mu.Unlock()

	invokeCoordinatorTaskComplete(onComplete, callbackTask, result)
	if allComplete {
		c.notifyAllComplete()
	}

	return nil
}

// truncate truncates a string to maxLen runes.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
