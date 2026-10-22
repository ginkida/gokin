package plugins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/mcp"
)

func dependencyFixture(t *testing.T) (*Manager, string, string, string) {
	t.Helper()
	m, market, root := startupFixture(t)
	manifest, _ := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	manifest["dependencies"] = json.RawMessage(`["middle"]`)
	writeTestJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), manifest)
	middle := filepath.Join(market, "plugins", "middle")
	leaf := filepath.Join(market, "plugins", "leaf")
	writeTestJSON(t, filepath.Join(middle, ".claude-plugin", "plugin.json"), map[string]any{"name": "middle", "version": "1.0.0", "dependencies": []string{"leaf"}})
	writeTestJSON(t, filepath.Join(leaf, ".claude-plugin", "plugin.json"), map[string]any{"name": "leaf", "version": "2.1.0"})
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": "./plugins/alpha"}, map[string]any{"name": "middle", "source": "./plugins/middle"}, map[string]any{"name": "leaf", "source": "./plugins/leaf"}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	return m, market, root, leaf
}

func TestDependencyInstallEnableDisableUninstallAndPrune(t *testing.T) {
	m, _, _, _ := dependencyFixture(t)
	result, err := m.Install(t.Context(), "alpha", InstallOptions{})
	if err != nil || len(result.Dependencies) != 2 {
		t.Fatalf("install=%+v %v", result, err)
	}
	rows, err := m.List()
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%+v %v", rows, err)
	}
	for _, row := range rows {
		if !row.Enabled || row.AutoInstalled != (row.ID != "alpha@acme") {
			t.Fatalf("dependency tracking=%+v", row)
		}
	}
	if selected := runtimePlugins(t, m); len(selected) != 3 {
		t.Fatalf("runtime=%+v", selected)
	}
	if _, err := m.SetEnabled(t.Context(), "leaf", "", false); err == nil || !strings.Contains(err.Error(), "required by") {
		t.Fatalf("disabled required leaf: %v", err)
	}
	if _, err := m.Uninstall(t.Context(), "middle", RemoveOptions{}); err == nil {
		t.Fatal("removed required middle")
	}
	for _, id := range []string{"alpha", "middle", "leaf"} {
		if _, err := m.SetEnabled(t.Context(), id, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SetEnabled(t.Context(), "alpha", "", true); err != nil {
		t.Fatal(err)
	}
	rows, _ = m.List()
	for _, row := range rows {
		if !row.Enabled {
			t.Fatalf("enable did not reactivate dependency: %+v", row)
		}
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	preview, err := m.Prune(t.Context(), PruneOptions{DryRun: true})
	if err != nil || len(preview.Pruned) != 2 || preview.Changed {
		t.Fatalf("preview=%+v %v", preview, err)
	}
	after, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("prune preview changed registry")
	}
	pruned, err := m.Prune(t.Context(), PruneOptions{})
	if err != nil || len(pruned.Pruned) != 2 || !pruned.Changed {
		t.Fatalf("prune=%+v %v", pruned, err)
	}
	if rows, err := m.List(); err != nil || len(rows) != 0 {
		t.Fatalf("after prune=%+v %v", rows, err)
	}
}

func TestDependencyInstallFailureLeavesEntireGraphUnchanged(t *testing.T) {
	m, _, root, leaf := dependencyFixture(t)
	manifest, _ := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	delete(manifest, "dependencies")
	writeTestJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), manifest)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(m.pluginDir, "installed_plugins.json")
	settings := filepath.Join(m.opts.UserDir, "settings.json")
	beforeRegistry, _ := os.ReadFile(registry)
	beforeSettings, _ := os.ReadFile(settings)
	manifest["dependencies"] = json.RawMessage(`["middle"]`)
	writeTestJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), manifest)
	writeTestJSON(t, filepath.Join(leaf, ".lsp.json"), map[string]any{"bad": map[string]any{"command": "unused", "unsupported": true}})
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true}); err == nil {
		t.Fatal("invalid dependency update succeeded")
	}
	afterRegistry, _ := os.ReadFile(registry)
	afterSettings, _ := os.ReadFile(settings)
	if !bytes.Equal(beforeRegistry, afterRegistry) || !bytes.Equal(beforeSettings, afterSettings) {
		t.Fatal("partial dependency graph was published")
	}
}

