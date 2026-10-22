package plugins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDependencyRepairPreservesParentAndDisabledDependencies(t *testing.T) {
	for _, operation := range []string{"startup", "repair", "marketplace-add"} {
		t.Run(operation, func(t *testing.T) {
			m, market, root, _ := dependencyFixture(t)
			if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
				t.Fatal(err)
			}
			rows, _ := m.List()
			var parentPath string
			for _, row := range rows {
				if row.ID == "alpha@acme" {
					parentPath = row.InstallPath
				}
				if row.ID == "leaf@acme" {
					if err := os.RemoveAll(row.InstallPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			// A repair must use the installed manifest even after the source changes.
			writeTestJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "version": "99.0.0", "dependencies": []string{"not-published"}})
			var err error
			switch operation {
			case "startup":
				var report StartupReport
				report, err = m.SyncStartup(t.Context())
				if len(report.Errors) != 0 || len(report.Installed) != 1 {
					t.Fatalf("report=%+v", report)
				}
			case "repair":
				_, err = m.RepairDependencies(t.Context())
			case "marketplace-add":
				_, err = m.AddMarketplace(t.Context(), market, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if selected := runtimePlugins(t, m); len(selected) != 3 {
				t.Fatalf("missing dependencies after repair: %+v", selected)
			}
			rows, _ = m.List()
			for _, row := range rows {
				if row.ID == "alpha@acme" && row.InstallPath != parentPath {
					t.Fatal("dependency repair updated the parent")
				}
			}
			// Simulate an external settings edit, which deliberately disables a child.
			path, fields, _ := m.settings("user")
			enabled, _ := enabledEntries(fields)
			enabled["leaf@acme"] = false
			fields["enabledPlugins"], _ = json.Marshal(enabled)
			writeTestJSON(t, path, fields)
			before, _ := os.ReadFile(path)
			report, err := m.RepairDependencies(t.Context())
			after, _ := os.ReadFile(path)
			if err != nil || len(report.Installed) != 0 || !bytes.Equal(before, after) {
				t.Fatalf("repair overrode disabled dependency: %+v %v", report, err)
			}
			for _, row := range rows {
				if row.ID == "middle@acme" {
					if err := os.RemoveAll(row.InstallPath); err != nil {
						t.Fatal(err)
					}
				}
			}
			report, err = m.RepairDependencies(t.Context())
			after, _ = os.ReadFile(path)
			if err != nil || len(report.Installed) != 1 || !bytes.Equal(before, after) {
				t.Fatalf("repair of another missing dependency overrode disabled child: %+v %v", report, err)
			}
		})
	}
}

func TestDependencyCLIPrunePreviewAndManualRetention(t *testing.T) {
	m, _, _, _ := dependencyFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "leaf", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"prune", "--json"}, {"autoremove", "--yes", "--dry-run", "--json"}} {
		output, err := runPluginCLI(t, m, args...)
		var result Result
		if err != nil || json.Unmarshal([]byte(output), &result) != nil || !result.DryRun || result.Changed || len(result.Pruned) != 1 {
			t.Fatalf("preview=%s %v", output, err)
		}
		if rows, _ := m.List(); len(rows) != 2 {
			t.Fatal("preview changed installs")
		}
	}
	output, err := runPluginCLI(t, m, "prune", "--yes", "--json")
	if err != nil || !strings.Contains(output, "middle@acme") {
		t.Fatalf("prune=%s %v", output, err)
	}
	if rows, _ := m.List(); len(rows) != 1 || rows[0].ID != "leaf@acme" || rows[0].AutoInstalled {
		t.Fatalf("manual plugin pruned: %+v", rows)
	}
}
