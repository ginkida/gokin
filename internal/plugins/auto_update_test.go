package plugins

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestDeclaredSyncDefersAutomaticUpdates(t *testing.T) {
	m, market, plugin := startupFixture(t)
	on := true
	startupDeclaration(t, m, "user", market, &on)
	if report, err := m.SyncDeclared(t.Context()); err != nil || len(report.Installed) != 1 || len(report.Errors) != 0 {
		t.Fatalf("initial=%+v %v", report, err)
	}
	rows, _ := m.List()
	old := rows[0].InstallPath
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "version": "2.0.0"})
	if report, err := m.SyncDeclared(t.Context()); err != nil || len(report.Updated) != 0 || len(report.Errors) != 0 {
		t.Fatalf("declaration sync=%+v %v", report, err)
	}
	rows, _ = m.List()
	if rows[0].InstallPath != old {
		t.Fatal("startup applied an automatic update")
	}
	// Disabled plugins are updated too, without changing their enabled state.
	if _, err := m.SetEnabled(t.Context(), "alpha", "user", false); err != nil {
		t.Fatal(err)
	}
	report, err := m.AutoUpdate(t.Context())
	if err != nil || len(report.Updated) != 1 || len(report.Errors) != 0 {
		t.Fatalf("auto=%+v %v", report, err)
	}
	rows, _ = m.List()
	if rows[0].InstallPath == old || rows[0].Enabled || rows[0].Version != "2.0.0" {
		t.Fatalf("auto rows=%+v", rows)
	}
}

func TestAutoUpdateUsesCurrentControlsAndDoesNotRegisterDeclarations(t *testing.T) {
	m, market, plugin := startupFixture(t)
	on := true
	startupDeclaration(t, m, "user", market, &on)
	if _, err := m.SyncDeclared(t.Context()); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	old := rows[0].InstallPath
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "version": "2.0.0"})
	path, fields, _ := m.settings("user")
	var catalogs map[string]json.RawMessage
	json.Unmarshal(fields["extraKnownMarketplaces"], &catalogs)
	catalogs["never-fetch"], _ = json.Marshal(map[string]any{"source": Source{Kind: "github", Repo: "invalid/never-fetch"}, "autoUpdate": true})
	fields["extraKnownMarketplaces"], _ = json.Marshal(catalogs)
	fields["env"], _ = json.Marshal(map[string]string{"DISABLE_AUTOUPDATER": "1"})
	writeTestJSON(t, path, fields)
	if report, err := m.AutoUpdate(t.Context()); err != nil || len(report.Registered)+len(report.Updated)+len(report.Errors) != 0 {
		t.Fatalf("disabled=%+v %v", report, err)
	}
	rows, _ = m.List()
	if rows[0].InstallPath != old {
		t.Fatal("current disable control ignored")
	}
	fields["env"], _ = json.Marshal(map[string]string{"DISABLE_AUTOUPDATER": "1", "FORCE_AUTOUPDATE_PLUGINS": "1"})
	writeTestJSON(t, path, fields)
	if report, err := m.AutoUpdate(t.Context()); err != nil || len(report.Updated) != 1 || len(report.Registered)+len(report.Errors) != 0 {
		t.Fatalf("forced=%+v %v", report, err)
	}
	known, _ := m.marketplaces()
	if len(known) != 1 {
		t.Fatal("auto-update registered a new marketplace")
	}
}

func TestAutoUpdatePreservesBrokenPluginAndUpdatesOthers(t *testing.T) {
	m, market, alpha := startupFixture(t)
	beta := filepath.Join(market, "plugins", "beta")
	writeTestJSON(t, filepath.Join(beta, ".claude-plugin", "plugin.json"), map[string]any{"name": "beta", "version": "1.0.0"})
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": "./plugins/alpha"}, map[string]any{"name": "beta", "source": "./plugins/beta"}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alpha", "beta"} {
		if _, err := m.Install(t.Context(), id, InstallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", true); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	old := rows[0].InstallPath
	writeTestJSON(t, filepath.Join(alpha, ".mcp.json"), map[string]any{"broken": map[string]any{"command": "UNUSED", "unknown": true}})
	writeTestJSON(t, filepath.Join(beta, ".claude-plugin", "plugin.json"), map[string]any{"name": "beta", "version": "2.0.0"})
	report, err := m.AutoUpdate(t.Context())
	if err != nil || len(report.Errors) != 1 || len(report.Updated) != 1 {
		t.Fatalf("auto=%+v %v", report, err)
	}
	rows, err = m.List()
	if err != nil || len(rows) != 2 || rows[0].InstallPath != old || !rows[0].Enabled || rows[1].Version != "2.0.0" {
		t.Fatalf("rows=%+v %v", rows, err)
	}
}
