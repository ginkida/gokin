package app

import (
	"context"
	"fmt"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/logging"
	"gokin/internal/plugins"
)

func (b *Builder) syncStartupPlugins(userDir string, trusted bool) {
	if !b.options.SyncDeclaredPlugins || b.options.Bare || b.cfg != nil && b.cfg.Bare || userDir == "" {
		return
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	managed := hooks.ClaudeManagedSettingsDir()
	if b.hooksManager != nil {
		_, managed = b.hooksManager.ClaudeSettingsDirectories()
	}
	manager, err := plugins.NewManager(plugins.Options{UserDir: userDir, ManagedDir: managed, WorkDir: b.workDir, Trusted: trusted})
	if err != nil {
		b.pluginStartupSummary = "Plugin startup: " + err.Error()
		logging.Warn("plugin startup", "error", err)
		return
	}
	var report plugins.StartupReport
	if b.options.NonInteractive {
		report, err = manager.SyncStartup(ctx)
		commands, commandErr := manager.RefreshCommandSources(ctx)
		report.Installed = append(report.Installed, commands.Installed...)
		report.Updated = append(report.Updated, commands.Updated...)
		report.Errors = append(report.Errors, commands.Errors...)
		report.Skipped = append(report.Skipped, commands.Skipped...)
		if commandErr != nil {
			report.Errors = append(report.Errors, commandErr.Error())
		}
	} else {
		report, err = manager.SyncDeclared(ctx)
		if pinErr := b.hooksManager.PinClaudePluginInstalls(); pinErr != nil {
			report.Errors = append(report.Errors, "Could not retain session plugin versions: "+pinErr.Error())
		} else {
			b.pluginAutoUpdater = newPluginAutoUpdater(manager)
			b.pluginCommandUpdater = newPluginCommandUpdater(manager)
		}
	}
	issues := append(append(report.Errors, report.Skipped...), report.Renamed...)
	if err != nil {
		issues = append(issues, err.Error())
	}
	if len(report.Registered)+len(report.Installed)+len(report.Updated)+len(report.Removed)+len(issues) == 0 {
		return
	}
	b.pluginStartupSummary = fmt.Sprintf("Plugins: %d marketplaces registered, %d installed, %d updated, %d removed.", len(report.Registered), len(report.Installed), len(report.Updated), len(report.Removed))
	for i, issue := range issues {
		logging.Warn("plugin startup", "issue", issue)
		if i < 8 {
			b.pluginStartupSummary += "\n" + issue
		}
	}
	if len(issues) > 8 {
		b.pluginStartupSummary += "\nRun `gokin plugin sync` to inspect all remaining plugin issues."
	}
}
