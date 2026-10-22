package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
)

func startupFixture(t *testing.T) (*Manager, string, string) {
	t.Helper()
	for _, key := range []string{"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "FORCE_AUTOUPDATE_PLUGINS"} {
		t.Setenv(key, "")
	}
	return fixture(t)
}

func startupDeclaration(t *testing.T, m *Manager, scope, market string, auto *bool) {
	t.Helper()
	path, fields, err := m.settings(scope)
	if err != nil {
		t.Fatal(err)
	}
	fields["extraKnownMarketplaces"], _ = json.Marshal(map[string]any{"acme": marketplaceDeclaration{Source: Source{Kind: "directory", Path: market}, AutoUpdate: auto}})
	fields["enabledPlugins"], _ = json.Marshal(map[string]bool{"alpha@acme": true})
	writeTestJSON(t, path, fields)
}

func TestStartupTrustAndProjectDeclarationRemainAtOriginalScope(t *testing.T) {
	for _, trusted := range []bool{false, true} {
		t.Run(map[bool]string{false: "untrusted", true: "trusted"}[trusted], func(t *testing.T) {
			m, market, _ := startupFixture(t)
			m.opts.Trusted = trusted
			relative, err := filepath.Rel(m.opts.WorkDir, market)
			if err != nil {
				t.Fatal(err)
			}
			startupDeclaration(t, m, "project", relative, nil)
			userPath, _ := m.settingsPath("user")
			projectPath, _ := m.settingsPath("project")
			userBefore, _ := os.ReadFile(userPath)
			projectBefore, _ := os.ReadFile(projectPath)
			report, err := m.SyncStartup(t.Context())
			if err != nil || len(report.Errors) != 0 {
				t.Fatalf("sync=%+v %v", report, err)
			}
			if !trusted {
				if len(report.Registered)+len(report.Installed) != 0 {
					t.Fatalf("untrusted project was applied: %+v", report)
				}
				if _, err := os.Stat(m.pluginDir); !os.IsNotExist(err) {
					t.Fatalf("untrusted declaration created a store: %v", err)
				}
				return
			}
			if len(report.Registered) != 1 || len(report.Installed) != 1 {
				t.Fatalf("project declaration not installed: %+v", report)
			}
			rows, err := m.List()
			if err != nil || len(rows) != 1 || rows[0].Scope != "project" || !rows[0].Enabled {
				t.Fatalf("project rows=%+v %v", rows, err)
			}
			if selected := runtimePlugins(t, m); len(selected) != 1 {
				t.Fatalf("project runtime=%+v", selected)
			}
			userAfter, _ := os.ReadFile(userPath)
			projectAfter, _ := os.ReadFile(projectPath)
			if !bytes.Equal(userBefore, userAfter) || !bytes.Equal(projectBefore, projectAfter) {
				t.Fatal("automatic registration/install rewrote or promoted declarations")
			}
			again, err := m.SyncStartup(t.Context())
			if err != nil || len(again.Registered)+len(again.Installed)+len(again.Updated)+len(again.Errors) != 0 {
				t.Fatalf("second sync was not a no-op: %+v %v", again, err)
			}
			if _, err := m.UpdateMarketplace(t.Context(), "acme", nil); err != nil {
				t.Fatal(err)
			}
			userAfter, _ = os.ReadFile(userPath)
			if !bytes.Equal(userBefore, userAfter) {
				t.Fatal("manual update promoted a project declaration")
			}
		})
	}
}

func TestStartupAutoUpdatesDisabledCopiesAndPreservesFailedVersion(t *testing.T) {
	m, market, plugin := startupFixture(t)
	on := true
	startupDeclaration(t, m, "user", market, &on)
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Installed) != 1 || len(report.Errors) != 0 {
		t.Fatalf("initial=%+v %v", report, err)
	}
	if _, err := m.SetEnabled(t.Context(), "alpha", "user", false); err != nil {
		t.Fatal(err)
	}
	rows, _ := m.List()
	old := rows[0].InstallPath
	if err := os.WriteFile(filepath.Join(plugin, "new.txt"), []byte("new release"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err = m.SyncStartup(t.Context())
	if err != nil || len(report.Updated) != 1 || len(report.Errors) != 0 {
		t.Fatalf("update=%+v %v", report, err)
	}
	rows, _ = m.List()
	if rows[0].Enabled || rows[0].InstallPath == old {
		t.Fatal("update re-enabled plugin or failed to switch immutable version")
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("previous version was removed")
	}
	before, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	writeTestJSON(t, filepath.Join(plugin, ".mcp.json"), map[string]any{"bad": map[string]any{"command": "unused", "invalidSetting": true}})
	report, err = m.SyncStartup(t.Context())
	if err != nil || len(report.Errors) != 1 {
		t.Fatalf("failed update=%+v %v", report, err)
	}
	after, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("failed automatic update changed working install")
	}
	if _, err := os.Stat(filepath.Join(m.opts.WorkDir, "NEVER_EXECUTED")); !os.IsNotExist(err) {
		t.Fatal("startup ran a hook")
	}
}

