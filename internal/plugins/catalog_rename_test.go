package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/pluginsecrets"
)

func renameCatalog(t *testing.T, market string, renames map[string]any, entries []any) {
	t.Helper()
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": entries, "renames": renames})
}

func TestCatalogRenameMigratesScopesOptionsSecretsAndChains(t *testing.T) {
	m, market, producer, _ := secretFixture(t)
	for _, scope := range []string{"user", "project", "local"} {
		if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: scope, Config: map[string]string{"token": "private-renamed-value", "value": "old-value"}}); err != nil {
			t.Fatal(err)
		}
	}
	// Preserve extra options saved by another consumer in each writable scope.
	for _, scope := range []string{"project", "local"} {
		path, fields, _ := m.settings(scope)
		fields["pluginConfigs"], _ = json.Marshal(map[string]any{"alpha@acme": map[string]any{"options": map[string]string{"value": scope}}})
		writeTestJSON(t, path, fields)
	}
	oldRows, _ := m.List()
	oldPath := oldRows[0].InstallPath
	manifest, err := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest["name"] = json.RawMessage(`"gamma"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta", "beta": "gamma"}, []any{map[string]any{"name": "gamma", "source": "./plugins/alpha"}})
	result, err := m.UpdateMarketplace(t.Context(), "acme", nil)
	if err != nil || len(result.Warnings) != 3 {
		t.Fatalf("migrate=%+v %v", result, err)
	}
	for _, scope := range []string{"user", "project", "local"} {
		_, fields, _ := m.settings(scope)
		if bytes.Contains(fields["enabledPlugins"], []byte("alpha@acme")) || !bytes.Contains(fields["enabledPlugins"], []byte("gamma@acme")) || bytes.Contains(fields["pluginConfigs"], []byte("alpha@acme")) {
			t.Fatalf("scope not migrated: %s %+v", scope, fields)
		}
	}
	fields, _ := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	values, err := pluginsecrets.Read(m.opts.UserDir, "gamma@acme", fields, m.opts.SecretKeyring)
	if err != nil || values["token"] != "private-renamed-value" {
		t.Fatalf("renamed secret unavailable: %v", err)
	}
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte("private-renamed-value")) {
		t.Fatal("secret in migration output")
	}
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Installed) != 3 || len(report.Errors) != 0 {
		t.Fatalf("provision=%+v %v", report, err)
	}
	selected, err := secretAuthority(m).ClaudePlugins()
	if err != nil || len(selected) != 1 || selected[0].ID() != "gamma@acme" {
		t.Fatalf("selection=%+v %v", selected, err)
	}
	env, err := selected[0].ProcessEnvironment(m.opts.WorkDir)
	if err != nil || env["CLAUDE_PLUGIN_OPTION_TOKEN"] != "private-renamed-value" || env["CLAUDE_PLUGIN_OPTION_VALUE"] != "old-value" {
		t.Fatalf("migrated environment: %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("migration deleted live snapshot", err)
	}
	before, _ := os.ReadFile(pluginsecrets.Path(m.opts.UserDir))
	if report, err = m.SyncDeclared(t.Context()); err != nil || len(report.Installed)+len(report.Errors)+len(report.Renamed) != 0 {
		t.Fatalf("repeat=%+v %v", report, err)
	}
	after, _ := os.ReadFile(pluginsecrets.Path(m.opts.UserDir))
	if !bytes.Equal(before, after) {
		t.Fatal("repeat migration rewrote ciphertext")
	}
	rows, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "alpha@acme" && (row.Status != "renamed" || row.RenamedTo != "gamma@acme" || row.Enabled) {
			t.Fatalf("old row=%+v", row)
		}
	}
}

func TestCatalogRenameDestinationConflictsAndNullRemoval(t *testing.T) {
	m, market, _ := fixture(t)
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	path, fields, _ := m.settings("user")
	fields["enabledPlugins"], _ = json.Marshal(map[string]bool{"alpha@acme": true, "beta@acme": false, "gone@acme": true})
	fields["pluginConfigs"], _ = json.Marshal(map[string]any{"alpha@acme": map[string]any{"options": map[string]any{"old": "retained", "shared": "old"}}, "beta@acme": map[string]any{"options": map[string]any{"shared": "new"}}, "gone@acme": map[string]any{"options": map[string]string{"drop": "yes"}}})
	writeTestJSON(t, path, fields)
	renameCatalog(t, market, map[string]any{"alpha": "beta", "gone": nil}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	report, err := m.ReconcileCatalogRemovals(t.Context())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("migrate=%+v %v", report, err)
	}
	_, fields, _ = m.settings("user")
	enabled, _ := enabledEntries(fields)
	if enabled["beta@acme"] || len(enabled) != 1 {
		t.Fatalf("destination disable lost: %+v", enabled)
	}
	configs, _ := renameObject(fields["pluginConfigs"])
	entry, _ := renameObject(configs["beta@acme"])
	options, _ := renameObject(entry["options"])
	if string(options["shared"]) != `"new"` || string(options["old"]) != `"retained"` || len(configs) != 1 {
		t.Fatalf("config merge=%s", fields["pluginConfigs"])
	}
}

func TestCatalogRenameManagedAliasesStayReadOnlyAndRequired(t *testing.T) {
	m, market, producer := fixture(t)
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	managedPath := filepath.Join(m.opts.ManagedDir, "managed-settings.json")
	writeTestJSON(t, managedPath, map[string]any{"enabledPlugins": map[string]bool{"alpha@acme": true}, "pluginConfigs": map[string]any{"alpha@acme": map[string]any{"options": map[string]string{"value": "managed-value"}}}})
	before, _ := os.ReadFile(managedPath)
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("sync=%+v %v", report, err)
	}
	selected := runtimePlugins(t, m)
	if len(selected) != 1 || selected[0].ID() != "beta@acme" {
		t.Fatalf("managed selection=%+v", selected)
	}
	env, err := selected[0].ProcessEnvironment(m.opts.WorkDir)
	if err != nil || env["CLAUDE_PLUGIN_OPTION_VALUE"] != "managed-value" {
		t.Fatalf("managed options: %v %+v", err, env)
	}
	if _, err := m.SetEnabled(t.Context(), "beta", "user", false); err == nil {
		t.Fatal("renamed managed plugin disabled")
	}
	if _, err := m.Uninstall(t.Context(), "beta", RemoveOptions{}); err == nil {
		t.Fatal("renamed managed plugin removed")
	}
	after, _ := os.ReadFile(managedPath)
	if !bytes.Equal(before, after) {
		t.Fatal("managed settings rewritten")
	}
}

func TestCatalogRenameLockedSecretsRollBackWholePublication(t *testing.T) {
	m, market, _, store := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "hidden"}}); err != nil {
		t.Fatal(err)
	}
	paths := []string{filepath.Join(m.pluginDir, "installed_plugins.json"), filepath.Join(m.pluginDir, "known_marketplaces.json"), filepath.Join(m.opts.UserDir, "settings.json"), pluginsecrets.Path(m.opts.UserDir)}
	before := map[string][]byte{}
	for _, path := range paths {
		before[path], _ = os.ReadFile(path)
	}
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	store.err = errors.New("locked")
	if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err == nil {
		t.Fatal("locked secret rename succeeded")
	}
	for _, path := range paths {
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before[path], after) {
			t.Fatalf("partial migration: %s", path)
		}
	}
}

func TestCatalogRenameGitRequiresExplicitNewInstallation(t *testing.T) {
	m, market, producer := startupFixture(t)
	gitTest(t, market, "init", "-q")
	gitTest(t, market, "add", ".")
	gitTest(t, market, "commit", "--no-gpg-sign", "-q", "-m", "initial")
	if _, err := m.AddMarketplace(t.Context(), "file://"+filepath.ToSlash(market), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	gitTest(t, market, "add", ".")
	gitTest(t, market, "commit", "--no-gpg-sign", "-q", "-m", "rename")
	if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Installed) != 0 || len(report.Errors) != 0 || len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0], "install it explicitly") {
		t.Fatalf("Git rename=%+v %v", report, err)
	}
	authority := hooks.NewManager(false, m.opts.WorkDir)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
	authority.ConfigureClaudePlugins(nil)
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 0 || len(authority.ClaudeDependencyWarnings()) != 1 {
		t.Fatalf("uncached=%+v %v warnings=%+v", selected, err, authority.ClaudeDependencyWarnings())
	}
	if _, err := m.Install(t.Context(), "beta@acme", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if selected := runtimePlugins(t, m); len(selected) != 1 || selected[0].ID() != "beta@acme" {
		t.Fatalf("installed=%+v", selected)
	}
}

func TestCatalogRenamePinnedSessionKeepsVersionAndActivation(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled", "removed"} {
		t.Run(mode, func(t *testing.T) {
			m, market, producer, _ := secretFixture(t)
			manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
			var schema map[string]map[string]any
			json.Unmarshal(manifest["userConfig"], &schema)
			schema["value"] = map[string]any{"type": "string", "required": true}
			manifest["userConfig"], _ = json.Marshal(schema)
			writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
			if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "kept-private", "value": "kept-value"}}); err != nil {
				t.Fatal(err)
			}
			if mode == "disabled" {
				if _, err := m.SetEnabled(t.Context(), "alpha", "user", false); err != nil {
					t.Fatal(err)
				}
			}
			authority := secretAuthority(m)
			if err := authority.PinClaudePluginInstalls(); err != nil {
				t.Fatal(err)
			}
			old, err := authority.ClaudePlugins()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "removed" {
				renameCatalog(t, market, map[string]any{"alpha": nil}, []any{})
			} else {
				manifest["name"] = json.RawMessage(`"beta"`)
				writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
				renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
			}
			report, err := m.SyncDeclared(t.Context())
			if err != nil || len(report.Errors) != 0 {
				t.Fatalf("migration=%+v %v", report, err)
			}
			pinned, err := authority.ClaudePlugins()
			if err != nil {
				t.Fatal("migration broke active session", err)
			}
			if mode == "disabled" {
				if len(pinned) != 0 {
					t.Fatal("rename enabled a disabled pinned plugin")
				}
			} else {
				if len(pinned) != 1 || pinned[0].ID() != "alpha@acme" || pinned[0].Root() != old[0].Root() {
					t.Fatalf("pin=%+v", pinned)
				}
				if err := old[0].CheckSelected(authority); err != nil {
					t.Fatal("unchanged runtime lost authority", err)
				}
				env, err := pinned[0].ProcessEnvironment(m.opts.WorkDir)
				if err != nil || env["CLAUDE_PLUGIN_OPTION_VALUE"] != "kept-value" || env["CLAUDE_PLUGIN_OPTION_TOKEN"] != "kept-private" {
					t.Fatalf("pinned options: %v", err)
				}
			}
			if err := authority.PinClaudePluginInstalls(); err != nil {
				t.Fatal(err)
			}
			current, err := authority.ClaudePlugins()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "enabled" {
				if len(current) != 1 || current[0].ID() != "beta@acme" {
					t.Fatalf("reload=%+v", current)
				}
			} else if len(current) != 0 {
				t.Fatalf("inactive plugin reloaded=%+v", current)
			}
		})
	}
}

func TestCatalogRenameDoesNotRewriteOtherWorkspace(t *testing.T) {
	m, market, producer := fixture(t)
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	foreignOpts := m.opts
	foreignOpts.WorkDir = t.TempDir()
	other, err := NewManager(foreignOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Install(t.Context(), "alpha", InstallOptions{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	foreignPath, _ := other.settingsPath("project")
	before, _ := os.ReadFile(foreignPath)
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	if report, err := m.SyncDeclared(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("current=%+v %v", report, err)
	}
	after, _ := os.ReadFile(foreignPath)
	if !bytes.Equal(before, after) {
		t.Fatal("migration rewrote foreign workspace")
	}
	if report, err := other.SyncDeclared(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("foreign startup=%+v %v", report, err)
	}
	_, fields, _ := other.settings("project")
	enabled, _ := enabledEntries(fields)
	if !enabled["beta@acme"] || enabled["alpha@acme"] {
		t.Fatalf("foreign scope=%+v", enabled)
	}
}

func TestCatalogRenameDependencyConsumersFollowCanonicalTarget(t *testing.T) {
	m, market, producer := startupFixture(t)
	consumer := filepath.Join(market, "plugins", "consumer")
	writeTestJSON(t, filepath.Join(consumer, ".claude-plugin", "plugin.json"), map[string]any{"name": "consumer", "dependencies": []string{"alpha"}})
	renameCatalog(t, market, nil, []any{map[string]any{"name": "alpha", "source": "./plugins/alpha"}, map[string]any{"name": "consumer", "source": "./plugins/consumer"}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "consumer", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}, map[string]any{"name": "consumer", "source": "./plugins/consumer"}})
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("sync=%+v %v", report, err)
	}
	selected := runtimePlugins(t, m)
	if len(selected) != 2 {
		t.Fatalf("consumers=%+v", selected)
	}
	if _, err := m.Uninstall(t.Context(), "beta", RemoveOptions{}); err == nil {
		t.Fatal("renamed dependency protection lost")
	}
	if _, err := m.Uninstall(t.Context(), "consumer", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	pruned, err := m.Prune(t.Context(), PruneOptions{DryRun: true})
	if err != nil || len(pruned.Pruned) != 1 || pruned.Pruned[0] != "beta@acme" {
		t.Fatalf("renamed automatic dependency became manual: %+v %v", pruned, err)
	}
}

func TestCatalogRenameLaterChainPreservesCurrentSecret(t *testing.T) {
	m, market, producer, _ := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "old-token"}}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	if report, err := m.SyncDeclared(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("first=%+v %v", report, err)
	}
	if _, err := m.Configure(t.Context(), "beta", map[string]string{"token": "current-token"}); err != nil {
		t.Fatal(err)
	}
	manifest["name"] = json.RawMessage(`"gamma"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta", "beta": "gamma"}, []any{map[string]any{"name": "gamma", "source": "./plugins/alpha"}})
	if report, err := m.SyncDeclared(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("next=%+v %v", report, err)
	}
	fields, _ := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	values, err := pluginsecrets.Read(m.opts.UserDir, "gamma@acme", fields, m.opts.SecretKeyring)
	if err != nil || values["token"] != "current-token" {
		t.Fatalf("retired version overwrote current credential: %v", err)
	}
}

