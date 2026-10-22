package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/plugins"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/ui"
)

func TestPluginAutoUpdateFirstUserInputAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := make(chan struct{}, 2)
	gate := make(chan struct{})
	var calls atomic.Int32
	u := &pluginAutoUpdater{done: make(chan struct{}), wait: func(ctx context.Context) bool {
		waiting <- struct{}{}
		select {
		case <-ctx.Done():
			return false
		case <-gate:
			return true
		}
	}, run: func(ctx context.Context) (plugins.StartupReport, error) {
		calls.Add(1)
		<-ctx.Done()
		return plugins.StartupReport{}, ctx.Err()
	}}
	a := &App{ctx: ctx, cancel: cancel, config: &config.Config{}, pluginAutoUpdater: u, processing: true, dropSteerLeftovers: true}
	// Programmatic continuation is rejected at the normal submit boundary and
	// must not count as the first real user message.
	a.handleSubmitWithIntent("continuation", false)
	if u.started.Load() {
		t.Fatal("programmatic input scheduled updates")
	}
	a.handleSubmitWithIntent("first real input", true)
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("first input didn't start the delay")
	}
	a.handleSubmitWithIntent("second real input", true)
	if calls.Load() != 0 || len(waiting) != 0 {
		t.Fatal("update ran before delay or started twice")
	}
	a.beginShutdown()
	u.waitForStop(t.Context())
	if calls.Load() != 0 {
		t.Fatal("shutdown during delay ran an update")
	}
}

func TestPluginAutoUpdateUsesSessionLifetimeAndStopsActiveJob(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	running := make(chan struct{})
	u := &pluginAutoUpdater{done: make(chan struct{}), wait: func(context.Context) bool { return true }, run: func(ctx context.Context) (plugins.StartupReport, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Minute {
			t.Error("unbounded update job")
		}
		close(running)
		<-ctx.Done()
		return plugins.StartupReport{}, ctx.Err()
	}}
	a := &App{ctx: ctx, cancel: cancel, config: &config.Config{}, pluginAutoUpdater: u}
	turnCtx, turnCancel := context.WithCancel(ctx)
	a.processingCancel = turnCancel
	a.processingCancel()
	if turnCtx.Err() == nil {
		t.Fatal("foreground fixture was not cancelled")
	}
	a.startPluginAutoUpdates()
	select {
	case <-running:
	case <-time.After(time.Second):
		t.Fatal("turn cancellation prevented session update")
	}
	a.beginShutdown()
	waitCtx, waitCancel := context.WithTimeout(t.Context(), time.Second)
	defer waitCancel()
	u.waitForStop(waitCtx)
	select {
	case <-u.done:
	default:
		t.Fatal("active updater outlived shutdown")
	}
}

func TestPluginAutoUpdateExcludedModes(t *testing.T) {
	for _, mode := range []string{"bare", "headless", "shutdown", "embedding-off"} {
		t.Run(mode, func(t *testing.T) {
			u := &pluginAutoUpdater{done: make(chan struct{}), wait: func(context.Context) bool { t.Error("excluded mode scheduled updates"); return false }}
			a := &App{ctx: t.Context(), config: &config.Config{}, pluginAutoUpdater: u}
			switch mode {
			case "bare":
				a.config.Bare = true
			case "headless":
				a.headlessDirect = true
			case "shutdown":
				a.shuttingDown = true
			case "embedding-off":
				a.pluginAutoUpdater = nil
			}
			a.startPluginAutoUpdates()
			if u.started.Load() {
				t.Fatal("excluded mode started updater")
			}
		})
	}
}

func TestPluginAutoUpdateNoticeKeepsFailuresAndReloadInstruction(t *testing.T) {
	notices := pluginAutoUpdateNotices(plugins.StartupReport{Updated: []string{"alpha@team [user]", "alpha@team [user]"}, Errors: []string{"broken fetch"}, Skipped: []string{"manual source", "broken fetch"}}, errors.New("deadline"))
	if len(notices) != 4 || notices[0].Type != ui.StatusInfo || !strings.Contains(notices[0].Message, "/reload-plugins") {
		t.Fatalf("notices=%+v", notices)
	}
	for _, notice := range notices[1:] {
		if notice.Type != ui.StatusWarning {
			t.Fatal("lost background failure")
		}
	}
}