func TestStartupManagedRequiredPluginUsesManagedOptionsAndPreservesUserDisable(t *testing.T) {
	m, market, plugin := startupFixture(t)
	path, user, _ := m.settings("user")
	user["enabledPlugins"] = json.RawMessage(`{"alpha@acme":false}`)
	writeTestJSON(t, path, user)
	before, _ := os.ReadFile(path)
	manifest, _ := readObject(filepath.Join(plugin, ".claude-plugin", "plugin.json"))
	manifest["userConfig"] = json.RawMessage(`{"value":{"type":"string","required":true}}`)
	writeTestJSON(t, filepath.Join(plugin, ".claude-plugin", "plugin.json"), manifest)
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{
		"extraKnownMarketplaces": map[string]any{"acme": marketplaceDeclaration{Source: Source{Kind: "directory", Path: market}}},
		"enabledPlugins":         map[string]bool{"alpha@acme": true},
		"pluginConfigs":          map[string]any{"alpha@acme": map[string]any{"options": map[string]string{"value": "managed-only"}}},
	})
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Installed) != 1 || len(report.Errors) != 0 {
		t.Fatalf("managed startup=%+v %v", report, err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("managed install overwrote user disable or option settings")
	}
	selected := runtimePlugins(t, m)
	if len(selected) != 1 {
		t.Fatal("managed override did not force plugin loading")
	}
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", true); err != nil {
		t.Fatal("managed entry without field should permit a user toggle", err)
	}
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.d", "10-updates.json"), map[string]any{"extraKnownMarketplaces": map[string]any{"acme": map[string]any{"source": Source{Kind: "directory", Path: market}, "autoUpdate": false}}})
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", true); err == nil || !strings.Contains(err.Error(), "managed") {
		t.Fatalf("managed auto-update field was overridden: %v", err)
	}
}

func TestStartupNeverReusesApprovalForBackgroundCommands(t *testing.T) {
	m, market, plugin := startupFixture(t)
	sentinel := filepath.Join(t.TempDir(), "installer-ran")
	command := "touch '" + sentinel + "'; printf '%s\\n' '" + plugin + "'"
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": Source{Kind: "command", Command: command}}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(command))
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{hex.EncodeToString(digest[:])}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", true); err != nil {
		t.Fatal(err)
	}
	for _, run := range []func(context.Context) (StartupReport, error){m.SyncStartup, m.AutoUpdate} {
		report, err := run(t.Context())
		if err != nil || len(report.Errors) != 0 || len(report.Skipped) != 1 {
			t.Fatalf("command automatic pass=%+v %v", report, err)
		}
		if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
			t.Fatal("automatic update reused an install command approval")
		}
	}
}

func TestStartupRepairsEnabledMissingCacheEvenWithAutoUpdateOff(t *testing.T) {
	m, market, _ := startupFixture(t)
	startupDeclaration(t, m, "user", market, nil)
	if report, err := m.SyncStartup(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("initial=%+v %v", report, err)
	}
	rows, _ := m.List()
	// The fixture is the sole owner; removing this snapshot simulates a lost cache.
	if err := os.RemoveAll(rows[0].InstallPath); err != nil {
		t.Fatal(err)
	}
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Installed) != 1 || len(report.Errors) != 0 {
		t.Fatalf("repair=%+v %v", report, err)
	}
	if selected := runtimePlugins(t, m); len(selected) != 1 {
		t.Fatal("cache repair did not restore runtime")
	}
}

func TestStartupToggleProjectScopeAndUpdateControls(t *testing.T) {
	m, market, _ := startupFixture(t)
	startupDeclaration(t, m, "project", market, nil)
	if report, err := m.SyncStartup(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("initial=%+v %v", report, err)
	}
	userPath, _ := m.settingsPath("user")
	before, _ := os.ReadFile(userPath)
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", true); err != nil {
		t.Fatal(err)
	}
	plan, err := m.startupPlan(t.Context())
	if err != nil || plan.Markets["acme"].AutoUpdate == nil || !*plan.Markets["acme"].AutoUpdate || plan.Markets["acme"].Scope != "project" {
		t.Fatalf("project toggle=%+v %v", plan.Markets, err)
	}
	after, _ := os.ReadFile(userPath)
	if !bytes.Equal(before, after) {
		t.Fatal("project toggle modified user settings")
	}
	t.Setenv("DISABLE_AUTOUPDATER", "1")
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", false); err == nil {
		t.Fatal("toggle ignored global update disable")
	}
	t.Setenv("FORCE_AUTOUPDATE_PLUGINS", "1")
	if _, err := m.SetMarketplaceAutoUpdate(t.Context(), "acme", false); err != nil {
		t.Fatal("explicit force did not permit plugin updates", err)
	}
}

