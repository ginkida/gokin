package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/hooks"
	"gokin/internal/lsp"
	"gokin/internal/mcp"
	"gokin/internal/skills"
)

func linkFixture(t *testing.T) (*Manager, string, string, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("command link mode is intentionally unavailable on Windows")
	}
	m, market, plugin := fixture(t)
	command := "printf '%s\\n' '" + plugin + "'"
	source := Source{Kind: "command", Command: command, Mode: "link"}
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": source, "version": "ignored-catalog-version"}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(command))
	approval := hex.EncodeToString(sum[:])
	return m, market, plugin, command, approval
}

func TestCommandLinkLoadsAllComponentsWithoutCopyingOrMutation(t *testing.T) {
	m, _, plugin, _, approval := linkFixture(t)
	original, _ := os.ReadFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	// An unsupported lock would warn on a copied plugin. Link mode must skip it.
	writeTestJSON(t, filepath.Join(plugin, "package.json"), map[string]any{"name": "fixture", "dependencies": map[string]string{"dep": "1.0.0"}})
	if err := os.WriteFile(filepath.Join(plugin, "yarn.lock"), []byte("unsupported"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}})
	if err != nil || len(result.Warnings) > 0 {
		t.Fatalf("install=%+v %v", result, err)
	}
	rows, err := m.List()
	if err != nil || len(rows) != 1 || !rows[0].Enabled {
		t.Fatalf("rows=%+v %v", rows, err)
	}
	record := rows[0].InstallRecord
	if record.LinkedRoot != plugin || !strings.HasPrefix(record.Version, "1.0-") || len(strings.TrimPrefix(record.Version, "1.0-")) != 12 {
		t.Fatalf("record=%+v", record)
	}
	entries, err := os.ReadDir(record.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceEntries, _ := os.ReadDir(plugin)
	if len(entries) != len(sourceEntries) {
		t.Fatal("cache projection lost top-level entries")
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink == 0 {
			t.Fatalf("copied %s", entry.Name())
		}
		target, _ := os.Readlink(filepath.Join(record.InstallPath, entry.Name()))
		if target != filepath.Join(plugin, entry.Name()) {
			t.Fatalf("target=%s", target)
		}
	}
	current, _ := os.ReadFile(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	if !bytes.Equal(original, current) {
		t.Fatal("link installation edited producer manifest")
	}
	authority := hooks.NewManager(true, m.opts.WorkDir)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
	authority.ConfigureClaudePlugins(nil)
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("selection=%+v %v", selected, err)
	}
	catalog := skills.NewCatalog(nil)
	catalog.ConfigureClaudePlugins(authority)
	if len(catalog.List()) != 1 {
		t.Fatalf("skills=%+v warnings=%+v", catalog.List(), catalog.Warnings())
	}
	types := agent.NewAgentTypeRegistry()
	types.ConfigureClaudePlugins(authority)
	foundAgent := false
	for _, definition := range types.SnapshotAgentTypes() {
		foundAgent = foundAgent || definition.Name == "alpha:review"
	}
	if !foundAgent {
		t.Fatalf("agent absent: %+v", types.PluginWarnings())
	}
	servers, err := mcp.LoadClaudePluginServers(authority, m.opts.WorkDir, true)
	if err != nil || len(servers) != 1 {
		t.Fatalf("MCP=%+v %v", servers, err)
	}
	languages, err := lsp.LoadPluginConfigs(authority, m.opts.WorkDir)
	if err != nil || len(languages) != 1 {
		t.Fatalf("LSP=%+v %v", languages, err)
	}
	if len(authority.GetHooks()) != 1 {
		t.Fatalf("hooks=%+v", authority.GetHooks())
	}
	if report, err := m.Validate(record.InstallPath); err != nil || report.Components["workflows"] != 1 {
		t.Fatalf("validate=%+v %v", report, err)
	}
	if _, err := m.Configure(t.Context(), "alpha", map[string]string{"value": "configured"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(t.Context(), "alpha", "user", false); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(t.Context(), "alpha", "user", true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plugin); err != nil {
		t.Fatal("uninstall deleted producer directory", err)
	}
}

func TestCommandLinkVersionIgnoresContentAndTracksDirectoryIdentity(t *testing.T) {
	m, market, plugin, _, approval := linkFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	old := rows[0].InstallRecord
	if err := os.WriteFile(filepath.Join(plugin, "skills", "review", "SKILL.md"), []byte("---\ndescription: Changed\n---\nChanged in place."), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true})
	if err != nil || result.Changed {
		t.Fatalf("content-only update=%+v %v", result, err)
	}
	catalog := skills.NewCatalog(nil)
	authority := hooks.NewManager(false, m.opts.WorkDir)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
	authority.ConfigureClaudePlugins(nil)
	catalog.ConfigureClaudePlugins(authority)
	for _, skill := range catalog.List() {
		if !strings.Contains(skill.Body, "Changed in place") {
			t.Fatal("live content was not read")
		}
	}
	if err := os.WriteFile(filepath.Join(plugin, "extra.txt"), []byte("new entry"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err = m.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil || !result.Changed {
		t.Fatalf("top-level change=%+v %v", result, err)
	}
	rows, _ = m.List()
	if rows[0].Digest == old.Digest {
		t.Fatal("top-level identity did not change")
	}
	// The exact same command can print a different directory on its next run.
	pathFile := filepath.Join(t.TempDir(), "root.txt")
	dynamic := "cat '" + pathFile + "'"
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": Source{Kind: "command", Command: dynamic, Mode: "link"}}}})
	if err := os.WriteFile(pathFile, []byte(plugin+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(dynamic))
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true, AcceptCommands: []string{hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	rows, _ = m.List()
	prior := rows[0].Digest
	other := filepath.Join(t.TempDir(), "alpha")
	if _, err := copyTree(plugin, other); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathFile, []byte(other+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil || !result.Changed {
		t.Fatalf("new path=%+v %v", result, err)
	}
	rows, _ = m.List()
	if rows[0].Digest == prior {
		t.Fatal("real path omitted from version")
	}
}

func TestCommandLinkLargeTreeAndContainedTopLevelSymlink(t *testing.T) {
	m, _, plugin, _, approval := linkFixture(t)
	file, err := os.Create(filepath.Join(plugin, "large-sdk.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxTreeBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	if err := os.Symlink("skills", filepath.Join(plugin, "linked-skills")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatalf("large link install: %v", err)
	}
}

func TestCommandLinkRejectsEscapingAndReplacedLinksAndInsideWorkspace(t *testing.T) {
	for _, mode := range []string{"escaping-source", "cache-target", "inside-source", "inside-child", "missing-source"} {
		t.Run(mode, func(t *testing.T) {
			m, _, plugin, _, approval := linkFixture(t)
			if mode == "escaping-source" {
				if err := os.Symlink(t.TempDir(), filepath.Join(plugin, "escape")); err != nil {
					t.Fatal(err)
				}
				if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err == nil {
					t.Fatal("accepted escaping top-level symlink")
				}
				return
			}
			if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
				t.Fatal(err)
			}
			rows, _ := m.List()
			record := rows[0].InstallRecord
			switch mode {
			case "cache-target":
				path := filepath.Join(record.InstallPath, "skills")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			case "missing-source":
				if err := os.Rename(plugin, plugin+"-moved"); err != nil {
					t.Fatal(err)
				}
			case "inside-source":
				m.opts.WorkDir = plugin
			case "inside-child":
				m.opts.WorkDir = filepath.Join(plugin, "skills")
			}
			authority := hooks.NewManager(false, m.opts.WorkDir)
			authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
			authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
			authority.ConfigureClaudePlugins(nil)
			selected, err := authority.ClaudePlugins()
			if mode == "inside-source" || mode == "inside-child" {
				if err != nil || len(selected) != 0 {
					t.Fatalf("inside=%+v %v", selected, err)
				}
			} else if err == nil {
				t.Fatal("unsafe or missing source loaded")
			}
		})
	}
}

func TestCommandModeChangeRequiresNewApproval(t *testing.T) {
	m, market, _, command, approval := linkFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": Source{Kind: "command", Command: command, Mode: "copy"}}}})
	_, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true})
	var required *CommandApprovalError
	if !errors.As(err, &required) || required.Mode != "copy" {
		t.Fatalf("mode approval=%+v %v", required, err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true, AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	if rows[0].LinkedRoot != "" || len(rows[0].LinkEntry) > 0 {
		t.Fatal("copy retained link authority")
	}
	if _, err := os.Readlink(filepath.Join(rows[0].InstallPath, "skills")); err == nil {
		t.Fatal("copy mode retained live source link")
	}
}

func TestCommandLinkEntryManifestAndSensitiveOptions(t *testing.T) {
	m, market, plugin, command, approval := linkFixture(t)
	m.opts.SecretKeyring = &memoryKeyring{keys: map[string]string{}}
	if err := os.Remove(filepath.Join(plugin, ".claude-plugin", "plugin.json")); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"name": "alpha", "version": "ignored-catalog-version", "source": Source{Kind: "command", Command: command, Mode: "link"}, "userConfig": map[string]any{"token": map[string]any{"type": "string", "sensitive": true, "required": true}, "value": map[string]any{"type": "string", "default": "ordinary"}}}
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{entry}})
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}, Config: map[string]string{"token": "secret-test"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := m.List()
	if err != nil || len(rows) != 1 || len(rows[0].Version) != 12 {
		t.Fatalf("rows=%+v %v", rows, err)
	}
	encoded, _ := json.Marshal(rows)
	if bytes.Contains(encoded, []byte("gokinLinkEntry")) || bytes.Contains(encoded, []byte("secret-test")) {
		t.Fatal("installer metadata or configured value leaked into list")
	}
	if report, err := m.Validate(rows[0].InstallPath); err != nil || report.Name != "alpha" {
		t.Fatalf("entry validation=%+v %v", report, err)
	}
	if _, err := m.Configure(t.Context(), "alpha", map[string]string{"token": "updated-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plugin, ".claude-plugin", "plugin.json")); !os.IsNotExist(err) {
		t.Fatal("synthesized entry manifest written into producer directory")
	}
	authority := secretAuthority(m)
	selected, err := authority.ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("selection=%+v %v", selected, err)
	}
	env, err := selected[0].ProcessEnvironment(m.opts.WorkDir)
	if err != nil || env["CLAUDE_PLUGIN_OPTION_TOKEN"] != "updated-secret" {
		t.Fatalf("secret expansion: %v", err)
	}
}

func TestCommandCopyVersionTracksContentsAndIgnoresEntryVersion(t *testing.T) {
	m, market, plugin, command, approval := linkFixture(t)
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "version": "catalog-only", "source": Source{Kind: "command", Command: command}}}})
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	old := rows[0].Version
	if !strings.HasPrefix(old, "1.0-") {
		t.Fatalf("version=%s", old)
	}
	if err := os.WriteFile(filepath.Join(plugin, "changed.txt"), []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true})
	if err != nil || !result.Changed {
		t.Fatalf("update=%+v %v", result, err)
	}
	rows, _ = m.List()
	if rows[0].Version == old {
		t.Fatal("copied output changed without a new version")
	}
	// Without a producer version the entry version must not prefix the hash.
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "userConfig": map[string]any{"value": map[string]any{"type": "string", "default": "literal"}}})
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true}); err != nil {
		t.Fatal(err)
	}
	rows, _ = m.List()
	if len(rows[0].Version) != 12 {
		t.Fatalf("entry version incorrectly used: %s", rows[0].Version)
	}
}

