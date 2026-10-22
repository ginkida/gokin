package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/pluginsecrets"
)

func withdrawCatalog(t *testing.T, market string, force bool, extra map[string]any) {
	t.Helper()
	catalog := map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{}, "forceRemoveDeletedPlugins": force}
	for key, value := range extra {
		catalog[key] = value
	}
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), catalog)
}

func TestCatalogWithdrawalRemovesScopesSecretsDataAndKeepsLiveFiles(t *testing.T) {
	m, market, _, _ := secretFixture(t)
	for _, scope := range []string{"user", "project", "local"} {
		if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: scope, Config: map[string]string{"token": "private-fixture"}}); err != nil {
			t.Fatal(err)
		}
	}
	rows, _ := m.List()
	oldPath := rows[0].InstallPath
	dataDir := filepath.Join(m.pluginDir, "data", hooks.ClaudePluginStorageKey("alpha@acme"))
	writeTestJSON(t, filepath.Join(dataDir, "state.json"), map[string]string{"owned": "data"})
	withdrawCatalog(t, market, true, nil)
	result, err := m.UpdateMarketplace(t.Context(), "acme", nil)
	if err != nil || len(result.Pruned) != 3 {
		t.Fatalf("result=%+v %v", result, err)
	}
	_, installed, err := m.installs()
	if err != nil || len(installed) != 0 {
		t.Fatalf("installed=%+v %v", installed, err)
	}
	for _, scope := range []string{"user", "project", "local"} {
		_, settings, err := m.settings(scope)
		if err != nil || bytes.Contains(settings["enabledPlugins"], []byte("alpha@acme")) || bytes.Contains(settings["pluginConfigs"], []byte("alpha@acme")) {
			t.Fatalf("retained %s config: %v", scope, err)
		}
	}
	fields, err := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	if err != nil {
		t.Fatal(err)
	}
	values, err := pluginsecrets.Read(m.opts.UserDir, "alpha@acme", fields, m.opts.SecretKeyring)
	if err != nil || len(values) != 0 {
		t.Fatalf("retained secret: %v", err)
	}
	if _, err := os.Stat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("data remains: %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("live snapshot removed: %v", err)
	}
	rows, err = m.List()
	if err != nil || len(rows) != 3 {
		t.Fatalf("flagged=%+v %v", rows, err)
	}
	for _, row := range rows {
		if row.Status != "removed-from-marketplace" || row.Enabled {
			t.Fatalf("flag=%+v", row)
		}
	}
	// A second lifecycle is idempotent and never recreates the withdrawn plugin.
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Removed)+len(report.Installed)+len(report.Errors) != 0 {
		t.Fatalf("repeat=%+v %v", report, err)
	}
}

func TestCatalogWithdrawalStartupControlsAndScopeOwnership(t *testing.T) {
	for _, mode := range []string{"no-force", "cached-startup", "untrusted", "other-workspace", "managed", "renamed", "null-rename"} {
		t.Run(mode, func(t *testing.T) {
			m, market, _ := fixture(t)
			if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
				t.Fatal(err)
			}
			scope := "user"
			if mode == "untrusted" || mode == "other-workspace" {
				scope = "project"
			}
			if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: scope}); err != nil {
				t.Fatal(err)
			}
			extra := map[string]any{}
			if mode == "managed" {
				writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"alpha@acme": true}})
			}
			if mode == "renamed" {
				extra["renames"] = map[string]any{"alpha": "beta"}
				extra["plugins"] = []any{map[string]any{"name": "beta", "source": "./plugins/alpha"}}
			}
			if mode == "null-rename" {
				extra["renames"] = map[string]any{"alpha": nil}
			}
			if mode == "untrusted" {
				m.opts.Trusted = false
			}
			if mode == "other-workspace" {
				m.opts.WorkDir = t.TempDir()
			}
			withdrawCatalog(t, market, mode != "no-force", extra)
			t.Setenv("DISABLE_AUTOUPDATER", "1")
			report, err := m.SyncDeclared(t.Context())
			if err != nil || len(report.Errors) != 0 {
				t.Fatalf("report=%+v %v", report, err)
			}
			_, installs, _ := m.installs()
			if mode == "cached-startup" {
				if len(installs) != 0 || len(report.Removed) != 1 {
					t.Fatalf("not removed: %+v %+v", installs, report)
				}
			} else if len(installs["alpha@acme"]) != 1 {
				t.Fatalf("removed protected record: %+v", report)
			}
		})
	}
}

