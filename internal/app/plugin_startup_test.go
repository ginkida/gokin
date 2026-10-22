package app

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestBuilderSynchronizesDeclaredPluginsBeforeWorkflowDiscovery(t *testing.T) {
	for _, mode := range []string{"sync", "interactive", "embedding-off", "bare"} {
		t.Run(mode, func(t *testing.T) {
			work, user, market := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", user)
			writeJSON := func(path string, value any) {
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
			plugin := filepath.Join(market, "plugins", "suite")
			writeJSON(filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "suite"})
			command := filepath.Join(plugin, "commands", "review.md")
			if err := os.MkdirAll(filepath.Dir(command), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(command, []byte("Review $ARGUMENTS carefully."), 0600); err != nil {
				t.Fatal(err)
			}
			writeJSON(filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "team", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "suite", "source": "./plugins/suite"}}})
			writeJSON(filepath.Join(user, "settings.json"), map[string]any{"extraKnownMarketplaces": map[string]any{"team": map[string]any{"source": map[string]string{"source": "directory", "path": market}}}, "enabledPlugins": map[string]bool{"suite@team": true}})
			cfg := config.DefaultConfig()
			cfg.Hooks.Enabled = false
			cfg.Session.Enabled = false
			options := BuildOptions{NonInteractive: mode != "interactive", SyncDeclaredPlugins: mode != "embedding-off", Bare: mode == "bare"}
			builder := NewBuilderWithOptions(cfg, work, options)
			t.Cleanup(builder.cancel)
			builder.mainClient = testkit.NewMockClient()
			builder.registry = tools.NewRegistry()
			catalog := skills.NewCatalog(nil)
			builder.registry.MustRegister(tools.NewSkillToolWithCatalogAndWorkDir(catalog, work))
			builder.executor = tools.NewExecutor(builder.registry, builder.mainClient, time.Second)
			builder.configDirErr = errors.New("disable persistence in test")
			if err := builder.initManagers(); err != nil {
				t.Fatal(err)
			}
			registry := filepath.Join(user, "plugins", "installed_plugins.json")
			if (builder.pluginAutoUpdater != nil) != (mode == "interactive") || (builder.pluginCommandUpdater != nil) != (mode == "interactive") {
				t.Fatal("builder scheduled updates outside opted-in interactive mode")
			}
			if mode != "sync" && mode != "interactive" {
				if _, err := os.Stat(registry); !os.IsNotExist(err) {
					t.Fatalf("%s implicitly installed a plugin: %v", mode, err)
				}
				return
			}
			if _, err := os.Stat(registry); err != nil {
				t.Fatal("declared plugin was not installed before discovery", err)
			}
			workflow, found := catalog.Get("suite:review")
			if !found || !strings.Contains(workflow.Body, "Review") {
				t.Fatalf("workflow=%+v found=%t", workflow, found)
			}
			if !strings.Contains(builder.pluginStartupSummary, "1 installed") {
				t.Fatal("startup result was not retained for the UI", builder.pluginStartupSummary)
			}
		})
	}
}
