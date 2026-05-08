package loops

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
)

// Spawner is the function the Runner calls to actually execute one
// iteration's prompt. Returns the agent's textual output, ok=false when
// the iteration "failed" in some agent-defined way (status != completed),
// and a Go error for transport / cancellation failures.
//
// Decoupling via injection keeps internal/loops free of any direct
// dependency on internal/agent — the wire-up happens in app/builder.go
// where both packages are visible. Tests pass in a stub Spawner that
// returns canned output without spinning up a real LLM call.
type Spawner func(ctx context.Context, prompt string) (output string, ok bool, err error)

// IdleChecker reports whether the app is currently free to start a new
// iteration. Wired to the App's processing flag in production: while the
// user is typing or the foreground request is in flight, loops queue
// (skip the tick) instead of contending for the same client / executor.
type IdleChecker func() bool

// Runner drives a Manager: every Period it scans active loops and fires
// the ones whose NextRunAt has passed, subject to IdleChecker permission.
// Designed as a single background goroutine — no per-loop concurrency,
// since the IdleChecker model already serializes against foreground work.
//
// Lifecycle: Start launches the goroutine; Stop signals it to exit.
// Both are idempotent and safe to call from any goroutine.
type Runner struct {
	mgr     *Manager
	spawn   Spawner
	isIdle  IdleChecker
	period  time.Duration
	timeout time.Duration

	mu       sync.Mutex
	started  bool
	stopChan chan struct{}
	stopOnce sync.Once

	// Hooks for tests / observability — fired on lifecycle events.
	// Production wiring leaves them nil; nil-checked at call site.
	onIterationStart func(loopID string)
	onIterationDone  func(loopID string, it Iteration)
}

// DefaultPollPeriod is how often the runner checks for due loops.
// 30s balances responsiveness (loops fire within ~30s of being due)
// against overhead (acquires no expensive resources per tick).
const DefaultPollPeriod = 30 * time.Second

// DefaultIterationTimeout caps how long a single iteration can run
// before being considered stuck. Loops are typically short tasks
// (read a file, run a check), but we don't want a hung iteration to
// block all other loops from firing forever.
const DefaultIterationTimeout = 10 * time.Minute

// NewRunner constructs a Runner. The Spawner and IdleChecker are
// required (nil panics at Start time so the wire-up bug is loud); period
// and timeout default to the constants above when zero.
func NewRunner(mgr *Manager, spawn Spawner, isIdle IdleChecker) *Runner {
	return &Runner{
		mgr:      mgr,
		spawn:    spawn,
		isIdle:   isIdle,
		period:   DefaultPollPeriod,
		timeout:  DefaultIterationTimeout,
		stopChan: make(chan struct{}),
	}
}

// SetPeriod overrides the poll period (mainly for tests; production
// uses DefaultPollPeriod). Must be called before Start.
func (r *Runner) SetPeriod(p time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		r.period = p
	}
}

// SetIterationTimeout overrides the per-iteration cap. Same lifecycle
// constraint as SetPeriod (call before Start).
func (r *Runner) SetIterationTimeout(t time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.started {
		r.timeout = t
	}
}

// SetIterationStartHook installs a callback fired just before each
// iteration's spawn call. Used by the TUI to emit a "loop firing"
// status update so users see the loop is alive.
func (r *Runner) SetIterationStartHook(fn func(loopID string)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onIterationStart = fn
}

// SetIterationDoneHook installs a callback fired after each iteration
// completes (success or failure). Used by the TUI to refresh status
// and by the memory-update path (Phase 3) to write summaries.
func (r *Runner) SetIterationDoneHook(fn func(loopID string, it Iteration)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onIterationDone = fn
}

