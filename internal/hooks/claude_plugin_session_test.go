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

func TestCachedStartupDefersMissingInstallsUntilExplicitReload(t *testing.T) {
	for _, recordExists := range []bool{false, true} {
		t.Run(map[bool]string{false: "no record", true: "missing cache"}[recordExists], func(t *testing.T) {
			m, _, user, managed := managedTestManager(t, false, true)
			m.ConfigureClaudePlugins(nil)
			if recordExists {
				pluginTestRegistry(t, user, map[string][]claudePluginInstall{"missing@acme": {{Scope: "user", InstallPath: filepath.Join(user, "absent")}}})
			}
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"missing@acme": true}})
			if err := m.PinCachedClaudePluginInstalls(); err != nil {
				t.Fatal(err)
			}
			if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 0 {
				t.Fatalf("cache miss blocked startup: %v %v", selected, err)
			}
			// A background install cannot change this session's selected set.
			root := pluginTestRoot(t, "missing", "")
			pluginTestRegistry(t, user, map[string][]claudePluginInstall{"missing@acme": {{Scope: "user", InstallPath: root}}})
			if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 0 {
				t.Fatalf("background install activated: %v %v", selected, err)
			}
			if err := m.PinClaudePluginInstalls(); err != nil {
				t.Fatal(err)
			}
			if selected, err := m.ClaudePlugins(); err != nil || len(selected) != 1 {
				t.Fatalf("explicit reload failed: %v %v", selected, err)
			}
			writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"required@acme": true}})
			if _, err := m.ClaudePlugins(); err == nil {
				t.Fatal("missing managed requirement was suppressed")
			}
		})
	}
}

func TestCachedStartupUsesEffectiveInstallScope(t *testing.T) {
	for _, test := range []struct {
		name, scope               string
		trusted, foreign, missing bool
	}{
		{"missing project", "project", true, false, true},
		{"missing local", "local", true, false, true},
		{"foreign project", "project", true, true, true},
		{"untrusted local", "local", false, false, true},
		{"cached project", "project", true, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, project, user, _ := managedTestManager(t, false, test.trusted)
			m.ConfigureClaudePlugins(nil)
			userRoot := pluginTestRoot(t, "guard", "")
			effectiveRoot := pluginTestRoot(t, "guard", "")
			if test.missing {
				effectiveRoot = filepath.Join(t.TempDir(), "missing")
			}
			recordProject := project
			if test.foreign {
				recordProject = t.TempDir()
			}
			pluginTestRegistry(t, user, map[string][]claudePluginInstall{"guard@acme": {
				{Scope: "user", InstallPath: userRoot},
				{Scope: test.scope, ProjectPath: recordProject, InstallPath: effectiveRoot},
			}})
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]bool{"guard@acme": true}})
			if err := m.PinCachedClaudePluginInstalls(); err != nil {
				t.Fatal(err)
			}
			selected, err := m.ClaudePlugins()
			if err != nil {
				t.Fatalf("effective cache miss blocked startup: %v", err)
			}
			if test.trusted && !test.foreign && test.missing {
				if len(selected) != 0 {
					t.Fatalf("lower-scope install replaced deferred effective install: %v", selected)
				}
			} else {
				want := userRoot
				if test.trusted && !test.foreign {
					want = effectiveRoot
				}
				want, _ = filepath.EvalSymlinks(want)
				if len(selected) != 1 || selected[0].Root() != want {
					t.Fatalf("selected=%v want root=%s", selected, want)
				}
			}
		})
	}
}
