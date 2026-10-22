package commands

import (
	"context"
	"fmt"
)

type ReloadPluginsCommand struct{}

func (*ReloadPluginsCommand) Name() string { return "reload-plugins" }
func (*ReloadPluginsCommand) Description() string {
	return "Reload plugin components and changed MCP/LSP servers"
}
func (*ReloadPluginsCommand) Usage() string { return "/reload-plugins [--force]" }
func (*ReloadPluginsCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{Category: CategoryTools, Icon: "plug", Priority: 40}
}
func (c *ReloadPluginsCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	force := len(args) == 1 && (args[0] == "--force" || args[0] == "force")
	if len(args) != 0 && !force {
		return "", fmt.Errorf("usage: %s", c.Usage())
	}
	if runtime, ok := app.(interface {
		ReloadClaudePluginsWithForce(context.Context, bool) (string, error)
	}); ok {
		return runtime.ReloadClaudePluginsWithForce(ctx, force)
	}
	if force {
		return "", fmt.Errorf("forced plugin reload is unavailable in this runtime")
	}
	if runtime, ok := app.(interface {
		ReloadClaudePlugins(context.Context) (string, error)
	}); ok {
		return runtime.ReloadClaudePlugins(ctx)
	}
	return "", fmt.Errorf("plugin reload is unavailable in this runtime")
}
