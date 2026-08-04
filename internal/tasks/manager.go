package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"

	"gokin/internal/fileutil"
	"gokin/internal/logging"
)

// CompletionHandler is called when a task completes.
type CompletionHandler func(task *Task)

// Manager manages background tasks.
type Manager struct {
	tasks   map[string]*Task
	workDir string
	counter int

	onComplete CompletionHandler

	mu sync.RWMutex
}

// NewManager creates a new task manager.
func NewManager(workDir string) *Manager {
	sweepStaleTaskOutputFiles(workDir)
	return &Manager{
		tasks:   make(map[string]*Task),
		workDir: workDir,
	}
}

// staleTaskOutputMaxAge is the mtime threshold for sweeping task-output logs
// left behind by PREVIOUS gokin runs. Far above the in-process 30-minute
// reap; a LIVE task owned by a concurrently-running second gokin process
// writes its log continuously, keeping mtime fresh, so it is never touched.
const staleTaskOutputMaxAge = 48 * time.Hour

var managedTaskOutputName = regexp.MustCompile(`^task_[0-9]+_[0-9]+(?:_[0-9]+)?\.log$`)

// sweepStaleTaskOutputFiles removes orphaned task-output logs. Cleanup only
// reaps tasks tracked by the CURRENT process's map, so .gokin/task-output
// files from crashed or exited runs used to accumulate on disk forever.
func sweepStaleTaskOutputFiles(workDir string) int {
	if workDir == "" {
		return 0
	}
	gokinDir := filepath.Join(workDir, ".gokin")
	dir := filepath.Join(workDir, ".gokin", "task-output")
	if _, err := os.Lstat(gokinDir); err != nil {
		return 0
	}
	if err := fileutil.EnsurePrivateDir(gokinDir); err != nil {
		return 0
	}
	if _, err := os.Lstat(dir); err != nil {
		return 0
	}
	if err := fileutil.EnsurePrivateDir(dir); err != nil {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-staleTaskOutputMaxAge)
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !managedTaskOutputName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.Mode().IsRegular() && info.ModTime().Before(cutoff) {
			if os.Remove(filepath.Join(dir, entry.Name())) == nil {
				removed++
			}
		}
	}
	if removed > 0 {
		logging.Debug("swept stale task output files", "dir", dir, "count", removed)
	}
	return removed
}

// SetCompletionHandler sets the handler called when tasks complete.
func (m *Manager) SetCompletionHandler(handler CompletionHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onComplete = handler
}

// Start starts a new background task and returns its ID. The task runs in the
// manager's own working directory; use StartInDir when the caller owns a
// different one (sub-agent tool clones share ONE manager so every task stays
// visible to /tasks, task_output and kill_shell).
func (m *Manager) Start(ctx context.Context, command string) (string, error) {
	return m.StartInDir(ctx, "", command)
}

// taskDirLocked resolves a per-task working directory against the manager's
// default. Caller holds m.mu.
func (m *Manager) taskDirLocked(dir string) string {
	if dir != "" {
		return dir
	}
	return m.workDir
}

// StartInDir starts a background task whose process runs in dir. An empty dir
// falls back to the manager's own working directory.
func (m *Manager) StartInDir(ctx context.Context, dir, command string) (string, error) {
	m.mu.Lock()
	m.counter++
	id := fmt.Sprintf("task_%d_%d_%d", time.Now().UnixNano(), os.Getpid(), m.counter)

	task := NewTask(id, command, m.taskDirLocked(dir))
	m.tasks[id] = task
	onComplete := m.onComplete
	m.mu.Unlock()

	// Start the task
	if err := task.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.tasks, id)
		m.mu.Unlock()
		return "", err
	}

	// Monitor for completion
	go m.monitorTask(task, onComplete)

	return id, nil
}

// StartWithArgs starts a new background task using direct exec (no shell interpretation).
// This prevents command injection attacks when constructing commands from user input.
func (m *Manager) StartWithArgs(ctx context.Context, program string, args []string) (string, error) {
	return m.StartWithArgsInDir(ctx, "", program, args)
}

// StartWithArgsInDir is StartWithArgs with an explicit working directory; an
// empty dir falls back to the manager's own.
func (m *Manager) StartWithArgsInDir(ctx context.Context, dir, program string, args []string) (string, error) {
	m.mu.Lock()
	m.counter++
	id := fmt.Sprintf("task_%d_%d_%d", time.Now().UnixNano(), os.Getpid(), m.counter)

	task := NewTaskWithArgs(id, program, args, m.taskDirLocked(dir))
	m.tasks[id] = task
	onComplete := m.onComplete
	m.mu.Unlock()

	// Start the task
	if err := task.Start(ctx); err != nil {
		m.mu.Lock()
		delete(m.tasks, id)
		m.mu.Unlock()
		return "", err
	}

	// Monitor for completion
	go m.monitorTask(task, onComplete)

	return id, nil
}