func TestCatalogWithdrawalPreservesOtherProjectOptions(t *testing.T) {
	m, market, _, _ := secretFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Config: map[string]string{"token": "shared"}}); err != nil {
		t.Fatal(err)
	}
	otherOpts := m.opts
	otherOpts.WorkDir = t.TempDir()
	other, err := NewManager(otherOpts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Install(t.Context(), "alpha", InstallOptions{Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	withdrawCatalog(t, market, true, nil)
	if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	_, installs, _ := m.installs()
	if len(installs["alpha@acme"]) != 1 {
		t.Fatal("other workspace erased")
	}
	fields, _ := m.readObject(pluginsecrets.Path(m.opts.UserDir))
	values, err := pluginsecrets.Read(m.opts.UserDir, "alpha@acme", fields, m.opts.SecretKeyring)
	if err != nil || values["token"] != "shared" {
		t.Fatalf("other workspace lost options: %v", err)
	}
	report, err := other.ReconcileCatalogRemovals(t.Context())
	if err != nil || len(report.Removed) != 1 {
		t.Fatalf("other=%+v %v", report, err)
	}
}

func TestCatalogWithdrawalDeactivatesConsumersAndPreservesIndependentPlugin(t *testing.T) {
	m, market, _ := fixture(t)
	for _, name := range []string{"bundle", "independent"} {
		manifest := map[string]any{"name": name}
		if name == "bundle" {
			manifest["dependencies"] = []string{"alpha"}
		}
		writeTestJSON(t, filepath.Join(market, "plugins", name, ".claude-plugin", "plugin.json"), manifest)
	}
	entries := []any{map[string]any{"name": "alpha", "source": "./plugins/alpha"}, map[string]any{"name": "bundle", "source": "./plugins/bundle"}, map[string]any{"name": "independent", "source": "./plugins/independent"}}
	withdrawCatalog(t, market, false, map[string]any{"plugins": entries})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bundle", "independent"} {
		if _, err := m.Install(t.Context(), name, InstallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	withdrawCatalog(t, market, true, map[string]any{"plugins": entries[1:]})
	if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	rows, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "bundle@acme" && (row.Enabled || len(row.Errors) == 0) {
			t.Fatalf("consumer=%+v", row)
		}
		if row.ID == "independent@acme" && !row.Enabled {
			t.Fatalf("independent=%+v", row)
		}
	}
	selected := runtimePlugins(t, m)
	if len(selected) != 1 || selected[0].ID() != "independent@acme" {
		t.Fatalf("runtime=%+v", selected)
	}
}

func TestCatalogWithdrawalReinstallClearsFlagAndCanceledUpdateKeepsState(t *testing.T) {
	m, market, _ := fixture(t)
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	withdrawCatalog(t, market, true, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.UpdateMarketplace(ctx, "acme", nil); err == nil {
		t.Fatal("canceled update succeeded")
	}
	after, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("canceled update changed registry")
	}
	if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err != nil {
		t.Fatal(err)
	}
	withdrawCatalog(t, market, true, map[string]any{"plugins": []any{map[string]any{"name": "alpha", "source": "./plugins/alpha"}}})
	if _, err := m.Install(t.Context(), "alpha@acme", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, err := m.List()
	if err != nil || len(rows) != 1 || !rows[0].Enabled || rows[0].Status != "" {
		t.Fatalf("reinstall=%+v %v", rows, err)
	}
}

func TestCatalogRenameValidationForRemovalExemptions(t *testing.T) {
	for _, raw := range []string{`{"alpha":"beta","beta":"alpha"}`, `{"alpha":"absent"}`, `{"alpha":"../bad"}`, `{"beta":null}`} {
		data := []byte(`{"name":"acme","owner":{"name":"Test"},"plugins":[{"name":"beta","source":"./beta"}],"renames":` + raw + `}`)
		if _, err := ParseCatalog(data); err == nil {
			t.Fatalf("accepted invalid map: %s", raw)
		}
	}
	data := []byte(`{"name":"acme","owner":{"name":"Test"},"plugins":[{"name":"new","source":"./new"}],"renames":{"old":"middle","middle":"new","gone":null}}`)
	catalog, err := ParseCatalog(data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(catalog)
	if !strings.Contains(string(encoded), `"renames"`) {
		t.Fatal("lost rename history")
	}
}

func TestCatalogWithdrawalAutoUpdateReportsRemoval(t *testing.T) {
	m, market, _ := startupFixture(t)
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", true); err != nil {
		t.Fatal(err)
	}
	withdrawCatalog(t, market, true, nil)
	report, err := m.AutoUpdate(t.Context())
	if err != nil || len(report.Removed) != 1 || len(report.Errors)+len(report.Installed)+len(report.Updated) != 0 {
		t.Fatalf("auto=%+v %v", report, err)
	}
}

func TestCatalogWithdrawalInvalidSecretStateRollsBackWholePublication(t *testing.T) {
	m, market, _ := fixture(t)
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, pluginsecrets.Path(m.opts.UserDir), map[string]any{"version": 99, "plugins": map[string]string{}})
	paths := []string{filepath.Join(m.pluginDir, "installed_plugins.json"), filepath.Join(m.pluginDir, "known_marketplaces.json"), filepath.Join(m.opts.UserDir, "settings.json"), pluginsecrets.Path(m.opts.UserDir)}
	before := map[string][]byte{}
	for _, path := range paths {
		before[path], _ = os.ReadFile(path)
	}
	withdrawCatalog(t, market, true, nil)
	if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err == nil {
		t.Fatal("accepted invalid credential state")
	}
	for _, path := range paths {
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before[path], after) {
			t.Fatalf("partial commit at %s", path)
		}
	}
}

func TestCatalogWithdrawalCLIFlagsAndUnrelatedSettings(t *testing.T) {
	m, market, _ := fixture(t)
	if _, err := runPluginCLI(t, m, "marketplace", "add", market); err != nil {
		t.Fatal(err)
	}
	if _, err := runPluginCLI(t, m, "install", "alpha"); err != nil {
		t.Fatal(err)
	}
	withdrawCatalog(t, market, true, nil)
	output, err := runPluginCLI(t, m, "marketplace", "update", "acme")
	if err != nil || !strings.Contains(output, "Removed from marketplace: alpha@acme [user]") {
		t.Fatalf("update=%q %v", output, err)
	}
	output, err = runPluginCLI(t, m, "list")
	if err != nil || !strings.Contains(output, "Flagged: Removed from marketplace") {
		t.Fatalf("list=%q %v", output, err)
	}
	_, settings, _ := m.settings("user")
	if string(settings["theme"]) != `"existing"` || !bytes.Contains(settings["pluginConfigs"], []byte("other@elsewhere")) {
		t.Fatal("withdrawal erased unrelated settings")
	}
}
