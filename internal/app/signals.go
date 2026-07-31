package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"gokin/internal/commands"
	"gokin/internal/logging"
	"gokin/internal/ui"
)

const (
	// GracefulShutdownTimeout is the maximum time to wait for graceful shutdown.
	GracefulShutdownTimeout = 10 * time.Second
	// ForcedShutdownTimeout is the time after which we force exit.
	ForcedShutdownTimeout = 15 * time.Second
)

// GoroutineTracker tracks running goroutines for graceful shutdown.
type GoroutineTracker struct {
	mu       sync.Mutex
	closed   bool
	inFlight int
	idle     chan struct{}
}

// NewGoroutineTracker creates a new goroutine tracker.
func NewGoroutineTracker() *GoroutineTracker {
	return &GoroutineTracker{}
}

// Add registers a new goroutine to track.
func (t *GoroutineTracker) Add() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	if t.inFlight == 0 {
		t.idle = make(chan struct{})
	}
	t.inFlight++
	return true
}

// Done marks a goroutine as completed.
func (t *GoroutineTracker) Done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inFlight == 0 {
		panic("app: GoroutineTracker.Done called without matching Add")
	}
	t.inFlight--
	if t.inFlight == 0 {
		close(t.idle)
		t.idle = nil
	}
}

// Wait waits for all tracked goroutines to complete.
func (t *GoroutineTracker) Wait() {
	t.mu.Lock()
	idle := t.idle
	t.mu.Unlock()
	if idle != nil {
		<-idle
	}
}

// WaitWithTimeout waits for all goroutines with a timeout.
// Returns true if all goroutines completed, false if timed out.
func (t *GoroutineTracker) WaitWithTimeout(timeout time.Duration) bool {
	t.mu.Lock()
	idle := t.idle
	t.mu.Unlock()
	if idle == nil {
		return true
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-idle:
		return true
	case <-timer.C:
		return false
	}
}

// WaitWithContext waits until all work admitted before Close has completed.
func (t *GoroutineTracker) WaitWithContext(ctx context.Context) error {
	t.mu.Lock()
	idle := t.idle
	t.mu.Unlock()
	if idle == nil {
		return nil
	}

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close prevents new goroutines from being added.
func (t *GoroutineTracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
}

// setupSignalHandler sets up signal handling for graceful shutdown.
// First Ctrl+C cancels current operation; second Ctrl+C forces full shutdown.
// Returns a cleanup function that should be called when the app exits.
func (a *App) setupSignalHandler() func() {
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)

	// Done channel to signal goroutine termination
	done := make(chan struct{})

	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.Error("signal handler recovered from panic", "panic", r)
			}
		}()
		for {
			select {
			case sig := <-sigChan:
				logging.Debug("received signal", "signal", sig)

				// SIGTERM/SIGQUIT — always full shutdown
				if sig == syscall.SIGTERM || sig == syscall.SIGQUIT {
					a.forceShutdown(sig)
					return
				}

				// First Ctrl+C uses the same complete lifecycle as Esc: cancel the
				// foreground owner, close steering, clear type-ahead, and reset any
				// Stop-hook continuation. Calling the raw context cancel here left
				// queued work able to auto-start and kept the cancel handle non-nil,
				// so even a second Ctrl+C could fail to exit.
				if a.cancelProcessing() {
					logging.Debug("cancelling current operation (first Ctrl+C)")
					a.safeSendToProgram(ui.StatusUpdateMsg{
						Type:    ui.StatusCancelled,
						Message: "Canceling... (Ctrl+C again to exit)",
					})
					// Wait for second signal for full shutdown
					continue
				}

				// No active processing — full shutdown
				a.forceShutdown(sig)
				return

			case <-done:
				return

			case <-a.ctx.Done():
				return
			}
		}
	}()

	// Return cleanup function
	return func() {
		signal.Stop(sigChan)
		close(done)
	}
}

// forceShutdown performs a full graceful shutdown and exits.
func (a *App) forceShutdown(sig os.Signal) {
	forceExitTimer := time.AfterFunc(ForcedShutdownTimeout, func() {
		logging.Warn("forced shutdown due to timeout")
		os.Exit(1)
	})
	defer forceExitTimer.Stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), GracefulShutdownTimeout)
	defer cancel()

	a.gracefulShutdown(shutdownCtx)

	if sig == syscall.SIGQUIT {
		logging.Info("exiting with core dump")
		os.Exit(128 + int(syscall.SIGQUIT))
	}
	os.Exit(0)
}

