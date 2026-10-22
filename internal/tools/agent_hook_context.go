package tools

import (
	"context"
	"fmt"
	"path/filepath"

	"gokin/internal/hooks"
	"google.golang.org/genai"
)

type agentHookReadTrackerKey struct{}

func readTrackerForContext(ctx context.Context, fallback *FileReadTracker) *FileReadTracker {
	if tracker, ok := ctx.Value(agentHookReadTrackerKey{}).(*FileReadTracker); ok {
		return tracker
	}
	return fallback
}

// A verifier can inspect and test through the caller's tools, but cannot
// recursively delegate or alter the surrounding conversation's control state.
func agentHookToolAllowed(name string) bool {
	switch name {
	case "task", "coordinate", "skill", "ask_user", "ask_agent", "task_stop", "kill_shell", "enter_plan_mode", "exit_plan_mode", "undo_plan", "redo_plan", "update_plan_progress", "todo", "memory", "shared_memory", "update_scratchpad", "memorize", "pin_context", "request_tool", "mcp_admin", "loop_control", "batch", "harness":
		return false
	}
	return true
}

func ContextWithAgentHookDispatcher(ctx context.Context, registry ToolRegistry, invoke func(context.Context, string, map[string]any) (ToolResult, error)) context.Context {
	if registry == nil || invoke == nil {
		return hooks.ContextWithAgentHookTools(ctx, nil, nil)
	}
	declarations := make([]*genai.FunctionDeclaration, 0)
	names := make([]string, 0)
	ceiling, restricted := ToolCapabilityCeilingFromContext(ctx)
	for _, declaration := range registry.Declarations() {
		if declaration == nil || !agentHookToolAllowed(declaration.Name) || restricted && !toolCapabilityAllows(ceiling, declaration.Name) {
			continue
		}
		copy := *declaration
		declarations = append(declarations, &copy)
		names = append(names, declaration.Name)
	}
	var envelopes []*genai.Tool
	if len(declarations) > 0 {
		envelopes = []*genai.Tool{{FunctionDeclarations: declarations}}
	}
	return hooks.ContextWithAgentHookTools(ctx, envelopes, func(ctx context.Context, call *genai.FunctionCall) (map[string]any, error) {
		if !hooks.AgentHookActive(ctx) || call == nil {
			return nil, fmt.Errorf("invalid agent hook invocation")
		}
		if !agentHookToolAllowed(call.Name) || !toolCapabilityAllows(names, call.Name) {
			return NewPolicyBlockedResult(PolicyBlockPermission, "Tool is unavailable to the hook verifier").ToMap(), nil
		}
		if current, restricted := ToolCapabilityCeilingFromContext(ctx); restricted && !toolCapabilityAllows(current, call.Name) {
			return NewPolicyBlockedResult(PolicyBlockPermission, "Tool is outside the invocation capability ceiling").ToMap(), nil
		}
		if _, exists := registry.Get(call.Name); !exists {
			return NewErrorResult("Tool is no longer available to the hook verifier").ToMap(), nil
		}
		// Preserve the verifier's fixed schema ceiling even if later registry
		// refreshes expose more tools to the foreground model.
		ctx = ContextWithToolCapabilityCeiling(ctx, names)
		result, err := invoke(ctx, call.Name, ClonePermissionArgs(call.Args))
		if err != nil {
			return nil, err
		}
		if result.HookStopReason != "" {
			return nil, HookStoppedError(result)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if tracker := readTrackerForContext(ctx, nil); tracker != nil && result.Success {
			if call.Name == "read" {
				path, _ := call.Args["file_path"].(string)
				if data, ok := result.Data.(map[string]any); ok {
					if canonical, ok := data["file_path"].(string); ok {
						path = canonical
					}
				}
				if filepath.IsAbs(path) {
					tracker.CheckAndRecord(path, GetIntDefault(call.Args, "offset", 1), GetIntDefault(call.Args, "limit", 2000), len(result.Content))
				}
			} else if IsWriteTool(call.Name) {
				tracker.Reset()
			}
		}
		return result.ToMap(), nil
	}, func(ctx context.Context) context.Context {
		return context.WithValue(ctx, agentHookReadTrackerKey{}, NewFileReadTracker())
	})
}
