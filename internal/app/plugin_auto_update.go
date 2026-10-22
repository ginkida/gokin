package app

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"gokin/internal/logging"
	"gokin/internal/plugins"
	"gokin/internal/ui"
)

const pluginAutoUpdateMaxDelay = 10 * time.Minute

type pluginAutoUpdater struct {
	once    sync.Once
	started atomic.Bool
	done    chan struct{}
	wait    func(context.Context) bool
	run     func(context.Context) (plugins.StartupReport, error)
}

func newPluginAutoUpdater(manager *plugins.Manager) *pluginAutoUpdater {
	return &pluginAutoUpdater{done: make(chan struct{}), run: manager.AutoUpdate, wait: func(ctx context.Context) bool {
		delay := time.Duration(rand.Int64N(int64(pluginAutoUpdateMaxDelay)))
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return ctx.Err() == nil
		}
	}}
}

// Called only for explicit user input. Turn cancellation doesn't cancel this
// session task, and retries/loops/recovery continuations don't schedule it.
func (a *App) startPluginAutoUpdates() {
	a.mu.Lock()
	updater, ctx := a.pluginAutoUpdater, a.ctx
	allowed := !a.shuttingDown && !a.headlessDirect && !a.headlessRunActive && (a.config == nil || !a.config.Bare)
	a.mu.Unlock()
	if !allowed || updater == nil || ctx == nil || ctx.Err() != nil {
		return
	}
	updater.once.Do(func() {
		updater.started.Store(true)
		a.safeGo("plugin-auto-update", func() {
			defer close(updater.done)
			if !updater.wait(ctx) || ctx.Err() != nil {
				return
			}
			jobCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			report, err := updater.run(jobCtx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			for _, notice := range pluginAutoUpdateNotices(report, err) {
				if notice.Type == ui.StatusWarning {
					logging.Warn("plugin auto-update", "issue", notice.Message)
				}
				a.safeSendToProgramAsync(notice)
			}
		})
	})
}

func pluginAutoUpdateNotices(report plugins.StartupReport, err error) []ui.StatusUpdateMsg {
	var notices []ui.StatusUpdateMsg
	seen := map[string]bool{}
	for _, id := range report.Updated {
		if seen[id] {
			continue
		}
		seen[id] = true
		notices = append(notices, ui.StatusUpdateMsg{Type: ui.StatusInfo, Message: fmt.Sprintf("Plugin updated: %s · Run /reload-plugins to apply.", id)})
	}
	for _, id := range report.Removed {
		notices = append(notices, ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: fmt.Sprintf("Plugin removed from marketplace: %s · Run /reload-plugins to apply.", id)})
	}
	issues := append(append([]string(nil), report.Errors...), report.Skipped...)
	if err != nil {
		issues = append(issues, err.Error())
	}
	for _, issue := range issues {
		if seen[issue] {
			continue
		}
		seen[issue] = true
		notices = append(notices, ui.StatusUpdateMsg{Type: ui.StatusWarning, Message: "Plugin auto-update: " + issue})
	}
	return notices
}

func (u *pluginAutoUpdater) waitForStop(ctx context.Context) {
	if u == nil || !u.started.Load() {
		return
	}
	select {
	case <-u.done:
	case <-ctx.Done():
		logging.Debug("plugin auto-update shutdown deadline reached")
	}
}