// gracefulShutdown performs a graceful shutdown with timeout.
func (a *App) gracefulShutdown(ctx context.Context) {
	logging.Debug("starting graceful shutdown")

	// 1. Cancel all ongoing operations (this signals goroutines to stop)
	a.beginShutdown()

	// 2. Cleanup signal handler
	if a.signalCleanup != nil {
		a.signalCleanup()
		a.signalCleanup = nil
	}

	// 3. Stop UI update manager
	if a.uiUpdateManager != nil {
		logging.Debug("stopping UI update manager")
		a.uiUpdateManager.Stop()
	}

	// 3b. Stop coordinator and meta-agent goroutines
	if a.coordinator != nil {
		logging.Debug("stopping coordinator")
		a.coordinator.Stop()
	}
	if a.metaAgent != nil {
		logging.Debug("stopping meta-agent")
		a.metaAgent.Stop()
	}

	// 3c. Stop the loops scheduler BEFORE cancelling agent goroutines
	// (step 4b) so it doesn't fire one final iteration in the gap
	// between ctx cancel and process exit. The Runner exits cleanly on
	// stopChan; safe to call before Start (sync.Once-protected).
	if a.loopRunner != nil {
		logging.Debug("stopping loops scheduler")
		a.loopRunner.Stop()
	}

	// 4. Cancel all running background tasks
	if a.taskManager != nil {
		logging.Debug("cancelling background tasks")
		a.taskManager.CancelAll()
	}

	// 4b. Cancel all running background agents
	var cancelledAgentIDs []string
	if a.agentRunner != nil {
		cancelledAgentIDs = a.agentRunner.CancelAll()
		for _, agentID := range cancelledAgentIDs {
			logging.Debug("cancelled background agent; awaiting finalization", "agent_id", agentID)
		}
	}

	// 4c. Cancellation only signals work to stop. Do not tear down MCP/client
	// dependencies or exit while run goroutines are still committing tool-pair
	// history, closing transcript files, applying isolated workspaces, and
	// persisting terminal results. Shell and agent waits share the shutdown
	// deadline and run concurrently so one slow group cannot starve the other.
	a.waitForBackgroundFinalization(ctx, cancelledAgentIDs)

	// Persist the result of the lifecycle barrier before tearing down session
	// dependencies. If foreground work finalized, its terminal transition has
	// cleared processing and the next launch must not report a false crash. If
	// the shutdown deadline expired first, processing deliberately remains true
	// so startup recovery honestly reports an interrupted request.
	if err := a.saveRecoverySnapshot(); err != nil {
		// The TUI has already stopped on the normal quit path. stderr is the
		// only reliable user-visible channel left; a log-only warning can be
		// invisible when file logging is disabled.
		logging.Error("final recovery snapshot failed during shutdown",
			"error", err)
		fmt.Fprintf(os.Stderr,
			"WARNING: failed to save crash-recovery state at shutdown: %v\n", err)
	}

	// 5. Shutdown MCP servers
	if a.mcpManager != nil {
		logging.Debug("shutting down MCP servers")
		if err := a.mcpManager.Shutdown(ctx); err != nil {
			logging.Debug("error shutting down MCP", "error", err)
		}
	}

	// 5b. Stop the independently managed gopls MCP process. It is lazy, so this
	// is a cheap no-op when semantic intelligence was never used.
	if a.codeIntelProvider != nil {
		logging.Debug("shutting down managed Go intelligence")
		if err := a.codeIntelProvider.Close(); err != nil {
			logging.Warn("failed to shut down managed Go intelligence", "error", err)
		}
	}

	// 6. Stop file watcher
	if a.fileWatcher != nil {
		logging.Debug("stopping file watcher")
		if err := a.fileWatcher.Stop(); err != nil {
			logging.Debug("error stopping file watcher", "error", err)
		}
	}

	// 6b. Stop search cache cleanup goroutine. Snapshot under a.mu — same
	// field ApplyConfig writes under the lock (see the OnFileChange
	// callback in app.go for the matching read-side fix).
	a.mu.Lock()
	sc := a.searchCache
	a.mu.Unlock()
	if sc != nil {
		sc.StopCleanup()
	}

	// 7b. Save active plan for later resume
	if a.planManager != nil {
		plan := a.planManager.GetCurrentPlan()
		if plan != nil && !plan.IsComplete() {
			logging.Debug("saving active plan for resume", "plan_id", plan.ID, "status", plan.Status)
			if err := a.planManager.SaveCurrentPlan(); err != nil {
				// Class match for v0.80.8: silent persistence failure
				// at shutdown means /resume-plan in the next session
				// will see stale or missing data. Surface the loss.
				logging.Error("failed to save active plan during shutdown — /resume-plan may not work next session",
					"plan_id", plan.ID, "error", err)
			}
		}
	}

	// 8. Cleanup spawned editor processes
	logging.Debug("cleaning up spawned processes")
	commands.CleanupSpawnedProcesses()

	// 10. Run on_exit hooks with timeout
	if a.hooksManager != nil {
		logging.Debug("running on_exit hooks")
		a.hooksManager.RunOnExit(ctx)
	}

	// 11. Save input history
	if a.tui != nil {
		if err := a.tui.SaveInputHistory(); err != nil {
			// Input history is the up-arrow command palette. Failing to
			// save means the user's recent commands won't be there next
			// session — annoying but not catastrophic. Warn so chronic
			// failures (perm flip, disk full) get noticed without
			// burying the gokin log under each shutdown.
			logging.Warn("failed to save input history during shutdown — recent commands won't appear next session",
				"error", err)
		}
	}

	// 12. Flush agent data (project learning) to prevent data loss
	if a.agentRunner != nil {
		logging.Debug("flushing agent data")
		a.agentRunner.Close()
	}

	// 12b. Flush persistent memory stores
	if a.errorStore != nil {
		if err := a.errorStore.Flush(); err != nil {
			// errorStore is the agent's learned-error map; flush failure
			// loses any new error patterns recorded this session. Warn
			// so chronic flush failures surface in field logs.
			logging.Warn("failed to flush error store during shutdown — learned error patterns from this session may be lost",
				"error", err)
		}
	}
	if a.exampleStore != nil {
		if err := a.exampleStore.Flush(); err != nil {
			// Same class as errorStore — example store holds curated
			// successful examples; chronic flush failure silently
			// regresses agent quality.
			logging.Warn("failed to flush example store during shutdown — learning from this session may be lost",
				"error", err)
		}
	}
	if a.memoryStore != nil {
		// The `memory` tool's kv store (remember/recall/forget) — was
		// missing from this block entirely (only errorStore/exampleStore
		// were flushed, despite Store.Flush() existing and being the exact
		// same class of debounced-save-on-shutdown concern). Store.Add
		// (the "remember" action) only schedules a 2s debounced save; a
		// user's last instruction being "remember X" followed by quitting
		// within that window silently lost the fact — the tool reported
		// success, but the process exited before the debounce timer fired.
		if err := a.memoryStore.Flush(); err != nil {
			logging.Warn("failed to flush memory store during shutdown — recently remembered facts may be lost",
				"error", err)
		}
	}
	if a.sessionMemory != nil {
		if err := a.sessionMemory.Close(ctx); err != nil {
			logging.Warn("failed to finish session memory extraction during shutdown — latest session summary may be stale",
				"error", err)
		}
	}

	// 13. Flush audit logger to ensure all entries are persisted.
	// Save errors used to be silently dropped — for an audit log used
	// to investigate incidents, that meant entries could vanish at
	// shutdown without anyone knowing. Now Flush returns the error;
	// surface it the same way as session save (Error log + stderr
	// since TUI is torn down by this point).
	if a.auditLogger != nil {
		logging.Debug("flushing audit logger")
		if err := a.auditLogger.Flush(); err != nil {
			logging.Error("audit log flush failed during shutdown — recent entries may be lost",
				"error", err)
			fmt.Fprintf(os.Stderr, "WARNING: failed to flush audit log at shutdown: %v\n", err)
		}
	}

	// 14. Stop and save session history via session manager (preferred) or fallback
	if a.sessionManager != nil {
		a.sessionManager.Stop()
		// Final session save during graceful shutdown. The prior version
		// discarded the error (`_ = ...`), so a save failure here meant
		// the user lost their conversation history without any signal —
		// e.g. disk full, permission revoked, sandboxed write blocked.
		// Surface to the log at Error level so post-mortem can see it,
		// and emit a stderr line as a last-ditch user notification (the
		// TUI is already torn down by this point in shutdown).
		if err := a.sessionManager.Save(); err != nil {
			logging.Error("final session save failed during shutdown — recent conversation may be lost",
				"error", err)
			fmt.Fprintf(os.Stderr, "WARNING: failed to save session at shutdown: %v\n", err)
		}
	} else {
		a.saveSessionHistory()
	}
	// Signal shutdown exits via os.Exit, so Run/main defers do not execute.
	// Release only after the final save, preserving the writer-exclusivity
	// invariant through the last persistence operation.
	if err := a.ReleaseSessionWriterLease(); err != nil {
		logging.Warn("failed to release session writer lease during shutdown", "error", err)
	}

	// 13. Close client
	if a.client != nil {
		_ = a.client.Close()
	}

	// 14. Close logging last
	logging.Debug("shutdown complete")
	logging.Close()
}

