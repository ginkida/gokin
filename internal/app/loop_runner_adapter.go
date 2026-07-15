package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gokin/internal/agent"
	"gokin/internal/client"
	"gokin/internal/logging"
	"gokin/internal/loops"
	"gokin/internal/ui"
)

// loopFailureIsTransient reports whether a failed loop iteration failed for a
// transient INFRASTRUCTURE reason (provider overload/rate-limit, network) as
// opposed to a genuine task failure. Classified here (in internal/app, which
// can import internal/client) from the structured error strings BEFORE they're
// concatenated into the iteration output — so the scheduler can wait the
// provider out with backoff instead of tripping the task-failure auto-pause.
// See client.IsTransientProviderError for why the set is deliberately tight
// (excludes ambiguous timeouts that may be the agent's own task overrunning).
func loopFailureIsTransient(result *agent.AgentResult, spawnErr, waitErr error) bool {
	if isIterationTimeout(spawnErr) || isIterationTimeout(waitErr) {
		return true
	}
	if client.IsTransientProviderError(spawnErr) || client.IsTransientProviderError(waitErr) {
		return true
	}
	if result != nil && result.Error != "" {
		err := errors.New(result.Error)
		return isIterationTimeout(err) || client.IsTransientProviderError(err)
	}
	return false
}

// isIterationTimeout reports whether err is the iteration's OWN timeout
// (iterationCtx deadline, DefaultIterationTimeout=15m) firing — as opposed to a
// genuine task failure. The runner derives iterationCtx via context.WithTimeout,
// so a DeadlineExceeded here means "this iteration ran too long", a CADENCE
// problem, NOT a task failure: it must not trip the 5-failure auto-pause breaker.
// Treated as transient so the loop-level exponential backoff (the right response
// to a too-tight cadence / a slow-because-overloaded provider) kicks in, with the
// far-more-lenient transient backstop still catching a permanently-stuck loop.
//
// Deliberately ADAPTER-LOCAL: client.IsTransientProviderError correctly EXCLUDES
// bare "deadline exceeded" for the shared request/agent retry layers (where a
// timeout could be the agent's own task overrunning, see CLAUDE.md). Only HERE
// does a deadline provably mean the iteration ctx fired. NOTE: Fix #1's "reached
// maximum turn limit" error is NOT a deadline, so it correctly stays a task
// failure (a task that can't fit the turn budget IS a task problem).
func isIterationTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "deadline exceeded")
}

// describeLoopNext returns a short human-readable suffix for the iteration-
// done toast that tells the user when the next iteration will run (or
// that there won't be one). Examples:
//   - "next in 5m"        — loop is running, next fire is in 5 minutes
//   - "next now"          — loop is running and due immediately
//   - "completed"         — loop hit MaxIterations on this iteration
//   - "stopped"           — agent emitted `done.` or user stopped
//   - "auto-paused"       — consecutive-failure breaker tripped
//   - ""                  — paused (no auto-pause), or loop is gone
//
// Mirrors the language users see in /loop status so they can predict the
// next iteration without typing a command.
func describeLoopNext(loop *loops.Loop) string {
	switch loop.Status {
	case loops.StatusCompleted:
		return "completed"
	case loops.StatusStopped:
		return "stopped"
	case loops.StatusPaused:
		if loop.AutoPaused {
			return "auto-paused"
		}
		return ""
	}

	if loop.NextRunAt.IsZero() {
		return ""
	}
	dur := time.Until(loop.NextRunAt)
	if dur < 0 {
		return "next now"
	}
	return "next in " + formatLoopDurationShort(dur)
}

// formatLoopDurationShort renders a time.Duration in the same compact form
// used by /loop list and /loop status (e.g. "5m", "1h30m", "2d"). Mirrored
// here so the iteration-done toast can show "next in <dur>" without
// pulling commands/loop_cmd.go's helper across packages.
func formatLoopDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	return fmt.Sprintf("%dd", days)
}