func TestDependencyManualInstallAndDisabledRootsSurviveUpdatesAndPrune(t *testing.T) {
	m, _, _, leaf := dependencyFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "leaf", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(t.Context(), "alpha", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(t.Context(), "middle", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(t.Context(), "leaf", "", false); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(leaf, "new.txt"), []byte("updated manually installed dependency"), 0600)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	for _, row := range rows {
		if row.ID == "leaf@acme" && row.AutoInstalled {
			t.Fatal("update converted manual plugin to auto-installed")
		}
		if row.Enabled {
			t.Fatalf("updating a disabled bundle re-enabled %s", row.ID)
		}
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	pruned, err := m.Prune(t.Context(), PruneOptions{})
	if err != nil || len(pruned.Pruned) != 1 || pruned.Pruned[0] != "middle@acme" {
		t.Fatalf("manual prune=%+v %v", pruned, err)
	}
	rows, _ = m.List()
	if len(rows) != 1 || rows[0].ID != "leaf@acme" {
		t.Fatalf("manual dependency disappeared: %+v", rows)
	}
}

func TestDependencyRepeatedInstallRepairsMissingChildCache(t *testing.T) {
	m, _, _, _ := dependencyFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	for _, row := range rows {
		if row.ID == "leaf@acme" {
			if err := os.RemoveAll(row.InstallPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if selected := runtimePlugins(t, m); len(selected) != 3 {
		t.Fatal("repeated install left missing child", selected)
	}
}

func TestDependencyCrossMarketplaceUsesOnlyRootAllowlist(t *testing.T) {
	m, market, root, _ := dependencyFixture(t)
	other := t.TempDir()
	writeTestJSON(t, filepath.Join(other, "plugins", "middle", ".claude-plugin", "plugin.json"), map[string]any{"name": "middle", "dependencies": []string{"leaf@third"}})
	writeTestJSON(t, filepath.Join(other, ".claude-plugin", "marketplace.json"), map[string]any{"name": "other", "owner": map[string]string{"name": "Other"}, "allowCrossMarketplaceDependenciesOn": []string{"third"}, "plugins": []any{map[string]any{"name": "middle", "source": "./plugins/middle"}}})
	third := t.TempDir()
	writeTestJSON(t, filepath.Join(third, "plugins", "leaf", ".claude-plugin", "plugin.json"), map[string]any{"name": "leaf"})
	writeTestJSON(t, filepath.Join(third, ".claude-plugin", "marketplace.json"), map[string]any{"name": "third", "owner": map[string]string{"name": "Third"}, "plugins": []any{map[string]any{"name": "leaf", "source": "./plugins/leaf"}}})
	for _, path := range []string{other, third} {
		if _, err := m.AddMarketplace(t.Context(), path, nil); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _ := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	manifest["dependencies"] = json.RawMessage(`["middle@other"]`)
	writeTestJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), manifest)
	catalog, _ := readObject(filepath.Join(market, ".claude-plugin", "marketplace.json"))
	catalog["allowCrossMarketplaceDependenciesOn"] = json.RawMessage(`["other"]`)
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), catalog)
	result, err := m.Install(t.Context(), "alpha", InstallOptions{})
	if err != nil || len(result.Warnings) == 0 {
		t.Fatalf("cross install=%+v %v", result, err)
	}
	rows, _ := m.List()
	if len(rows) != 2 {
		t.Fatalf("nested allowlist installed third marketplace: %+v", rows)
	}
	for _, row := range rows {
		if row.Enabled || len(row.Errors) == 0 {
			t.Fatalf("incomplete bundle reported active: %+v", row)
		}
	}
	if selected := runtimePlugins(t, m); len(selected) != 0 {
		t.Fatalf("incomplete bundle loaded: %+v", selected)
	}
	if _, err := m.Install(t.Context(), "leaf@third", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal("already-enabled cross dependency was refused", err)
	}
	if selected := runtimePlugins(t, m); len(selected) != 3 {
		t.Fatalf("reviewed cross dependency did not satisfy bundle: %+v", selected)
	}
}

func TestDependencyEntryCrossMarketplaceRefusalIsAtomic(t *testing.T) {
	m, market, root, _ := dependencyFixture(t)
	manifest, _ := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	delete(manifest, "dependencies")
	writeTestJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), manifest)
	catalog, _ := readObject(filepath.Join(market, ".claude-plugin", "marketplace.json"))
	var entries []map[string]any
	json.Unmarshal(catalog["plugins"], &entries)
	entries[0]["dependencies"] = []string{"external@other"}
	catalog["plugins"], _ = json.Marshal(entries)
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), catalog)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "allowCrossMarketplaceDependenciesOn") {
		t.Fatalf("entry cross dependency=%v", err)
	}
	if _, err := os.Stat(filepath.Join(m.pluginDir, "installed_plugins.json")); !os.IsNotExist(err) {
		t.Fatal("entry dependency refusal published root")
	}
}

