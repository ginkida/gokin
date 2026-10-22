package hooks

import (
	"context"
	"os"
	"strconv"
	"time"
)

const defaultSessionEndTimeout = 1500 * time.Millisecond
const maxSessionEndBudget = 60 * time.Second

type sessionEndTimeoutKey struct{}

func effectiveClaudeTimeout(ctx context.Context, hook *Hook) time.Duration {
	if hook.Type == SessionEnd && !hook.Claude.TimeoutExplicit {
		if configured, ok := ctx.Value(sessionEndTimeoutKey{}).(time.Duration); ok {
			return configured
		}
	}
	return hook.Claude.Timeout
}

func sessionEndEnvironmentTimeout() (time.Duration, bool) {
	ms, err := strconv.ParseInt(os.Getenv("CLAUDE_CODE_SESSIONEND_HOOKS_TIMEOUT_MS"), 10, 64)
	if err != nil || ms <= 0 {
		return defaultSessionEndTimeout, false
	}
	if ms > int64(maxSessionEndBudget/time.Millisecond) {
		return maxSessionEndBudget, true
	}
	return time.Duration(ms) * time.Millisecond, true
}

// SessionEndBudget is shared across handlers. Plugin/skill timeout declarations
// cannot raise it; an explicit environment budget overrides settings maxima.
func (m *Manager) SessionEndBudget(reason string) (time.Duration, time.Duration) {
	perDefault, override := sessionEndEnvironmentTimeout()
	if override {
		return perDefault, perDefault
	}
	budget := perDefault
	for _, hook := range m.GetHooks() {
		if hook.Type == SessionEnd && hook.claudeSettings && hook.claudePlugin == nil && hook.Claude.TimeoutExplicit && hook.Matches(SessionEnd, reason) && m.HookEnabled(hook) && hook.Claude.Timeout > budget {
			budget = hook.Claude.Timeout
		}
	}
	if budget > maxSessionEndBudget {
		budget = maxSessionEndBudget
	}
	return budget, perDefault
}

// RunSessionEnd ignores decision output and joins process cleanup before the
// owning session releases its lease or tears down MCP. Caller cancellation is
// detached deliberately: cleanup still runs after Esc, timeout, or shutdown.
func (m *Manager) RunSessionEnd(ctx context.Context, workDir, reason string) []Result {
	budget, perDefault := m.SessionEndBudget(reason)
	ctx = context.WithValue(context.WithoutCancel(ctx), sessionEndTimeoutKey{}, perDefault)
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	info := RuntimeInfoFromContext(ctx)
	info.ToolNameOverride, info.ToolUseID, info.AgentID, info.AgentType = "", "", "", ""
	ctx = ContextWithRuntimeInfo(ctx, info)
	owner := NewAsyncScope(ctx, nil)
	defer owner.Close()
	ctx = ContextWithAsyncScope(ctx, owner)
	hctx := NewContext(reason, nil, workDir)
	hctx.Reason = reason
	results := m.RunScoped(ctx, SessionEnd, hctx)
	for i := range results {
		results[i].Output = ""
		results[i].BlockReason, results[i].StopReason, results[i].AdditionalContext, results[i].PermissionDecision = "", "", "", ""
		results[i].UpdatedInput = nil
		results[i].SystemMessage, results[i].SessionTitle = "", ""
	}
	return results
}

// CaptureEvent freezes executable runtime registrations before a successful
// session replacement clears/restores the live skill scope. It never loads
// executable definitions from persisted session data.
func (s *Scope) CaptureEvent(kind Type) *Scope {
	captured := &Scope{groups: make(map[string][]*scopedHook)}
	if s == nil {
		return captured
	}
	for _, original := range s.snapshotEntries() {
		original.owner.mu.Lock()
		if original.hook.Type != kind || original.claimed || original.consumed {
			original.owner.mu.Unlock()
			continue
		}
		hook := *original.hook
		definition := CloneClaudeCommands([]ClaudeCommand{*hook.Claude})[0]
		hook.Claude = &definition
		entry := &scopedHook{hook: &hook, owner: captured, index: original.index}
		entry.onceObserver = func(index int) {
			original.owner.mu.Lock()
			original.consumed = true
			observer := original.onceObserver
			original.owner.mu.Unlock()
			if observer != nil {
				observer(index)
			}
		}
		original.owner.mu.Unlock()
		if _, exists := captured.groups[hook.Source]; !exists {
			captured.order = append(captured.order, hook.Source)
		}
		captured.groups[hook.Source] = append(captured.groups[hook.Source], entry)
		captured.count++
	}
	return captured
}

func (s *Scope) ConsumedEntries() map[string][]int {
	result := make(map[string][]int)
	if s == nil {
		return result
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for source, entries := range s.groups {
		for _, entry := range entries {
			if entry.consumed {
				result[source] = append(result[source], entry.index)
			}
		}
	}
	return result
}
