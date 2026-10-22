package app

import (
	"context"
	"fmt"
	"time"

	"gokin/internal/chat"
	"gokin/internal/goals"
	"gokin/internal/logging"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

// ClearConversationForRewind is the explicit /clear boundary. Provider/login
// resets and plan compaction keep their existing in-place reset semantics.
func (a *App) ClearConversationForRewind(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("clear requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.clearConversationChecked(func() (int, error) { return a.rotateClearedConversation(ctx) })
}

func (a *App) rotateClearedConversation(ctx context.Context) (int, error) {
	if a == nil || a.session == nil {
		return 0, nil
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	previous := a.session.GetState()
	ending := a.captureSessionEndLocked(previous, "clear")
	sourceID := previous.ID
	fresh := chat.NewSession().GetState()
	fresh.WorkDir, fresh.Provider = previous.WorkDir, previous.Provider
	fresh.Version = previous.Version + 1
	persistent := a.sessionManager.PersistenceEnabled()
	var targetLease *chat.SessionWriterLease
	if persistent {
		if a.sessionLease == nil {
			lease, err := chat.AcquireSessionWriterLease(sourceID)
			if err != nil {
				return 0, err
			}
			a.sessionLease = lease
		}
		if !a.sessionLease.IsActive() || a.sessionLease.SessionID() != sourceID {
			return 0, fmt.Errorf("active conversation does not own its writer lease")
		}
		lease, err := chat.AcquireSessionWriterLease(fresh.ID)
		if err != nil {
			return 0, err
		}
		targetLease = lease
	}
	committed := false
	var rotateErr error
	if persistent {
		committed, rotateErr = a.sessionManager.RotateConversation(fresh)
	} else {
		rotateErr = a.session.RestoreFromState(fresh)
		committed = rotateErr == nil
	}
	if !committed {
		if targetLease != nil {
			_ = targetLease.Release()
		}
		return 0, rotateErr
	}
	a.invalidateSessionStartLocked()
	a.advanceConversationEpoch()
	a.stopGoalTimer()
	a.cancelRecoveryTimersForSession(sourceID)
	dropped := a.dropRecoveryPendingForSession(sourceID)
	previous.PendingRecoveries, previous.ToolCheckpoints = nil, nil
	previous.Goal = goals.Clear(previous.Goal, "Cleared with conversation", time.Now())
	keepPrevious := sessionStateHasDurableWork(previous)
	a.runSessionEndLocked(ending)
	if keepPrevious {
		a.previousClearSessionID = sourceID
		if persistent {
			a.previousClearState = nil
		} else {
			a.previousClearState = previous
		}
	}
	if persistent {
		oldLease := a.sessionLease
		a.sessionLease = targetLease
		if keepPrevious {
			if a.previousClearLease != nil && a.previousClearLease != oldLease {
				if err := a.previousClearLease.Release(); err != nil {
					logging.Warn("release replaced previous-session lease", "error", err)
				}
			}
			a.previousClearLease = oldLease
		} else if err := oldLease.Release(); err != nil {
			logging.Warn("release unused cleared session writer lease", "error", err)
		}
	}
	a.resetRewindOwner()
	tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
	if a.executor != nil {
		a.executor.SetSessionID(fresh.ID)
		a.executor.ResetSideEffectLedger()
		a.executor.SetSideEffectDedup(false)
	}
	a.mu.Lock()
	a.scratchpad = ""
	a.mu.Unlock()
	if a.agentRunner != nil {
		a.agentRunner.SetSharedScratchpad("")
	}
	a.startSessionStartLocked("clear", true)
	if rotateErr != nil {
		return dropped, fmt.Errorf("%w: conversation rotation: %v", errRecoveryCommitUncertain, rotateErr)
	}
	return dropped, nil
}

func (a *App) PreviousClearSessionID() string {
	if a == nil {
		return ""
	}
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	return a.previousClearSessionID
}

// ResumePreviousClearSession resolves a process-local menu entry, then uses the
// same validation and lease transfer as /resume. The expected identity protects
// against a stale menu answering a later clear operation.
func (a *App) ResumePreviousClearSession(ctx context.Context, expected string, force bool) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("resume requires a context")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.sessionLeaseMu.Lock()
	if err := ctx.Err(); err != nil {
		a.sessionLeaseMu.Unlock()
		return "", err
	}
	id := a.previousClearSessionID
	if expected != "" && expected != id {
		a.sessionLeaseMu.Unlock()
		return "", fmt.Errorf("previous conversation is no longer available in this process")
	}
	if id == "" {
		a.sessionLeaseMu.Unlock()
		return "", fmt.Errorf("no previous conversation from /clear is available in this process")
	}
	ephemeral := a.previousClearState
	if ephemeral == nil {
		a.sessionLeaseMu.Unlock()
		history, err := chat.NewHistoryManager()
		if err != nil {
			return "", err
		}
		state, err := history.LoadFull(id)
		if err != nil {
			return "", err
		}
		state, err = a.switchSession(ctx, state, force, id)
		if err != nil {
			return "", err
		}
		a.resetGrantedDirs()
		if a.permManager != nil {
			a.permManager.ClearSession()
		}
		return fmt.Sprintf("Previous conversation %s restored (%d messages).", id, len(state.History)), nil
	}
	defer a.sessionLeaseMu.Unlock()
	if !force {
		current := a.session.GetState()
		if current.WorkDir != ephemeral.WorkDir || current.Provider != ephemeral.Provider {
			return "", fmt.Errorf("previous conversation has a different workspace or provider")
		}
	}
	if err := tools.ValidateSkillHookRestore(a.registry, a.hooksManager, ephemeral.SkillHooks); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	current := a.session.GetState()
	if err := a.session.RestoreFromState(ephemeral); err != nil {
		return "", err
	}
	tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
	if err := tools.RestoreSkillHooks(a.registry, a.hooksManager); err != nil {
		_ = a.session.RestoreFromState(current)
		tools.BindSkillInvocationLedger(a.registry, a.session.InvocationLedger())
		_ = tools.RestoreSkillHooks(a.registry, a.hooksManager)
		return "", err
	}
	a.advanceConversationEpoch()
	a.stopGoalTimer()
	a.cancelRecoveryTimersForSession(current.ID)
	a.dropRecoveryPendingForSession(current.ID)
	a.previousClearSessionID = ""
	a.previousClearState = nil
	a.resetRewindOwner()
	tools.ResetSkillPermissionGrants(a.registry)
	a.resetGrantedDirs()
	if a.permManager != nil {
		a.permManager.ClearSession()
	}
	if a.executor != nil {
		a.executor.SetSessionID(id)
		a.executor.ResetSideEffectLedger()
		a.executor.SetSideEffectDedup(false)
	}
	scratchpad := a.session.GetScratchpad()
	a.mu.Lock()
	a.scratchpad = scratchpad
	a.mu.Unlock()
	if a.agentRunner != nil {
		a.agentRunner.SetSharedScratchpad(scratchpad)
	}
	a.safeSendToProgram(ui.ScratchpadMsg(scratchpad))
	return fmt.Sprintf("Previous conversation %s restored (%d messages).", id, len(ephemeral.History)), nil
}

func (a *App) resetRewindOwner() {
	a.rewindMu.Lock()
	defer a.rewindMu.Unlock()
	if a.undoManager != nil {
		a.undoManager.SetObserver(nil)
		a.undoManager.Clear()
	}
	a.rewindStore = nil
	a.rewindSessionID = ""
}