func TestDependencyCommandsNeverRunAsPartOfBundleInstall(t *testing.T) {
	m, market, _, _ := dependencyFixture(t)
	catalog, _ := readObject(filepath.Join(market, ".claude-plugin", "marketplace.json"))
	var entries []map[string]any
	json.Unmarshal(catalog["plugins"], &entries)
	sentinel := filepath.Join(t.TempDir(), "dependency-installer-ran")
	entries[2]["source"] = Source{Kind: "command", Command: "touch '" + sentinel + "'"}
	catalog["plugins"], _ = json.Marshal(entries)
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), catalog)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "explicit plugin install/update") {
		t.Fatalf("dependency command=%v", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("bundle ran dependency installer")
	}
	if _, err := os.Stat(filepath.Join(m.pluginDir, "installed_plugins.json")); !os.IsNotExist(err) {
		t.Fatal("bundle with missing command dependency partially installed")
	}
}

func TestDependencyManagedBundleActivationRevokesWhenParentAuthorityChanges(t *testing.T) {
	m, market, _, leaf := dependencyFixture(t)
	writeTestJSON(t, filepath.Join(leaf, ".mcp.json"), map[string]any{"service": map[string]any{"command": "UNUSED_BINARY"}})
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"alpha@acme": true}, "extraKnownMarketplaces": map[string]any{"acme": map[string]any{"source": Source{Kind: "directory", Path: market}}}})
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Errors) > 0 {
		t.Fatalf("managed bundle=%+v %v", report, err)
	}
	authority := hooks.NewManager(false, m.opts.WorkDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudePlugins(nil)
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 3 {
		t.Fatalf("managed selected=%+v %v", selected, err)
	}
	var leafPlugin hooks.ClaudePlugin
	for _, plugin := range selected {
		if plugin.ID() == "leaf@acme" {
			leafPlugin = plugin
		}
	}
	if !leafPlugin.ExplicitlyEnabled() {
		t.Fatal("managed bundle did not activate dependency components")
	}
	servers, err := mcp.LoadClaudePluginServers(authority, m.opts.WorkDir, false)
	if err != nil || len(servers) != 2 {
		t.Fatalf("managed MCP definitions=%+v %v", servers, err)
	}
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{})
	if err := leafPlugin.CheckSelected(authority); err == nil {
		t.Fatal("old dependency component retained revoked derived authority")
	}
}

func TestDependencyProjectScopePruneAndSharedRoots(t *testing.T) {
	m, _, _, _ := dependencyFixture(t)
	for _, scope := range []string{"user", "project"} {
		if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	preview, err := m.Prune(t.Context(), PruneOptions{Scope: "project", DryRun: true})
	// The user bundle retains its own compatible copies. Redundant project
	// copies can be pruned without damaging the inherited consumer.
	if err != nil || len(preview.Pruned) != 2 {
		t.Fatalf("shared scope preview=%+v %v", preview, err)
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{Scope: "user"}); err != nil {
		t.Fatal(err)
	}
	pruned, err := m.Prune(t.Context(), PruneOptions{Scope: "project"})
	if err != nil || len(pruned.Pruned) != 2 {
		t.Fatalf("project prune=%+v %v", pruned, err)
	}
	rows, _ := m.List()
	if len(rows) != 2 {
		t.Fatalf("prune removed other scope: %+v", rows)
	}
}

func TestDependencyPrunePreviewDoesNotCreateStore(t *testing.T) {
	m, _, _ := startupFixture(t)
	result, err := m.Prune(t.Context(), PruneOptions{DryRun: true})
	if err != nil || len(result.Pruned) != 0 {
		t.Fatalf("empty prune=%+v %v", result, err)
	}
	if _, err := os.Stat(m.pluginDir); !os.IsNotExist(err) {
		t.Fatal("read-only prune created plugin store")
	}
}