// renderLoopTokens renders a token count compactly (1234 -> "1.2K",
// 2_500_000 -> "2.5M"). Mirrors loops.renderTokenCount / commands.formatTokenCount,
// kept local so the app package needn't reach across for one formatter.
func renderLoopTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fK", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// newLoopSpawner returns a loops.Spawner that uses the App's existing
// agent.Runner to execute each iteration in an isolated sub-agent.
//
// Why a sub-agent rather than injecting through the main message
// pipeline:
//
//  1. Isolation — loop iterations don't pollute the user's main chat
//     session. A long-running daily loop would otherwise accumulate
//     hundreds of messages in the user's history.
//  2. Cancellation — sub-agents have their own ctx; killing a stuck
//     iteration doesn't affect the user's foreground request.
//  3. Visibility — /loop status reads results from loop state files,
//     decoupling display from the active session.
//
// agentType "general" is the catch-all type that does whatever the task
// description says. maxTurns=25 caps a runaway iteration while leaving
// enough room for substantive engineering work: a typical "fix bug X"
// iteration does grep+glob (2) → read 3-5 files (3-5) → run tests (1)
// → edit 1-3 files (3) → re-run tests (1) → commit (1) — that's ~12-15
// turns minimum. The default sub-agent cap of 15 was leaving no
// headroom for retries or multi-file fixes; 25 feels right (less than
// 2x cost vs 15, but enough headroom for real work).
//
// Each iteration runs via SpawnWithContext (the same path plan execution uses)
// so the loop agent receives the FULL durable project context — project
// guidelines (build/test/lint commands), project instructions, ProjectLearning,
// the memory store, session + working memory, and the active contract — instead
// of starting blind and re-deriving conventions every iteration.
//
// Returns a SpawnResult (output + ok + transient classification):
//   - output: agent's textual result, captured from result.Output (partial
//     work preserved even on failure).
//   - ok: true when result.Status == AgentStatusCompleted. False on
//     failure / cancellation / max-turn cutoff (reported via ok=false, not err).
//   - err: non-nil only when the agent runner isn't initialized.
func newLoopSpawner(a *App) loops.Spawner {
	return func(ctx context.Context, prompt string) (loops.SpawnResult, error) {
		runner := a.agentRunner
		if runner == nil {
			return loops.SpawnResult{}, fmt.Errorf("agent runner not initialized")
		}

		// Use the same model as the main client so the user gets
		// consistent behavior between manual interactions and loop
		// iterations. Empty model lets the runner pick the default.
		// Read the client under clientMu (leaf lock) — this closure runs on the
		// /loop scheduler's background goroutine and races the ApplyConfig/failover
		// a.client swap; a bare read is a data race.
		model := ""
		if cl := a.clientSnapshot(); cl != nil {
			model = cl.GetModel()
		}

		const maxTurns = 25

		// Build the durable project context so a /loop iteration UNDERSTANDS the
		// project like a foreground turn instead of re-deriving conventions every
		// time. BuildSubAgentPrompt aggregates project guidelines (build/test/lint
		// commands from detection), project instructions, ProjectLearning, the
		// memory store, session + working memory, and the active contract — and
		// SpawnWithContext injects it via SetProjectContext. This is the
		// load-bearing half of the LEARN→PERSIST→RELOAD loop: memorize-written
		// facts (and everything else aggregated here) flow into the NEXT
		// iteration's system prompt. Built on the scheduler goroutine; the
		// promptBuilder's memory-pointer fields are boot-set (not re-pointed by
		// ApplyConfig) and internally locked, so this is race-safe, and
		// applyPromptBudget caps the size.
		projectCtx := ""
		if a.promptBuilder != nil {
			projectCtx = a.promptBuilder.BuildSubAgentPromptForTask(prompt)
		}

		// SpawnWithContext runs the agent to completion synchronously and returns
		// a NON-NIL result directly (no separate Wait). onText=nil — loop
		// iterations don't stream to the TUI, only the summary matters.
		// skipPermissions=false — loops honor the user's permission policy.
		_, result, spawnErr := runner.SpawnWithContext(ctx, "general", prompt, maxTurns, model, projectCtx, nil, false, nil)

		ok := result != nil &&
			result.Status == agent.AgentStatusCompleted &&
			result.Error == "" &&
			spawnErr == nil

		// Classify a non-OK result from the structured errors (before they get
		// concatenated into output below) so the scheduler can wait out a
		// transient provider problem instead of counting it as a task failure.
		transient := !ok && loopFailureIsTransient(result, spawnErr, nil)

		output := ""
		if result != nil {
			output = strings.TrimSpace(result.Output)
			if !ok && result.Error != "" {
				// Surface the agent's error in the output so the iteration
				// summary captures something useful.
				if output == "" {
					output = "agent error: " + result.Error
				} else {
					output += "\n\n[agent error: " + result.Error + "]"
				}
			}
		}
		// If Spawn errored but we DID capture partial output, append the
		// spawn-side error too — it's the proximate cause and the user
		// will want to see it next to whatever the agent managed to say.
		if spawnErr != nil {
			suffix := "[spawn error: " + spawnErr.Error() + "]"
			if output == "" {
				output = suffix
			} else {
				output += "\n\n" + suffix
			}
		}

		out := loops.SpawnResult{Output: output, OK: ok, Transient: transient}
		if result != nil {
			out.TokensIn = result.InputTokens
			out.TokensOut = result.OutputTokens
			// Churn signal: did the iteration run any code/repo-mutating tool? A
			// run of OK-but-no-change iterations on an action task is "spinning"
			// (task done or stuck) — the scheduler warns then auto-pauses.
			out.MadeChanges = result.MutatingToolCalls > 0
			// Concrete anchors for the next iteration: which files this one
			// actually changed (workDir-relative, from the agent's done-gate
			// touched-paths ledger). Capped at record time by the loops layer.
			out.FilesTouched = result.TouchedPaths
		}
		return out, nil
	}
}