func TestPluginAutoUpdateWithdrawalNotice(t *testing.T) {
	notices := pluginAutoUpdateNotices(plugins.StartupReport{Removed: []string{"old@team [user]"}}, nil)
	if len(notices) != 1 || notices[0].Type != ui.StatusWarning || !strings.Contains(notices[0].Message, "old@team") || !strings.Contains(notices[0].Message, "/reload-plugins") {
		t.Fatalf("notices=%+v", notices)
	}
}

func TestBackgroundUpdateKeepsSessionVersionsUntilExplicitReload(t *testing.T) {
	work, user, managed, market := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path string, value any) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "FORCE_AUTOUPDATE_PLUGINS"} {
		t.Setenv(key, "")
	}
	parent := filepath.Join(market, "bundle")
	write(filepath.Join(parent, ".claude-plugin", "plugin.json"), map[string]any{"name": "bundle", "version": "1.0.0"})
	write(filepath.Join(market, "child", ".claude-plugin", "plugin.json"), map[string]any{"name": "child"})
	write(filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "team", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "bundle", "source": "./bundle"}, map[string]any{"name": "child", "source": "./child"}}})
	installer, err := plugins.NewManager(plugins.Options{UserDir: user, ManagedDir: managed, WorkDir: work})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := installer.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.Install(t.Context(), "bundle", plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.SetMarketplaceAutoUpdate(t.Context(), "team", true); err != nil {
		t.Fatal(err)
	}
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(managed)
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins(nil)
	if err := authority.PinClaudePluginInstalls(); err != nil {
		t.Fatal(err)
	}
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("initial=%+v %v", selected, err)
	}
	old := selected[0]
	write(filepath.Join(parent, ".claude-plugin", "plugin.json"), map[string]any{"name": "bundle", "version": "2.0.0", "dependencies": []string{"child"}})
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled = false
	program, capture := newCapturingProgram(t)
	updater := newPluginAutoUpdater(installer)
	updater.wait = func(context.Context) bool { return true }
	a := &App{ctx: t.Context(), config: cfg, workDir: work, hooksManager: authority, registry: tools.NewRegistry(), client: testkit.NewMockClient(), pluginAutoUpdater: updater, program: program, processing: true, dropSteerLeftovers: true}
	a.handleSubmitWithIntent("first user message", true)
	select {
	case <-updater.done:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled background pass didn't finish")
	}
	deadline := time.Now().Add(time.Second)
	notified := false
	for time.Now().Before(deadline) {
		capture.mu.Lock()
		for _, msg := range capture.msgs {
			if notice, ok := msg.(ui.StatusUpdateMsg); ok && strings.Contains(notice.Message, "Plugin updated: bundle@team") && strings.Contains(notice.Message, "/reload-plugins") {
				notified = true
			}
		}
		capture.mu.Unlock()
		if notified {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !notified {
		t.Fatal("successful background update didn't notify the user")
	}
	selected, err = authority.ClaudePlugins()
	if err != nil || len(selected) != 1 || selected[0].Root() != old.Root() {
		t.Fatalf("live versions changed=%+v %v", selected, err)
	}
	if err := old.CheckSelected(authority); err != nil {
		t.Fatal("old component lost authority before reload", err)
	}
	if output, err := a.ReloadClaudePlugins(t.Context()); err != nil || !strings.Contains(output, "Reloaded 2 plugins") {
		t.Fatalf("reload=%q %v", output, err)
	}
	if err := old.CheckSelected(authority); err == nil {
		t.Fatal("old component retained authority after reload")
	}
	selected, err = authority.ClaudePlugins()
	if err != nil || len(selected) != 2 {
		t.Fatalf("new versions not activated=%+v %v", selected, err)
	}
}
