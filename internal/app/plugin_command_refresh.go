package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gokin/internal/plugins"
	"gokin/internal/ui"
)

func newPluginCommandUpdater(manager *plugins.Manager) *pluginAutoUpdater {
	return &pluginAutoUpdater{done: make(chan struct{}), run: manager.RefreshCommandSources, wait: func(ctx context.Context) bool {
		timer := time.NewTimer(500 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return ctx.Err() == nil
		}
	}}
}

// Unlike ordinary auto-updates this task belongs to session startup, without
// waiting for a first message. Existing exact command approvals are checked by
// the lifecycle manager immediately before execution.
func (a *App) startPluginCommandRefresh() {
	a.mu.Lock()
	updater, ctx := a.pluginCommandUpdater, a.ctx
	allowed := !a.shuttingDown && !a.headlessDirect && !a.headlessRunActive && (a.config == nil || !a.config.Bare)
	a.mu.Unlock()
	if !allowed || updater == nil || ctx == nil || ctx.Err() != nil {
		return
	}
	updater.once.Do(func() {
		updater.started.Store(true)
		a.safeGo("plugin-command-refresh", func() {
			defer close(updater.done)
			if !updater.wait(ctx) || ctx.Err() != nil {
				return
			}
			report, err := updater.run(ctx)
			if ctx.Err() != nil {
				return
			}
			issues := append(append([]string(nil), report.Errors...), report.Skipped...)
			if err != nil {
				issues = append(issues, err.Error())
			}
			for _, issue := range issues {
				a.safeSendToProgramAsync(ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: "Plugin command refresh: " + issue})
			}
			if len(report.Updated)+len(report.Installed) == 0 {
				return
			}
			// Do not replace tools under an executing foreground turn. The next
			// idle boundary adopts the same components as explicit reload.
			for ctx.Err() == nil {
				reloadCtx, reloadCancel := context.WithTimeout(ctx, 2*time.Minute)
				message, reloadErr := a.reloadClaudePlugins(reloadCtx, false, true)
				reloadCancel()
				if errors.Is(reloadErr, errPluginReloadBusy) {
					timer := time.NewTimer(100 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return
					case <-timer.C:
					}
					continue
				}
				if ctx.Err() != nil {
					return
				}
				kind := ui.StatusInfo
				if reloadErr != nil {
					kind = ui.StatusWarning
					if !errors.Is(reloadErr, errPluginReloadCache) {
						message = fmt.Sprintf("Command-source plugins updated; reload failed: %v. Run /reload-plugins to apply.", reloadErr)
					}
				} else {
					message = "Command-source plugins reloaded. " + message
				}
				a.safeSendToProgramAsync(ui.StatusUpdateMsg{Type: kind, Message: message})
				return
			}
		})
	})
}