func TestCommandLinkMissingProducerCanBeUninstalled(t *testing.T) {
	m, _, plugin, _, approval := linkFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(plugin, plugin+"-gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Uninstall(t.Context(), "alpha", RemoveOptions{}); err != nil {
		t.Fatalf("missing producer cannot be removed: %v", err)
	}
}

func TestCommandLinkLiveHookProcess(t *testing.T) {
	m, _, plugin, _, approval := linkFixture(t)
	script := filepath.Join(plugin, "scripts", "read.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte(`printf '%s:' "$CLAUDE_PLUGIN_OPTION_VALUE"
cat "$CLAUDE_PLUGIN_ROOT/live.txt"
`), 0700); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), map[string]any{"name": "alpha", "userConfig": map[string]any{"value": map[string]any{"type": "string", "default": "ordinary"}}, "hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Read", "hooks": []any{map[string]any{"type": "command", "command": `sh "${CLAUDE_PLUGIN_ROOT}/scripts/read.sh"`}}}}}})
	if err := os.WriteFile(filepath.Join(plugin, "live.txt"), []byte("one"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	authority := hooks.NewManager(true, m.opts.WorkDir)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
	authority.ConfigureClaudePlugins(nil)
	for _, version := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(plugin, "live.txt"), []byte(version), 0600); err != nil {
			t.Fatal(err)
		}
		results := authority.RunPreTool(t.Context(), "read", map[string]any{"file_path": "example"})
		if len(results) != 1 || results[0].Error != nil || results[0].Output != "ordinary:"+version {
			t.Fatalf("live hook=%+v", results)
		}
	}
}

func TestCommandLinkProducerVersionIsPartOfUpdateIdentity(t *testing.T) {
	m, _, plugin, _, approval := linkFixture(t)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	old := rows[0].InstallRecord
	manifest, err := readObject(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest["version"] = json.RawMessage(`"2.0"`)
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest)
	result, err := m.Install(t.Context(), "alpha", InstallOptions{Update: true})
	if err != nil || !result.Changed {
		t.Fatalf("version update=%+v %v", result, err)
	}
	rows, _ = m.List()
	if rows[0].Digest != old.Digest || !strings.HasPrefix(rows[0].Version, "2.0-") {
		t.Fatal("producer version or file hashing is incorrect")
	}
}

func TestCommandLinkUnrelatedEntryRemovalDoesNotDisableComponents(t *testing.T) {
	m, _, plugin, _, approval := linkFixture(t)
	unused := filepath.Join(plugin, "unused.txt")
	if err := os.WriteFile(unused, []byte("temporary producer data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{approval}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(unused); err != nil {
		t.Fatal(err)
	}
	if selected := runtimePlugins(t, m); len(selected) != 1 {
		t.Fatal("unrelated dangling cache link disabled plugin")
	}
}
