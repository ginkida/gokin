package app

import (
	"context"
	"errors"
	"fmt"
	"gokin/internal/lsp"
	"strings"
	"time"

	"gokin/internal/mcp"
	"gokin/internal/plugins"
	"gokin/internal/tools"
)

// ReloadClaudePlugins is an explicit user lifecycle operation. Passive
// discovery never starts/replaces a server from a model request.
func (a *App) ReloadClaudePlugins(ctx context.Context) (string, error) {
	return a.ReloadClaudePluginsWithForce(ctx, false)
}

func (a *App) ReloadClaudePluginsWithForce(ctx context.Context, force bool) (string, error) {
	message, err := a.reloadClaudePlugins(ctx, force, false)
	if errors.Is(err, errPluginReloadCache) {
		return message, nil
	}
	return message, err
}

func (a *App) reloadClaudePlugins(ctx context.Context, force, automatic bool) (string, error) {
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	return a.reloadClaudePluginsLocked(ctx, force, automatic)
}

func (a *App) reloadClaudePluginsLocked(ctx context.Context, force, automatic bool) (string, error) {
	a.mu.Lock()
	busy, stopped := a.processing, a.shuttingDown
	a.mu.Unlock()
	if automatic && busy {
		return "", errPluginReloadBusy
	}
	if stopped {
		return "", context.Canceled
	}
	a.mcpMutationMu.Lock()
	defer a.mcpMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	a.mu.Lock()
	bare := a.config != nil && a.config.Bare
	allowDefault := a.config != nil && a.config.MCP.Enabled
	manager, authority, workDir, lifetimeCtx := a.mcpManager, a.hooksManager, a.workDir, a.ctx
	a.mu.Unlock()
	if bare {
		return "", fmt.Errorf("plugin loading is unavailable in bare mode")
	}
	if lifetimeCtx == nil {
		lifetimeCtx = context.WithoutCancel(ctx)
	}
	if authority == nil {
		return "", fmt.Errorf("plugin authority is unavailable")
	}
	var issues []string
	userDir, managedDir := authority.ClaudeSettingsDirectories()
	if userDir != "" {
		installer, err := plugins.NewManager(plugins.Options{UserDir: userDir, ManagedDir: managedDir, WorkDir: workDir, Trusted: authority.ClaudeWorkspaceTrusted()})
		if err != nil {
			return "", err
		}
		repairCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		withdrawals, withdrawalErr := installer.ReconcileCatalogRemovals(repairCtx)
		issues = append(issues, withdrawals.Errors...)
		issues = append(issues, withdrawals.Renamed...)
		issues = append(issues, withdrawals.Skipped...)
		for _, id := range withdrawals.Removed {
			issues = append(issues, "Removed from marketplace: "+id)
		}
		if withdrawalErr != nil {
			issues = append(issues, withdrawalErr.Error())
		}
		renamed, renameErr := installer.RestoreRenamedLocalPlugins(repairCtx)
		issues = append(issues, renamed.Errors...)
		issues = append(issues, renamed.Skipped...)
		if renameErr != nil {
			issues = append(issues, renameErr.Error())
		}
		restored, restoreErr := installer.RestoreCommandSources(repairCtx)
		issues = append(issues, restored.Errors...)
		issues = append(issues, restored.Skipped...)
		if restoreErr != nil {
			issues = append(issues, restoreErr.Error())
		}
		report, repairErr := installer.RepairDependencies(repairCtx)
		cancel()
		issues = append(issues, report.Errors...)
		issues = append(issues, report.Skipped...)
		if repairErr != nil {
			issues = append(issues, repairErr.Error())
		}
	}
	if !force {
		changes, err := a.pluginReloadCacheChanges(authority, manager, workDir, allowDefault)
		if err != nil {
			return "", err
		}
		if len(changes) > 0 {
			return pluginReloadCacheMessage(changes), errPluginReloadCache
		}
	}
	if err := authority.PinClaudePluginInstalls(); err != nil {
		return "", fmt.Errorf("reload plugin install versions: %w", err)
	}
	lspChanged, err := a.reloadLSPPlugins(ctx, lifetimeCtx)
	if err != nil {
		return "", err
	}
	selected, err := authority.ClaudePlugins()
	if err != nil {
		return "", err
	}
	issues = append(issues, authority.ClaudeDependencyWarnings()...)
	formatResult := func(mcpChanged int) string {
		message := fmt.Sprintf("Reloaded %d plugins; %d MCP servers updated; %d LSP servers updated.", len(selected), mcpChanged, lspChanged)
		seen := map[string]bool{}
		for _, issue := range issues {
			if !seen[issue] {
				message += "\n" + issue
				seen[issue] = true
			}
		}
		return message
	}
	if a.agentTypeRegistry != nil {
		_ = a.agentTypeRegistry.SnapshotAgentTypes()
	}
	if a.registry != nil {
		if tool, ok := a.registry.Get("skill"); ok {
			if skill, ok := tool.(*tools.SkillTool); ok {
				skill.ConfigureClaudePlugins(authority)
			}
		}
	}
	if manager == nil {
		configs, err := mcp.LoadClaudePluginServers(authority, workDir, allowDefault)
		if err != nil {
			return "", err
		}
		if len(configs) == 0 {
			if client := a.GetMainClient(); client != nil && a.registry != nil {
				client.SetTools(a.toolsForCurrentMode())
			}
			return formatResult(0), nil
		}
		manager = mcp.NewManager(nil)
		manager.SetToolsChangedCallback(a.SyncMCPToolsForServer)
		a.mu.Lock()
		a.mcpManager = manager
		a.mu.Unlock()
	}
	changed, reloadErr := manager.ReconcileClaudePlugins(ctx, authority, workDir, allowDefault)
	if lifetimeCtx == nil {
		lifetimeCtx = context.WithoutCancel(ctx)
	}
	manager.StartPluginPolicyWatch(lifetimeCtx, authority, workDir, allowDefault)
	for _, name := range changed {
		a.SyncMCPToolsForServer(name)
	}
	if reloadErr != nil {
		return "", reloadErr
	}
	return formatResult(len(changed)), nil
}

