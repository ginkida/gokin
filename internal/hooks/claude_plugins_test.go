package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginTestRoot(t *testing.T, name, command string) string {
	t.Helper()
	root := t.TempDir()
	writeClaudeSettingsTestFile(t, filepath.Join(root, ".claude-plugin", "plugin.json"), map[string]any{"name": name})
	if command != "" {
		writeClaudeSettingsTestFile(t, filepath.Join(root, "hooks", "hooks.json"), settingsTestEvent("PreToolUse", "Write", command))
	}
	return root
}

func pluginTestRegistry(t *testing.T, user string, records map[string][]claudePluginInstall) {
	t.Helper()
	writeClaudeSettingsTestFile(t, filepath.Join(user, "plugins", "installed_plugins.json"), map[string]any{"version": 2, "plugins": records})
}

func TestPluginHooksManifestFormsMergeDeduplicateAndRefresh(t *testing.T) {
	m, root, _, _ := managedTestManager(t, false, false)
	plugin := pluginTestRoot(t, "guard", "printf x >> default-runs")
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, "extra.json"), settingsTestEvent("PreToolUse", "Write", "printf x >> extra-runs"))
	inline := settingsTestEvent("PreToolUse", "Write", "printf x >> inline-runs")["hooks"]
	manifest := map[string]any{"name": "guard", "hooks": []any{"./extra.json", inline, inline, "./hooks/hooks.json"}}
	manifestPath := filepath.Join(plugin, ".claude-plugin", "plugin.json")
	writeClaudeSettingsTestFile(t, manifestPath, manifest)
	m.ConfigureClaudePlugins([]string{plugin})
	for i := 0; i < 2; i++ {
		results := m.RunPreTool(t.Context(), "write", nil)
		if len(results) != 3 || !m.IsEnabled() {
			t.Fatalf("explicit plugin hooks did not merge/dedup with native hooks disabled: %+v", results)
		}
		for _, result := range results {
			if result.Error != nil || !strings.HasPrefix(result.Hook.Source, "claude:plugin:guard@inline") {
				t.Fatalf("plugin result=%+v", result)
			}
		}
	}
	for _, name := range []string{"default-runs", "extra-runs", "inline-runs"} {
		assertManagedMarker(t, root, name, "xx")
	}
	original, _ := os.ReadFile(manifestPath)
	if err := os.WriteFile(manifestPath, []byte("invalid manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckReady(); err == nil {
		t.Fatal("explicit plugins bypassed malformed manifest with native hooks disabled")
	}
	if err := os.WriteFile(manifestPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckReady(); err != nil {
		t.Fatalf("identical plugin repair failed: %v", err)
	}
}

func TestManagedForcedPluginSurvivesDisableAndPreventsSideloadShadow(t *testing.T) {
	m, root, user, managed := managedTestManager(t, false, false)
	official := pluginTestRoot(t, "guard", "printf x >> mandatory-plugin")
	fake := pluginTestRoot(t, "guard", "printf x >> forged-plugin")
	pluginTestRegistry(t, user, map[string][]claudePluginInstall{"guard@org": {{Scope: "user", InstallPath: official}}})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"disableAllHooks": true, "enabledPlugins": map[string]any{"guard@org": false}})
	policy := map[string]any{"allowManagedHooksOnly": true, "enabledPlugins": map[string]any{"guard@org": true}}
	path := filepath.Join(managed, "managed-settings.json")
	writeClaudeSettingsTestFile(t, path, policy)
	m.ConfigureClaudePlugins([]string{fake})
	results := m.RunPreTool(t.Context(), "write", nil)
	if len(results) != 1 || results[0].Error != nil || !results[0].Hook.claudeManaged || results[0].Hook.claudePlugin.ID != "guard@org" {
		t.Fatalf("forced plugin enforcement=%+v", results)
	}
	assertManagedMarker(t, root, "mandatory-plugin", "x")
	assertManagedMarker(t, root, "forged-plugin", "")
	policy["disableAllHooks"] = true
	writeClaudeSettingsTestFile(t, path, policy)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 0 {
		t.Fatalf("managed disable did not disable forced plugin: %+v", results)
	}
	delete(policy, "disableAllHooks")
	policy["enabledPlugins"] = map[string]any{"guard@org": false}
	delete(policy, "allowManagedHooksOnly")
	writeClaudeSettingsTestFile(t, path, policy)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 0 {
		t.Fatalf("blocked managed plugin replaced by local copy: %+v", results)
	}
}

