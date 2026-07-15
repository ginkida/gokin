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
	completed map[string]bool   // terminal taskIDs (successful, failed, or skipped)

	// Callbacks
	onTaskStart    func(task *CoordinatedTask)
	onTaskComplete func(task *CoordinatedTask, result *AgentResult)
	onAllComplete  func(results map[string]*AgentResult)

	// Event-driven channels for efficient processing
	taskReadyCh chan struct{}             // Signals when a task becomes ready
	agentDoneCh chan coordinatorAgentDone // Carries the waiter's owned result snapshot
	// completionCh is the durable task-state completion event used by Wait.
	// It closes as soon as the sealed graph is terminal, before observer
	// callbacks necessarily return. onAllComplete has the stronger contract:
	// it runs only after every registered onTaskComplete callback is delivered.
	completionCh    chan struct{}
	completeOnce    sync.Once
	allCompleteOnce sync.Once
	startOnce       sync.Once
	sealed          bool // graph is immutable after Start begins
	// pendingCallbacks keeps all-complete behind task-complete delivery even
	// when callbacks re-enter the coordinator and mutate/cancel other tasks.
	pendingCallbacks int

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

// coordinatorAgentDone transfers Runner.WaitWithContext's immutable completion
// snapshot directly to the coordinator. Re-reading Runner.results after Wait
// would reopen an eviction window: cleanup may legitimately remove the shared
// entry once the waiter has consumed its pinned snapshot.
type coordinatorAgentDone struct {
	agentID string
	result  *AgentResult
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
		taskReadyCh:  make(chan struct{}, 100),             // Buffered to avoid blocking
		agentDoneCh:  make(chan coordinatorAgentDone, 100), // Buffered for agent completions
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
	if c.sealed {
		c.mu.Unlock()
		logging.Warn("coordinator: rejected task added after Start", "agent_type", agentType)
		return ""
	}

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

	var completion *coordinatorTaskCompletion
	if failedID, failedTask, failed := c.failedDependencyLocked(task); failed {
		result := c.failTaskForDependencyLocked(task, failedID, failedTask)
		completion = &coordinatorTaskCompletion{
			task: cloneCoordinatedTask(task), result: cloneAgentResult(result),
		}
	} else if c.areDependenciesMet(task) {
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
	onComplete := c.onTaskComplete
	if completion != nil {
		c.pendingCallbacks++
	}
	c.mu.Unlock()

	if completion != nil {
		c.deliverTaskCompletions(onComplete, []coordinatorTaskCompletion{*completion})
	}

	return taskID
}

// areDependenciesMet checks if all dependencies completed successfully.
// c.completed tracks terminal tasks for accounting, so it cannot be used as
// the scheduling predicate: failed and cancelled tasks are terminal too.
func (c *Coordinator) areDependenciesMet(task *CoordinatedTask) bool {
	for _, depID := range task.Dependencies {
		dependency := c.tasks[depID]
		if dependency == nil || dependency.Status != TaskStatusCompleted {
			return false
		}
	}
	return true
}

// Start begins processing tasks.
func (c *Coordinator) Start() {
	c.startOnce.Do(func() {
		// Seal the graph before launching the loop. AddTask and Start serialize on
		// the same lock, so a task is either fully part of this run or rejected.
		c.mu.Lock()
		c.sealed = true
		c.mu.Unlock()
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

		case done := <-c.agentDoneCh:
			// An agent completed - handle it
			c.handleAgentCompletionResult(done.agentID, done.result)

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
	c.deliverTaskCompletions(onComplete, failed)
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

// deliverTaskCompletions drains a batch that was registered in
// pendingCallbacks while coordinator state was locked. Task-state completion
// is published before invoking observers: an onTaskComplete callback may call
// Wait without waiting on its own return. The final decrement is the safe
// point for onAllComplete: every task callback in this batch (including
// re-entrant nested batches) has returned.
func (c *Coordinator) deliverTaskCompletions(callback func(*CoordinatedTask, *AgentResult), completions []coordinatorTaskCompletion) {
	if len(completions) == 0 {
		return
	}

	// Wait observes committed task state, not callback delivery. In particular,
	// do this before entering user code so a final task callback can safely call
	// Wait instead of forming a cycle with pendingCallbacks.
	c.publishCompletionIfReady()

	for _, completion := range completions {
		invokeCoordinatorTaskComplete(callback, completion.task, completion.result)
	}

	c.mu.Lock()
	c.pendingCallbacks -= len(completions)
	if c.pendingCallbacks < 0 {
		// Defensive invariant guard: never let accounting corruption publish an
		// early all-complete event.
		logging.Error("coordinator: completion callback accounting underflow",
			"pending", c.pendingCallbacks, "delivered", len(completions))
		c.pendingCallbacks = 0
	}
	ready := c.sealed && c.isAllCompleteLocked()
	c.mu.Unlock()
	if ready {
		c.notifyAllComplete()
	}
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
			failed = append(failed, c.resolveDependentsLocked(task.ID)...)
			continue
		}
		started = append(started, cloneCoordinatedTask(task))
		availableSlots--
	}
	c.pendingCallbacks += len(failed)
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
		result, err := c.runner.WaitWithContext(c.ctx, agentID)
		if err != nil && c.ctx.Err() == nil {
			result = &AgentResult{
				AgentID:   agentID,
				Status:    AgentStatusFailed,
				Error:     fmt.Sprintf("agent completion unavailable: %v", err),
				Completed: true,
			}
		}
		if result != nil {
			select {
			case c.agentDoneCh <- coordinatorAgentDone{agentID: agentID, result: cloneAgentResult(result)}:
			case <-c.ctx.Done():
			}
		}
	}()
	return nil
}

// checkCompletedAgents checks for completed agents and updates tasks.
func (c *Coordinator) checkCompletedAgents() {
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
	var callbacks []coordinatorTaskCompletion

	// Now process completed agents
	for _, ca := range completed {
		task := c.tasks[ca.taskID]
		if task == nil {
			delete(c.running, ca.agentID)
			continue
		}

		// Update task status
		if ca.result.IsSuccess() {
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

		callbacks = append(callbacks, coordinatorTaskCompletion{
			task:   cloneCoordinatedTask(task),
			result: cloneAgentResult(ca.result),
		})

		// Resolve dependent tasks while state is locked. Notifications are
		// returned and delivered only after the lock is released.
		callbacks = append(callbacks, c.resolveDependentsLocked(ca.taskID)...)
	}
	c.pendingCallbacks += len(callbacks)

	c.mu.Unlock()

	// Callbacks OUTSIDE lock
	c.deliverTaskCompletions(onComplete, callbacks)

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

// failedDependencyLocked returns the first prerequisite that cannot satisfy
// task. The caller holds c.mu.
func (c *Coordinator) failedDependencyLocked(task *CoordinatedTask) (string, *CoordinatedTask, bool) {
	for _, dependencyID := range task.Dependencies {
		dependency := c.tasks[dependencyID]
		if dependency == nil || dependency.Status == TaskStatusFailed {
			return dependencyID, dependency, true
		}
	}
	return "", nil, false
}

// failTaskForDependencyLocked commits a terminal result for work that was
// never started because one of its prerequisites failed. The caller holds
// c.mu.
func (c *Coordinator) failTaskForDependencyLocked(task *CoordinatedTask, dependencyID string, dependency *CoordinatedTask) *AgentResult {
	dependencyStatus := string(AgentStatusFailed)
	dependencyError := ""
	if dependency == nil {
		dependencyStatus = "missing"
	} else if dependency.Result != nil {
		if dependency.Result.Status != "" {
			dependencyStatus = string(dependency.Result.Status)
		}
		dependencyError = dependency.Result.Error
	}

	reason := fmt.Sprintf("dependency %q failed", dependencyID)
	if dependency == nil {
		reason = fmt.Sprintf("dependency %q does not exist", dependencyID)
	} else if dependencyStatus == string(AgentStatusCancelled) {
		reason = fmt.Sprintf("dependency %q was cancelled", dependencyID)
	}
	if dependencyError != "" {
		reason += ": " + truncate(dependencyError, 240)
	}

	result := &AgentResult{
		Type:      task.AgentType,
		Status:    AgentStatusFailed,
		Error:     reason,
		Completed: true,
		Metadata: map[string]any{
			"dependency_id":     dependencyID,
			"dependency_status": dependencyStatus,
		},
	}
	task.Status = TaskStatusFailed
	task.Result = result
	c.completed[task.ID] = true
	return result
}

// resolveDependentsLocked advances the graph after a task reaches a terminal
// state. Successful prerequisites can make blocked work ready. A failed or
// cancelled prerequisite makes that work impossible, so it is failed without
// spawning an agent and the failure is propagated iteratively through all
// descendants. The caller holds c.mu; returned callbacks must be invoked only
// after releasing it.
func (c *Coordinator) resolveDependentsLocked(terminalID string) []coordinatorTaskCompletion {
	pending := append([]string(nil), c.dependencies[terminalID]...)
	var completions []coordinatorTaskCompletion

	for i := 0; i < len(pending); i++ {
		depTaskID := pending[i]
		task := c.tasks[depTaskID]
		if task == nil || task.Status != TaskStatusBlocked {
			continue
		}

		if failedID, failedTask, failed := c.failedDependencyLocked(task); failed {
			result := c.failTaskForDependencyLocked(task, failedID, failedTask)
			completions = append(completions, coordinatorTaskCompletion{
				task: cloneCoordinatedTask(task), result: cloneAgentResult(result),
			})
			pending = append(pending, c.dependencies[depTaskID]...)

			logging.Info("coordinator: task skipped after dependency failure",
				"task_id", depTaskID,
				"dependency_id", failedID,
				"error", result.Error)
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
				"unblocked_by", terminalID)
		}
	}

	return completions
}

// handleAgentCompletion handles a single agent completion event.
func (c *Coordinator) handleAgentCompletion(agentID string) {
	if c.runner == nil {
		return
	}
	result, ok := c.runner.completedResultLocked(agentID)
	if !ok {
		return
	}
	c.handleAgentCompletionResult(agentID, result)
}

// handleAgentCompletionResult commits the exact result snapshot owned by the
// Runner waiter. It must never re-read Runner.results: that shared history is
// evictable immediately after WaitWithContext returns.
func (c *Coordinator) handleAgentCompletionResult(agentID string, result *AgentResult) {
	c.mu.Lock()

	taskID, ok := c.running[agentID]
	if !ok {
		c.mu.Unlock()
		return
	}

	if result == nil || !result.Completed {
		c.mu.Unlock()
		return
	}

	task := c.tasks[taskID]
	if task == nil {
		c.mu.Unlock()
		return
	}

	// Update task status
	if result.IsSuccess() {
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

	// Snapshot callback and resolve dependents under lock.
	onComplete := c.onTaskComplete
	callbacks := []coordinatorTaskCompletion{{
		task: cloneCoordinatedTask(task), result: cloneAgentResult(result),
	}}
	callbacks = append(callbacks, c.resolveDependentsLocked(taskID)...)
	c.pendingCallbacks += len(callbacks)
	c.mu.Unlock()

	// Callbacks OUTSIDE lock
	c.deliverTaskCompletions(onComplete, callbacks)
}

// isAllComplete reports full coordinator quiescence: all tasks are terminal
// and all task-complete callbacks have returned.
func (c *Coordinator) isAllComplete() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isAllCompleteLocked()
}

// isAllCompleteLocked reports completion while the caller holds c.mu for
// reading or writing.
func (c *Coordinator) isAllCompleteLocked() bool {
	if len(c.running) > 0 || c.pendingCallbacks > 0 {
		return false
	}
	return c.allTasksTerminalLocked()
}

// allTasksTerminalLocked reports the durable coordination state independently
// of observer delivery. The caller holds c.mu for reading or writing.
func (c *Coordinator) allTasksTerminalLocked() bool {
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

// publishCompletionIfReady publishes the durable event consumed by Wait. It
// deliberately ignores pendingCallbacks: callbacks observe already-committed
// task state and are allowed to call Wait themselves.
func (c *Coordinator) publishCompletionIfReady() {
	c.mu.Lock()
	if c.sealed && c.allTasksTerminalLocked() {
		c.completeOnce.Do(func() {
			close(c.completionCh)
		})
	}
	c.mu.Unlock()
}

// notifyAllComplete publishes task-state completion (if needed) and invokes
// the all-complete observer once task-complete delivery is fully drained.
func (c *Coordinator) notifyAllComplete() {
	// Completion is a durable, broadcast event. Closing completionCh wakes any
	// number of current waiters and remains observable by waiters that arrive
	// after the process loop exits. Wait used to install its own function into
	// onAllComplete, which both overwrote the user's callback and allowed two
	// concurrent waiters to overwrite each other (leaving one blocked forever).
	// Keep notification independent from the optional observer callback.
	c.mu.Lock()
	// Wait may already have been released by publishCompletionIfReady, but the
	// all-complete observer is held back until task callbacks have returned.
	if !c.sealed || !c.allTasksTerminalLocked() || c.pendingCallbacks > 0 {
		c.mu.Unlock()
		return
	}
	cb := c.onAllComplete
	results := make(map[string]*AgentResult, len(c.tasks))
	for taskID, task := range c.tasks {
		results[taskID] = cloneAgentResult(task.Result)
	}

	firstNotification := false
	c.completeOnce.Do(func() {
		close(c.completionCh)
	})
	c.allCompleteOnce.Do(func() {
		firstNotification = true
	})
	c.mu.Unlock()
	if !firstNotification {
		return
	}
	invokeCoordinatorAllComplete(cb, results)
}

// Wait blocks until the sealed task graph reaches terminal state. Observer
// callbacks may still be returning; onAllComplete is the notification that
// task-complete callback delivery has also drained.
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
	return c.cancelRunningIDs()
}

func (c *Coordinator) cancelRunningIDs() int {
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

// CancelRunningAndWait requests cancellation for every running agent and then
// waits, under one caller-supplied deadline, until Runner publishes their real
// terminal results. This is the teardown boundary used by ephemeral coordinate
// calls: returning immediately after Cancel can overlap the next foreground
// step with tool/workspace cleanup still executing in orphaned goroutines.
func (c *Coordinator) CancelRunningAndWait(ctx context.Context) int {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.RLock()
	ids := make([]string, 0, len(c.running))
	for agentID := range c.running {
		ids = append(ids, agentID)
	}
	runner := c.runner
	c.mu.RUnlock()
	if runner == nil {
		return 0
	}

	cancelled := 0
	for _, id := range ids {
		if err := runner.Cancel(id); err == nil {
			cancelled++
		}
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		// The coordinator monitor may also be waiting on this ID. Runner waiters
		// are lossless multi-consumers, and an already-cleaned result reports
		// ErrAgentResultUnavailable immediately rather than hanging teardown.
		_, _ = runner.WaitWithContext(ctx, id)
	}
	return cancelled
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

	// A running task remains running until the Runner publishes its real final
	// result after tool/workspace cleanup. Cancellation is only a request. If
	// the result won the race and is already published, consume it now rather
	// than overwriting success/failure with a synthetic cancellation.
	for agentID, tid := range c.running {
		if tid != taskID {
			continue
		}
		if c.runner == nil {
			c.mu.Unlock()
			return fmt.Errorf("agent runner is not configured")
		}
		if result, published := c.runner.completedResultLocked(agentID); published {
			if result.IsSuccess() {
				task.Status = TaskStatusCompleted
				c.recordReflectionFeedback(result, true)
			} else {
				task.Status = TaskStatusFailed
				c.recordReflectionFeedback(result, false)
			}
			task.Result = result
			c.completed[taskID] = true
			delete(c.running, agentID)
			callbacks := []coordinatorTaskCompletion{{
				task: cloneCoordinatedTask(task), result: cloneAgentResult(result),
			}}
			callbacks = append(callbacks, c.resolveDependentsLocked(taskID)...)
			c.pendingCallbacks += len(callbacks)
			onComplete := c.onTaskComplete
			c.mu.Unlock()
			c.deliverTaskCompletions(onComplete, callbacks)
			return nil
		}
		if err := c.runner.Cancel(agentID); err != nil {
			c.mu.Unlock()
			return err
		}
		c.mu.Unlock()
		return nil
	}

	// A task that never started has no cleanup owner, so cancellation can become
	// terminal immediately.
	c.queue.RemoveTask(taskID)
	task.Status = TaskStatusFailed
	task.Result = &AgentResult{
		Type:      task.AgentType,
		Status:    AgentStatusCancelled,
		Error:     "cancelled by coordinator",
		Completed: true,
	}
	c.completed[taskID] = true

	callbacks := []coordinatorTaskCompletion{{
		task: cloneCoordinatedTask(task), result: cloneAgentResult(task.Result),
	}}
	callbacks = append(callbacks, c.resolveDependentsLocked(taskID)...)
	c.pendingCallbacks += len(callbacks)
	onComplete := c.onTaskComplete
	c.mu.Unlock()

	c.deliverTaskCompletions(onComplete, callbacks)

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
