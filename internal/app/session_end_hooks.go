package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"gokin/internal/chat"
	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/tools"
)

type sessionEndInvocation struct {
	lineage conversationLineage
	state   *chat.SessionState
	scope   *hooks.Scope
	reason  string
}

// Callers hold the session lease and capture before mutating the live skill
// ledger. Execution is deferred until the replacement transaction commits.
func (a *App) captureSessionEndLocked(state *chat.SessionState, reason string) *sessionEndInvocation {
	if a.hooksManager == nil || state == nil {
		return nil
	}
	var scope *hooks.Scope
	if a.registry != nil {
		if registered, ok := a.registry.Get("skill"); ok {
			if skill, ok := registered.(*tools.SkillTool); ok {
				scope = skill.HookScope()
			}
		}
	}
	return &sessionEndInvocation{lineage: conversationLineage{sessionID: state.ID, epoch: a.recoveryEpoch.Load()}, state: state, scope: scope.CaptureEvent(hooks.SessionEnd), reason: reason}
}

func (a *App) runSessionEndLocked(invocation *sessionEndInvocation) {
	if invocation == nil || a.hasSessionEnd && a.lastSessionEnd == invocation.lineage {
		return
	}
	a.hasSessionEnd, a.lastSessionEnd = true, invocation.lineage
	ctx := hooks.ContextWithRuntimeInfo(context.Background(), hooks.RuntimeInfo{SessionID: invocation.state.ID, ProjectDir: a.workDir, SessionTitle: invocation.state.Title})
	ctx = hooks.ContextWithScope(ctx, invocation.scope)
	for _, result := range a.hooksManager.RunSessionEnd(ctx, a.workDir, invocation.reason) {
		if result.Error != nil {
			logging.Warn("SessionEnd hook failed", "hook", result.Hook.DisplayName(), "reason", invocation.reason, "error", result.Error)
			fmt.Fprintf(os.Stderr, "Warning: SessionEnd hook %q failed: %v\n", result.Hook.DisplayName(), result.Error)
		}
	}
	changed := false
	consumedEntries := invocation.scope.ConsumedEntries()
	for source, indices := range consumedEntries {
		for i := range invocation.state.SkillHooks {
			activation := &invocation.state.SkillHooks[i]
			if source != "skill:"+activation.Path {
				continue
			}
			for _, index := range indices {
				if index < len(activation.Consumed) && !activation.Consumed[index] {
					activation.Consumed[index] = true
					changed = true
				}
			}
		}
	}
	if !changed || a.sessionManager == nil || !a.sessionManager.PersistenceEnabled() {
		return
	}
	// Both /clear and /resume retain the source writer lease until this point.
	// Persist its once outcomes without applying its history to the new owner.
	if a.session.GetID() == invocation.lineage.sessionID {
		if a.recoveryEpoch.Load() == invocation.lineage.epoch {
			if err := a.sessionManager.Save(); err != nil {
				logging.Warn("save SessionEnd once state", "error", err)
			}
		}
		return
	}
	history, err := chat.NewHistoryManager()
	if err == nil {
		// Rotation may have saved other once completions after our capture.
		// Merge only this event's consumed bits into that durable source while
		// retaining its activity timestamp, history and other completion state.
		var saved *chat.SessionState
		saved, err = history.LoadFull(invocation.state.ID)
		if err == nil {
			for i := range saved.SkillHooks {
				stored := &saved.SkillHooks[i]
				for _, completed := range invocation.state.SkillHooks {
					if stored.Path != completed.Path || stored.Generation != completed.Generation || stored.DefinitionHash != completed.DefinitionHash {
						continue
					}
					for _, index := range consumedEntries["skill:"+completed.Path] {
						if index < len(stored.Consumed) {
							stored.Consumed[index] = true
						}
					}
				}
			}
			err = history.SaveFullState(saved)
		}
	}
	if err != nil {
		logging.Warn("save ended-session once state", "error", err)
	}
}

// EndSessionHooks is idempotent for the current conversation generation. It is
// called before MCP teardown and before releasing the session writer lease.
// Hook failures never change the owning command's exit status.
func (a *App) EndSessionHooks(ctx context.Context, reason string) {
	if a == nil || a.session == nil || a.hooksManager == nil {
		return
	}
	a.closeSessionStartHooks()
	if !strings.Contains("|clear|resume|logout|prompt_input_exit|other|", "|"+reason+"|") {
		reason = "other"
	}
	// A timed-out foreground writer may still own this barrier. Never turn a
	// bounded shutdown into an unbounded wait, or inspect a half-published owner.
	if ctx == nil {
		ctx = context.Background()
	}
	if !a.sessionLeaseMu.TryLock() {
		if ctx.Err() != nil {
			logging.Warn("SessionEnd skipped: conversation writer is still active after shutdown deadline")
			return
		}
		waitBudget, _ := a.hooksManager.SessionEndBudget(reason)
		waitCtx, cancel := context.WithTimeout(ctx, waitBudget)
		defer cancel()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for !a.sessionLeaseMu.TryLock() {
			select {
			case <-waitCtx.Done():
				logging.Warn("SessionEnd skipped: conversation writer did not finish before shutdown deadline")
				return
			case <-ticker.C:
			}
		}
	}
	defer a.sessionLeaseMu.Unlock()
	a.runSessionEndLocked(a.captureSessionEndLocked(a.session.GetState(), reason))
}

func (a *App) ClearConversationForSessionEnd(ctx context.Context, reason string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return a.clearConversationChecked(func() (int, error) { return a.replaceConversationRootWithReason(nil, false, reason) })
}
