package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

// TaskStatus represents the current state of a task in the dependency graph.
type TaskStatus int

const (
	TaskStatusPending TaskStatus = iota
	TaskStatusReady
	TaskStatusRunning
	TaskStatusCompleted
	TaskStatusFailed
	TaskStatusSkipped
)

func (s TaskStatus) String() string {
	switch s {
	case TaskStatusPending:
		return "⏳ Pending"
	case TaskStatusReady:
		return "✅ Ready"
	case TaskStatusRunning:
		return "🔄 Running"
	case TaskStatusCompleted:
		return "✓ Completed"
	case TaskStatusFailed:
		return "❌ Failed"
	case TaskStatusSkipped:
		return "⊘ Skipped"
	default:
		return "Unknown"
	}
}

// Task represents a task in the dependency graph.
type DependencyTask struct {
	ID           string
	Message      string
	Priority     int
	Dependencies []string
	Status       TaskStatus
	Error        error
	Result       any
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	Execute      func(ctx context.Context) error
}

// TaskDependencies manages a DAG (Directed Acyclic Graph) of tasks with dependencies.
type TaskDependencies struct {
	tasks    map[string]*DependencyTask
	mu       sync.RWMutex
	taskChan chan *DependencyTask

	// UI callback for status changes
	onStatusChange func(taskID string, status TaskStatus)
}

// NewTaskDependencies creates a new task dependency manager.
func NewTaskDependencies() *TaskDependencies {
	return &TaskDependencies{
		tasks:    make(map[string]*DependencyTask),
		taskChan: make(chan *DependencyTask, 100),
	}
}

// AddTask adds a task to the dependency graph.
func (td *TaskDependencies) AddTask(task *DependencyTask) error {
	td.mu.Lock()
	defer td.mu.Unlock()

	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}

	if _, exists := td.tasks[task.ID]; exists {
		return fmt.Errorf("task with ID %s already exists", task.ID)
	}
	if err := validateDependencyReferences(td.tasks, task.ID, task.Dependencies); err != nil {
		return err
	}

	stored := cloneDependencyTask(task)
	stored.Status = TaskStatusPending
	stored.CreatedAt = time.Now()
	td.tasks[stored.ID] = stored

	return nil
}

// AddTaskWithDependencies adds a task with its dependencies.
func (td *TaskDependencies) AddTaskWithDependencies(task *DependencyTask, dependencies []string) error {
	td.mu.Lock()
	defer td.mu.Unlock()

	if task == nil {
		return fmt.Errorf("task cannot be nil")
	}
	if task.ID == "" {
		return fmt.Errorf("task ID cannot be empty")
	}
	if _, exists := td.tasks[task.ID]; exists {
		return fmt.Errorf("task with ID %s already exists", task.ID)
	}

	// Validate the whole operation before mutating td.tasks. The previous
	// add-then-validate order left a ghost task behind on any invalid edge.
	if err := validateDependencyReferences(td.tasks, task.ID, dependencies); err != nil {
		return err
	}

	stored := cloneDependencyTask(task)
	stored.Dependencies = append([]string(nil), dependencies...)
	stored.Status = TaskStatusPending
	stored.CreatedAt = time.Now()
	td.tasks[stored.ID] = stored
	return nil
}

func validateDependencyReferences(tasks map[string]*DependencyTask, taskID string, dependencies []string) error {
	seen := make(map[string]struct{}, len(dependencies))
	for _, dependencyID := range dependencies {
		if _, exists := tasks[dependencyID]; !exists {
			return fmt.Errorf("dependency %s not found for task %s", dependencyID, taskID)
		}
		if _, duplicate := seen[dependencyID]; duplicate {
			return fmt.Errorf("duplicate dependency %s for task %s", dependencyID, taskID)
		}
		seen[dependencyID] = struct{}{}
	}
	return nil
}

