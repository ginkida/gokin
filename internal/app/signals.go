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
	wg     sync.WaitGroup
	mu     sync.Mutex
	closed bool
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
	t.wg.Add(1)
	return true
}

// Done marks a goroutine as completed.
func (t *GoroutineTracker) Done() {
	t.wg.Done()
}

// Wait waits for all tracked goroutines to complete.
func (t *GoroutineTracker) Wait() {
	t.wg.Wait()
}

// WaitWithTimeout waits for all goroutines with a timeout.
// Returns true if all goroutines completed, false if timed out.
func (t *GoroutineTracker) WaitWithTimeout(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		t.wg.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return true
	case <-timer.C:
		return false
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

				// First Ctrl+C: cancel current processing if active
				a.processingMu.Lock()
				cancelFn := a.processingCancel
				a.processingMu.Unlock()

				if cancelFn != nil {
					logging.Debug("cancelling current operation (first Ctrl+C)")
					a.safeSendToProgram(ui.StatusUpdateMsg{
						Type:    ui.StatusCancelled,
						Message: "Canceling... (Ctrl+C again to exit)",
					})
					cancelFn()
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
	if a.cancel != nil {
		a.cancel()
	}

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
	if a.agentRunner != nil {
		for _, agentID := range a.agentRunner.ListRunning() {
			logging.Debug("cancelling background agent", "agent_id", agentID)
			_ = a.agentRunner.Cancel(agentID)
		}
	}

	// 5. Shutdown MCP servers
	if a.mcpManager != nil {
		logging.Debug("shutting down MCP servers")
		if err := a.mcpManager.Shutdown(ctx); err != nil {
			logging.Debug("error shutting down MCP", "error", err)
		}
	}

	// 6. Stop file watcher
	if a.fileWatcher != nil {
		logging.Debug("stopping file watcher")
		if err := a.fileWatcher.Stop(); err != nil {
			logging.Debug("error stopping file watcher", "error", err)
		}
	}

	// 6b. Stop search cache cleanup goroutine
	if a.searchCache != nil {
		a.searchCache.StopCleanup()
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

	// 13. Flush audit logger to ensure all entries are persisted
	if a.auditLogger != nil {
		logging.Debug("flushing audit logger")
		a.auditLogger.Flush()
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

	// 13. Close client
	if a.client != nil {
		_ = a.client.Close()
	}

	// 14. Close logging last
	logging.Debug("shutdown complete")
	logging.Close()
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

	if err := historyMgr.Save(a.session); err != nil {
		logging.Error("failed to save session history during shutdown — recent conversation may be lost",
			"error", err)
		fmt.Fprintf(os.Stderr, "WARNING: failed to save session at shutdown: %v\n", err)
	}
}
