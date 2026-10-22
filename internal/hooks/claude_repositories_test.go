package hooks

import (
	"path/filepath"
	"testing"
)

func TestRegisteredRepositoryPluginScopeAndPreview(t *testing.T) {
	root, user, repository := t.TempDir(), t.TempDir(), t.TempDir()
	plugin := pluginTestRoot(t, "registered-guard", "true")
	pluginTestRegistry(t, user, map[string][]claudePluginInstall{"registered-guard@catalog": {{Scope: "project", ProjectPath: repository, InstallPath: plugin}}})
	writeClaudeSettingsTestFile(t, filepath.Join(repository, ".claude/settings.json"), map[string]any{"enabledPlugins": map[string]any{"registered-guard@catalog": true}})
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 0 {
		t.Fatal("unregistered root leaked plugins", selected, err)
	}
	m.ConfigureClaudeRepositoryDirs([]string{repository})
	selected, err := m.ClaudePlugins()
	if err != nil || len(selected) != 1 || !samePluginProject(selected[0].Root(), plugin) {
		t.Fatal("registered project-scoped plugin was not loaded", selected, err)
	}
	preview, err := m.PreviewClaudePluginInstalls()
	if err != nil {
		t.Fatal(err)
	}
	if selected, err := preview.ClaudePlugins(); err != nil || len(selected) != 1 {
		t.Fatal("reload preview lost registered roots", selected, err)
	}
	m.ConfigureClaudeRepositoryDirs(nil)
	if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 0 {
		t.Fatal("root removal retained plugin", selected, err)
	}
}

func TestRegisteredRepositoryCannotOverrideTrustOrManagedOnly(t *testing.T) {
	for _, mode := range []string{"untrusted", "managed-only"} {
		t.Run(mode, func(t *testing.T) {
			root, user, repository := t.TempDir(), t.TempDir(), t.TempDir()
			m := NewManager(true, root)
			m.ConfigureClaudeSettings(root, user, mode != "untrusted")
			if mode == "managed-only" {
				managed := t.TempDir()
				m.ConfigureClaudeManagedSettings(managed)
				writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"allowManagedHooksOnly": true})
			}
			writeClaudeSettingsTestFile(t, filepath.Join(repository, ".claude/settings.json"), settingsTestEvent("PreToolUse", "Write", "exit 2"))
			m.ConfigureClaudeRepositoryDirs([]string{repository})
			if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 0 {
				t.Fatal("registered root bypassed trust or managed-only policy", results)
			}
		})
	}
}