func cloneDependencyTask(task *DependencyTask) *DependencyTask {
	if task == nil {
		return nil
	}
	clone := *task
	clone.Dependencies = append([]string(nil), task.Dependencies...)
	if task.StartedAt != nil {
		startedAt := *task.StartedAt
		clone.StartedAt = &startedAt
	}
	if task.CompletedAt != nil {
		completedAt := *task.CompletedAt
		clone.CompletedAt = &completedAt
	}
	clone.Result = cloneDependencyResult(task.Result)
	return &clone
}

func cloneDependencyResult(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, item := range typed {
			clone[key] = cloneDependencyResult(item)
		}
		return clone
	case map[string]string:
		clone := make(map[string]string, len(typed))
		for key, item := range typed {
			clone[key] = item
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for i, item := range typed {
			clone[i] = cloneDependencyResult(item)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

// GetTask retrieves a caller-owned task snapshot by ID.
func (td *TaskDependencies) GetTask(id string) (*DependencyTask, bool) {
	td.mu.RLock()
	defer td.mu.RUnlock()
	task, exists := td.tasks[id]
	return cloneDependencyTask(task), exists
}

// GetAllTasks returns caller-owned task snapshots.
func (td *TaskDependencies) GetAllTasks() []*DependencyTask {
	td.mu.RLock()
	defer td.mu.RUnlock()

	tasks := make([]*DependencyTask, 0, len(td.tasks))
	for _, task := range td.tasks {
		tasks = append(tasks, cloneDependencyTask(task))
	}
	return tasks
}

// SetOnStatusChange sets the optional status observer.
func (td *TaskDependencies) SetOnStatusChange(callback func(taskID string, status TaskStatus)) {
	td.mu.Lock()
	td.onStatusChange = callback
	td.mu.Unlock()
}

// GetTaskStatus returns the current status of a task.
func (td *TaskDependencies) GetTaskStatus(id string) TaskStatus {
	td.mu.RLock()
	defer td.mu.RUnlock()

	if task, exists := td.tasks[id]; exists {
		return task.Status
	}
	return TaskStatusPending
}

// MarkTaskStatus updates the status of a task.
func (td *TaskDependencies) MarkTaskStatus(id string, status TaskStatus, err error) {
	td.mu.Lock()
	var callback func(string, TaskStatus)
	if task, exists := td.tasks[id]; exists {
		oldStatus := task.Status
		task.Status = status
		task.Error = err
		now := time.Now()

		switch status {
		case TaskStatusRunning:
			task.StartedAt = &now
		case TaskStatusCompleted, TaskStatusFailed, TaskStatusSkipped:
			task.CompletedAt = &now
		}

		// Trigger UI callback if status changed
		if oldStatus != status {
			callback = td.onStatusChange
		}
	}
	td.mu.Unlock()

	invokeDependencyStatusChange(callback, id, status)
}

func invokeDependencyStatusChange(callback func(string, TaskStatus), id string, status TaskStatus) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("task dependency status callback panicked",
				"task_id", id, "status", status.String(),
				"panic", recovered, "stack", logging.PanicStack())
		}
	}()
	callback(id, status)
}

