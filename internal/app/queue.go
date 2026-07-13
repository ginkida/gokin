package app

import (
	"container/heap"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"gokin/internal/logging"
)

// Priority represents the priority level of a task.
type Priority int

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
)

// String returns the string representation of the priority.
func (p Priority) String() string {
	switch p {
	case PriorityHigh:
		return "HIGH"
	case PriorityNormal:
		return "NORMAL"
	case PriorityLow:
		return "LOW"
	default:
		return "UNKNOWN"
	}
}

// QueueTask represents a queued task with priority.
type QueueTask struct {
	ID        string
	Message   string
	Priority  Priority
	CreatedAt time.Time
	Context   context.Context
	// Callback to execute the task
	Execute func(ctx context.Context) error
	// Callback for completion
	OnComplete func(error)
	// Index for heap (needed by container/heap)
	index    int
	sequence int
}

// priorityQueue implements heap.Interface and holds Tasks.
type priorityQueue []*QueueTask

func (pq priorityQueue) Len() int { return len(pq) }

func (pq priorityQueue) Less(i, j int) bool {
	// Higher priority comes first
	if pq[i].Priority != pq[j].Priority {
		return pq[i].Priority > pq[j].Priority
	}
	// If same priority, older tasks come first (FIFO)
	if !pq[i].CreatedAt.Equal(pq[j].CreatedAt) {
		return pq[i].CreatedAt.Before(pq[j].CreatedAt)
	}
	return pq[i].sequence < pq[j].sequence
}

func (pq priorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *priorityQueue) Push(x any) {
	n := len(*pq)
	item := x.(*QueueTask)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *priorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	*pq = old[0 : n-1]
	return item
}

// QueueManager manages prioritized task queue.
type QueueManager struct {
	queue priorityQueue

	// Task tracking
	tasks       map[string]*QueueTask
	taskCounter int
	mu          sync.RWMutex

	// Processing
	processing     bool
	currentTask    *QueueTask
	queueWake      chan struct{}
	processorToken chan struct{}

	// Configuration
	maxQueueSize int
	enabled      bool

	// Metrics
	totalQueued       int
	totalProcessed    int
	totalDropped      int
	highPriorityCount int
	totalWaitTime     time.Duration

	// Callbacks
	onTaskStart    func(task *QueueTask)
	onTaskComplete func(task *QueueTask, err error)
}

// NewQueueManager creates a new queue manager.
func NewQueueManager(maxQueueSize int) *QueueManager {
	if maxQueueSize <= 0 {
		maxQueueSize = 1
	}
	qm := &QueueManager{
		queue:          make(priorityQueue, 0),
		tasks:          make(map[string]*QueueTask),
		queueWake:      make(chan struct{}, 1),
		processorToken: make(chan struct{}, 1),
		maxQueueSize:   maxQueueSize,
		enabled:        true,
	}
	qm.processorToken <- struct{}{}
	heap.Init(&qm.queue)
	return qm
}

func (qm *QueueManager) notifyWork() {
	select {
	case qm.queueWake <- struct{}{}:
	default:
	}
}

func cloneQueueTask(task *QueueTask) *QueueTask {
	if task == nil {
		return nil
	}
	clone := *task
	clone.index = -1
	return &clone
}

func invokeQueueTaskStart(callback func(*QueueTask), task *QueueTask) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("queue start callback panicked",
				"task_id", task.ID, "panic", recovered, "stack", logging.PanicStack())
		}
	}()
	callback(cloneQueueTask(task))
}

func invokeQueueTaskComplete(callback func(*QueueTask, error), task *QueueTask, err error) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("queue completion callback panicked",
				"task_id", task.ID, "panic", recovered, "stack", logging.PanicStack())
		}
	}()
	callback(cloneQueueTask(task), err)
}

func invokeQueueTaskSpecificComplete(callback func(error), taskID string, err error) {
	if callback == nil {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("queue task completion callback panicked",
				"task_id", taskID, "panic", recovered, "stack", logging.PanicStack())
		}
	}()
	callback(err)
}

func executeQueueTask(ctx context.Context, taskID string, execute func(context.Context) error) (err error) {
	if execute == nil {
		return fmt.Errorf("queued task %s has no execute callback", taskID)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("queued task %s panicked: %v", taskID, recovered)
			logging.Error("queued task panicked",
				"task_id", taskID, "panic", recovered, "stack", logging.PanicStack())
		}
	}()
	return execute(ctx)
}

