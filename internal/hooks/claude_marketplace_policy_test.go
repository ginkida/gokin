package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedMarketplacePolicyRevokesInstalledSourcesButNotSideloads(t *testing.T) {
	plugin, user, work, managed := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha"})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "plugins", "installed_plugins.json"), map[string]any{"version": 2, "plugins": map[string]any{"alpha@acme": []any{map[string]any{"scope": "user", "installPath": plugin}}}})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "plugins", "known_marketplaces.json"), map[string]any{"acme": map[string]any{"source": map[string]any{"source": "github", "repo": "acme/catalog"}}})
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"strictKnownMarketplaces": []any{map[string]any{"source": "github", "repo": "acme/*"}}})
	manager := NewManager(false, work)
	manager.ConfigureClaudeSettings(work, user, false)
	manager.ConfigureClaudeManagedSettings(managed)
	manager.ConfigureClaudePlugins(nil)
	if selected, err := manager.ClaudePlugins(); err != nil || len(selected) != 1 {
		t.Fatalf("allowed=%+v %v", selected, err)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"strictKnownMarketplaces": []any{}})
	if _, err := manager.ClaudePlugins(); err == nil {
		t.Fatal("installed source survived allowlist revocation")
	}
	os.Remove(filepath.Join(user, "plugins", "installed_plugins.json"))
	manager.ConfigureClaudePlugins([]string{plugin})
	if selected, err := manager.ClaudePlugins(); err != nil || len(selected) != 1 {
		t.Fatalf("allowlist incorrectly gated sideload=%+v %v", selected, err)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"strictKnownMarketplaces": []any{}, "disableSideloadFlags": true})
	if _, err := manager.ClaudePlugins(); err == nil {
		t.Fatal("disableSideloadFlags bypassed")
	}
}
func TestSkillsDirectoryBlockDoesNotRequireUnrelatedMarketplaceProvenance(t *testing.T) {
	plugin, user, work, managed := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha"})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "plugins", "installed_plugins.json"), map[string]any{"version": 2, "plugins": map[string]any{"alpha@acme": []any{map[string]any{"scope": "user", "installPath": plugin}}}})
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"blockedMarketplaces": []any{map[string]any{"source": "skills-dir"}}})
	manager := NewManager(false, work)
	manager.ConfigureClaudeSettings(work, user, false)
	manager.ConfigureClaudeManagedSettings(managed)
	manager.ConfigureClaudePlugins(nil)
	if selected, err := manager.ClaudePlugins(); err != nil || len(selected) != 1 {
		t.Fatalf("unrelated source blocked=%+v %v", selected, err)
	}
}

func TestPluginStorageUsesCompleteSourceIdentity(t *testing.T) {
	for _, pair := range [][2]string{{"a-b@c", "a@b-c"}, {"a.b@c", "a-b@c"}, {"плагин@org", "------@org"}} {
		if ClaudePluginStorageKey(pair[0]) == ClaudePluginStorageKey(pair[1]) {
			t.Fatalf("storage collision for %v", pair)
		}
	}
}