// claimForExecution atomically checks that a task has not already reached a
// non-runnable state and that every prerequisite completed successfully. A
// static topological plan alone is insufficient: an earlier level may have
// failed and marked this task skipped after the plan was built.
func (td *TaskDependencies) claimForExecution(id string) (*DependencyTask, bool) {
	td.mu.Lock()
	task, exists := td.tasks[id]
	if !exists || (task.Status != TaskStatusPending && task.Status != TaskStatusReady) {
		td.mu.Unlock()
		return nil, false
	}

	for _, dependencyID := range task.Dependencies {
		dependency, exists := td.tasks[dependencyID]
		if !exists {
			err := fmt.Errorf("dependency %s not found", dependencyID)
			task.Status = TaskStatusSkipped
			task.Error = err
			now := time.Now()
			task.CompletedAt = &now
			callback := td.onStatusChange
			td.mu.Unlock()
			invokeDependencyStatusChange(callback, id, TaskStatusSkipped)
			return nil, false
		}
		switch dependency.Status {
		case TaskStatusCompleted:
			// This prerequisite is satisfied.
		case TaskStatusFailed, TaskStatusSkipped:
			err := fmt.Errorf("dependency %s did not complete successfully", dependencyID)
			task.Status = TaskStatusSkipped
			task.Error = err
			now := time.Now()
			task.CompletedAt = &now
			callback := td.onStatusChange
			td.mu.Unlock()
			invokeDependencyStatusChange(callback, id, TaskStatusSkipped)
			return nil, false
		default:
			// Another execution may still own this prerequisite. Do not run the
			// dependent early or overwrite either task's current state.
			td.mu.Unlock()
			return nil, false
		}
	}

	task.Status = TaskStatusRunning
	task.Error = nil
	now := time.Now()
	task.StartedAt = &now
	claimed := cloneDependencyTask(task)
	callback := td.onStatusChange
	td.mu.Unlock()
	invokeDependencyStatusChange(callback, id, TaskStatusRunning)
	return claimed, true
}

// skipPendingTask performs a cancellation transition only while this
// execution still owns an unclaimed task. It deliberately leaves Running and
// terminal tasks untouched so concurrent cancellation cannot overwrite their
// executor-owned result.
func (td *TaskDependencies) skipPendingTask(id string, err error) bool {
	td.mu.Lock()
	task, exists := td.tasks[id]
	if !exists || (task.Status != TaskStatusPending && task.Status != TaskStatusReady) {
		td.mu.Unlock()
		return false
	}
	task.Status = TaskStatusSkipped
	task.Error = err
	now := time.Now()
	task.CompletedAt = &now
	callback := td.onStatusChange
	td.mu.Unlock()
	invokeDependencyStatusChange(callback, id, TaskStatusSkipped)
	return true
}

// BuildExecutionOrder builds the execution order using Kahn's algorithm.
// Returns tasks grouped by execution level (tasks in the same level can run in parallel).
func (td *TaskDependencies) BuildExecutionOrder() ([][]string, error) {
	td.mu.RLock()
	defer td.mu.RUnlock()
	return td.buildExecutionOrderLocked()
}

func (td *TaskDependencies) buildExecutionOrderLocked() ([][]string, error) {
	if len(td.tasks) == 0 {
		return nil, nil
	}

	// Calculate in-degree for each task
	inDegree := make(map[string]int)
	adjList := make(map[string][]string)

	for id := range td.tasks {
		inDegree[id] = 0
		adjList[id] = []string{}
	}

	for id, task := range td.tasks {
		for _, dep := range task.Dependencies {
			adjList[dep] = append(adjList[dep], id)
			inDegree[id]++
		}
	}

	// Find tasks with no dependencies (in-degree = 0)
	queue := make([]string, 0)
	for id, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	sort.Strings(queue)

	// Build execution levels
	var levels [][]string
	visited := make(map[string]bool)

	for len(queue) > 0 {
		level := queue
		levels = append(levels, level)
		queue = make([]string, 0)

		for _, taskId := range level {
			if visited[taskId] {
				continue
			}
			visited[taskId] = true

			// Reduce in-degree for dependent tasks
			for _, dependentId := range adjList[taskId] {
				inDegree[dependentId]--
				if inDegree[dependentId] == 0 {
					queue = append(queue, dependentId)
				}
			}
		}
		sort.Strings(queue)
	}

	// Check for cycles
	if len(visited) != len(td.tasks) {
		return nil, fmt.Errorf("cyclic dependency detected")
	}

	return levels, nil
}

// ExecutePlan represents the execution plan for tasks with dependencies.
type ExecutePlan struct {
	Levels         [][]string
	TotalTasks     int
	ExecutionDepth int
	MaxParallel    int
}