// monitorTask waits for task completion and calls the handler.
// Defer-recover guards against a panicky onComplete callback — without it
// any handler that nil-derefs (e.g. accessing a stale parent in tests or
// during shutdown) would crash the whole process.
func (m *Manager) monitorTask(task *Task, onComplete CompletionHandler) {
	defer func() {
		if r := recover(); r != nil {
			logging.Error("task monitor goroutine panicked",
				"task_id", task.ID,
				"panic", r,
				"stack", logging.PanicStack())
		}
	}()

	<-task.Done()

	if onComplete != nil {
		onComplete(task)
	}
}

// Get returns a task by ID.
func (m *Manager) Get(id string) (*Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task, ok := m.tasks[id]
	return task, ok
}

// GetOutput returns the output of a task.
func (m *Manager) GetOutput(id string) (string, bool) {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return "", false
	}
	return task.GetOutput(), true
}

// GetInfo returns information about a task.
func (m *Manager) GetInfo(id string) (Info, bool) {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return Info{}, false
	}
	return task.GetInfo(), true
}

// Cancel cancels a running task.
func (m *Manager) Cancel(id string) error {
	m.mu.RLock()
	task, ok := m.tasks[id]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("task not found: %s", id)
	}

	task.Cancel()
	return nil
}

// List returns all tasks.
func (m *Manager) List() []Info {
	m.mu.RLock()
	result := make([]Info, 0, len(m.tasks))
	for _, task := range m.tasks {
		result = append(result, task.GetInfo())
	}
	m.mu.RUnlock()

	sortTaskInfos(result, true)
	return result
}

// ListRunning returns all running tasks.
func (m *Manager) ListRunning() []Info {
	m.mu.RLock()
	var result []Info
	for _, task := range m.tasks {
		if task.IsRunning() {
			result = append(result, task.GetInfo())
		}
	}
	m.mu.RUnlock()

	sortTaskInfos(result, false)
	return result
}

// ListCompleted returns all completed tasks.
func (m *Manager) ListCompleted() []Info {
	m.mu.RLock()
	var result []Info
	for _, task := range m.tasks {
		if task.IsComplete() {
			result = append(result, task.GetInfo())
		}
	}
	m.mu.RUnlock()

	sortTaskInfos(result, false)
	return result
}

// sortTaskInfos gives every task-listing surface a stable, useful order.
// Map iteration is intentionally random; without sorting, callers that cap
// their output (notably /tasks) can hide recent work while showing older
// entries. The mixed list keeps active commands visible, then orders newest
// first. ID is a deterministic tie-breaker for synthetic/test snapshots.
func sortTaskInfos(infos []Info, runningFirst bool) {
	sort.Slice(infos, func(i, j int) bool {
		if runningFirst {
			iRunning := infos[i].Status == StatusRunning.String()
			jRunning := infos[j].Status == StatusRunning.String()
			if iRunning != jRunning {
				return iRunning
			}
		}
		if !infos[i].StartTime.Equal(infos[j].StartTime) {
			return infos[i].StartTime.After(infos[j].StartTime)
		}
		return infos[i].ID < infos[j].ID
	})
}

// Cleanup removes completed tasks older than the given duration.
func (m *Manager) Cleanup(maxAge time.Duration) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	count := 0
	cutoff := time.Now().Add(-maxAge)

	for id, task := range m.tasks {
		if task.IsCompleteAndBefore(cutoff) {
			// Clean up output file if it exists
			if fp := task.Output.FilePath(); fp != "" {
				os.Remove(fp)
			}
			delete(m.tasks, id)
			count++
		}
	}
	return count
}

// CancelAll cancels all running tasks.
func (m *Manager) CancelAll() {
	m.mu.RLock()
	tasks := make([]*Task, 0)
	for _, task := range m.tasks {
		if task.IsRunning() {
			tasks = append(tasks, task)
		}
	}
	m.mu.RUnlock()

	for _, task := range tasks {
		task.Cancel()
	}
}

// WaitAll waits until every task known at the call boundary has published a
// terminal state and closed its output file. This is deliberately separate
// from CancelAll: shutdown first cancels shell tasks and agents together, then
// waits for both groups concurrently within one shared deadline.
func (m *Manager) WaitAll(ctx context.Context) error {
	m.mu.RLock()
	snapshot := make([]*Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		snapshot = append(snapshot, task)
	}
	m.mu.RUnlock()

	for _, task := range snapshot {
		select {
		case <-task.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Count returns the number of tasks.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tasks)
}

// RunningCount returns the number of running tasks.
func (m *Manager) RunningCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, task := range m.tasks {
		if task.IsRunning() {
			count++
		}
	}
	return count
}