func TestPluginScopeSelectionPrecedenceAndDisabledCopyFallback(t *testing.T) {
	m, root, user, _ := managedTestManager(t, true, true)
	global := pluginTestRoot(t, "guard", "printf x >> global-plugin")
	foreign := pluginTestRoot(t, "guard", "printf x >> foreign-plugin")
	local := pluginTestRoot(t, "guard", "printf x >> local-plugin")
	explicit := pluginTestRoot(t, "guard", "printf x >> explicit-plugin")
	pluginTestRegistry(t, user, map[string][]claudePluginInstall{"guard@org": {
		{Scope: "project", ProjectPath: t.TempDir(), InstallPath: foreign},
		{Scope: "user", InstallPath: global},
		{Scope: "local", ProjectPath: root, InstallPath: local},
	}})
	m.ConfigureClaudePlugins([]string{explicit})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("explicit precedence=%+v", results)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]any{"guard@inline": false}})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("disabled copy prevented installed fallback: %+v", results)
	}
	m.ConfigureClaudeSettings(root, user, false)
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("untrusted project retained local record or lost user fallback: %+v", results)
	}
	assertManagedMarker(t, root, "explicit-plugin", "x")
	assertManagedMarker(t, root, "local-plugin", "x")
	assertManagedMarker(t, root, "global-plugin", "x")
	assertManagedMarker(t, root, "foreign-plugin", "")
}

func TestPluginExecAndShellEnvironmentPreserveLiteralPathsAndOptionValues(t *testing.T) {
	m, root, user, _ := managedTestManager(t, false, false)
	plugin := filepath.Join(t.TempDir(), "plugin spaces ' $() ` chars")
	value := "literal ' $(touch injected) `touch injected` $HOME"
	manifest := map[string]any{"name": "literal", "userConfig": map[string]any{"value": map[string]any{"type": "string", "title": "Value", "description": "Value", "required": true}}, "hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Write", "hooks": []any{
		map[string]any{"type": "command", "command": "sh", "args": []string{"-c", `printf '%s\n' "$1" "$2" "$3" "$CLAUDE_PLUGIN_OPTION_VALUE" > "$4"`, "literal", "${user_config.value}", "${CLAUDE_PLUGIN_ROOT}", "${CLAUDE_PLUGIN_DATA}", "${CLAUDE_PROJECT_DIR}/exec-observed"}},
		map[string]any{"type": "command", "command": `printf '%s\n' "$CLAUDE_PLUGIN_ROOT" "$CLAUDE_PLUGIN_DATA" "$CLAUDE_PLUGIN_OPTION_VALUE" > shell-observed`},
	}}}}}
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"pluginConfigs": map[string]any{"literal@inline": map[string]any{"options": map[string]any{"value": value}}}})
	m.ConfigureClaudePlugins([]string{plugin})
	results := m.RunPreTool(t.Context(), "write", nil)
	if len(results) != 2 {
		t.Fatalf("plugin options=%+v", results)
	}
	for _, result := range results {
		if result.Error != nil {
			t.Fatal(result.Error)
		}
	}
	runtime := results[0].Hook.claudePlugin
	for name, want := range map[string]string{"exec-observed": strings.Join([]string{value, filepath.ToSlash(runtime.Root), filepath.ToSlash(runtime.Data), value}, "\n") + "\n", "shell-observed": strings.Join([]string{filepath.ToSlash(runtime.Root), filepath.ToSlash(runtime.Data), value}, "\n") + "\n"} {
		assertManagedMarker(t, root, name, want)
	}
	if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
		t.Fatal("option was reinterpreted as shell code")
	}
	if info, err := os.Stat(runtime.Data); err != nil || !info.IsDir() {
		t.Fatal("persistent plugin data directory unavailable")
	}
	// A normal settings handler does not inherit a plugin's process environment.
	t.Setenv("CLAUDE_PLUGIN_ROOT", "forged")
	t.Setenv("CLAUDE_PLUGIN_OPTION_VALUE", "forged")
	m.ConfigureClaudePlugins(nil)
	m.SetEnabled(true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), settingsTestEvent("PreToolUse", "Write", `printf '%s' "${CLAUDE_PLUGIN_ROOT-unset}/${CLAUDE_PLUGIN_OPTION_VALUE-unset}"`))
	results = m.RunPreTool(t.Context(), "write", nil)
	if len(results) != 1 || results[0].Output != "unset/unset" {
		t.Fatalf("plugin environment leaked: %+v", results)
	}
}

