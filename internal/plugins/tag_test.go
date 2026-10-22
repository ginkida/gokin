package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func releaseFixture(t *testing.T) (*Manager, string, string) {
	t.Helper()
	m, market, plugin := fixture(t)
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "version": "1.2.3"})
	gitTest(t, market, "init", "-b", "main")
	gitTest(t, market, "config", "user.name", "Release Fixture")
	gitTest(t, market, "config", "user.email", "release@example.test")
	gitTest(t, market, "add", ".")
	gitTest(t, market, "commit", "--no-gpg-sign", "-m", "release")
	return m, market, plugin
}

func TestPluginTagPreviewCreateAnnotationAndDuplicate(t *testing.T) {
	m, repo, plugin := releaseFixture(t)
	gitTest(t, repo, "config", "tag.gpgSign", "true")
	// A configured signer must never be invoked by the release command.
	gitTest(t, repo, "config", "gpg.program", "GOKIN_RELEASE_MUST_NOT_EXECUTE")
	output, err := runPluginCLI(t, m, "tag", plugin, "--dry-run", "--json")
	var preview TagResult
	if err != nil || json.Unmarshal([]byte(output), &preview) != nil || preview.Outcome != "ok" || preview.Changed || !preview.DryRun || preview.Tag != "alpha--v1.2.3" || len(preview.Commands) != 2 {
		t.Fatalf("preview=%s %v", output, err)
	}
	if preview.MarketplaceFile != filepath.Join(repo, ".claude-plugin", "marketplace.json") || preview.VersionFile != filepath.Join(plugin, ".claude-plugin", "plugin.json") {
		t.Fatalf("release source=%+v", preview)
	}
	if tags := gitTest(t, repo, "tag", "--list"); tags != "" {
		t.Fatalf("dry-run created tags: %s", tags)
	}
	// Edits elsewhere in the marketplace do not make this plugin dirty.
	if err := os.WriteFile(filepath.Join(repo, "unrelated.txt"), []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err = runPluginCLI(t, m, "tag", plugin, "--message", "Release %s; literal $(touch NEVER)", "--json")
	var created TagResult
	if err != nil || json.Unmarshal([]byte(output), &created) != nil || !created.Changed || created.Pushed || created.Commit != preview.Commit {
		t.Fatalf("created=%s %v", output, err)
	}
	if kind := gitTest(t, repo, "cat-file", "-t", created.Tag); kind != "tag" {
		t.Fatalf("release is not annotated: %s", kind)
	}
	if annotation := gitTest(t, repo, "for-each-ref", "--format=%(contents)", "refs/tags/"+created.Tag); annotation != "Release 1.2.3; literal $(touch NEVER)" {
		t.Fatalf("annotation=%q", annotation)
	}
	if _, err := os.Stat(filepath.Join(plugin, "NEVER")); !os.IsNotExist(err) {
		t.Fatal("annotation was executed")
	}
	if _, err := os.Stat(filepath.Join(m.opts.UserDir, "plugins")); !os.IsNotExist(err) {
		t.Fatal("tag created plugin management state")
	}
	for _, dryRun := range []bool{false, true} {
		_, err := m.Tag(t.Context(), plugin, TagOptions{DryRun: dryRun})
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("duplicate accepted: %v", err)
		}
	}
}