// GetPlan returns the execution plan.
func (td *TaskDependencies) GetPlan() (*ExecutePlan, error) {
	td.mu.RLock()
	defer td.mu.RUnlock()
	levels, err := td.buildExecutionOrderLocked()
	if err != nil {
		return nil, err
	}

	if len(levels) == 0 {
		return &ExecutePlan{
			Levels:         [][]string{},
			TotalTasks:     0,
			ExecutionDepth: 0,
			MaxParallel:    0,
		}, nil
	}

	maxParallel := 0
	for _, level := range levels {
		if len(level) > maxParallel {
			maxParallel = len(level)
		}
	}

	return &ExecutePlan{
		Levels:         levels,
		TotalTasks:     len(td.tasks),
		ExecutionDepth: len(levels),
		MaxParallel:    maxParallel,
	}, nil
}

// DependencyStats represents statistics about task dependencies.
type DependencyStats struct {
	TotalTasks      int
	Pending         int
	Ready           int
	Running         int
	Completed       int
	Failed          int
	Skipped         int
	ExecutionLevels int
	HasCycles       bool
}

// GetStats returns statistics about the dependency graph.
func (td *TaskDependencies) GetStats() DependencyStats {
	td.mu.RLock()
	defer td.mu.RUnlock()

	stats := DependencyStats{
		TotalTasks: len(td.tasks),
	}

	for _, task := range td.tasks {
		switch task.Status {
		case TaskStatusPending:
			stats.Pending++
		case TaskStatusReady:
			stats.Ready++
		case TaskStatusRunning:
			stats.Running++
		case TaskStatusCompleted:
			stats.Completed++
		case TaskStatusFailed:
			stats.Failed++
		case TaskStatusSkipped:
			stats.Skipped++
		}
	}

	// Check for cycles
	levels, err := td.buildExecutionOrderLocked()
	stats.HasCycles = err != nil

	// Calculate execution depth
	if err == nil {
		stats.ExecutionLevels = len(levels)
	}

	return stats
}

// DependencyManager manages task dependencies with queue integration.
type DependencyManager struct {
	deps *TaskDependencies
}

// NewDependencyManager creates a new dependency manager.
func NewDependencyManager() *DependencyManager {
	return &DependencyManager{
		deps: NewTaskDependencies(),
	}
}

// AddTask adds a task to the dependency manager.
func (dm *DependencyManager) AddTask(task *DependencyTask) error {
	return dm.deps.AddTask(task)
}

// AddTaskWithDependencies adds a task with dependencies.
func (dm *DependencyManager) AddTaskWithDependencies(task *DependencyTask, dependencies []string) error {
	return dm.deps.AddTaskWithDependencies(task, dependencies)
}

// GetTask retrieves a task by ID.
func (dm *DependencyManager) GetTask(id string) (*DependencyTask, bool) {
	return dm.deps.GetTask(id)
}

// GetPlan returns the execution plan.
func (dm *DependencyManager) GetPlan() (*ExecutePlan, error) {
	return dm.deps.GetPlan()
}

// GetStats returns statistics.
func (dm *DependencyManager) GetStats() DependencyStats {
	return dm.deps.GetStats()
}

// SetOnStatusChange sets the optional task-status observer.
func (dm *DependencyManager) SetOnStatusChange(callback func(taskID string, status TaskStatus)) {
	dm.deps.SetOnStatusChange(callback)
}