func TestCatalogRenameManagedDocumentPrecedence(t *testing.T) {
	m, market, producer := fixture(t)
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"beta@acme": false}})
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.d", "20-required.json"), map[string]any{"enabledPlugins": map[string]bool{"alpha@acme": true}})
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("sync=%+v %v", report, err)
	}
	selected := runtimePlugins(t, m)
	if len(selected) != 1 || selected[0].ID() != "beta@acme" {
		t.Fatalf("later managed alias lost=%+v", selected)
	}
	if _, err := m.Uninstall(t.Context(), "beta", RemoveOptions{}); err == nil {
		t.Fatal("later managed requirement lost during lifecycle")
	}
}

func TestCatalogRenameDoesNotMoveSecretsToRebasedMarketplace(t *testing.T) {
	m, _, _, _ := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "private-before-source-change"}}); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	renameCatalog(t, other, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./beta"}})
	path, settings, _ := m.settings("user")
	settings["extraKnownMarketplaces"], _ = json.Marshal(map[string]any{"acme": map[string]any{"source": Source{Kind: "directory", Path: other}}})
	writeTestJSON(t, path, settings)
	before, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	report, err := m.SyncDeclared(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Errors) == 0 || !strings.Contains(strings.Join(report.Errors, " "), "changed marketplace source") {
		t.Fatalf("unsafe credential migration not refused=%+v", report)
	}
	after, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed source rebase changed installed registry")
	}
	fields, _ := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	values, err := pluginsecrets.Read(m.opts.UserDir, "beta@acme", fields, m.opts.SecretKeyring)
	if err != nil || len(values) != 0 {
		t.Fatal("credential copied to replacement source")
	}
}