func (a *App) waitForBackgroundFinalization(ctx context.Context, agentIDs []string) {
	type waitResult struct {
		kind string
		err  error
	}
	waiters := 0
	results := make(chan waitResult, 3)

	waiters++
	go func() {
		results <- waitResult{
			kind: "foreground processing",
			err:  a.foregroundWorkers.WaitWithContext(ctx),
		}
	}()

	if a.taskManager != nil {
		waiters++
		go func() {
			results <- waitResult{kind: "shell tasks", err: a.taskManager.WaitAll(ctx)}
		}()
	}
	if a.agentRunner != nil && len(agentIDs) > 0 {
		waiters++
		go func() {
			results <- waitResult{
				kind: "agents",
				err:  a.agentRunner.WaitAllWithContext(ctx, agentIDs),
			}
		}()
	}

	for range waiters {
		select {
		case result := <-results:
			if result.err != nil {
				logging.Warn("background finalization incomplete during shutdown",
					"kind", result.kind, "error", result.err)
			}
		case <-ctx.Done():
			logging.Warn("shutdown deadline reached while awaiting background finalization",
				"error", ctx.Err())
			return
		}
	}
}

// beginShutdown atomically closes foreground admission, then cancels the owner
// that may already be running. Close-before-wait guarantees no Add can race
// the shutdown snapshot: a request that claimed processing just before this
// boundary is either already tracked or rejected by Add.
func (a *App) beginShutdown() {
	a.mu.Lock()
	a.shuttingDown = true
	a.dropSteerLeftovers = true
	a.mu.Unlock()

	// Close is idempotent. Call it on every entry so concurrent quit/signal
	// paths cannot observe shuttingDown=true before the first caller has sealed
	// the tracker and begin waiting while Add is still possible.
	a.foregroundWorkers.Close()

	a.processingMu.Lock()
	if a.processingCancel != nil {
		a.processingCancel()
	}
	a.processingMu.Unlock()

	if a.cancel != nil {
		a.cancel()
	}
}

