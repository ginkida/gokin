package permission

import "context"

type toolIdentityKey struct{}
type toolIdentity struct{ runtime, canonical string }

// ContextWithToolIdentity binds source identity to this particular registered
// tool invocation. It does not depend on a process-global alias table, so a
// sibling session's disconnect cannot erase another session's deny matching.
func ContextWithToolIdentity(ctx context.Context, runtime, canonical string) context.Context {
	return context.WithValue(ctx, toolIdentityKey{}, toolIdentity{runtime, canonical})
}

func canonicalInvocationTool(ctx context.Context, runtime string) string {
	identity, _ := ctx.Value(toolIdentityKey{}).(toolIdentity)
	if identity.runtime == runtime && identity.canonical != "" {
		return identity.canonical
	}
	return runtime
}

func invocationCacheTool(ctx context.Context, runtime string) string {
	canonical := canonicalInvocationTool(ctx, runtime)
	if canonical != runtime {
		return runtime + "\x00" + canonical
	}
	return runtime
}

func invocationToolPolicy(ctx context.Context, rules *Rules, runtime string) Level {
	canonical := canonicalInvocationTool(ctx, runtime)
	original, hasOriginal := rules.ToolPolicies[runtime]
	source, hasSource := rules.ToolPolicies[canonical]
	if hasOriginal && original == LevelDeny || hasSource && source == LevelDeny {
		return LevelDeny
	}
	if hasOriginal {
		return original
	}
	if hasSource {
		return source
	}
	return rules.DefaultPolicy
}

func TemporaryToolRulesDenyWithIdentity(ctx context.Context, denies []string, runtime string, args map[string]any, workDir string) bool {
	return TemporaryToolRulesDeny(denies, runtime, args, workDir) || TemporaryToolRulesDeny(denies, canonicalInvocationTool(ctx, runtime), args, workDir)
}

func invocationToolAllowed(ctx context.Context, allows []string, runtime string, args map[string]any, workDir string) bool {
	return temporaryToolGrantMatchesAny(allows, runtime, args, workDir) || temporaryToolGrantMatchesAny(allows, canonicalInvocationTool(ctx, runtime), args, workDir)
}
