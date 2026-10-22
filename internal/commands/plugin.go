package commands

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"gokin/internal/hooks"
	"gokin/internal/plugins"
)

type PluginCommand struct{}

func (*PluginCommand) Name() string        { return "plugin" }
func (*PluginCommand) Description() string { return "Manage plugins and marketplaces" }
func (*PluginCommand) Usage() string {
	return "/plugin [list|install|update|enable|disable|uninstall|configure|validate|tag|prune|sync|marketplace]"
}
func (*PluginCommand) GetMetadata() CommandMetadata {
	return CommandMetadata{Category: CategoryTools, Icon: "plug", Priority: 42}
}
func (*PluginCommand) Execute(ctx context.Context, args []string, app AppInterface) (string, error) {
	if cfg := app.GetConfig(); cfg != nil && cfg.Bare {
		return "", fmt.Errorf("plugin management is unavailable in bare mode")
	}
	if len(args) == 0 {
		args = []string{"list", "--available"}
	}
	var output bytes.Buffer
	command := plugins.NewCommand(func() (plugins.Options, error) {
		cfg := app.GetConfig()
		trusted := false
		if cfg != nil {
			trusted = hooks.IsWorkspaceTrusted(app.GetWorkDir(), cfg.Hooks.TrustedWorkspaces)
		}
		opts := plugins.Options{WorkDir: app.GetWorkDir(), Trusted: trusted}
		if manager := app.GetHooksManager(); manager != nil {
			opts.UserDir, opts.ManagedDir = manager.ClaudeSettingsDirectories()
		}
		return opts, nil
	})
	command.SetContext(ctx)
	command.SetIn(strings.NewReader(""))
	command.SetArgs(args)
	command.SetOut(&output)
	command.SetErr(&output)
	if err := command.Execute(); err != nil {
		return output.String(), err
	}
	return output.String(), nil
}