func (b *Builder) initLSP() error {
	if b.options.Bare || b.cfg != nil && b.cfg.Bare || b.registry == nil {
		return nil
	}
	configs, err := lsp.LoadPluginConfigs(b.hooksManager, b.workDir)
	if err != nil {
		return err
	}
	if len(configs) == 0 {
		return nil
	}
	manager, err := lsp.NewManager(b.ctx, b.hooksManager, b.workDir)
	if err != nil {
		return err
	}
	tool := tools.NewLSPTool(b.workDir)
	tool.SetManager(manager)
	if err := b.registry.Register(tool); err != nil {
		_ = manager.Close()
		return err
	}
	b.lspManager = manager
	if b.mainClient != nil {
		b.mainClient.SetTools(b.selectToolSets())
	}
	return nil
}

func (a *App) reloadLSPPlugins(ctx, lifetime context.Context) (int, error) {
	a.mu.Lock()
	manager := a.lspManager
	a.mu.Unlock()
	if manager == nil {
		configs, err := lsp.LoadPluginConfigs(a.hooksManager, a.workDir)
		if err != nil {
			return 0, err
		}
		if len(configs) == 0 {
			return 0, nil
		}
		manager, err = lsp.NewManager(lifetime, a.hooksManager, a.workDir)
		if err != nil {
			return 0, err
		}
		a.mu.Lock()
		a.lspManager = manager
		a.mu.Unlock()
		if err := a.syncLSPTool(manager); err != nil {
			return 0, err
		}
		return len(configs), nil
	}
	changed, err := manager.Reload(ctx)
	if syncErr := a.syncLSPTool(manager); err == nil {
		err = syncErr
	}
	return changed, err
}

func (a *App) syncLSPTool(manager *lsp.Manager) error {
	if a.registry == nil {
		return nil
	}
	if len(manager.Status()) == 0 {
		if existing, ok := a.registry.Get("lsp"); ok {
			if _, owned := existing.(*tools.LSPTool); owned {
				a.registry.Unregister("lsp")
			}
		}
	} else if existing, ok := a.registry.Get("lsp"); ok {
		if tool, owned := existing.(*tools.LSPTool); owned {
			tool.SetManager(manager)
		} else {
			return fmt.Errorf("LSP tool name is occupied")
		}
	} else {
		tool := tools.NewLSPTool(a.workDir)
		tool.SetManager(manager)
		if err := a.registry.Register(tool); err != nil {
			return err
		}
	}
	a.refreshToolCapabilityCeiling()
	if client := a.GetMainClient(); client != nil {
		client.SetTools(a.toolsForCurrentMode())
	}
	return nil
}

func (a *App) GetLSPStatus() string {
	a.mu.Lock()
	manager := a.lspManager
	a.mu.Unlock()
	if manager == nil || len(manager.Status()) == 0 {
		return "No LSP plugins loaded. Select a language-server plugin with --plugin-dir, then use /reload-plugins after configuration changes."
	}
	var out strings.Builder
	out.WriteString("Plugin language servers:\n")
	for _, row := range manager.Status() {
		state := "idle (starts on first matching operation)"
		if row.Running {
			state = "running"
		}
		if row.Error != "" {
			state = "error: " + row.Error
		}
		fmt.Fprintf(&out, "- %s [%s]: %s; restarts: %d\n", row.Name, strings.Join(row.Extensions, ", "), state, row.Restarts)
	}
	return out.String()
}
