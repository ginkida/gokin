package tasks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
)

// SafeEnvVars is the whitelist of environment variables passed to task commands.
// This prevents leaking sensitive environment variables like API keys.
var SafeEnvVars = []string{
	"PATH",
	"HOME",
	"USER",
	"SHELL",
	"TERM",
	"LANG",
	"LC_ALL",
	"LC_CTYPE",
	"TMPDIR",
	"TMP",
	"TEMP",
	"EDITOR",
	"VISUAL",
	"PAGER",
	"XDG_CONFIG_HOME",
	"XDG_DATA_HOME",
	"XDG_CACHE_HOME",
	"XDG_RUNTIME_DIR",
	"GOPATH",
	"GOROOT",
	"GOPROXY",
	"GOPRIVATE",
	"GOFLAGS",
	"NODE_PATH",
	"NPM_CONFIG_PREFIX",
	"PYTHONPATH",
	"VIRTUAL_ENV",
	"GIT_AUTHOR_NAME",
	"GIT_AUTHOR_EMAIL",
	"GIT_COMMITTER_NAME",
	"GIT_COMMITTER_EMAIL",
}

// buildSafeEnv creates a sanitized environment for command execution.
func buildSafeEnv() []string {
	env := make([]string, 0, len(SafeEnvVars))
	for _, key := range SafeEnvVars {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	// Always set a safe PATH if not already set
	hasPath := false
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			hasPath = true
			break
		}
	}
	if !hasPath {
		env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin")
	}
	// Set TERM for proper terminal handling
	hasTerm := false
	for _, e := range env {
		if strings.HasPrefix(e, "TERM=") {
			hasTerm = true
			break
		}
	}
	if !hasTerm {
		env = append(env, "TERM=xterm-256color")
	}
	return env
}

// Status represents the status of a background task.
type Status int

const (
	StatusPending Status = iota
	StatusRunning
	StatusCompleted
	StatusFailed
	StatusCancelled
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

// safeBuffer is a bytes.Buffer protected by its own mutex for concurrent access.
// This is needed because exec.Cmd writes to Stdout/Stderr from OS goroutines
// while GetOutput/GetInfo read concurrently.
//
// When OutputFile is set, writes go to both the in-memory buffer and a file.
// The in-memory buffer is capped at maxMemoryOutputBytes; beyond that, only
// the file contains the full output. String() returns in-memory content.
// FullString() reads from file if available and in-memory buffer was truncated.
type safeBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	file       *os.File
	filePath   string
	totalBytes int64
	truncated  bool
}

const maxMemoryOutputBytes = 10 * 1024 * 1024 // 10 MB cap for in-memory output

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Always write to file if available
	if b.file != nil {
		if _, err := b.file.Write(p); err != nil {
			// Disable file-backed output on first failure so we don't
			// keep attempting broken writes on every subsequent chunk.
			logging.Warn("task output file write failed; disabling file output", "path", b.filePath, "error", err)
			b.file.Close()
			b.file = nil
		}
	}

	b.totalBytes += int64(len(p))

	// Write to in-memory buffer only if under cap
	if !b.truncated && b.buf.Len()+len(p) <= maxMemoryOutputBytes {
		return b.buf.Write(p)
	}

	if !b.truncated {
		b.truncated = true
	}
	return len(p), nil // Accept write but don't store in memory
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if b.truncated {
		s += fmt.Sprintf("\n\n[Output truncated in memory: %d bytes total. Full output in: %s]",
			b.totalBytes, b.filePath)
	}
	return s
}

// SetOutputFile configures file-backed output streaming.
func (b *safeBuffer) SetOutputFile(path string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	b.file = f
	b.filePath = path
	return nil
}

// Close closes the output file if open.
func (b *safeBuffer) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		b.file.Close()
		b.file = nil
	}
}

// FilePath returns the path to the output file, or empty string if none.
func (b *safeBuffer) FilePath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.filePath
}

// TotalBytes returns the total bytes written (including file-only output).
func (b *safeBuffer) TotalBytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalBytes
}

// Task represents a background task.
type Task struct {
	ID        string
	Command   string
	Status    Status
	Output    safeBuffer
	Error     string
	ExitCode  int
	StartTime time.Time
	EndTime   time.Time
	WorkDir   string

	// Program and Args allow exec without shell interpretation (prevents injection).
	Program string
	Args    []string

	cmd            *exec.Cmd
	cancelFunc     context.CancelFunc
	processStarted bool
	done           chan struct{} // closed when task reaches a terminal state
	doneOnce       sync.Once
	mu             sync.RWMutex
}

// NewTask creates a new background task.
func NewTask(id, command, workDir string) *Task {
	return &Task{
		ID:      id,
		Command: command,
		Status:  StatusPending,
		WorkDir: workDir,
		done:    make(chan struct{}),
	}
}

// NewTaskWithArgs creates a new background task that executes a program directly
// without shell interpretation. This prevents command injection attacks.
func NewTaskWithArgs(id, program string, args []string, workDir string) *Task {
	return &Task{
		ID:      id,
		Command: program + " " + fmt.Sprintf("%v", args),
		Program: program,
		Args:    args,
		Status:  StatusPending,
		WorkDir: workDir,
		done:    make(chan struct{}),
	}
}

