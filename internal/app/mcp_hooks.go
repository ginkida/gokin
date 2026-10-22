package app

import (
	"context"
	"fmt"

	"gokin/internal/hooks"
)

func (a *App) invokeMCPHook(ctx context.Context, server, tool string, input map[string]any) (hooks.MCPHookOutput, error) {
	manager := a.GetMCPManager()
	if manager == nil {
		return hooks.MCPHookOutput{}, fmt.Errorf("MCP is unavailable for this hook")
	}
	return manager.CallHookTool(ctx, server, tool, input)
}