// Enabled returns whether the queue is enabled.
func (qm *QueueManager) Enabled() bool {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.enabled
}

// SetEnabled enables or disables the queue.
func (qm *QueueManager) SetEnabled(enabled bool) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.enabled = enabled
}

// Enqueue adds a task to the queue with the given priority.
// Returns the task ID and an error if the queue is full.
func (qm *QueueManager) Enqueue(ctx context.Context, message string, priority Priority, execute func(context.Context) error, onComplete func(error)) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if execute == nil {
		return "", fmt.Errorf("execute callback cannot be nil")
	}
	if !qm.Enabled() {
		go func(ctx context.Context) {
			err := executeQueueTask(ctx, "direct", execute)
			invokeQueueTaskSpecificComplete(onComplete, "direct", err)
		}(ctx)
		return "direct", nil
	}

	qm.mu.Lock()
	var evicted *QueueTask
	var evictionErr error

	// Check queue size
	if qm.queue.Len() >= qm.maxQueueSize {
		if priority != PriorityHigh {
			qm.totalDropped++
			qm.mu.Unlock()
			return "", fmt.Errorf("queue full (max %d), task dropped", qm.maxQueueSize)
		}

		// High priority may preempt only lower-priority work. Scan the heap
		// because its last element is not guaranteed to be the worst item.
		worst := -1
		for i, queued := range qm.queue {
			if queued.Priority >= priority {
				continue
			}
			if worst == -1 || queued.Priority < qm.queue[worst].Priority ||
				(queued.Priority == qm.queue[worst].Priority &&
					(queued.CreatedAt.After(qm.queue[worst].CreatedAt) ||
						(queued.CreatedAt.Equal(qm.queue[worst].CreatedAt) && queued.sequence > qm.queue[worst].sequence))) {
				worst = i
			}
		}
		if worst == -1 {
			qm.totalDropped++
			qm.mu.Unlock()
			return "", fmt.Errorf("queue full (max %d), no lower-priority task can be preempted", qm.maxQueueSize)
		}
		evicted = heap.Remove(&qm.queue, worst).(*QueueTask)
		delete(qm.tasks, evicted.ID)
		qm.totalDropped++
		evictionErr = fmt.Errorf("task preempted by high-priority queue item")
	}

	// Create task
	qm.taskCounter++
	taskID := fmt.Sprintf("queue_%d_%d", time.Now().Unix(), qm.taskCounter)

	task := &QueueTask{
		ID:         taskID,
		Message:    message,
		Priority:   priority,
		CreatedAt:  time.Now(),
		Context:    ctx,
		Execute:    execute,
		OnComplete: onComplete,
		sequence:   qm.taskCounter,
	}

	qm.tasks[taskID] = task
	heap.Push(&qm.queue, task)
	qm.totalQueued++

	if priority == PriorityHigh {
		qm.highPriorityCount++
	}
	onTaskComplete := qm.onTaskComplete
	qm.mu.Unlock()
	qm.notifyWork()

	if evicted != nil {
		invokeQueueTaskComplete(onTaskComplete, evicted, evictionErr)
		invokeQueueTaskSpecificComplete(evicted.OnComplete, evicted.ID, evictionErr)
	}

	return taskID, nil
}

// Dequeue removes and returns the highest priority task.
// Returns nil if the queue is empty.
func (qm *QueueManager) Dequeue() *QueueTask {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	if qm.queue.Len() == 0 {
		return nil
	}

	task := heap.Pop(&qm.queue).(*QueueTask)
	delete(qm.tasks, task.ID)
	return task
}

// Peek returns the highest priority task without removing it.
// Returns nil if the queue is empty.
func (qm *QueueManager) Peek() *QueueTask {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	if qm.queue.Len() == 0 {
		return nil
	}

	return cloneQueueTask(qm.queue[0])
}

// GetTask returns a task by ID.
func (qm *QueueManager) GetTask(id string) (*QueueTask, bool) {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	task, ok := qm.tasks[id]
	return cloneQueueTask(task), ok
}

// Remove removes a task from the queue by ID.
// Returns true if the task was found and removed.
func (qm *QueueManager) Remove(id string) bool {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	task, ok := qm.tasks[id]
	if !ok {
		return false
	}

	// Remove from heap
	heap.Remove(&qm.queue, task.index)
	delete(qm.tasks, id)

	return true
}

