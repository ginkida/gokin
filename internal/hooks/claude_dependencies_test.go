package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginDependenciesLocalOverrideAndTransitiveRevocation(t *testing.T) {
	m, _, user, _ := managedTestManager(t, false, false)
	parent := pluginTestRoot(t, "parent", "printf parent")
	child := pluginTestRoot(t, "child", "printf child")
	remote := pluginTestRoot(t, "child", "printf remote")
	unrelated := pluginTestRoot(t, "unrelated", "printf unrelated")
	writeClaudeSettingsTestFile(t, filepath.Join(parent, ".claude-plugin", "plugin.json"), map[string]any{"name": "parent", "dependencies": []any{map[string]string{"name": "child", "marketplace": "team", "version": "^9.0"}}})
	writeClaudeSettingsTestFile(t, filepath.Join(remote, ".claude-plugin", "plugin.json"), map[string]any{"name": "child", "version": "9.1.0"})
	pluginTestRegistry(t, user, map[string][]claudePluginInstall{"child@team": {{Scope: "user", InstallPath: remote}}})
	m.ConfigureClaudePlugins([]string{parent, child, unrelated})
	selected, err := m.ClaudePlugins()
	if err != nil || len(selected) != 3 {
		t.Fatalf("local unversioned dependency rejected: %+v %v", selected, err)
	}
	var parentSelection ClaudePlugin
	for _, plugin := range selected {
		if plugin.Name == "parent" {
			parentSelection = plugin
		}
	}
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"child@inline": false}})
	selected, err = m.ClaudePlugins()
	if err != nil || len(selected) != 2 {
		t.Fatalf("disabled local dependency retained parent: %+v %v", selected, err)
	}
	for _, plugin := range selected {
		if plugin.Name == "parent" {
			t.Fatal("disabled local dependency fell back to remote")
		}
	}
	if err := parentSelection.CheckSelected(m); err == nil {
		t.Fatal("previously selected parent survived dependency revocation")
	}
	if warnings := strings.Join(m.ClaudeDependencyWarnings(), "\n"); !strings.Contains(warnings, "child@inline") || !strings.Contains(warnings, "disabled") {
		t.Fatalf("dependency diagnostic=%s", warnings)
	}
	for _, hook := range m.GetHooks() {
		if hook.claudePlugin != nil && hook.claudePlugin.ID == "parent@inline" {
			t.Fatal("inactive parent's hooks remain registered")
		}
	}
}

func TestPluginDependenciesMissingInstalledCachePreservesUnrelatedPlugins(t *testing.T) {
	for _, registered := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing-cache", false: "missing-record"}[registered], func(t *testing.T) {
			m, _, user, _ := managedTestManager(t, true, false)
			parent := pluginTestRoot(t, "parent", "printf parent")
			middle := pluginTestRoot(t, "middle", "printf middle")
			child := pluginTestRoot(t, "child", "printf child")
			unrelated := pluginTestRoot(t, "unrelated", "printf unrelated")
			writeClaudeSettingsTestFile(t, filepath.Join(parent, ".claude-plugin", "plugin.json"), map[string]any{"name": "parent", "dependencies": []string{"middle"}})
			writeClaudeSettingsTestFile(t, filepath.Join(middle, ".claude-plugin", "plugin.json"), map[string]any{"name": "middle", "dependencies": []string{"child"}})
			records := map[string][]claudePluginInstall{"parent@team": {{Scope: "user", InstallPath: parent}}, "middle@team": {{Scope: "user", InstallPath: middle}}, "unrelated@team": {{Scope: "user", InstallPath: unrelated}}}
			if registered {
				records["child@team"] = []claudePluginInstall{{Scope: "user", InstallPath: child}}
			}
			pluginTestRegistry(t, user, records)
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"parent@team": true, "middle@team": true, "child@team": true, "unrelated@team": true}})
			if err := os.RemoveAll(child); err != nil {
				t.Fatal(err)
			}
			selected, err := m.ClaudePlugins()
			if err != nil || len(selected) != 1 || selected[0].Name != "unrelated" {
				t.Fatalf("missing dependency prevented unrelated plugin loading: %+v %v", selected, err)
			}
			if warnings := m.ClaudeDependencyWarnings(); len(warnings) != 2 {
				t.Fatalf("transitive diagnostics=%+v", warnings)
			}
			if registered {
				writeClaudeSettingsTestFile(t, filepath.Join(child, ".claude-plugin", "plugin.json"), map[string]any{"name": "child"})
				if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 4 {
					t.Fatalf("restored cache did not recover graph: %+v %v", selected, err)
				}
			}
		})
	}
}
