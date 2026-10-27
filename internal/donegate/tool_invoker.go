package donegate

import (
	"context"
	"slices"

	"gokin/internal/tools"
)

// WithToolInvoker keeps check discovery registry-based while execution uses
// the owning dispatcher, including its permissions, hooks and tool ceilings.
func WithToolInvoker(ctx context.Context, reg ToolGetter, invoke func(context.Context, string, map[string]any) (tools.ToolResult, error), preflight func(context.Context, string, map[string]any) bool) ToolGetter {
	ceiling, restricted := tools.ToolCapabilityCeilingFromContext(ctx)
	return invokingRegistry{reg: reg, invoke: invoke, preflight: preflight, ceiling: ceiling, restricted: restricted}
}

type invokingRegistry struct {
	reg        ToolGetter
	invoke     func(context.Context, string, map[string]any) (tools.ToolResult, error)
	preflight  func(context.Context, string, map[string]any) bool
	ceiling    []string
	restricted bool
}

func (r invokingRegistry) Get(name string) (tools.Tool, bool) {
	if r.restricted && !slices.Contains(r.ceiling, name) {
		return nil, false
	}
	tool, ok := r.reg.Get(name)
	if !ok {
		return nil, false
	}
	return invokingTool{Tool: tool, invoke: r.invoke, preflight: r.preflight}, true
}

type invokingTool struct {
	tools.Tool
	invoke    func(context.Context, string, map[string]any) (tools.ToolResult, error)
	preflight func(context.Context, string, map[string]any) bool
}

func (t invokingTool) Execute(ctx context.Context, args map[string]any) (tools.ToolResult, error) {
	if t.preflight != nil && !t.preflight(ctx, t.Name(), args) {
		return tools.NewPolicyBlockedResult(tools.PolicyBlockPermission, "Verification denied by permission policy; check was not attempted"), nil
	}
	return t.invoke(ctx, t.Name(), args)
}