// Len returns the number of tasks in the queue.
func (qm *QueueManager) Len() int {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.queue.Len()
}

// Clear removes all tasks from the queue.
func (qm *QueueManager) Clear() {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	qm.queue = make(priorityQueue, 0)
	qm.tasks = make(map[string]*QueueTask)
	heap.Init(&qm.queue)
}

// GetStats returns queue statistics.
type QueueStats struct {
	Length            int
	TotalQueued       int
	TotalProcessed    int
	TotalDropped      int
	HighPriorityCount int
	AverageWaitTime   time.Duration
}

func (qm *QueueManager) GetStats() QueueStats {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	stats := QueueStats{
		Length:            qm.queue.Len(),
		TotalQueued:       qm.totalQueued,
		TotalProcessed:    qm.totalProcessed,
		TotalDropped:      qm.totalDropped,
		HighPriorityCount: qm.highPriorityCount,
	}
	if qm.totalProcessed > 0 {
		stats.AverageWaitTime = qm.totalWaitTime / time.Duration(qm.totalProcessed)
	}
	return stats
}

// SetCallbacks sets the callbacks for task lifecycle events.
func (qm *QueueManager) SetCallbacks(onStart func(*QueueTask), onComplete func(*QueueTask, error)) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	qm.onTaskStart = onStart
	qm.onTaskComplete = onComplete
}

// ProcessQueue processes tasks from the queue in priority order.
// This should be run as a goroutine.
func (qm *QueueManager) ProcessQueue(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	// QueueManager exposes one currentTask/processing lifecycle, so only one
	// processor may own it at a time. A channel token lets replacement workers
	// wait without losing cancellation while the previous worker winds down.
	select {
	case <-ctx.Done():
		return
	case <-qm.processorToken:
	}
	defer func() { qm.processorToken <- struct{}{} }()

	for {
		if ctx.Err() != nil {
			return
		}

		task := qm.Dequeue()
		if task == nil {
			select {
			case <-ctx.Done():
				return
			case <-qm.queueWake:
				continue
			}
		}

		// Process the task
		qm.processTask(ctx, task)
	}
}

// processTask executes a single task.
func (qm *QueueManager) processTask(ctx context.Context, task *QueueTask) {
	qm.mu.Lock()
	qm.processing = true
	qm.currentTask = task
	onStart := qm.onTaskStart
	onComplete := qm.onTaskComplete
	qm.mu.Unlock()
	waitTime := time.Since(task.CreatedAt)
	defer func() {
		qm.mu.Lock()
		qm.processing = false
		qm.currentTask = nil
		qm.totalProcessed++
		qm.totalWaitTime += waitTime
		qm.mu.Unlock()
	}()

	invokeQueueTaskStart(onStart, task)

	taskCtx := task.Context
	if taskCtx == nil {
		taskCtx = context.Background()
	}
	execCtx, cancel := context.WithCancelCause(taskCtx)
	stopProcessCancellation := context.AfterFunc(ctx, func() { cancel(ctx.Err()) })
	if err := ctx.Err(); err != nil {
		cancel(err)
	}
	defer func() {
		stopProcessCancellation()
		cancel(nil)
	}()

	var err error
	if execCtx.Err() != nil {
		err = context.Cause(execCtx)
	} else {
		err = executeQueueTask(execCtx, task.ID, task.Execute)
	}

	invokeQueueTaskComplete(onComplete, task, err)
	invokeQueueTaskSpecificComplete(task.OnComplete, task.ID, err)
}

// GetCurrentTask returns the currently processing task.
func (qm *QueueManager) GetCurrentTask() *QueueTask {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return cloneQueueTask(qm.currentTask)
}

// IsProcessing returns whether a task is currently being processed.
func (qm *QueueManager) IsProcessing() bool {
	qm.mu.RLock()
	defer qm.mu.RUnlock()
	return qm.processing
}

// ListTasks returns all tasks in priority order.
func (qm *QueueManager) ListTasks() []*QueueTask {
	qm.mu.RLock()
	defer qm.mu.RUnlock()

	tasks := make([]*QueueTask, 0, qm.queue.Len())
	for _, task := range qm.queue {
		tasks = append(tasks, cloneQueueTask(task))
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority > tasks[j].Priority
		}
		if !tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
		}
		return tasks[i].sequence < tasks[j].sequence
	})

	return tasks
}