// isLoopRunnerIdle reports whether the App is currently free for a
// loop iteration to fire. Loop iterations are deferred while the user
// is actively interacting (typing or agent processing) to avoid
// contending for the same client / executor.
//
// Reads:
//   - a.processing: true while a foreground request is in flight.
//   - the type-ahead queue: non-empty while messages wait behind another.
//
// Both are short mutex reads; cheap to call from the scheduler's
// every-30s tick.
func (a *App) isLoopRunnerIdle() bool {
	a.mu.Lock()
	processing := a.processing
	a.mu.Unlock()
	if processing {
		return false
	}
	return a.pendingCount() == 0
}

// onLoopIterationStart fires before the spawn call. Surfaces a
// non-intrusive status toast so the user can see the loop is alive.
// Status messages flow through StatusInfo (auto-clears) so they don't
// pile up in the chat history.
func (a *App) onLoopIterationStart(loopID string) {
	logging.Info("loops: iteration starting", "loop_id", loopID)
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    ui.StatusInfo,
		Message: fmt.Sprintf("Loop %s iteration starting in background...", loopID),
	})
}

// onLoopIterationPersistFailed fires when an iteration ran but its result
// couldn't be saved to disk (the manager rolled the in-memory state back).
// Surfaces an honest, actionable warning instead of the misleading success
// toast the old fall-through showed.
func (a *App) onLoopIterationPersistFailed(loopID string, n int, err error) {
	logging.Warn("loops: iteration ran but could not be persisted",
		"loop_id", loopID, "iteration", n, "error", err)
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type: ui.StatusRecoverableError,
		Message: fmt.Sprintf(
			"Loop %s #%d ran but couldn't be saved to disk (%v) — check disk space / permissions; the iteration was not recorded.",
			loopID, n, err),
	})
}