func TestStartupCatalogFetchCannotOverwriteChangedDeclaration(t *testing.T) {
	m, _, _ := startupFixture(t)
	path, fields, _ := m.settings("user")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.WriteFile(path, []byte(`{"theme":"new","extraKnownMarketplaces":{}}`), 0600); err != nil {
			http.Error(w, "fixture write failed", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"acme","owner":{"name":"Acme"},"plugins":[]}`))
	}))
	defer server.Close()
	fields["extraKnownMarketplaces"], _ = json.Marshal(map[string]any{"acme": map[string]any{"source": Source{Kind: "url", URL: server.URL}}})
	writeTestJSON(t, path, fields)
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Errors) != 1 || len(report.Registered) != 0 {
		t.Fatalf("changed declaration=%+v %v", report, err)
	}
	known, err := m.marketplaces()
	if err != nil || len(known) != 0 {
		t.Fatalf("stale declaration was registered: %+v %v", known, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"theme":"new"`) {
		t.Fatal("startup overwrote an external writer")
	}
}

func TestStartupCanceledBeforeAnyMutation(t *testing.T) {
	m, market, _ := startupFixture(t)
	startupDeclaration(t, m, "user", market, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.SyncStartup(ctx); err != context.Canceled {
		t.Fatalf("canceled startup=%v", err)
	}
	if _, err := os.Stat(m.pluginDir); !os.IsNotExist(err) {
		t.Fatal("canceled startup created store")
	}
}

func TestStartupRelativeMarketplaceResolvesMainCheckoutFromWorktree(t *testing.T) {
	m, market, _ := startupFixture(t)
	main := t.TempDir()
	mainMarket := filepath.Join(main, "market")
	if _, err := copyTree(market, mainMarket); err != nil {
		t.Fatal(err)
	}
	gitTest(t, main, "init", "-b", "main")
	writeTestJSON(t, filepath.Join(main, ".claude", "settings.json"), map[string]any{"extraKnownMarketplaces": map[string]any{"acme": map[string]any{"source": Source{Kind: "directory", Path: "./market"}}}, "enabledPlugins": map[string]bool{"alpha@acme": true}})
	gitTest(t, main, "add", ".")
	gitTest(t, main, "commit", "-m", "fixture")
	worktree := filepath.Join(t.TempDir(), "worktree")
	gitTest(t, main, "worktree", "add", "-b", "topic", worktree)
	manager, err := NewManager(Options{UserDir: m.opts.UserDir, ManagedDir: m.opts.ManagedDir, WorkDir: worktree, Trusted: true})
	if err != nil {
		t.Fatal(err)
	}
	report, err := manager.SyncStartup(t.Context())
	if err != nil || len(report.Installed) != 1 || len(report.Errors) != 0 {
		t.Fatalf("worktree startup=%+v %v", report, err)
	}
	known, _ := manager.MarketplaceList()
	if !samePath(known["acme"].Source.Path, mainMarket) {
		t.Fatalf("marketplace resolved inside worktree: %+v", known["acme"])
	}
}

func TestStartupProjectExternalSourcesRequireExplicitInstall(t *testing.T) {
	m, market, _ := startupFixture(t)
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Acme"}, "plugins": []any{map[string]any{"name": "alpha", "source": Source{Kind: "git", URL: "https://example.invalid/alpha.git"}}}})
	startupDeclaration(t, m, "project", market, nil)
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Errors) != 0 || len(report.Skipped) != 1 || len(report.Installed) != 0 {
		t.Fatalf("external project source=%+v %v", report, err)
	}
	if _, err := os.Stat(filepath.Join(m.pluginDir, "installed_plugins.json")); !os.IsNotExist(err) {
		t.Fatal("external project plugin was automatically installed")
	}
}

func TestStartupGlobalDisableAndManagedForceControlActualUpdates(t *testing.T) {
	m, market, plugin := startupFixture(t)
	on := true
	startupDeclaration(t, m, "user", market, &on)
	if report, err := m.SyncStartup(t.Context()); err != nil || len(report.Errors) != 0 {
		t.Fatalf("initial=%+v %v", report, err)
	}
	rows, _ := m.List()
	old := rows[0].InstallPath
	os.WriteFile(filepath.Join(plugin, "changed.txt"), []byte("update"), 0600)
	t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Updated) != 0 || len(report.Errors) != 0 {
		t.Fatalf("disabled updates=%+v %v", report, err)
	}
	rows, _ = m.List()
	if rows[0].InstallPath != old {
		t.Fatal("disabled background traffic changed installed version")
	}
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"env": map[string]string{"FORCE_AUTOUPDATE_PLUGINS": "1"}})
	report, err = m.SyncStartup(t.Context())
	if err != nil || len(report.Updated) != 1 || len(report.Errors) != 0 {
		t.Fatalf("managed force=%+v %v", report, err)
	}
}

