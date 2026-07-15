package tools

import (
	"context"
	"sort"
)

type agentToolCapabilityCeilingKey struct{}

// ContextWithAgentToolCapabilityCeiling binds a delegated run to the caller's
// tool authority. The value is hidden runtime policy, not model-controlled
// input. A non-nil (including empty) slice means restricted; nil means that no
// parent ceiling was supplied (the foreground task tool).
func ContextWithAgentToolCapabilityCeiling(ctx context.Context, names []string) context.Context {
	if names == nil {
		return ctx
	}
	return context.WithValue(ctx, agentToolCapabilityCeilingKey{}, normalizeToolNames(names))
}

// AgentToolCapabilityCeilingFromContext returns a defensive deterministic
// snapshot and whether a parent capability ceiling was supplied.
func AgentToolCapabilityCeilingFromContext(ctx context.Context) ([]string, bool) {
	if ctx == nil {
		return nil, false
	}
	names, ok := ctx.Value(agentToolCapabilityCeilingKey{}).([]string)
	if !ok {
		return nil, false
	}
	cloned := make([]string, len(names))
	copy(cloned, names)
	return cloned, true
}

func normalizeToolNames(names []string) []string {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			set[name] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(set))
	for name := range set {
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return normalized
}