// Start launches the background poller goroutine. Idempotent — second
// call is a no-op. The goroutine exits cleanly on ctx.Done() or Stop().
func (r *Runner) Start(ctx context.Context) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	if r.spawn == nil {
		r.mu.Unlock()
		panic("loops.Runner: Start called with nil Spawner — wire-up bug")
	}
	if r.isIdle == nil {
		r.mu.Unlock()
		panic("loops.Runner: Start called with nil IdleChecker — wire-up bug")
	}
	r.started = true
	period := r.period
	r.mu.Unlock()

	go r.run(ctx, period)
}

// Stop signals the poller to exit. Idempotent. Safe to call before Start
// (won't deadlock — stopChan is already closed-on-second-call via sync.Once).
func (r *Runner) Stop() {
	r.stopOnce.Do(func() { close(r.stopChan) })
}

// run is the poller loop. Exits on ctx.Done() OR Stop().
func (r *Runner) run(ctx context.Context, period time.Duration) {
	logging.Info("loops: scheduler started", "period", period)
	defer logging.Info("loops: scheduler stopped")

	// Defer panic recovery — a corrupt loop or a buggy Spawner mustn't
	// take down the whole gokin process. Per CLAUDE.md reliability
	// invariants, every long-lived background goroutine wraps recover.
	defer func() {
		if rec := recover(); rec != nil {
			logging.Error("loops: scheduler panic recovered",
				"panic", rec,
				"stack", logging.PanicStack())
		}
	}()

	ticker := time.NewTicker(period)
	defer ticker.Stop()

	// Don't immediately tick on start — let the app finish coming up,
	// log noise during startup is annoying. First tick fires after
	// `period`.
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopChan:
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

// tick scans active loops for ones that are due AND fires the first
// one we find. Firing is serialized — at most ONE iteration per tick —
// so multiple due loops naturally interleave with foreground work
// instead of all firing at once and stomping on each other.
//
// Skips entirely if the IdleChecker says the app is busy.
func (r *Runner) tick(ctx context.Context) {
	if !r.isIdle() {
		return
	}

	now := time.Now()
	for _, l := range r.mgr.Active() {
		if !l.IsDue(now) {
			continue
		}
		// Re-check idle just before firing — the gap between
		// initial check and now might have seen a user message arrive.
		if !r.isIdle() {
			return
		}
		r.fireOne(ctx, l)
		return
	}
}

// fireOne executes a single iteration: builds the prompt, calls
// Spawner, captures the result into an Iteration, persists.
func (r *Runner) fireOne(ctx context.Context, l *Loop) {
	r.mu.Lock()
	startHook := r.onIterationStart
	doneHook := r.onIterationDone
	timeout := r.timeout
	r.mu.Unlock()

	if startHook != nil {
		startHook(l.ID)
	}

	prompt := BuildIterationPrompt(l)

	iterationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	started := time.Now()
	output, ok, err := r.spawn(iterationCtx, prompt)
	duration := time.Since(started)

	it := Iteration{
		N:         l.IterationCount + 1,
		StartedAt: started,
		Duration:  duration,
		OK:        ok && err == nil,
	}

	switch {
	case err != nil:
		it.Summary = "Iteration error: " + truncateErr(err.Error())
		it.OK = false
	case !ok:
		// Spawner returned ok=false but no error — typically means the
		// agent failed gracefully (timed out, hit max turns). Capture
		// whatever output we got so the user can see what happened.
		it.Summary = summarizeOutput(output, "iteration did not complete cleanly")
	default:
		it.Summary = summarizeOutput(output, "iteration completed")
		it.NextHint = parseNextHint(output)
	}

	if recordErr := r.mgr.RecordIteration(l.ID, it); recordErr != nil {
		logging.Warn("loops: failed to record iteration",
			"loop_id", l.ID, "iteration", it.N, "error", recordErr)
	}

	if doneHook != nil {
		doneHook(l.ID, it)
	}
}

// BuildIterationPrompt constructs the user-facing prompt sent to the
// agent for one iteration. Includes the task description and recent
// summary context so the agent picks up where it left off.
//
// Exported so the runner package can test prompt shape and the future
// Phase 3 memory writer can format consistently.
func BuildIterationPrompt(l *Loop) string {
	var sb strings.Builder
	sb.WriteString("[Loop iteration ")
	fmt.Fprintf(&sb, "%d", l.IterationCount+1)
	if l.MaxIterations > 0 {
		fmt.Fprintf(&sb, "/%d", l.MaxIterations)
	}
	sb.WriteString("]\n\n")
	sb.WriteString("Task: ")
	sb.WriteString(l.Task)
	sb.WriteString("\n")

	// Include the last 3 iteration summaries so the agent has context
	// without bloating the prompt. Avoids re-discovery of work already
	// done in prior iterations.
	if len(l.Iterations) > 0 {
		sb.WriteString("\nRecent context (newest first):\n")
		showCount := 3
		if showCount > len(l.Iterations) {
			showCount = len(l.Iterations)
		}
		for i := len(l.Iterations) - 1; i >= len(l.Iterations)-showCount; i-- {
			it := l.Iterations[i]
			fmt.Fprintf(&sb, "  - #%d (%s): %s\n",
				it.N,
				it.StartedAt.Format("2006-01-02 15:04"),
				it.Summary)
		}
	}

	sb.WriteString("\nProceed with the next step. Keep the response focused — your last sentence will be captured as the iteration summary.")
	return sb.String()
}

// summarizeOutput extracts a 1-2 sentence summary from the agent's
// response text. Heuristic: prefer the last non-empty paragraph; fall
// back to the truncated full output. The agent's prompt instructs it
// to "keep the response focused — your last sentence will be captured
// as the iteration summary", so the heuristic mirrors that contract.
func summarizeOutput(output, fallback string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return fallback
	}

	// Try last paragraph (split by double newline).
	paragraphs := strings.Split(output, "\n\n")
	for i := len(paragraphs) - 1; i >= 0; i-- {
		p := strings.TrimSpace(paragraphs[i])
		if p != "" && !looksLikeMetadata(p) {
			return truncateForSummary(p)
		}
	}

	// Fall back to the full output.
	return truncateForSummary(output)
}

