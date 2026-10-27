package permission

import (
	"context"
	"strings"
)

type permissionPreflightKey struct{}

// Preflight uses the dispatcher's policy evaluator without prompting or
// recording a user decision. An interactive ask remains eligible to execute;
// without an approval channel it is denied, just as at dispatch time.
func (m *Manager) Preflight(ctx context.Context, toolName string, args map[string]any, grants, denies []string, canPrompt bool) (*Response, error) {
	ctx = context.WithValue(ctx, permissionPreflightKey{}, canPrompt)
	return m.CheckWithTemporaryToolRules(ctx, toolName, args, grants, denies)
}

// UnconditionallyDenied classifies all inputs, never a sample argument map.
// Scoped allows keep an ask tool available even when other inputs need approval.
// The caller must also account for invocation-local grants and pre-tool hooks.
func (m *Manager) UnconditionallyDenied(ctx context.Context, toolName string, headless bool) bool {
	if m == nil {
		return false
	}
	// Scoped managers follow parent revocations through the same refresh path
	// as Check. A session decision for one input cannot classify all inputs.
	_ = m.refreshFromParent(ctx, toolName, nil, WorkDirFromContext(ctx))
	m.mu.RLock()
	defer m.mu.RUnlock()
	canonical := canonicalInvocationTool(ctx, toolName)
	for _, rule := range m.runDenies {
		if ToolDenyRuleMatchesName(rule, toolName) || ToolDenyRuleMatchesName(rule, canonical) {
			return true
		}
	}
	if !m.enabled {
		return false
	}
	policy := invocationToolPolicy(ctx, m.rules, toolName)
	if policy == LevelDeny {
		return true
	}
	if policy == LevelAllow || !headless {
		return false
	}
	// dontAsk still permits conservative read-only Bash inputs. Session
	// approvals are input-scoped; retain the tool if any might apply.
	if m.dontAsk && toolName == "bash" || m.sessionCache.Len() > 0 {
		return false
	}
	for _, rule := range m.runAllows {
		name, _, _ := strings.Cut(rule, "(")
		if toolRuleNameMatches(name, toolName) || toolRuleNameMatches(name, canonical) {
			return false
		}
	}
	return true
}