func TestPluginContainmentAndUnsupportedConfigurationFailClosed(t *testing.T) {
	for _, kind := range []string{"escape", "symlink", "missing", "wrapper", "shell-option", "unsupported-event", "sensitive-option", "missing-required-option"} {
		t.Run(kind, func(t *testing.T) {
			m, root, _, _ := managedTestManager(t, false, false)
			plugin := pluginTestRoot(t, "guard", "")
			manifest := map[string]any{"name": "guard"}
			switch kind {
			case "escape":
				manifest["hooks"] = "./../outside.json"
			case "missing":
				manifest["hooks"] = "./missing.json"
			case "symlink":
				outside := filepath.Join(t.TempDir(), "outside.json")
				writeClaudeSettingsTestFile(t, outside, settingsTestEvent("PreToolUse", "Write", "touch forbidden"))
				if err := os.Symlink(outside, filepath.Join(plugin, "linked.json")); err != nil {
					t.Fatal(err)
				}
				manifest["hooks"] = "./linked.json"
			case "wrapper":
				writeClaudeSettingsTestFile(t, filepath.Join(plugin, "bad.json"), settingsTestEvent("PreToolUse", "Write", "touch forbidden")["hooks"])
				manifest["hooks"] = "./bad.json"
			case "shell-option":
				manifest["hooks"] = settingsTestEvent("PreToolUse", "Write", "echo ${user_config.value}")["hooks"]
				manifest["userConfig"] = map[string]any{"value": map[string]any{"type": "string", "default": "x"}}
			case "unsupported-event":
				manifest["hooks"] = map[string]any{"UnsupportedEvent": []any{}}
			case "sensitive-option":
				manifest["userConfig"] = map[string]any{"secret": map[string]any{"type": "string", "sensitive": true, "required": true}}
				manifest["hooks"] = settingsTestEvent("PreToolUse", "Write", "touch forbidden")["hooks"]
			case "missing-required-option":
				manifest["userConfig"] = map[string]any{"value": map[string]any{"type": "string", "required": true}}
				manifest["hooks"] = settingsTestEvent("PreToolUse", "Write", "touch forbidden")["hooks"]
			}
			writeClaudeSettingsTestFile(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest)
			m.ConfigureClaudePlugins([]string{plugin})
			if err := m.CheckReady(); err == nil {
				t.Fatal("unavailable required plugin configuration ignored")
			}
			if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].StopReason == "" {
				t.Fatalf("failed plugin check silently removed: %+v", results)
			}
			assertManagedMarker(t, root, "forbidden", "")
		})
	}
}

func TestPluginIdenticalCommandsInDifferentRootsStaySeparateAndSideloadPolicyApplies(t *testing.T) {
	m, root, _, managed := managedTestManager(t, false, false)
	first := pluginTestRoot(t, "first", "printf x >> runs")
	second := pluginTestRoot(t, "second", "printf x >> runs")
	m.ConfigureClaudePlugins([]string{first, second})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 2 {
		t.Fatalf("distinct plugin environment was deduplicated: %+v", results)
	}
	assertManagedMarker(t, root, "runs", "xx")
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"disableSideloadFlags": true})
	if err := m.CheckReady(); err == nil || !strings.Contains(err.Error(), "disableSideloadFlags") {
		t.Fatalf("managed sideload policy bypassed: %v", err)
	}
}

