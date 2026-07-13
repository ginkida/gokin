package app

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gokin/internal/logging"
	"gokin/internal/ui"
)

// UIEventBroadcaster broadcasts task execution events to UI.
// Uses a single goroutine per send with WaitGroup tracking to prevent leaks.
type UIEventBroadcaster struct {
	program *tea.Program
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
	wg      sync.WaitGroup // Tracks all pending send goroutines
	enabled bool
	stopped bool

	// Rate-limit noisy progress updates per task. Lifecycle transitions must
	// never be dropped merely because another task emitted an event recently.
	lastProgress map[string]time.Time
	minInterval  time.Duration
}

// NewUIEventBroadcaster creates a new UI event broadcaster
func NewUIEventBroadcaster(program *tea.Program) *UIEventBroadcaster {
	ctx, cancel := context.WithCancel(context.Background())
	return &UIEventBroadcaster{
		program:      program,
		ctx:          ctx,
		cancel:       cancel,
		enabled:      true,
		lastProgress: make(map[string]time.Time),
		minInterval:  50 * time.Millisecond,
	}
}

// NewUIEventBroadcasterWithContext creates a new UI event broadcaster with a parent context
func NewUIEventBroadcasterWithContext(ctx context.Context, program *tea.Program) *UIEventBroadcaster {
	if ctx == nil {
		ctx = context.Background()
	}
	childCtx, cancel := context.WithCancel(ctx)
	return &UIEventBroadcaster{
		program:      program,
		ctx:          childCtx,
		cancel:       cancel,
		enabled:      true,
		lastProgress: make(map[string]time.Time),
		minInterval:  50 * time.Millisecond,
	}
}

// Stop stops the broadcaster, cancels pending sends, and waits for goroutine cleanup.
func (b *UIEventBroadcaster) Stop() {
	b.mu.Lock()
	if !b.stopped {
		b.stopped = true
		if b.cancel != nil {
			b.cancel()
		}
	}
	b.mu.Unlock()

	// Wait for all tracked goroutines with timeout
	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// All goroutines cleaned up
	case <-time.After(2 * time.Second):
		// Bumped Debug→Warn: a stuck broadcaster goroutine at shutdown
		// is symptomatic of a UI message that program.Send blocked on,
		// or a hung downstream consumer. Visible in default logs so
		// post-mortem can correlate with whatever happened around it.
		logging.Warn("broadcaster shutdown timeout — some goroutines may still be running",
			"timeout", "2s")
	}
}

// sendAsync sends a message to the UI program in a tracked goroutine. Bubble
// Tea's Send can block before Program.Run starts, so callers must never perform
// the send synchronously on an application lifecycle path.
func (b *UIEventBroadcaster) sendAsync(msg tea.Msg) {
	b.mu.Lock()
	if !b.enabled || b.stopped || b.program == nil {
		b.mu.Unlock()
		return
	}

	program := b.program
	ctx := b.ctx
	// Register under the same mutex Stop uses to set stopped. Once Stop owns
	// the mutex, no positive Add can begin concurrently with its subsequent
	// Wait, preserving sync.WaitGroup's zero-counter reuse contract.
	b.wg.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				// program.Send may panic if channel is closed during shutdown.
				// Capture stack trace so the rare non-shutdown panic case
				// (e.g. nil-deref inside Bubble Tea) is debuggable from logs
				// instead of just "recovered from panic" with no signal.
				logging.Debug("broadcaster send recovered from panic",
					"error", r,
					"stack", logging.PanicStack())
			}
		}()

		// Final context check inside goroutine
		select {
		case <-ctx.Done():
			return
		default:
			program.Send(msg)
		}
	}()
}

// Enable enables event broadcasting
func (b *UIEventBroadcaster) Enable() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enabled = true
}

// Disable disables event broadcasting
func (b *UIEventBroadcaster) Disable() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enabled = false
}

// IsEnabled returns whether broadcasting is enabled
func (b *UIEventBroadcaster) IsEnabled() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.enabled && !b.stopped
}

func (b *UIEventBroadcaster) IsStopped() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.stopped
}

// BroadcastTaskStart broadcasts a task start event
func (b *UIEventBroadcaster) BroadcastTaskStart(taskID, message, planType string) {
	b.sendAsync(ui.TaskStartedEvent{
		TaskID:   taskID,
		Message:  message,
		PlanType: planType,
	})
}

// BroadcastTaskComplete broadcasts a task completion event
func (b *UIEventBroadcaster) BroadcastTaskComplete(taskID string, success bool, duration time.Duration, err error, planType string) {
	b.mu.Lock()
	delete(b.lastProgress, taskID)
	b.mu.Unlock()

	b.sendAsync(ui.TaskCompletedEvent{
		TaskID:   taskID,
		Success:  success,
		Duration: duration,
		Error:    err,
		PlanType: planType,
	})
}

// BroadcastTaskProgress broadcasts a task progress event
func (b *UIEventBroadcaster) BroadcastTaskProgress(taskID string, progress float64, message string) {
	now := time.Now()
	b.mu.Lock()
	if !b.enabled || b.stopped || b.program == nil {
		b.mu.Unlock()
		return
	}
	if last := b.lastProgress[taskID]; !last.IsZero() && now.Sub(last) < b.minInterval {
		b.mu.Unlock()
		return
	}
	b.lastProgress[taskID] = now
	b.mu.Unlock()

	b.sendAsync(ui.TaskProgressEvent{
		TaskID:   taskID,
		Progress: progress,
		Message:  message,
	})
}

// SetProgram sets the tea.Program for broadcasting
func (b *UIEventBroadcaster) SetProgram(program *tea.Program) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.program = program
}
