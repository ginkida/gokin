package app

import (
	"context"
	"fmt"
	"strings"

	"gokin/internal/agent"
	"gokin/internal/logging"
	"gokin/internal/loops"
	"gokin/internal/ui"
)

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
// Returns (output, ok, err):
//   - output: agent's textual result, captured from result.Output.
//   - ok: true when result.Status == AgentStatusCompleted. False on
//     failure / cancellation / max-turn cutoff.
//   - err: non-nil only on Spawn-time error (transport, missing client).
//     A failed agent run reports via ok=false, not err — the loop
//     iteration still records what happened.
func newLoopSpawner(a *App) loops.Spawner {
	return func(ctx context.Context, prompt string) (string, bool, error) {
		runner := a.agentRunner
		if runner == nil {
			return "", false, fmt.Errorf("agent runner not initialized")
		}

		// Use the same model as the main client so the user gets
		// consistent behavior between manual interactions and loop
		// iterations. Empty model lets the runner pick the default.
		model := ""
		if a.client != nil {
			model = a.client.GetModel()
		}

		const maxTurns = 25

		agentID, spawnErr := runner.Spawn(ctx, "general", prompt, maxTurns, model)
		if spawnErr != nil && agentID == "" {
			// True Spawn-time failure (couldn't construct/register agent).
			// No agentID means there's no result to retrieve — surface
			// the error directly.
			return "", false, fmt.Errorf("spawn loop iteration: %w", spawnErr)
		}

		// Spawn is synchronous (blocks until agent.Run returns), so the
		// result is already in the runner's results map by the time we
		// get here — even if Spawn returned an error, the agent likely
		// produced partial output that we want to capture in the
		// iteration summary instead of throwing away.
		//
		// WaitWithContext's fast path hits immediately for a completed
		// result; we keep it (rather than calling GetResult) so a not-
		// yet-completed result waits cleanly for completion via the
		// channel notification.
		result, waitErr := runner.WaitWithContext(ctx, agentID)
		if waitErr != nil && result == nil {
			return "", false, fmt.Errorf("wait loop iteration: %w", waitErr)
		}

		ok := result != nil &&
			result.Status == agent.AgentStatusCompleted &&
			result.Error == "" &&
			spawnErr == nil

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

		return output, ok, nil
	}
}

// isLoopRunnerIdle reports whether the App is currently free for a
// loop iteration to fire. Loop iterations are deferred while the user
// is actively interacting (typing or agent processing) to avoid
// contending for the same client / executor.
//
// Reads:
//   - a.processing: true while a foreground request is in flight.
//   - a.pendingMessage: true while a message is queued behind another.
//
// Both are short reads under a.mu; cheap to call from the scheduler's
// every-30s tick.
func (a *App) isLoopRunnerIdle() bool {
	a.mu.Lock()
	processing := a.processing
	a.mu.Unlock()
	if processing {
		return false
	}
	a.pendingMu.Lock()
	pending := a.pendingMessage != ""
	a.pendingMu.Unlock()
	return !pending
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

// onLoopIterationDone fires after the iteration result is recorded.
// Logs the outcome for post-mortem, posts a status update, and (per
// the loop's UpdateMemory setting) writes the latest snapshot to a
// human-readable markdown file under <workDir>/.gokin/loops/<id>.md.
//
// The markdown write happens AFTER the manager's RecordIteration call
// — the JSON state is the source of truth; the markdown is a
// convenience for grep / browser navigation.
func (a *App) onLoopIterationDone(loopID string, it loops.Iteration) {
	statusType := ui.StatusInfo
	if !it.OK {
		statusType = ui.StatusWarning
	}
	logging.Info("loops: iteration done",
		"loop_id", loopID,
		"iteration", it.N,
		"ok", it.OK,
		"duration", it.Duration)
	a.safeSendToProgram(ui.StatusUpdateMsg{
		Type:    statusType,
		Message: fmt.Sprintf("Loop %s #%d: %s", loopID, it.N, it.Summary),
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
				logging.Warn("loops: auto-paused after consecutive failures",
					"loop_id", loopID,
					"consecutive_failures", loop.ConsecutiveFailures)
				a.safeSendToProgram(ui.StatusUpdateMsg{
					Type: ui.StatusRecoverableError,
					Message: fmt.Sprintf(
						"Loop %s auto-paused after %d failures in a row. /loop output %s for details, /loop resume %s when fixed.",
						loopID, loop.ConsecutiveFailures, loopID, loopID),
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