// Start starts the task execution.
func (t *Task) Start(ctx context.Context) error {
	t.mu.Lock()
	if t.Status != StatusPending {
		t.mu.Unlock()
		return fmt.Errorf("task already started")
	}

	// Create cancellable context
	execCtx, cancel := context.WithCancel(ctx)
	t.cancelFunc = cancel

	// Create command - use Program/Args if set (no shell interpretation),
	// otherwise fall back to shell execution
	if t.Program != "" {
		t.cmd = exec.CommandContext(execCtx, t.Program, t.Args...)
	} else {
		t.cmd = exec.CommandContext(execCtx, "sh", "-c", t.Command)
	}
	t.cmd.Dir = t.WorkDir
	t.cmd.Stdout = &t.Output
	t.cmd.Stderr = &t.Output

	// Set up file-backed output streaming for long-running tasks
	outputDir := filepath.Join(t.WorkDir, ".gokin", "task-output")
	if err := t.Output.SetOutputFile(filepath.Join(outputDir, t.ID+".log")); err != nil {
		// Non-fatal: task runs fine, but /task-output won't have persistent log.
		// Common cause: .gokin/task-output directory can't be created (e.g., read-only fs).
		fmt.Fprintf(&t.Output, "[warning: file-backed output unavailable: %s]\n", err)
	}

	// Use sanitized environment to prevent leaking sensitive env vars
	t.cmd.Env = buildSafeEnv()

	// Set up process group for proper cleanup of child processes
	setProcAttr(t.cmd)

	t.Status = StatusRunning
	t.StartTime = time.Now()
	t.mu.Unlock()

	// Run in background
	go t.run()

	return nil
}

// run executes the command and updates status.
func (t *Task) run() {
	defer t.Output.Close() // Close output file when task finishes

	err := t.cmd.Start()

	t.mu.Lock()
	if err != nil {
		defer t.mu.Unlock()
		defer t.doneOnce.Do(func() { close(t.done) }) // Guarantees done is closed on any exit path

		// Release context resources regardless of how the command finished.
		if t.cancelFunc != nil {
			t.cancelFunc()
		}

		t.EndTime = time.Now()

		if t.Status == StatusCancelled {
			return
		}

		t.Status = StatusFailed
		t.Error = err.Error()
		t.ExitCode = -1
		return
	}

	t.processStarted = true
	cancelled := t.Status == StatusCancelled
	t.mu.Unlock()

	if cancelled {
		killProcessGroup(t.cmd)
	}

	err = t.cmd.Wait()

	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.doneOnce.Do(func() { close(t.done) }) // Guarantees done is closed on any exit path

	// Release context resources regardless of how the command finished.
	if t.cancelFunc != nil {
		t.cancelFunc()
	}

	t.EndTime = time.Now()

	if t.Status == StatusCancelled {
		if err == nil {
			t.ExitCode = 0
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			t.ExitCode = exitErr.ExitCode()
		} else {
			t.ExitCode = -1
		}
		return
	}

	if err != nil {
		t.Status = StatusFailed
		t.Error = err.Error()
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.ExitCode = exitErr.ExitCode()
		} else {
			t.ExitCode = -1
		}
	} else {
		t.Status = StatusCompleted
		t.ExitCode = 0
	}
}

// Cancel cancels the task.
func (t *Task) Cancel() {
	t.mu.Lock()
	if t.Status != StatusRunning || t.cancelFunc == nil {
		t.mu.Unlock()
		return
	}

	cancel := t.cancelFunc
	cmd := t.cmd
	processStarted := t.processStarted

	t.Status = StatusCancelled
	t.EndTime = time.Now()
	t.mu.Unlock()

	cancel()

	if processStarted {
		// Kill entire process group for proper cleanup once Start() has published cmd.Process.
		killProcessGroup(cmd)
	}
}

// Done returns a channel that is closed when the task reaches a terminal state.
func (t *Task) Done() <-chan struct{} { return t.done }

// GetStatus returns the current status.
func (t *Task) GetStatus() Status {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status
}

// GetOutput returns the current output.
func (t *Task) GetOutput() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Output.String()
}

// GetError returns the error message if failed.
func (t *Task) GetError() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Error
}

// IsRunning returns true if the task is still running.
func (t *Task) IsRunning() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status == StatusRunning
}

// IsComplete returns true if the task has finished (success, fail, or cancelled).
func (t *Task) IsComplete() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusCancelled
}

// IsCompleteAndBefore returns true if the task is complete and ended before cutoff.
// Reads both Status and EndTime atomically under one lock.
func (t *Task) IsCompleteAndBefore(cutoff time.Time) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return (t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusCancelled) &&
		t.EndTime.Before(cutoff)
}

// durationLocked returns the task duration. Caller must hold t.mu.
func (t *Task) durationLocked() time.Duration {
	if t.StartTime.IsZero() {
		return 0
	}
	if t.EndTime.IsZero() {
		return time.Since(t.StartTime)
	}
	return t.EndTime.Sub(t.StartTime)
}

// Duration returns the task duration.
func (t *Task) Duration() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.durationLocked()
}

// Info returns a summary of the task.
type Info struct {
	ID        string
	Command   string
	Status    string
	Output    string
	Error     string
	ExitCode  int
	Duration  time.Duration
	StartTime time.Time
	EndTime   time.Time
}

// GetInfo returns task information.
func (t *Task) GetInfo() Info {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return Info{
		ID:        t.ID,
		Command:   t.Command,
		Status:    t.Status.String(),
		Output:    t.Output.String(),
		Error:     t.Error,
		ExitCode:  t.ExitCode,
		Duration:  t.durationLocked(),
		StartTime: t.StartTime,
		EndTime:   t.EndTime,
	}
}