func TestPluginTagRejectsDirtyReleaseFiles(t *testing.T) {
	for _, kind := range []string{"modified", "staged", "untracked", "deleted", "marketplace", "ignored-manifest"} {
		t.Run(kind, func(t *testing.T) {
			m, repo, plugin := releaseFixture(t)
			skill := filepath.Join(plugin, "skills", "review", "SKILL.md")
			switch kind {
			case "modified", "staged":
				if err := os.WriteFile(skill, []byte("---\ndescription: Changed\n---\nChanged skill.\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "staged" {
					gitTest(t, repo, "add", ".")
				}
			case "untracked":
				if err := os.WriteFile(filepath.Join(plugin, "new.txt"), []byte("new"), 0600); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := os.Remove(skill); err != nil {
					t.Fatal(err)
				}
			case "marketplace":
				writeTestJSON(t, filepath.Join(repo, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Changed"}, "plugins": []any{map[string]any{"name": "alpha", "source": "./plugins/alpha"}}})
			case "ignored-manifest":
				gitTest(t, repo, "rm", "--cached", "plugins/alpha/.claude-plugin/plugin.json")
				if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("plugins/alpha/.claude-plugin/plugin.json\n"), 0600); err != nil {
					t.Fatal(err)
				}
				gitTest(t, repo, "add", ".gitignore")
				gitTest(t, repo, "commit", "--no-gpg-sign", "-m", "ignore manifest")
			}
			if _, err := m.Tag(t.Context(), plugin, TagOptions{}); err == nil {
				t.Fatal("dirty or untracked release was tagged")
			}
			if tags := gitTest(t, repo, "tag", "--list"); tags != "" {
				t.Fatalf("failed validation created tags: %s", tags)
			}
		})
	}
}

func TestPluginTagForceReplacesOnlyLocalTag(t *testing.T) {
	m, repo, plugin := releaseFixture(t)
	oldCommit := gitTest(t, repo, "rev-parse", "HEAD")
	if _, err := m.Tag(t.Context(), plugin, TagOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "new.txt"), []byte("next commit"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "--no-gpg-sign", "-m", "next")
	if err := os.WriteFile(filepath.Join(plugin, "dirty.txt"), []byte("dirty"), 0600); err != nil {
		t.Fatal(err)
	}
	preview, err := m.Tag(t.Context(), plugin, TagOptions{Force: true, DryRun: true})
	if err != nil || preview.Changed || gitTest(t, repo, "rev-parse", preview.Tag+"^{commit}") != oldCommit {
		t.Fatalf("force preview=%+v %v", preview, err)
	}
	result, err := m.Tag(t.Context(), plugin, TagOptions{Force: true})
	if err != nil || !result.Changed || result.Commit == oldCommit || gitTest(t, repo, "rev-parse", result.Tag+"^{commit}") != result.Commit {
		t.Fatalf("force=%+v %v", result, err)
	}
	if strings.Contains(strings.Join(result.Commands[1], " "), "--force") {
		t.Fatal("local tag replacement also forced a remote overwrite")
	}
}

func TestPluginTagVersionsAndComponentValidation(t *testing.T) {
	for _, tc := range []struct {
		name, pluginVersion, marketVersion string
		valid                              bool
	}{
		{"missing", "", "", false},
		{"short", "1.2", "", false},
		{"range", "^1.2.3", "", false},
		{"mismatch", "1.2.3", "2.0.0", false},
		{"catalog-version", "", "2.1.0", true},
		{"same", "1.2.3", "1.2.3", true},
		{"prerelease", "2.0.0-beta.1+build.7", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, repo, plugin := releaseFixture(t)
			manifest := map[string]any{"name": "alpha"}
			if tc.pluginVersion != "" {
				manifest["version"] = tc.pluginVersion
			}
			writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest)
			writeTestJSON(t, filepath.Join(repo, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "version": tc.marketVersion, "source": "./plugins/alpha"}}})
			gitTest(t, repo, "add", ".")
			gitTest(t, repo, "commit", "--no-gpg-sign", "--allow-empty", "-m", "version")
			result, err := m.Tag(t.Context(), plugin, TagOptions{DryRun: true})
			if (err == nil) != tc.valid {
				t.Fatalf("tag=%+v %v", result, err)
			}
			if tc.name == "catalog-version" && result.VersionFile != result.MarketplaceFile {
				t.Fatal("catalog version source was lost")
			}
		})
	}
	m, repo, plugin := releaseFixture(t)
	writeTestJSON(t, filepath.Join(plugin, ".mcp.json"), map[string]any{"bad": map[string]any{"command": "unused", "unsupported": true}})
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "--no-gpg-sign", "-m", "invalid component")
	if _, err := m.Tag(t.Context(), plugin, TagOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("force bypassed component validation: %v", err)
	}
}

func TestPluginTagPushAndFailureRetainsLocalTag(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "push", true: "failed-push"}[fail], func(t *testing.T) {
			m, repo, plugin := releaseFixture(t)
			remote := t.TempDir()
			gitTest(t, remote, "init", "--bare")
			gitTest(t, repo, "remote", "add", "releases", remote)
			if fail {
				gitTest(t, repo, "config", "remote.releases.pushurl", filepath.Join(t.TempDir(), "missing.git"))
			}
			output, err := runPluginCLI(t, m, "tag", plugin, "--push", "--remote", "releases", "--json")
			var result TagResult
			if json.Unmarshal([]byte(output), &result) != nil || !result.Changed || result.Pushed == fail || (err != nil) != fail {
				t.Fatalf("push=%s %v", output, err)
			}
			if tag := gitTest(t, repo, "rev-parse", result.Tag+"^{commit}"); tag != result.Commit {
				t.Fatal("push lost local tag")
			}
			if !fail && gitTest(t, remote, "rev-parse", result.Tag+"^{commit}") != result.Commit {
				t.Fatal("remote release differs")
			}
			if fail && (result.Outcome != "failed" || !strings.Contains(result.Message, "locally")) {
				t.Fatal("failed push did not report the retained local tag")
			}
		})
	}
}

