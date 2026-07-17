package loops

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gokin/internal/logging"
)

// SpawnResult is the structured outcome of executing one iteration's prompt.
// Carried as a struct (rather than a bare tuple) so the classification the
// adapter computes — notably Transient — travels cleanly to the runner without
// internal/loops needing to import internal/client.
type SpawnResult struct {
	// Output is the agent's textual result (captured even on failure so the
	// iteration summary preserves partial work — see CLAUDE.md loop invariant).
	Output string
	// OK is true when the iteration completed successfully.
	OK bool
	// Transient is true when a NON-OK result was caused by an infrastructure
	// hiccup (provider overload, rate limit, network) rather than a genuine
	// task failure. The scheduler treats transient failures leniently: they
	// don't trip the task-failure auto-pause breaker, and they trigger
	// exponential loop-level backoff so a busy/unreachable provider is waited
	// out instead of burning the loop. Meaningless when OK is true.
	Transient bool
	// TokensIn / TokensOut are this iteration's billed input / generated output
	// tokens. Surfaced so the user can see what a background loop is spending
	// (a runaway unattended loop can quietly burn a provider's quota).
	TokensIn  int
	TokensOut int
	// MadeChanges is true when the iteration ran at least one code/repo-mutating
	// tool — the churn signal. A run of OK-but-no-change iterations on an ACTION
	// task means the loop is spinning (task likely done, or stuck); the scheduler
	// warns the agent and eventually auto-pauses. Set by the adapter from
	// AgentResult.MutatingToolCalls.
	MadeChanges bool
	// TimedOut is true when a NON-OK result was caused by the iteration's own
	// time budget (iterationCtx deadline) firing mid-work. It stays Transient
	// for the breaker (a cadence problem, not a task failure), but the NEXT
	// iteration's prompt treats it as a CUTOFF: the agent was working and got
	// clipped, so it must continue from the handoff, not restart (v0.100.102 —
	// a real loop burned 1.7M input tokens across 3 consecutive timed-out
	// iterations that each started over).
	TimedOut bool
	// FilesTouched lists the files the iteration successfully mutated
	// (workDir-relative). Set by the adapter from AgentResult.TouchedPaths;
	// capped to MaxFilesTouchedPerIteration when recorded on the Iteration.
	FilesTouched []string
}

// Spawner is the function the Runner calls to actually execute one
// iteration's prompt. Returns a SpawnResult (output + ok + transient
// classification) and a Go error for transport / cancellation failures that
// prevented the iteration from even producing a result.
//
// Decoupling via injection keeps internal/loops free of any direct
// dependency on internal/agent — the wire-up happens in app/builder.go
// where both packages are visible. Tests pass in a stub Spawner that
// returns canned output without spinning up a real LLM call.
type Spawner func(ctx context.Context, prompt string) (SpawnResult, error)

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

	mu               sync.Mutex
	started          bool
	stopChan         chan struct{}
	stopOnce         sync.Once
	iterationRunning atomic.Bool // true while fireOne is executing

	// In-flight iteration cancellation. inFlightCancel is non-nil exactly
	// while fireOne executes a spawn; CancelInFlight kills the iteration's
	// context so the sub-agent stops NOW (its partial work is preserved by
	// the agent layer). userCancelled marks that the cancellation was a
	// deliberate user action — fireOne then SKIPS recording the iteration
	// (same discipline as the shutdown skip: a user kill is not a task
	// failure and must not poison the auto-pause streak). Guarded by
	// inFlightMu (leaf lock, never nested with mu).
	inFlightMu     sync.Mutex
	inFlightCancel context.CancelFunc
	inFlightLoopID string
	userCancelled  bool

	// scanRotation is tick()-only state (single scheduler goroutine, see
	// run()) — the round-robin start index into the NEXT tick's Active()
	// scan. Without it, a loop that's due on EVERY tick (e.g. a
	// sub-poll-interval self-paced loop) permanently starves every loop
	// created after it: Active() always returns loops in fixed CreatedAt
	// order and tick() fires only the first due one it finds.
	scanRotation int

	// Hooks for tests / observability — fired on lifecycle events.
	// Production wiring leaves them nil; nil-checked at call site.
	onIterationStart func(loopID string)
	onIterationDone  func(loopID string, it Iteration)
	// onIterationPersistFailed fires when an iteration ran but its result could
	// NOT be saved (RecordIteration returned a real persistence error, after the
	// manager rolled the in-memory state back). The adapter surfaces an honest
	// "ran but couldn't be saved" warning. Kept separate from onIterationDone so
	// the loops package stays free of any ui dependency.
	onIterationPersistFailed func(loopID string, n int, err error)
}