// ExecuteDependencies executes tasks respecting dependencies.
// maxParallel specifies the maximum number of tasks to run concurrently per level.
func (dm *DependencyManager) ExecuteDependencies(ctx context.Context, maxParallel int) error {
	if maxParallel <= 0 {
		return fmt.Errorf("maxParallel must be greater than zero")
	}
	levels, err := dm.deps.BuildExecutionOrder()
	if err != nil {
		return fmt.Errorf("failed to build execution order: %w", err)
	}

	logging.Info("executing tasks with dependencies",
		"total_levels", len(levels),
		"total_tasks", dm.deps.GetStats().TotalTasks,
		"max_parallel", maxParallel)
	var executionErrors []error

	for levelIdx, level := range levels {
		logging.Info("starting execution level",
			"level", levelIdx+1,
			"tasks", len(level))

		// Execute tasks in this level in parallel
		semaphore := make(chan struct{}, maxParallel)
		var wg sync.WaitGroup
		var errorsMu sync.Mutex
		levelErrors := make([]error, 0)

		for _, taskID := range level {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				recordError := func(err error) {
					errorsMu.Lock()
					levelErrors = append(levelErrors, err)
					errorsMu.Unlock()
				}

				// Do not leave cancelled work blocked behind the parallelism
				// semaphore. The second check closes the race where cancellation
				// happens concurrently with a successful acquire.
				if err := ctx.Err(); err != nil {
					if dm.deps.skipPendingTask(id, err) {
						recordError(err)
					}
					return
				}
				select {
				case semaphore <- struct{}{}:
				case <-ctx.Done():
					err := ctx.Err()
					if dm.deps.skipPendingTask(id, err) {
						recordError(err)
					}
					return
				}
				defer func() { <-semaphore }()
				if err := ctx.Err(); err != nil {
					if dm.deps.skipPendingTask(id, err) {
						recordError(err)
					}
					return
				}

				// Recover from a panic in task.Execute so one bad task
				// doesn't crash the whole process. Marks the task failed
				// and records the panic as an error so callers waiting
				// on this level don't see "running forever".
				var execErr error
				defer func() {
					if r := recover(); r != nil {
						execErr = fmt.Errorf("task %s panicked: %v", id, r)
						logging.Error("task panic recovered",
							"id", id,
							"panic", r)
						dm.deps.MarkTaskStatus(id, TaskStatusFailed, execErr)
						recordError(execErr)
					}
				}()

				task, claimed := dm.deps.claimForExecution(id)
				if !claimed {
					logging.Debug("task is not runnable; preserving current status", "id", id)
					return
				}

				logging.Debug("executing task",
					"id", id,
					"message", task.Message)

				// Execute task
				var err error
				if task.Execute != nil {
					err = task.Execute(ctx)
				}

				if err != nil {
					logging.Warn("task failed",
						"id", id,
						"error", err)
					dm.deps.MarkTaskStatus(id, TaskStatusFailed, err)
					recordError(err)
				} else {
					logging.Debug("task completed",
						"id", id)
					dm.deps.MarkTaskStatus(id, TaskStatusCompleted, nil)
				}
			}(taskID)
		}

		// Wait for all tasks in this level to complete
		wg.Wait()

		// If any task failed, mark dependent tasks as skipped
		if len(levelErrors) > 0 {
			executionErrors = append(executionErrors, levelErrors...)
			dm.markDependentTasksSkipped(level)
		}

		logging.Info("execution level completed",
			"level", levelIdx+1,
			"failed", len(levelErrors))
	}

	return errors.Join(executionErrors...)
}

// markDependentTasksSkipped marks all tasks that depend on failed tasks as skipped.
func (dm *DependencyManager) markDependentTasksSkipped(currentLevel []string) {
	// Find all failed tasks in current level
	failedTasks := make(map[string]bool)
	for _, taskID := range currentLevel {
		if task, exists := dm.deps.GetTask(taskID); exists {
			if task.Status == TaskStatusFailed {
				failedTasks[taskID] = true
			}
		}
	}

	if len(failedTasks) == 0 {
		return
	}

	// Mark all tasks that depend on failed tasks as skipped
	for _, task := range dm.deps.GetAllTasks() {
		if task.Status == TaskStatusPending {
			for _, dep := range task.Dependencies {
				if failedTasks[dep] {
					logging.Info("marking task as skipped due to failed dependency",
						"id", task.ID,
						"failed_dependency", dep)
					dm.deps.MarkTaskStatus(task.ID, TaskStatusSkipped, fmt.Errorf("dependency %s failed", dep))
					break
				}
			}
		}
	}
}