func TestPluginTagRootPluginAndDefaultPath(t *testing.T) {
	m, repo, plugin := releaseFixture(t)
	// Put a plugin beside its catalog. Tagging must validate the plugin too.
	writeTestJSON(t, filepath.Join(repo, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "version": "1.2.3"})
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "--no-gpg-sign", "-m", "root plugin")
	m.opts.WorkDir = repo
	output, err := runPluginCLI(t, m, "tag", "--dry-run")
	if err != nil || !strings.Contains(output, "alpha--v1.2.3") || !strings.Contains(output, "Dry run") {
		t.Fatalf("default path=%q %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("root plugin edit"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Tag(t.Context(), repo, TagOptions{DryRun: true}); err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("root plugin skipped dirty check: %v", err)
	}
	writeTestJSON(t, filepath.Join(repo, ".mcp.json"), map[string]any{"bad": map[string]any{"command": "unused", "unsupported": true}})
	if _, err := m.Tag(t.Context(), repo, TagOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "validation failed") {
		t.Fatalf("root plugin skipped validation: %v", err)
	}
	// A standalone nested repository cannot borrow its enclosing catalog.
	gitTest(t, plugin, "init", "-b", "nested")
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "version": "3.0.0"})
	gitTest(t, plugin, "add", ".")
	gitTest(t, plugin, "commit", "--no-gpg-sign", "-m", "nested plugin")
	result, err := m.Tag(t.Context(), plugin, TagOptions{DryRun: true})
	if err != nil || result.MarketplaceFile != "" || result.Version != "3.0.0" {
		t.Fatalf("nested repo=%+v %v", result, err)
	}
}

func TestPluginTagInvalidRemoteAndMissingRemoteDoNotCreateTags(t *testing.T) {
	for _, remote := range []string{"-option", "bad:remote", "missing"} {
		t.Run(remote, func(t *testing.T) {
			m, repo, plugin := releaseFixture(t)
			if _, err := m.Tag(t.Context(), plugin, TagOptions{Push: true, Remote: remote}); err == nil {
				t.Fatal("invalid remote accepted")
			}
			if tags := gitTest(t, repo, "tag", "--list"); tags != "" {
				t.Fatal("remote preflight failure created a tag")
			}
		})
	}
}

func TestPluginTagReleasesResolveAsDependencies(t *testing.T) {
	m, market, _, leaf := dependencyFixture(t)
	gitTest(t, market, "init", "-b", "main")
	gitTest(t, market, "config", "user.name", "Release Fixture")
	gitTest(t, market, "config", "user.email", "release@example.test")
	for _, version := range []string{"1.2.0", "1.2.9", "2.0.0"} {
		writeTestJSON(t, filepath.Join(leaf, ".claude-plugin", "plugin.json"), map[string]any{"name": "leaf", "version": version})
		gitTest(t, market, "add", ".")
		gitTest(t, market, "commit", "--no-gpg-sign", "-m", version)
		if output, err := runPluginCLI(t, m, "tag", leaf); err != nil {
			t.Fatalf("publish %s: %s %v", version, output, err)
		}
	}
	middle := filepath.Join(market, "plugins", "middle", ".claude-plugin", "plugin.json")
	writeTestJSON(t, middle, map[string]any{"name": "middle", "version": "1.0.0", "dependencies": []any{map[string]string{"name": "leaf", "version": "~1.2"}}})
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	rows, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == "leaf@acme" {
			if row.ResolvedVersion != "1.2.9" || row.GitCommitSHA != gitTest(t, market, "rev-parse", "leaf--v1.2.9^{commit}") {
				t.Fatalf("release resolved incorrectly: %+v", row)
			}
			return
		}
	}
	t.Fatal("released dependency was not installed")
}
