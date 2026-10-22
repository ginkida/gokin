package app

import (
	"context"
	"fmt"

	"gokin/internal/hooks"
)

type asyncHookWakeKey struct{}

func (a *App) withAsyncHooks(ctx context.Context) context.Context {
	lineage, ok := conversationLineageFromContext(ctx)
	if !ok {
		lineage = a.captureConversationLineage()
	}
	a.asyncHooksMu.Lock()
	if a.asyncHooksScope == nil || a.asyncHooksLineage != lineage {
		a.asyncHooksScope.Cancel()
		parent := a.ctx
		if parent == nil {
			parent = context.Background()
		}
		a.asyncHooksLineage = lineage
		a.asyncHooksScope = hooks.NewAsyncScope(parent, a.tryWakeAsyncHooks, func() error {
			if !a.conversationLineageMatches(lineage) {
				return fmt.Errorf("async hook belongs to an old conversation")
			}
			return nil
		})
	}
	scope := a.asyncHooksScope
	a.asyncHooksMu.Unlock()
	return a.withSessionStartHooks(hooks.ContextWithAsyncScope(ctx, scope))
}

func (a *App) cancelAsyncHooks() {
	a.asyncHooksMu.Lock()
	scope := a.asyncHooksScope
	a.asyncHooksScope = nil
	a.asyncHooksMu.Unlock()
	scope.Cancel()
	a.cancelFileAsyncHooks()
}

func (a *App) hasPendingAsyncHooks() bool {
	a.asyncHooksMu.Lock()
	scope := a.asyncHooksScope
	a.asyncHooksMu.Unlock()
	return scope.HasPendingWork() || a.pendingFileAsyncHooks()
}

func (a *App) cancelIdleAsyncHooks() bool {
	if !a.hasPendingAsyncHooks() {
		return false
	}
	a.cancelProcessing()
	return true
}

func (a *App) closeAsyncHooks() {
	a.asyncHooksMu.Lock()
	scope := a.asyncHooksScope
	a.asyncHooksScope = nil
	a.asyncHooksMu.Unlock()
	scope.Close()
}

// Conversation replacement cannot let an old process publish into the new root.
// Cancel is nonblocking so callers may hold the session persistence lease.
func (a *App) advanceConversationEpoch() uint64 {
	epoch := a.recoveryEpoch.Add(1)
	a.cancelAsyncHooks()
	return epoch
}

func (a *App) tryWakeAsyncHooks() {
	a.asyncHooksMu.Lock()
	scope, lineage := a.asyncHooksScope, a.asyncHooksLineage
	a.asyncHooksMu.Unlock()
	guard := scope.RewakeGuard()
	if guard == nil {
		a.sessionStartMu.Lock()
		owner := a.sessionStart
		a.sessionStartMu.Unlock()
		if owner == nil {
			return
		}
		scope, lineage = owner.async, conversationLineage{sessionID: owner.id, epoch: a.recoveryEpoch.Load()}
		guard = scope.RewakeGuard()
		if guard == nil {
			if fileScope := a.currentFileAsyncHooks(owner); fileScope != nil {
				scope, guard = fileScope, fileScope.RewakeGuard()
			}
			if guard == nil {
				return
			}
		}
	}
	a.sessionLeaseMu.Lock()
	if a.session == nil || a.session.GetID() != lineage.sessionID || a.recoveryEpoch.Load() != lineage.epoch {
		a.sessionLeaseMu.Unlock()
		return
	}
	a.mu.Lock()
	if !a.running || a.shuttingDown || a.dropSteerLeftovers || a.processing || a.headlessRunActive || a.headlessDirect || a.pendingCount() > 0 {
		a.mu.Unlock()
		a.sessionLeaseMu.Unlock()
		return
	}
	a.processing = true
	ctx := a.claimForegroundContextLocked()
	ctx = withConversationLineage(ctx, lineage.sessionID, lineage.epoch)
	ctx = hooks.ContextWithAsyncScope(ctx, scope)
	ctx = hooks.ContextWithAsyncRewakeGuard(ctx, guard)
	ctx = context.WithValue(ctx, asyncHookWakeKey{}, scope)
	a.mu.Unlock()
	a.sessionLeaseMu.Unlock()
	a.startAcceptedSubmit(ctx, "Respond to the completed background hook result.", "", nil, false)
}
