package plugins

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func taggedDependencyFixture(t *testing.T) (*Manager, string, string, string) {
	t.Helper()
	m, market, root, leaf := dependencyFixture(t)
	middle := filepath.Join(market, "plugins", "middle")
	manifest, _ := readObject(filepath.Join(middle, ".claude-plugin", "plugin.json"))
	manifest["dependencies"] = json.RawMessage(`[{"name":"leaf","version":"~1.2"}]`)
	writeTestJSON(t, filepath.Join(middle, ".claude-plugin", "plugin.json"), manifest)
	gitTest(t, market, "init", "-b", "main")
	for _, version := range []string{"1.2.0", "1.2.9", "2.0.0"} {
		// Tags are authoritative even when a manifest's version lags behind.
		writeTestJSON(t, filepath.Join(leaf, ".claude-plugin", "plugin.json"), map[string]any{"name": "leaf", "version": "1.0.0"})
		if err := os.WriteFile(filepath.Join(leaf, "release.txt"), []byte(version), 0600); err != nil {
			t.Fatal(err)
		}
		gitTest(t, market, "add", ".")
		gitTest(t, market, "commit", "-m", version)
		gitTest(t, market, "tag", "-a", "leaf--v"+version, "-m", version)
	}
	return m, market, root, leaf
}

func TestDependencyHighestMatchingTagCombinedRangesAndMovedTag(t *testing.T) {
	m, market, _, leaf := taggedDependencyFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	var original InstallRecord
	for _, row := range rows {
		if row.ID == "leaf@acme" {
			original = row.InstallRecord
		}
	}
	if original.ResolvedVersion != "1.2.9" || !strings.HasPrefix(original.Version, "1.2.9-") {
		t.Fatalf("tag resolution=%+v", original)
	}
	data, _ := os.ReadFile(filepath.Join(original.InstallPath, "release.txt"))
	if string(data) != "1.2.9" {
		t.Fatalf("wrong tagged content %q", data)
	}
	if selected := runtimePlugins(t, m); len(selected) != 3 {
		t.Fatal("tag version was not used for load constraints", selected)
	}
	beta := filepath.Join(market, "plugins", "beta")
	writeTestJSON(t, filepath.Join(beta, ".claude-plugin", "plugin.json"), map[string]any{"name": "beta", "dependencies": []any{map[string]string{"name": "leaf", "version": ">=1.2.0 <2.0.0"}}})
	catalog, _ := readObject(filepath.Join(market, ".claude-plugin", "marketplace.json"))
	var entries []map[string]any
	json.Unmarshal(catalog["plugins"], &entries)
	entries = append(entries, map[string]any{"name": "beta", "source": "./plugins/beta"})
	catalog["plugins"], _ = json.Marshal(entries)
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), catalog)
	if _, err := m.Install(t.Context(), "beta", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	writeTestJSON(t, filepath.Join(beta, ".claude-plugin", "plugin.json"), map[string]any{"name": "beta", "dependencies": []any{map[string]string{"name": "leaf", "version": "~2.0"}}})
	if _, err := m.Install(t.Context(), "beta", InstallOptions{Update: true}); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflict was accepted: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("conflicting update changed working versions")
	}
	if err := os.WriteFile(filepath.Join(leaf, "release.txt"), []byte("tag moved to new commit"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, market, "add", ".")
	gitTest(t, market, "commit", "-m", "moved tag")
	gitTest(t, market, "tag", "-f", "leaf--v1.2.9")
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	rows, _ = m.List()
	for _, row := range rows {
		if row.ID == "leaf@acme" {
			if row.GitCommitSHA == original.GitCommitSHA || row.InstallPath == original.InstallPath || row.ResolvedVersion != "1.2.9" {
				t.Fatalf("moved tag reused old content: %+v", row)
			}
			data, _ := os.ReadFile(filepath.Join(row.InstallPath, "release.txt"))
			if string(data) != "tag moved to new commit" {
				t.Fatalf("moved content=%q", data)
			}
		}
	}
}

func TestDependencyOwnRepositoryWithoutMatchingTagRollsBack(t *testing.T) {
	m, market, root, _ := dependencyFixture(t)
	repo := t.TempDir()
	writeTestJSON(t, filepath.Join(repo, ".claude-plugin", "plugin.json"), map[string]any{"name": "remote", "version": "3.0.0"})
	gitTest(t, repo, "init", "-b", "main")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-m", "current")
	manifest, _ := readObject(filepath.Join(root, ".claude-plugin", "plugin.json"))
	manifest["dependencies"] = json.RawMessage(`[{"name":"remote","version":"^1.0"}]`)
	writeTestJSON(t, filepath.Join(root, ".claude-plugin", "plugin.json"), manifest)
	catalog, _ := readObject(filepath.Join(market, ".claude-plugin", "marketplace.json"))
	var entries []map[string]any
	json.Unmarshal(catalog["plugins"], &entries)
	entries = append(entries, map[string]any{"name": "remote", "source": Source{Kind: "url", URL: (&url.URL{Scheme: "file", Path: repo}).String()}})
	catalog["plugins"], _ = json.Marshal(entries)
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), catalog)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err == nil || !strings.Contains(err.Error(), "no git tag satisfying") {
		t.Fatalf("missing tag=%v", err)
	}
	if _, err := os.Stat(filepath.Join(m.pluginDir, "installed_plugins.json")); !os.IsNotExist(err) {
		t.Fatal("failed dependency install published root registry")
	}
}