// onLoopIterationDone fires after the iteration result is recorded.
// Logs the outcome for post-mortem, posts a status update, and (per
// the loop's UpdateMemory setting) writes the latest snapshot to a
// human-readable markdown file under <workDir>/.gokin/loops/<id>.md.
//
// The markdown write happens AFTER the manager's RecordIteration call
// — the JSON state is the source of truth; the markdown is a
// convenience for grep / browser navigation.
func (a *App) onLoopIterationDone(loopID string, it loops.Iteration) {
	// A transient (provider overloaded / network) failure is expected and
	// self-healing — the loop backs off and retries — so keep it calm (Info),
	// reserving the Warning severity for genuine task failures the user may
	// need to act on.
	statusType := ui.StatusInfo
	if !it.OK && !it.Transient {
		statusType = ui.StatusWarning
	}
	logging.Info("loops: iteration done",
		"loop_id", loopID,
		"iteration", it.N,
		"ok", it.OK,
		"duration", it.Duration)

	// Compute the "next fire" suffix from the post-iteration loop state.
	// Re-read the manager to capture the freshly-set NextRunAt (the
	// RecordIteration call that just preceded us recomputed it from the
	// agent's optional `next:` hint).
	nextSuffix := ""
	// Ring the bell when the loop FINISHED on this iteration (max-iterations
	// completion or agent `done.` self-stop) — a "come back, it's done" signal
	// for a user who stepped away. The auto-pause path below rings its own bell
	// (it's a Paused state, not Completed/Stopped, so no double-ring).
	bell := false
	if a.loopManager != nil {
		if loop, ok := a.loopManager.Get(loopID); ok {
			nextSuffix = describeLoopNext(loop)
			bell = loop.Status == loops.StatusCompleted || loop.Status == loops.StatusStopped
		}
	}

	msg := fmt.Sprintf("Loop %s #%d: %s", loopID, it.N, it.Summary)
	if nextSuffix != "" {
		msg += " — " + nextSuffix
	}
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    statusType,
		Message: msg,
		Bell:    bell,
	})

	// Re-read the loop from the manager so we capture post-iteration
	// state (auto-pause flag, markdown content). The `it` parameter is
	// just the latest result; the manager has the full picture.
	if a.loopManager != nil {
		if loop, ok := a.loopManager.Get(loopID); ok {
			// Surface the auto-pause to the user via a high-visibility
			// warning toast. AppendIteration sets AutoPaused=true when
			// ConsecutiveFailures crosses the limit; without surfacing
			// it here, the user wouldn't know why the loop suddenly
			// stopped firing — they'd think gokin was broken.
			if loop.AutoPaused && loop.Status == loops.StatusPaused {
				// Two distinct causes: a genuine TASK-failure streak vs. a
				// provider that's been unavailable for a very long run of
				// TRANSIENT failures (the backstop). Report the right one —
				// ConsecutiveFailures is 0 on a transient-backstop pause, so
				// the old "after 0 failures" wording would confuse the user.
				// Report the TRUE trigger recorded at the pause site, not a
				// reconstruction from state (which mislabels overlapping
				// conditions — e.g. a failing task that also crossed its
				// budget paused for FAILURES, not budget).
				var msg string
				switch loop.AutoPauseReason {
				case loops.AutoPauseTokenBudget:
					spent := loop.TotalTokensIn + loop.TotalTokensOut
					logging.Warn("loops: auto-paused — token budget reached",
						"loop_id", loopID, "spent", spent, "budget", loop.MaxTotalTokens)
					msg = fmt.Sprintf(
						"Loop %s auto-paused — token budget reached (~%s spent / %s cap). Start a new loop with a higher --max-tokens, or /loop resume %s.",
						loopID, renderLoopTokens(spent), renderLoopTokens(loop.MaxTotalTokens), loopID)
				case loops.AutoPauseProviderUnavailable:
					logging.Warn("loops: auto-paused — provider unavailable for an extended period",
						"loop_id", loopID,
						"consecutive_transient_failures", loop.ConsecutiveTransientFailures)
					msg = fmt.Sprintf(
						"Loop %s auto-paused — provider unavailable for a long time (%d retries). /loop resume %s when it's back.",
						loopID, loop.ConsecutiveTransientFailures, loopID)
				case loops.AutoPauseNoProgress:
					logging.Warn("loops: auto-paused — no progress (succeeding without changes)",
						"loop_id", loopID, "consecutive_no_progress", loop.ConsecutiveNoProgress)
					msg = fmt.Sprintf(
						"Loop %s auto-paused — %d iterations in a row made no changes (task likely complete, or stuck). /loop output %s to review, /loop resume %s if there's more to do.",
						loopID, loop.ConsecutiveNoProgress, loopID, loopID)
				default:
					logging.Warn("loops: auto-paused after consecutive failures",
						"loop_id", loopID,
						"consecutive_failures", loop.ConsecutiveFailures)
					msg = fmt.Sprintf(
						"Loop %s auto-paused after %d failures in a row. /loop output %s for details, /loop resume %s when fixed.",
						loopID, loop.ConsecutiveFailures, loopID, loopID)
				}
				a.safeSendToProgram(ui.StatusUpdateMsg{
					Type:    ui.StatusRecoverableError,
					Message: msg,
					Bell:    true, // auto-pause needs attention — ring it
				})
			} else if loop.Status == loops.StatusRunning &&
				loop.ConsecutiveTransientFailures == loops.TransientFailureWarnThreshold {
				// Mid-streak heads-up: the loop is still running and backing off,
				// but the provider has been struggling for a while. Fire ONCE
				// (== threshold) so the user can intervene early instead of only
				// finding out at the far-off backstop. Calm Warning (self-healing)
				// with an actionable hint.
				logging.Warn("loops: provider struggling mid-streak",
					"loop_id", loopID,
					"consecutive_transient_failures", loop.ConsecutiveTransientFailures)
				a.safeSendToProgram(ui.StatusUpdateMsg{
					Type: ui.StatusWarning,
					Message: fmt.Sprintf(
						"Loop %s: provider unavailable for %d iterations and still retrying (auto-pauses at %d). Consider /provider or checking your connection.",
						loopID, loop.ConsecutiveTransientFailures, loops.TransientFailureLimit),
				})
			}

			// Write per-loop markdown for human-readable persistent
			// context. Skip silently when memory writer is disabled
			// (e.g. unit-test builds without workDir).
			if a.loopMemory != nil && loop.UpdateMemory {
				if err := a.loopMemory.WriteLoop(loop); err != nil {
					logging.Warn("loops: failed to write iteration markdown",
						"loop_id", loopID, "error", err)
				}
			}
		}
	}
}

// CancelInFlightLoopIteration kills the currently-executing iteration of the
// given loop ("" = any). Used by /loop stop and the loop_control tool so that
// STOPPING a loop also stops the work the user is watching right now —
// pause stays graceful (in-flight iteration finishes) by design.
func (a *App) CancelInFlightLoopIteration(loopID string) bool {
	if a.loopRunner == nil {
		return false
	}
	return a.loopRunner.CancelInFlight(loopID)
}