// DefaultPollPeriod is how often the runner checks for due loops.
// 30s balances responsiveness (loops fire within ~30s of being due)
// against overhead (acquires no expensive resources per tick).
const DefaultPollPeriod = 30 * time.Second

// staggerDivisor spreads overdue-on-restart loops across one poll period.
// With period=30s and divisor=3, each overdue loop fires ~10s apart —
// matching the at-most-one-per-tick steady-state cadence so a restart
// doesn't thundering-herd the provider.
const staggerDivisor = 3

// minStaggerStep is the floor for the stagger interval when the poll period
// is very small (tests). Keeps the step meaningful even with a 1s period.
const minStaggerStep = 5 * time.Second

// DefaultIterationTimeout caps how long a single iteration can run
// before being considered stuck — a hung iteration must not block all
// other loops from firing forever.
//
// 30m (raised from 15m in v0.100.102): reasoning-heavy models (K3
// always-on reasoning, GLM 5.2) legitimately spend 10+ minutes of
// thinking per round on open-ended "improve the app" tasks — real loop
// data showed 3 CONSECUTIVE iterations dying on the 15m deadline
// mid-work (1.7M input tokens spent, nothing finished). A timed-out
// iteration is still recorded (TimedOut, transient for the breaker)
// and the next iteration continues from its handoff/files — but the
// budget should let a normal substantive iteration FINISH.
//
// Set ABOVE the request-level overload patience (client.DefaultOverloadRetryPolicy
// MaxTotal, 10m): when a provider is overloaded the agent waits it out WITHIN
// the iteration, and if that patience is exhausted we want the resulting
// overload error (which classifies as transient → loop backoff) to surface
// BEFORE this ctx deadline fires — a bare ctx timeout is ambiguous and would
// be miscounted as a task failure. The 5m of headroom covers the failed API
// round-trips between waits.
const DefaultIterationTimeout = 30 * time.Minute

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

