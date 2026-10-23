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
	if !b.options.SyncDeclaredPlugins || b.options.Bare || b.options.NoPlugins || b.cfg != nil && b.cfg.Bare || userDir == "" {
		return
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
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
	report, err := manager.SyncCached(ctx)
	if pinErr := b.hooksManager.PinCachedClaudePluginInstalls(); pinErr != nil {
		report.Errors = append(report.Errors, "Could not retain session plugin versions: "+pinErr.Error())
	} else {
		b.pluginStartupRefresh = func(ctx context.Context) (plugins.StartupReport, error) {
			report, err := manager.RefreshStartup(ctx)
			if b.options.NonInteractive && ctx.Err() == nil {
				commands, commandErr := manager.RefreshCommandSources(ctx)
				report.Installed = append(report.Installed, commands.Installed...)
				report.Updated = append(report.Updated, commands.Updated...)
				report.Errors = append(report.Errors, commands.Errors...)
				if commandErr != nil {
					report.Errors = append(report.Errors, commandErr.Error())
				}
			}
			return report, err
		}
		if !b.options.NonInteractive {
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

// Refresh only after construction has pinned the cached runtime. New installs
// become available on the next launch/reload; this worker never emits stdout.
func (a *App) startPluginStartupRefresh(run func(context.Context) (plugins.StartupReport, error)) {
	if run == nil {
		return
	}
	ctx, cancel := context.WithTimeout(a.ctx, 2*time.Minute)
	done := make(chan struct{})
	a.mu.Lock()
	a.pluginStartupCancel, a.pluginStartupDone = cancel, done
	a.mu.Unlock()
	a.safeGo("plugin-startup-refresh", func() {
		defer close(done)
		defer cancel()
		report, err := run(ctx)
		if err != nil {
			logging.Debug("background plugin refresh", "error", err)
		}
		for _, issue := range report.Errors {
			logging.Debug("background plugin refresh", "issue", issue)
		}
	})
}

// ClosePluginStartupRefresh lets one-shot CLI callers cancel and join the
// background refresh before process exit. Interactive shutdown cancels App.ctx.
func (a *App) ClosePluginStartupRefresh() {
	a.mu.Lock()
	cancel, done := a.pluginStartupCancel, a.pluginStartupDone
	a.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		logging.Debug("plugin startup refresh shutdown deadline reached")
	}
}