func TestStartupInlineCatalogFormattingIsNotANewSource(t *testing.T) {
	m, _, _ := startupFixture(t)
	path, fields, _ := m.settings("user")
	fields["extraKnownMarketplaces"] = json.RawMessage(`{"inline-team":{"source":{"source":"settings","name":"inline-team","owner":{ "email":"test@example.invalid", "name":"Team" },"plugins":[]}}}`)
	writeTestJSON(t, path, fields)
	first, err := m.SyncStartup(t.Context())
	if err != nil || len(first.Registered) != 1 || len(first.Errors) != 0 {
		t.Fatalf("first inline sync=%+v %v", first, err)
	}
	fields["extraKnownMarketplaces"] = json.RawMessage(`{"inline-team":{"source":{"source":"settings","name":"inline-team","owner":{"name":"Team","email":"test@example.invalid"},"plugins":[]}}}`)
	writeTestJSON(t, path, fields)
	again, err := m.SyncStartup(t.Context())
	if err != nil || len(again.Registered)+len(again.Errors) != 0 {
		t.Fatalf("formatted inline source was re-registered: %+v %v", again, err)
	}
}

func TestStartupManagedMarketplaceReplacesLowerSourceAtEveryCurrentScope(t *testing.T) {
	m, market, plugin := startupFixture(t)
	if err := os.WriteFile(filepath.Join(plugin, "origin.txt"), []byte("lower-source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"user", "project"} {
		if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	newMarket := filepath.Join(t.TempDir(), "managed-market")
	if _, err := copyTree(market, newMarket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newMarket, "plugins", "alpha", "origin.txt"), []byte("managed-source"), 0600); err != nil {
		t.Fatal(err)
	}
	writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{
		"extraKnownMarketplaces": map[string]any{"acme": map[string]any{"source": Source{Kind: "directory", Path: newMarket}, "autoUpdate": false}},
		"enabledPlugins":         map[string]bool{"alpha@acme": true},
	})
	authority := hooks.NewManager(false, m.opts.WorkDir)
	authority.ConfigureClaudeSettings(m.opts.WorkDir, m.opts.UserDir, true)
	authority.ConfigureClaudeManagedSettings(m.opts.ManagedDir)
	authority.ConfigureClaudePlugins(nil)
	if selected, err := authority.ClaudePlugins(); err == nil || len(selected) > 0 {
		t.Fatalf("required marketplace loaded a lower source before reconciliation: %+v %v", selected, err)
	}
	report, err := m.SyncStartup(t.Context())
	if err != nil || len(report.Registered) != 1 || len(report.Installed) != 2 || len(report.Errors) != 0 {
		t.Fatalf("managed rebase=%+v %v", report, err)
	}
	rows, err := m.List()
	if err != nil || len(rows) != 2 {
		t.Fatalf("rebased rows=%+v %v", rows, err)
	}
	for _, row := range rows {
		data, err := os.ReadFile(filepath.Join(row.InstallPath, "origin.txt"))
		if err != nil || string(data) != "managed-source" || row.MarketplaceSource == nil || !samePath(row.MarketplaceSource.Path, newMarket) {
			t.Fatalf("%s retained lower source: %q %+v %v", row.Scope, data, row.MarketplaceSource, err)
		}
	}
	if selected := runtimePlugins(t, m); len(selected) != 1 || selected[0].Root() != rows[0].InstallPath {
		// Both scopes share the same content-addressed snapshot.
		t.Fatalf("managed runtime=%+v", selected)
	}
}

func TestStartupOfficialEnableDeclaresMarketplaceWithoutFetchingDuringPlan(t *testing.T) {
	m, _, _ := startupFixture(t)
	path, fields, _ := m.settings("user")
	fields["enabledPlugins"] = json.RawMessage(`{"review@claude-plugins-official":true}`)
	writeTestJSON(t, path, fields)
	plan, err := m.startupPlan(t.Context())
	entry, exists := plan.Markets["claude-plugins-official"]
	if err != nil || !exists || entry.Source.Kind != "github" || entry.Source.Repo != "anthropics/claude-plugins-official" {
		t.Fatalf("implicit official declaration=%+v %v", entry, err)
	}
	if _, err := os.Stat(m.pluginDir); !os.IsNotExist(err) {
		t.Fatal("plan inspection fetched or created a plugin store")
	}
}