func TestPluginSkillsDirectoryContainerAndDisabledBrokenInstall(t *testing.T) {
	m, root, user, _ := managedTestManager(t, true, true)
	source := pluginTestRoot(t, "personal", "printf x >> personal")
	parent := filepath.Join(user, "skills")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(source, filepath.Join(parent, "personal")); err != nil {
		t.Fatal(err)
	}
	pluginTestRegistry(t, user, map[string][]claudePluginInstall{"broken@org": {{Scope: "user", InstallPath: "/unavailable/plugin"}}})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]any{"broken@org": false}})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("skills-directory discovery=%+v", results)
	}
	assertManagedMarker(t, root, "personal", "x")
	// Explicit container flags find each child manifest, including comma paths.
	container := t.TempDir()
	child := pluginTestRoot(t, "container", "printf x >> container")
	if err := os.Rename(child, filepath.Join(container, "a, b")); err != nil {
		t.Fatal(err)
	}
	m.SetEnabled(false)
	m.ConfigureClaudePlugins([]string{container})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("container loading=%+v", results)
	}
	assertManagedMarker(t, root, "container", "x")
}

func TestPluginRegistryInvalidVersionAndMissingEnabledSource(t *testing.T) {
	for _, registry := range []any{map[string]any{"version": 1, "plugins": map[string]any{}}, map[string]any{"version": 2, "plugins": []any{}}, map[string]any{"version": 2, "plugins": map[string]any{}}} {
		t.Run("registry", func(t *testing.T) {
			m, _, user, _ := managedTestManager(t, true, false)
			writeClaudeSettingsTestFile(t, filepath.Join(user, "plugins", "installed_plugins.json"), registry)
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"enabledPlugins": map[string]any{"missing@org": true}})
			if err := m.CheckReady(); err == nil {
				data, _ := json.Marshal(registry)
				t.Fatalf("bad registry/unavailable plugin accepted: %s", data)
			}
		})
	}
}

func TestManagedPluginOptionsNullRestoresUserValuesAndMissingForcedSourceFails(t *testing.T) {
	m, _, user, managed := managedTestManager(t, false, false)
	plugin := pluginTestRoot(t, "guard", `printf '%s' "$CLAUDE_PLUGIN_OPTION_VALUE"`)
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "guard", "userConfig": map[string]any{"value": map[string]any{"type": "string", "default": "default"}}})
	pluginTestRegistry(t, user, map[string][]claudePluginInstall{"guard@org": {{Scope: "user", InstallPath: plugin}}})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"pluginConfigs": map[string]any{"guard@org": map[string]any{"options": map[string]any{"value": "user"}}}})
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.json"), map[string]any{"allowManagedHooksOnly": true, "enabledPlugins": map[string]any{"guard@org": true}, "pluginConfigs": map[string]any{"guard@org": map[string]any{"options": map[string]any{"value": "managed"}}}})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Output != "managed" {
		t.Fatalf("managed options precedence=%+v", results)
	}
	writeClaudeSettingsTestFile(t, filepath.Join(managed, "managed-settings.d", "final.json"), map[string]any{"pluginConfigs": map[string]any{"guard@org": map[string]any{"options": map[string]any{"value": nil}}}})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Output != "user" {
		t.Fatalf("managed null erased lower user value=%+v", results)
	}
	m2, _, _, managed2 := managedTestManager(t, false, false)
	writeClaudeSettingsTestFile(t, filepath.Join(managed2, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]any{"missing@inline": true}})
	if err := m2.CheckReady(); err == nil {
		t.Fatal("missing required managed inline plugin silently ignored")
	}
}

func TestPluginOptionSchemaRejectsUnknownControlsAndAcceptsUnicodeNames(t *testing.T) {
	m, root, _, _ := managedTestManager(t, false, false)
	plugin := pluginTestRoot(t, "проверка", "printf unicode >> marker")
	m.ConfigureClaudePlugins([]string{plugin})
	if results := m.RunPreTool(t.Context(), "write", nil); len(results) != 1 || results[0].Error != nil {
		t.Fatalf("valid Unicode plugin name rejected: %+v", results)
	}
	assertManagedMarker(t, root, "marker", "unicode")
	manifest := map[string]any{"name": "проверка", "userConfig": map[string]any{"value": map[string]any{"type": "string", "default": "x", "sensitve": true}}}
	writeClaudeSettingsTestFile(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest)
	if err := m.CheckReady(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("misspelled option privacy control ignored: %v", err)
	}
}