// SetIterationPersistFailedHook installs a callback fired when an iteration
// ran but its result couldn't be persisted (RecordIteration failed). Used by
// the TUI to warn the user the work wasn't saved (disk full / permissions).
func (r *Runner) SetIterationPersistFailedHook(fn func(loopID string, n int, err error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onIterationPersistFailed = fn
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
// The scan starts from scanRotation (round-robin, advanced past whichever
// loop fires) rather than always from index 0 — Active() returns loops in
// fixed CreatedAt order, so a fixed start-at-0 scan lets a loop that's due
// on EVERY tick (e.g. a sub-poll-interval self-paced loop) permanently
// starve every loop created after it, since it's always found first.
//
// Skips entirely if the IdleChecker says the app is busy.
func (r *Runner) tick(ctx context.Context) {
	// Check ctx BEFORE anything else: if the app is shutting down, don't
	// even scan — a fireOne launched here would find ctx.Err() != nil at
	// its own entry and return immediately, wasting a goroutine spawn +
	// startHook + SetFiring/ClearFiring cycle.
	if ctx.Err() != nil {
		return
	}
	if r.iterationRunning.Load() || !r.isIdle() {
		return
	}

	active := r.mgr.Active()
	if len(active) == 0 {
		// Hygiene: reset rotation when the loop set empties so the next set
		// starts scanning from index 0. NOT load-bearing — the scan below
		// applies `scanRotation % len(active)` and iterates the WHOLE slice,
		// so a stale rotation can only change scan ORDER, never skip a loop
		// (any value mod 1 == 0 for a solo loop).
		r.scanRotation = 0
		return
	}

	now := time.Now()
	start := r.scanRotation % len(active)
	for i := range active {
		idx := (start + i) % len(active)
		l := active[idx]
		if !l.IsDue(now) {
			continue
		}
		if !r.isIdle() {
			return
		}
		r.scanRotation = idx + 1
		r.iterationRunning.Store(true)
		go func() {
			defer func() {
				if rv := recover(); rv != nil {
					logging.Error("loops: panic in fireOne", "panic", rv)
				}
				r.iterationRunning.Store(false)
			}()
			r.fireOne(ctx, l)
		}()
		return
	}
}

// fireOne executes a single iteration: builds the prompt, calls
// Spawner, captures the result into an Iteration, persists.
func (r *Runner) fireOne(ctx context.Context, l *Loop) {
	r.mu.Lock()
	startHook := r.onIterationStart
	doneHook := r.onIterationDone
	persistFailedHook := r.onIterationPersistFailed
	timeout := r.timeout
	r.mu.Unlock()

	// Parent context already canceled (app shutting down — the runner's ctx is
	// the app root ctx) before we even start: don't fire a doomed iteration.
	if ctx.Err() != nil {
		return
	}

	// The loop may have been paused/stopped between tick()'s snapshot and this
	// goroutine starting (e.g. user typed /loop pause during the isIdle wait).
	// Re-check status from the authoritative manager state — the `l` pointer is
	// a snapshot from Active() and may be stale. Firing a paused loop wastes a
	// spawn cycle and produces a phantom iteration that RecordIteration will
	// silently discard (ErrLoopNotRunning).
	current, ok := r.mgr.Get(l.ID)
	if !ok {
		logging.Info("loops: loop removed before fireOne, skipping", "loop_id", l.ID)
		return
	}
	if current.Status != StatusRunning {
		logging.Info("loops: loop no longer running before fireOne, skipping",
			"loop_id", l.ID, "status", current.Status)
		return
	}

	if startHook != nil {
		startHook(l.ID)
	}

	prompt := BuildIterationPrompt(l)

	iterationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r.inFlightMu.Lock()
	r.inFlightCancel = cancel
	r.inFlightLoopID = l.ID
	r.userCancelled = false
	r.inFlightMu.Unlock()
	defer func() {
		r.inFlightMu.Lock()
		r.inFlightCancel = nil
		r.inFlightLoopID = ""
		r.inFlightMu.Unlock()
	}()

	started := time.Now()
	// Mark this loop as firing-now so /loop status/list can show "running"
	// while the (minutes-long) iteration executes; cleared on return.
	r.mgr.SetFiring(l.ID, started)
	defer r.mgr.ClearFiring()
	res, err := r.spawn(iterationCtx, prompt)
	output, ok := res.Output, res.OK
	duration := time.Since(started)

	// If the PARENT context was canceled DURING the iteration (app shutdown),
	// this iteration was INTERRUPTED, not failed. Recording it would (a) pollute
	// the consecutive-failure streak toward auto-pause and (b) persist a
	// meaningless "context canceled" failure to disk that survives to the next
	// startup. Skip — same discipline as the ErrLoopNotRunning skip below, and
	// the same class as the end-of-turn-gate cancellation fixes: a cancellation
	// is not a failure. NOTE: this checks the PARENT ctx, NOT iterationCtx — the
	// iteration's OWN timeout cancels only iterationCtx (parent stays alive) and
	// SHOULD still record as a normal (failed) iteration.
	if ctx.Err() != nil {
		logging.Info("loops: iteration interrupted by shutdown, skipping record",
			"loop_id", l.ID, "iteration", l.IterationCount+1)
		return
	}
	r.inFlightMu.Lock()
	cancelledByUser := r.userCancelled
	r.inFlightMu.Unlock()
	if cancelledByUser {
		logging.Info("loops: iteration cancelled by user, skipping record",
			"loop_id", l.ID, "iteration", l.IterationCount+1)
		return
	}

	it := Iteration{
		N:         l.IterationCount + 1,
		StartedAt: started,
		Duration:  duration,
		OK:        ok && err == nil,
		// Transient classification only matters for a failed iteration; the
		// adapter sets it from the underlying provider error. A spawn-time
		// transport error (err != nil) is a wiring/construction failure, not a
		// provider hiccup, so it stays non-transient (counts as a task failure).
		Transient: res.Transient && !ok && err == nil,
		TimedOut:  res.TimedOut && !ok && err == nil,
		// Churn signal — did this iteration actually change code/repo? Only read
		// by AppendIteration on an OK iteration (action tasks), so it's harmless
		// to carry on a failed one.
		MadeChanges: res.MadeChanges,
		TokensIn:    res.TokensIn,
		TokensOut:   res.TokensOut,
		// Concrete anchors for the next iteration + the markdown log. Capped —
		// the value is in the common few-files case, not exhaustiveness.
		FilesTouched: capFilesTouched(res.FilesTouched),
		// The agent's own working state (HANDOFF: block). Parsed from the raw
		// output REGARDLESS of ok — a failed-in-verification or cut-off
		// iteration's partial output is exactly when continuity matters most.
		Handoff: parseHandoff(output),
	}

	// agentDone signals "the work this loop was for is complete" — the
	// agent voluntarily ending iteration via "done." marker. Captured
	// here, acted on AFTER the iteration is recorded so the markdown
	// preserves the final summary.
	agentDone := false

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
		agentDone = parseDoneSignal(output)
	}

	recordErr := r.mgr.RecordIteration(l.ID, it)
	switch {
	case errors.Is(recordErr, ErrLoopGone):
		// User removed the loop while this iteration was running.
		// No record to update, no downstream callbacks to fire — the
		// loop and all its files are gone by design.
		logging.Info("loops: iteration finished but loop was removed mid-run, skipping record",
			"loop_id", l.ID, "iteration", it.N)
		return
	case errors.Is(recordErr, ErrLoopNotRunning):
		// Loop was paused/stopped mid-iteration. Don't append — the user
		// has explicitly walked away from this state. Skip the doneHook
		// too: the markdown writer + status toast assume the iteration
		// was a legitimate part of the active run.
		logging.Info("loops: iteration finished but loop is no longer running, skipping record",
			"loop_id", l.ID, "iteration", it.N, "error", recordErr)
		return
	case recordErr != nil:
		// Real persistence failure (disk full / EPERM / readonly fs). The
		// manager already rolled the in-memory loop BACK to its pre-mutation
		// snapshot (IterationCount, Iterations, token totals, failure streaks
		// all reverted). Firing the success doneHook here would show a phantom
		// "#N completed" toast for work that isn't on disk, write markdown from
		// stale state, and — worse — act on the `done.` self-termination marker
		// below against a missing iteration (Stop with the final iteration lost
		// on restart). Surface an HONEST persist-failure warning and return
		// BEFORE doneHook and the agentDone/Stop block.
		logging.Warn("loops: failed to record iteration",
			"loop_id", l.ID, "iteration", it.N, "error", recordErr)
		if persistFailedHook != nil {
			persistFailedHook(l.ID, it.N, recordErr)
		}
		return
	}

	// Agent self-termination: if the iteration ended with "done.", stop the
	// loop so it doesn't keep firing. Done AFTER RecordIteration (so the final
	// summary is persisted) but BEFORE the doneHook, so the hook observes the
	// now-Stopped state — its toast reads "stopped" (accurate) instead of
	// "next in 5m", and it can ring the "loop finished" bell. Stop is a no-op
	// if the loop is already terminal (completed/stopped/removed).
	if agentDone {
		if err := r.mgr.Stop(l.ID); err != nil {
			logging.Debug("loops: self-terminate Stop failed (likely already terminal)",
				"loop_id", l.ID, "error", err)
		} else {
			logging.Info("loops: agent self-terminated via 'done' marker",
				"loop_id", l.ID, "iterations", l.IterationCount+1)
		}
	}

	if doneHook != nil {
		doneHook(l.ID, it)
	}
}

// BuildIterationPrompt constructs the user-facing prompt sent to the
// agent for one iteration. Includes the task description and recent
// summary context so the agent picks up where it left off.
//
// For substantial multi-iteration work (e.g. "fix bugs in this app"),
// the inline 3-summary preview is too thin — the agent needs to know
// what FILES prior iterations touched, what tests they ran, what
// they decided. We point the agent at the persisted markdown so it
// can `read .gokin/loops/<id>.md` for full history when needed.
// Without this hint the agent re-derives prior work each iteration
// and drifts in direction.
//
// Exported so the runner package can test prompt shape and the
// memory writer can format consistently.
func BuildIterationPrompt(l *Loop) string {
	monitor := IsMonitorTask(l.Task)
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

	// Loop self-awareness (B): surface the loop's own position so the agent can
	// decide to stop / slow down / change approach instead of running blind. All
	// read from existing Loop state — purely additive context, no new control flow.
	var status []string
	if l.MaxIterations > 0 {
		switch rem := l.MaxIterations - l.IterationCount; {
		case rem == 1:
			status = append(status, "this is your LAST iteration")
		case rem > 1:
			status = append(status, fmt.Sprintf("%d iterations left", rem))
		}
	}
	if l.IterationCount > 0 {
		status = append(status, fmt.Sprintf("%d done so far (%d ok / %d failed)", l.IterationCount, l.SuccessCount, l.FailureCount))
	}
	if l.ConsecutiveFailures > 0 {
		status = append(status, fmt.Sprintf("%d consecutive failures (auto-pause at %d)", l.ConsecutiveFailures, ConsecutiveFailureLimit))
	}
	if l.MaxTotalTokens > 0 {
		pct := int(float64(l.TotalTokensIn+l.TotalTokensOut) / float64(l.MaxTotalTokens) * 100)
		status = append(status, fmt.Sprintf("token budget %d%% used", pct))
	}
	if len(status) > 0 {
		sb.WriteString("Loop status: " + strings.Join(status, "; ") + ".\n")
	}

	// Recent context (C1): wider window (3→6) + a per-iteration [no changes]
	// marker so the agent sees the trajectory and whether it's actually moving,
	// not just the last sentence. Iterations that changed files show them —
	// concrete anchors so the agent finds its own prior work without grepping.
	if len(l.Iterations) > 0 {
		sb.WriteString("\nRecent context (newest first):\n")
		showCount := 6
		if showCount > len(l.Iterations) {
			showCount = len(l.Iterations)
		}
		for i := len(l.Iterations) - 1; i >= len(l.Iterations)-showCount; i-- {
			it := l.Iterations[i]
			marker := ""
			if it.OK && !it.MadeChanges && !monitor {
				marker = " [no changes]"
			}
			fmt.Fprintf(&sb, "  - #%d (%s)%s: %s\n",
				it.N, it.StartedAt.Format("2006-01-02 15:04"), marker, it.Summary)
			if files := renderFilesTouchedInline(it.FilesTouched); files != "" {
				fmt.Fprintf(&sb, "    files: %s\n", files)
			}
		}
		// Pointer to the full log for the context the inline preview lacks.
		// UpdateMemory==false loops won't have this file, so only mention it then.
		if l.UpdateMemory {
			fmt.Fprintf(&sb, "\nFull iteration log (all %d, with files touched + outcomes): .gokin/loops/%s.md\n", l.IterationCount, l.ID)
			sb.WriteString("Read it when you need to see what prior iterations actually did, not just their last sentence.\n")
		}
	}

	// Cross-iteration working state (the HANDOFF loop): inject the newest
	// handoff VERBATIM so multi-iteration work continues as ONE project. The
	// newest non-empty handoff wins — a transient-failed iteration in between
	// (provider hiccup, no output) must not erase the working state.
	if handoff, n := latestHandoff(l.Iterations); handoff != "" {
		fmt.Fprintf(&sb, "\nYour working state from iteration #%d (you wrote this — it is your working memory between iterations):\n%s\n", n, handoff)
		if last := l.Iterations[len(l.Iterations)-1]; !last.OK && (!last.Transient || last.TimedOut) {
			sb.WriteString("Your previous iteration did NOT finish cleanly (failed or was cut off mid-work). Continue from the working state above — do NOT restart or redo work it lists as done.\n")
		} else {
			sb.WriteString("Continue from this state — don't re-derive or redo what it lists as done.\n")
		}
	}

	// Churn warning (A): succeeding but not changing anything → likely done or
	// stuck. Action tasks only (monitor loops are supposed to be no-change).
	if !monitor && l.ConsecutiveNoProgress >= NoProgressWarnThreshold {
		fmt.Fprintf(&sb, "\n⚠ Your last %d iterations made NO code changes. If the task is genuinely complete, end with `done.`. If you're stuck, CHANGE your approach — do not repeat what already didn't work. The loop auto-pauses after %d no-change iterations so it can't burn credits spinning.\n", l.ConsecutiveNoProgress, NoProgressLimit)
	}

	// Learn→persist→reload coaching: project guidelines + ProjectLearning are
	// already in context; lean on them and memorize new durable facts.
	sb.WriteString("\nThe project's conventions, build/test/lint commands, structure, and what prior iterations learned are already in your context — use them; don't re-derive what you already know. If you discover a DURABLE fact (a build/test/lint command, a convention, where something lives, a gotcha), call the `memorize` tool to save it so every future iteration starts smarter.")

	// Scope discipline (D): keep an unattended loop from drifting into unrequested
	// large changes on a vague task.
	sb.WriteString("\n\nStay focused on THIS task. Do NOT expand into unrelated refactors, reorganizations, or 'improvements' the task didn't ask for — if you think a broader change is warranted, note it in your summary instead of doing it unprompted.")

	// Handoff coaching (action tasks): the block above is only as good as what
	// the agent writes. Monitor loops don't carry plan state — their summaries
	// suffice — so keep their prompt lean.
	if !monitor {
		sb.WriteString("\n\nNear the end of your response — as its own paragraph, BEFORE your final summary sentence — write a `HANDOFF:` block of 2-6 short bullet lines (`- DONE: …`, `- IN PROGRESS: …`, `- NEXT: …`, `- BLOCKER: …`). It is carried verbatim into your next iteration and is your only working memory between iterations; without it the next iteration starts from scratch. Update it every iteration — never repeat a stale one. You also have a HARD per-iteration time budget (~30 minutes): write a first HANDOFF EARLY (after your plan) and refresh it after each milestone — the LAST one you wrote survives even if the budget cuts you off mid-work, so the next iteration continues instead of restarting.")
	}

	sb.WriteString("\n\nProceed with the next step. Keep the response focused — your last sentence will be captured as the iteration summary.")

	if l.Mode == ModeSelfPaced {
		sb.WriteString("\n\nIf there's nothing to do until a specific event, end with a line like 'next: 30m' or 'wait 1h' — the scheduler will respect it as the floor for the next iteration.")
	}

	// Done coaching (B), task-shape-aware. A monitor loop is supposed to keep
	// running (no-change is normal); an action loop should stop when verified done.
	if monitor {
		sb.WriteString("\n\nThis is a MONITORING task — it's expected to keep running and many iterations may find nothing to do; that's normal, not failure. Only end with a line containing only `done.` if the thing you watch is permanently resolved and there's nothing left to monitor.")
	} else {
		sb.WriteString("\n\nIf the task this loop was created for is genuinely complete — verify it (build/tests pass, the change is actually done) — end the response with a line containing only `done.` and the loop will stop. Don't keep iterating on a finished task.")
	}

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
// status markers, ATX-style markdown headers (`## Summary`). Keeps the
// summary readable: a heading-only paragraph is the section label, not
// the substance, and the substance lives in the next paragraph that
// summarizeOutput will then prefer.
func looksLikeMetadata(p string) bool {
	if strings.HasPrefix(p, "```") {
		return true
	}
	// A HANDOFF working-state block is machine-facing (re-injected into the
	// next iteration's prompt), not a human summary — if the agent puts it
	// LAST despite the coaching, skip it so the toast stays a real sentence.
	if strings.HasPrefix(strings.ToUpper(p), "HANDOFF:") {
		return true
	}
	if strings.HasPrefix(p, "---") || strings.HasPrefix(p, "===") {
		return true
	}
	if strings.HasPrefix(p, "#") {
		// Reject only when the entire paragraph is a single heading line
		// — not a paragraph that incidentally starts with `#` (e.g. a
		// shell prompt example like `# this comment is the answer`). A
		// real header has no embedded newlines.
		if !strings.ContainsRune(p, '\n') {
			return true
		}
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
// loopMachineErrWrapperRe strips retry-taxonomy wrappers ("model response
// error (other): ") from iteration summaries — they mean nothing to the
// user reading /loop status, the toast, or the markdown log. Mirrors the
// ui package's display strip (kept local — loops has no ui dependency,
// same precedent as renderTokenCount).
var loopMachineErrWrapperRe = regexp.MustCompile(`(?i)\b(model response error|function response error)(\s*\([^)]{0,40}\))?:\s*`)

// truncateErr prepares an error for the iteration summary: machine wrappers
// stripped, and over-long text elided in the MIDDLE — the tail is where a
// provider error's actionable half lives ("… switch provider with
// /provider"), and the old tail-cut at 150 runes amputated exactly that.
func truncateErr(s string) string {
	s = strings.TrimSpace(loopMachineErrWrapperRe.ReplaceAllString(s, ""))
	const maxRunes = 200
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	head := maxRunes * 2 / 3
	tail := maxRunes - head - 1
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
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

// parseDoneSignal returns true when the agent's output ends with an
// explicit "I'm finished" marker. Lets self-improvement loops
// terminate themselves when the agent decides the work is complete,
// instead of running until MaxIterations or until the user notices
// and types /loop stop.
//
// Conservative format: only the LAST non-empty line, exact match for
// "done.", "DONE", "loop done.", or "loop_done". Avoids false
// positives from prose like "Done with that file" or "I'm done
// reviewing this section" — the agent must opt in deliberately by
// ending its response with one of these specific markers, which the
// iteration prompt teaches.
// latestHandoff returns the newest non-empty handoff among the loop's
// iterations and the iteration number it came from. Scanning back past
// handoff-less iterations matters: a transient provider failure in between
// produces no output and must not erase the working state.
func latestHandoff(iterations []Iteration) (string, int) {
	for i := len(iterations) - 1; i >= 0; i-- {
		if h := iterations[i].Handoff; h != "" {
			return h, iterations[i].N
		}
	}
	return "", 0
}

// renderFilesTouchedInline renders a compact files list for the inline
// recent-context block: up to 4 paths, then "+N more". Returns "" for none.
func renderFilesTouchedInline(files []string) string {
	if len(files) == 0 {
		return ""
	}
	const show = 4
	if len(files) <= show {
		return strings.Join(files, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(files[:show], ", "), len(files)-show)
}

// parseHandoff extracts the agent's trailing "HANDOFF:" working-state block
// from an iteration's output. The block starts at the LAST line beginning
// with "HANDOFF:" (case-insensitive) and runs until the first blank line
// after it (the agent is coached to put its final summary sentence in a
// separate paragraph AFTER the block) — or until a closing `done.` marker
// line / end of output. Returns "" when no block is present. Bounded at
// MaxHandoffRunes: this is model output that re-enters the next prompt (the
// bounded-model-input class).
func parseHandoff(output string) string {
	lines := strings.Split(output, "\n")
	start := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(lines[i])), "HANDOFF:") {
			start = i
			break
		}
	}
	if start == -1 {
		return ""
	}

	var block []string
	// Keep whatever follows "HANDOFF:" on the same line (a one-line handoff).
	first := strings.TrimSpace(strings.TrimSpace(lines[start])[len("HANDOFF:"):])
	if first != "" {
		block = append(block, first)
	}
	for _, raw := range lines[start+1:] {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			break // end of the handoff paragraph
		}
		switch strings.ToLower(trimmed) {
		case "done.", "done", "loop done.", "loop_done", "loop: done":
			// The self-termination marker is not part of the working state.
			continue
		}
		block = append(block, line)
	}
	handoff := strings.TrimSpace(strings.Join(block, "\n"))
	if handoff == "" {
		return ""
	}
	if runes := []rune(handoff); len(runes) > MaxHandoffRunes {
		handoff = string(runes[:MaxHandoffRunes]) + "…"
	}
	return handoff
}

// capFilesTouched bounds a SpawnResult's touched-files list to
// MaxFilesTouchedPerIteration for storage on the Iteration.
func capFilesTouched(files []string) []string {
	if len(files) == 0 {
		return nil
	}
	if len(files) > MaxFilesTouchedPerIteration {
		files = files[:MaxFilesTouchedPerIteration]
	}
	out := make([]string, len(files))
	copy(out, files)
	return out
}

func parseDoneSignal(output string) bool {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		switch strings.ToLower(line) {
		case "done.", "done", "loop done.", "loop_done", "loop: done":
			return true
		}
		// Stop scanning at the first non-empty line — the marker must
		// be the closing line, not buried earlier in the output.
		return false
	}
	return false
}

// CancelInFlight cancels the currently-executing iteration of loopID ("" =
// whichever iteration is running). The spawned agent's context is killed, so
// it stops within its next cancellation check; partial work is preserved by
// the agent layer. The interrupted iteration is NOT recorded (a user kill is
// not a task failure). Returns whether an iteration was actually cancelled.
func (r *Runner) CancelInFlight(loopID string) bool {
	r.inFlightMu.Lock()
	defer r.inFlightMu.Unlock()
	if r.inFlightCancel == nil {
		return false
	}
	if loopID != "" && loopID != r.inFlightLoopID {
		return false
	}
	r.userCancelled = true
	r.inFlightCancel()
	return true
}
