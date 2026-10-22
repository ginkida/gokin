package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/plugins"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestReloadPluginsRepairsDependenciesBeforeRuntimeDiscovery(t *testing.T) {
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
	write(filepath.Join(market, "bundle", ".claude-plugin", "plugin.json"), map[string]any{"name": "bundle", "dependencies": []string{"child"}})
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
	rows, _ := installer.List()
	for _, row := range rows {
		if row.ID == "child@team" {
			if err := os.RemoveAll(row.InstallPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	authority := hooks.NewManager(false, work)
	authority.ConfigureClaudeManagedSettings(managed)
	authority.ConfigureClaudeSettings(work, user, false)
	authority.ConfigureClaudePlugins(nil)
	cfg := config.DefaultConfig()
	cfg.MCP.Enabled = false
	app := &App{ctx: t.Context(), config: cfg, workDir: work, registry: tools.NewRegistry(), client: testkit.NewMockClient(), hooksManager: authority}
	output, err := app.ReloadClaudePlugins(t.Context())
	if err != nil || !strings.Contains(output, "Reloaded 2 plugins") {
		t.Fatalf("reload=%q %v", output, err)
	}
	if warnings := authority.ClaudeDependencyWarnings(); len(warnings) != 0 {
		t.Fatal("repaired runtime still reports missing dependencies", warnings)
	}
	write(filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"bundle@team": true, "child@team": false}})
	output, err = app.ReloadClaudePlugins(t.Context())
	if err != nil || !strings.Contains(output, "disabled") || !strings.Contains(output, "child@team") {
		t.Fatalf("reload hid disabled dependency diagnostic: %q %v", output, err)
	}
}