func TestCatalogRenameRetiredSettingsRequireCurrentIdentifier(t *testing.T) {
	m, market, producer, _ := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "current-secret"}}); err != nil {
		t.Fatal(err)
	}
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	if report, err := m.SyncDeclared(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("sync=%+v %v", report, err)
	}
	if _, err := m.Configure(t.Context(), "alpha", map[string]string{"token": "stale-secret"}); err == nil || !strings.Contains(err.Error(), "renamed to beta@acme") {
		t.Fatalf("stale configure=%v", err)
	}
	if _, err := m.SetEnabled(t.Context(), "alpha", "user", false); err == nil {
		t.Fatal("stale disable silently accepted")
	}
	selected, err := secretAuthority(m).ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("current selection=%+v %v", selected, err)
	}
	env, err := selected[0].ProcessEnvironment(m.opts.WorkDir)
	if err != nil || env["CLAUDE_PLUGIN_OPTION_TOKEN"] != "current-secret" {
		t.Fatal("stale configuration changed active secret")
	}
}

func TestCatalogRenameOrphanedSecretNeedsVerifiedInstallation(t *testing.T) {
	m, market, _, _ := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "orphaned-private-value"}}); err != nil {
		t.Fatal(err)
	}
	doc, _, _ := m.installs()
	doc["plugins"] = json.RawMessage(`{}`)
	writeTestJSON(t, filepath.Join(m.pluginDir, "installed_plugins.json"), doc)
	before, _ := os.ReadFile(pluginsecrets.Path(m.opts.UserDir))
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err == nil || !strings.Contains(err.Error(), "provenance") {
		t.Fatalf("orphaned secret migrated without provenance: %v", err)
	}
	after, _ := os.ReadFile(pluginsecrets.Path(m.opts.UserDir))
	if !bytes.Equal(before, after) {
		t.Fatal("failed migration changed orphaned ciphertext")
	}
}

func TestCatalogRenameValidateUsesManagedAliasOptions(t *testing.T) {
	m, market, producer := fixture(t)
	manifest, _ := readObject(filepath.Join(producer, ".claude-plugin", "plugin.json"))
	manifest["userConfig"], _ = json.Marshal(map[string]any{"value": map[string]any{"type": "string", "required": true}})
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"pluginConfigs": map[string]any{"alpha@acme": map[string]any{"options": map[string]string{"value": "managed-only"}}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	manifest["name"] = json.RawMessage(`"beta"`)
	writeTestJSON(t, filepath.Join(producer, ".claude-plugin", "plugin.json"), manifest)
	renameCatalog(t, market, map[string]any{"alpha": "beta"}, []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}})
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Errors) != 0 {
		t.Fatalf("sync=%+v %v", report, err)
	}
	rows, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "beta@acme" {
			if _, err := m.Validate(row.InstallPath); err != nil {
				t.Fatal("valid renamed installation failed validation", err)
			}
			return
		}
	}
	t.Fatal("renamed installation missing")
}