// saveSessionHistory saves the current session to disk.
//
// Fallback path used when sessionManager is nil. Errors here are equally
// fatal for persistence as the primary path in shutdown() — a Debug log
// (the prior version) was invisible in normal operation, silently
// dropping the same data v0.80.8 was meant to protect. Both failure
// branches now log at Error level + emit a stderr warning so the user
// sees what was lost.
func (a *App) saveSessionHistory() {
	if a.session == nil {
		return
	}

	historyMgr, err := a.GetHistoryManager()
	if err != nil {
		logging.Error("failed to create history manager during shutdown — session not saved",
			"error", err)
		fmt.Fprintf(os.Stderr, "WARNING: failed to save session at shutdown: %v\n", err)
		return
	}

	// SaveFull (not the older Save) so we capture tool calls and
	// responses too — Save() drops everything except role+text, which
	// would leave the resumed session blind to the tool work the user
	// just did. Pre-fix this path used Save() and silently degraded
	// any session resumed from this fallback (sessionManager==nil).
	if err := historyMgr.SaveFull(a.session); err != nil {
		logging.Error("failed to save session history during shutdown — recent conversation may be lost",
			"error", err)
		fmt.Fprintf(os.Stderr, "WARNING: failed to save session at shutdown: %v\n", err)
	}
}
