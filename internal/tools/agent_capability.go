package tools

import (
	"context"
	"sort"

	"google.golang.org/genai"
)

type agentToolCapabilityCeilingKey struct{}
type toolSchemaCeilingKey struct {
	executor *Executor
}

// ContextWithToolCapabilityCeiling binds any executor invocation to an exact
// set of tool names. A non-nil (including empty) slice means restricted; nil
// means unrestricted. The policy is runtime-only and cannot be widened by the
// model, permission settings, or delegated agents.
func ContextWithToolCapabilityCeiling(ctx context.Context, names []string) context.Context {
	if names == nil {
		return ctx
	}
	return context.WithValue(ctx, agentToolCapabilityCeilingKey{}, normalizeToolNames(names))
}

// ToolCapabilityCeilingFromContext returns a defensive deterministic snapshot
// and whether a capability ceiling was supplied.
func ToolCapabilityCeilingFromContext(ctx context.Context) ([]string, bool) {
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

// ContextWithToolSchemaCeiling binds model-visible declarations to an exact
// request schema without changing executor authority for trusted internal
// callbacks. Router may narrow this set but must never widen it.
func ContextWithToolSchemaCeiling(ctx context.Context, executor *Executor, names []string) context.Context {
	if names == nil {
		return ctx
	}
	return context.WithValue(ctx, toolSchemaCeilingKey{executor: executor}, normalizeToolNames(names))
}

// ToolSchemaCeilingFromContext returns the model-visible upper bound.
func ToolSchemaCeilingFromContext(ctx context.Context, executor *Executor) ([]string, bool) {
	if ctx == nil {
		return nil, false
	}
	names, ok := ctx.Value(toolSchemaCeilingKey{executor: executor}).([]string)
	if !ok {
		return nil, false
	}
	cloned := make([]string, len(names))
	copy(cloned, names)
	return cloned, true
}

// ContextWithAgentToolCapabilityCeiling binds a delegated run to the caller's
// tool authority. The value is hidden runtime policy, not model-controlled
// input. A non-nil (including empty) slice means restricted; nil means that no
// parent ceiling was supplied (the foreground task tool).
func ContextWithAgentToolCapabilityCeiling(ctx context.Context, names []string) context.Context {
	return ContextWithToolCapabilityCeiling(ctx, names)
}

// AgentToolCapabilityCeilingFromContext returns a defensive deterministic
// snapshot and whether a parent capability ceiling was supplied.
func AgentToolCapabilityCeilingFromContext(ctx context.Context) ([]string, bool) {
	return ToolCapabilityCeilingFromContext(ctx)
}

// FilterGeminiToolsByCapability drops every declaration outside the ceiling.
// Any code path that pushes a tool schema onto a client must run its set
// through this, or the model is advertised tools the executor will refuse at
// call time — wasted turns and a schema that contradicts `--tools`.
func FilterGeminiToolsByCapability(base []*genai.Tool, ceiling []string) []*genai.Tool {
	allowed := normalizeToolNames(ceiling)
	filtered := make([]*genai.Tool, 0, len(base))
	for _, envelope := range base {
		if envelope == nil {
			continue
		}
		declarations := make([]*genai.FunctionDeclaration, 0, len(envelope.FunctionDeclarations))
		for _, declaration := range envelope.FunctionDeclarations {
			if declaration == nil {
				continue
			}
			if toolCapabilityAllows(allowed, declaration.Name) {
				declarations = append(declarations, declaration)
			}
		}
		if len(declarations) == 0 {
			continue
		}
		cloned := *envelope
		cloned.FunctionDeclarations = declarations
		filtered = append(filtered, &cloned)
	}
	return filtered
}

// FilterGeminiToolsExcluding returns a defensive schema copy without the named
// declarations. It is used for request-level feature policy after broader
// registry/plan-mode filtering has already selected the base schema.
func FilterGeminiToolsExcluding(base []*genai.Tool, names ...string) []*genai.Tool {
	excluded := make(map[string]struct{}, len(names))
	for _, name := range names {
		excluded[name] = struct{}{}
	}
	filtered := make([]*genai.Tool, 0, len(base))
	for _, envelope := range base {
		if envelope == nil {
			continue
		}
		declarations := make([]*genai.FunctionDeclaration, 0, len(envelope.FunctionDeclarations))
		for _, declaration := range envelope.FunctionDeclarations {
			if declaration == nil {
				continue
			}
			if _, drop := excluded[declaration.Name]; !drop {
				declarations = append(declarations, declaration)
			}
		}
		if len(declarations) == 0 {
			continue
		}
		cloned := *envelope
		cloned.FunctionDeclarations = declarations
		filtered = append(filtered, &cloned)
	}
	return filtered
}

func toolCapabilityAllows(names []string, toolName string) bool {
	index := sort.SearchStrings(names, toolName)
	return index < len(names) && names[index] == toolName
}

func intersectToolCapabilities(left, right []string) []string {
	intersection := make([]string, 0, min(len(left), len(right)))
	for _, name := range left {
		if toolCapabilityAllows(right, name) {
			intersection = append(intersection, name)
		}
	}
	return normalizeToolNames(intersection)
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
