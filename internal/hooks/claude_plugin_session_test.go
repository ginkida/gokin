package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinnedPluginVersionsDeferUpdatesAndNewDependencies(t *testing.T) {
	m, _, user, managed := managedTestManager(t, false, true)
	m.ConfigureClaudePlugins(nil)
	old := pluginTestRoot(t, "guard", "")
	next := pluginTestRoot(t, "guard", "")
	child := pluginTestRoot(t, "child", "")
	old, _ = filepath.EvalSymlinks(old)
	registry := map[string][]claudePluginInstall{"guard@acme": {{Scope: "user", InstallPath: old}}}
	pluginTestRegistry(t, user, registry)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"guard@acme": true}})
	if err := m.PinClaudePluginInstalls(); err != nil {
		t.Fatal(err)
	}
	registry["guard@acme"] = []claudePluginInstall{{Scope: "user", InstallPath: next}}
	registry["child@acme"] = []claudePluginInstall{{Scope: "user", InstallPath: child}}
	pluginTestRegistry(t, user, registry)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"guard@acme": true, "child@acme": true}})
	selected, err := m.ClaudePlugins()
	if err != nil || len(selected) != 1 || selected[0].Root() != old {
		t.Fatalf("background update changed session: %+v %v", selected, err)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"guard@acme": false, "child@acme": true}})
	if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 0 {
		t.Fatalf("pin defeated user disable: %+v %v", selected, err)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"guard@acme": true}})
	if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 1 || selected[0].Root() != old {
		t.Fatalf("pin defeated managed force: %+v %v", selected, err)
	}
	if err := m.PinClaudePluginInstalls(); err != nil {
		t.Fatal(err)
	}
	if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 2 || selected[0].Root() == old {
		t.Fatalf("explicit reload didn't adopt updates: %+v %v", selected, err)
	}
	// Disk registry corruption cannot silently replace the pinned executable
	// set, and a failed explicit refresh preserves its previous snapshot.
	for _, bad := range []string{"broken", `{"version":99,"plugins":{}}`, `{"version":2,"plugins":null}`, `{"version":2,"plugins":{"invalid":[]}}`} {
		if err := os.WriteFile(filepath.Join(user, "plugins", "installed_plugins.json"), []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if err := m.PinClaudePluginInstalls(); err == nil {
			t.Fatal("invalid registry accepted")
		}
		if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 2 {
			t.Fatalf("failed refresh lost working snapshot: %v", err)
		}
	}
}

func TestPinnedMissingDeclarationsAndNewManagedRequirementsFailClosed(t *testing.T) {
	for _, managedRequirement := range []bool{false, true} {
		m, _, user, managed := managedTestManager(t, false, false)
		m.ConfigureClaudePlugins(nil)
		if !managedRequirement {
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"missing@acme": true}})
		}
		if err := m.PinClaudePluginInstalls(); err != nil {
			t.Fatal(err)
		}
		if managedRequirement {
			writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"missing@acme": true}})
		}
		if _, err := m.ClaudePlugins(); err == nil {
			t.Fatal("pin hid a missing required declaration")
		}
	}
}