// looksLikeMetadata heuristically rejects paragraphs that are clearly
// not the substance of the iteration — code blocks, file listings,
// status markers. Keeps the summary readable.
func looksLikeMetadata(p string) bool {
	if strings.HasPrefix(p, "```") {
		return true
	}
	if strings.HasPrefix(p, "---") || strings.HasPrefix(p, "===") {
		return true
	}
	return false
}

// truncateForSummary caps a string at ~200 runes, ellipsized. Long
// summaries hurt the UI listing more than they help a human reader.
func truncateForSummary(s string) string {
	const maxRunes = 200
	r := []rune(strings.TrimSpace(s))
	if len(r) <= maxRunes {
		return string(r)
	}
	return string(r[:maxRunes-3]) + "..."
}

// truncateErr trims an error string to keep the iteration log readable.
// Some agent errors include full stack traces that would balloon the
// summary if pasted verbatim.
func truncateErr(s string) string {
	if r := []rune(strings.TrimSpace(s)); len(r) > 150 {
		return string(r[:147]) + "..."
	}
	return strings.TrimSpace(s)
}

// parseNextHint scans the agent's output for "next:" / "wait N" /
// "next iteration in N" hints that signal a longer self-paced delay.
// Returns the hint string verbatim — Loop.AppendIteration parses it
// with parseNextHintSeconds.
//
// Conservative: only recognizes well-formed hints on their own line.
// Avoids false positives where the model says "wait" in regular prose.
func parseNextHint(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "next:") {
			return strings.TrimSpace(line[5:])
		}
		if strings.HasPrefix(lower, "next check in ") {
			return strings.TrimSpace(line[len("next check in "):])
		}
		if strings.HasPrefix(lower, "wait ") && len(line) <= 20 {
			// "wait 30m" — short enough to be a hint, not prose
			return strings.TrimSpace(line)
		}
	}
	return ""
}
