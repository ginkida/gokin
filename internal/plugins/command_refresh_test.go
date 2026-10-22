package plugins

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func commandRefreshFixture(t *testing.T) (*Manager, string, string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	m, market, plugin := startupFixture(t)
	counter := filepath.Join(t.TempDir(), "runs")
	command := "printf x >> '" + counter + "'; printf '%s\\n' '" + plugin + "'"
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": Source{Kind: "command", Command: command}}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(command))
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(counter, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return m, market, plugin, counter
}

func TestCommandRefreshRunsOnceDespiteOrdinaryUpdateControls(t *testing.T) {
	for _, control := range []string{"DISABLE_UPDATES", "DISABLE_AUTOUPDATER", "settings"} {
		t.Run(control, func(t *testing.T) {
			m, _, plugin, counter := commandRefreshFixture(t)
			if control == "settings" {
				path, fields, _ := m.settings("user")
				fields["env"], _ = json.Marshal(map[string]string{"DISABLE_AUTOUPDATER": "1"})
				writeTestJSON(t, path, fields)
			} else {
				t.Setenv(control, "1")
			}
			rows, _ := m.List()
			old := rows[0].InstallPath
			if err := os.WriteFile(filepath.Join(plugin, "new.txt"), []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			report, err := m.RefreshCommandSources(t.Context())
			if err != nil || len(report.Updated) != 1 || len(report.Errors)+len(report.Skipped) != 0 {
				t.Fatalf("refresh=%+v %v", report, err)
			}
			rows, _ = m.List()
			if rows[0].InstallPath == old {
				t.Fatal("command output not adopted")
			}
			if report, err = m.RefreshCommandSources(t.Context()); err != nil || len(report.Updated)+len(report.Errors) != 0 {
				t.Fatalf("second=%+v %v", report, err)
			}
			data, _ := os.ReadFile(counter)
			if string(data) != "x" {
				t.Fatalf("runs=%q", data)
			}
			// A fresh host/session performs its own once-only check.
			next, err := NewManager(m.opts)
			if err != nil {
				t.Fatal(err)
			}
			if report, err = next.RefreshCommandSources(t.Context()); err != nil || len(report.Updated)+len(report.Errors) != 0 {
				t.Fatalf("fresh=%+v %v", report, err)
			}
			data, _ = os.ReadFile(counter)
			if string(data) != "xx" {
				t.Fatalf("session runs=%q", data)
			}
		})
	}
}

func TestCommandRefreshHonorsNonessentialTrafficAndEffectiveDisable(t *testing.T) {
	for _, mode := range []string{"env", "settings", "managed-env", "disabled", "managed-disabled", "commands-disabled", "provenance", "unaccepted", "changed-command", "changed-mode"} {
		t.Run(mode, func(t *testing.T) {
			m, market, _, counter := commandRefreshFixture(t)
			path, fields, _ := m.settings("user")
			switch mode {
			case "env":
				t.Setenv("CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "1")
				t.Setenv("FORCE_AUTOUPDATE_PLUGINS", "1")
			case "settings":
				fields["env"], _ = json.Marshal(map[string]string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1", "FORCE_AUTOUPDATE_PLUGINS": "1"})
				writeTestJSON(t, path, fields)
			case "managed-env":
				writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"env": map[string]string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}})
			case "disabled":
				if _, err := m.SetEnabled(t.Context(), "alpha", "user", false); err != nil {
					t.Fatal(err)
				}
			case "managed-disabled":
				writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"enabledPlugins": map[string]bool{"alpha@acme": false}})
			case "commands-disabled":
				writeTestJSON(t, filepath.Join(m.opts.ManagedDir, "managed-settings.json"), map[string]any{"disableCommandPluginSources": true})
			case "changed-command", "changed-mode":
				known, _ := m.marketplaces()
				var marketRecord Marketplace
				json.Unmarshal(known["acme"], &marketRecord)
				catalog, err := m.readCatalog("acme", marketRecord)
				if err != nil {
					t.Fatal(err)
				}
				var source Source
				json.Unmarshal(catalog.Plugins[0].Source, &source)
				if mode == "changed-command" {
					source.Command = "printf UNREVIEWED >> '" + counter + "'"
				} else {
					source.Mode = "link"
				}
				writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": source}}})
			case "provenance", "unaccepted":
				doc, installs, _ := m.installs()
				var record map[string]json.RawMessage
				json.Unmarshal(installs["alpha@acme"][0], &record)
				if mode == "provenance" {
					delete(record, "gokinMarketplaceSource")
				} else {
					delete(record, "gokinApprovedCommands")
				}
				installs["alpha@acme"][0], _ = json.Marshal(record)
				doc["plugins"], _ = json.Marshal(installs)
				writeTestJSON(t, filepath.Join(m.pluginDir, "installed_plugins.json"), doc)
			}
			report, err := m.RefreshCommandSources(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Installed)+len(report.Updated) != 0 {
				t.Fatalf("excluded refresh=%+v", report)
			}
			if strings.HasPrefix(mode, "changed-") || mode == "provenance" || mode == "unaccepted" {
				if len(report.Skipped) != 1 || !strings.Contains(report.Skipped[0], "explicit plugin update") {
					t.Fatalf("missing review diagnostic=%+v", report)
				}
			}
			data, _ := os.ReadFile(counter)
			if len(data) != 0 {
				t.Fatalf("excluded command ran: %q", data)
			}
		})
	}
}

func TestCommandRestoreMissingCacheAndReloadRetry(t *testing.T) {
	m, _, _, counter := commandRefreshFixture(t)
	rows, _ := m.List()
	path := rows[0].InstallPath
	// Retire the cache outside its expected location; no source files are touched.
	if err := os.Rename(path, path+"-retired"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DISABLE_AUTOUPDATER", "1")
	report, err := m.SyncDeclared(t.Context())
	if err != nil || len(report.Installed) != 1 || len(report.Errors)+len(report.Skipped) != 0 {
		t.Fatalf("restore=%+v %v", report, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("cache not restored", err)
	}
	if report, err = m.RefreshCommandSources(t.Context()); err != nil || len(report.Updated)+len(report.Installed) != 0 {
		t.Fatalf("duplicate=%+v %v", report, err)
	}
	data, _ := os.ReadFile(counter)
	if string(data) != "x" {
		t.Fatalf("runs=%q", data)
	}
	if err := os.Rename(path, path+"-retired-again"); err != nil {
		t.Fatal(err)
	}
	report, err = m.RestoreCommandSources(t.Context())
	if err != nil || len(report.Installed) != 1 {
		t.Fatalf("reload restore=%+v %v", report, err)
	}
	data, _ = os.ReadFile(counter)
	if string(data) != "xx" {
		t.Fatalf("reload runs=%q", data)
	}
}

func TestCommandRefreshFailurePreservesWorkingCopyAndContinuesOthers(t *testing.T) {
	m, market, _, counter := commandRefreshFixture(t)
	beta := filepath.Join(market, "plugins", "beta")
	writeTestJSON(t, filepath.Join(beta, ".claude-plugin", "plugin.json"), map[string]any{"name": "beta"})
	known, _ := m.marketplaces()
	var marketRecord Marketplace
	json.Unmarshal(known["acme"], &marketRecord)
	catalog, err := m.readCatalog("acme", marketRecord)
	if err != nil {
		t.Fatal(err)
	}
	var alphaSource Source
	json.Unmarshal(catalog.Plugins[0].Source, &alphaSource)
	betaCommand := "printf '%s\\n' '" + beta + "'"
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": alphaSource}, map[string]any{"name": "beta", "source": Source{Kind: "command", Command: betaCommand}}}})
	sum := sha256.Sum256([]byte(betaCommand))
	if _, err := m.Install(t.Context(), "beta", InstallOptions{AcceptCommands: []string{hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	// Alpha's accepted command now returns no plugin path: replacing its producer
	// directory with an empty directory makes output validation fail.
	rows, _ := m.List()
	old := rows[0].InstallPath
	alphaRoot := filepath.Join(market, "plugins", "alpha")
	if err := os.Rename(alphaRoot, alphaRoot+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(alphaRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, "new.txt"), []byte("updated"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := m.RefreshCommandSources(t.Context())
	if err != nil || len(report.Errors) != 1 || len(report.Updated) != 1 {
		t.Fatalf("refresh=%+v %v", report, err)
	}
	rows, _ = m.List()
	if rows[0].InstallPath != old || !rows[0].Enabled {
		t.Fatal("failed source replaced working copy")
	}
	data, _ := os.ReadFile(counter)
	if string(data) != "x" {
		t.Fatalf("runs=%q", data)
	}
}

func TestCommandRefreshCancellationAndScopePrecedence(t *testing.T) {
	m, _, _, counter := commandRefreshFixture(t)
	before, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.RefreshCommandSources(ctx); err == nil {
		t.Fatal("canceled refresh succeeded")
	}
	after, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("canceled refresh wrote state")
	}
	var record InstallRecord
	_, installs, _ := m.installs()
	json.Unmarshal(installs["alpha@acme"][0], &record)
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{Scope: "local", AcceptCommands: record.ApprovedCommands}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(counter, nil, 0600)
	if _, err := m.RefreshCommandSources(t.Context()); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(counter)
	if string(data) != "x" {
		t.Fatalf("hidden scope also refreshed: %q", data)
	}
}

func TestCommandRefreshActiveCancellationPreservesRegistry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX command fixture")
	}
	m, market, plugin := startupFixture(t)
	gate, started := filepath.Join(t.TempDir(), "gate"), filepath.Join(t.TempDir(), "started")
	command := "if [ -f '" + gate + "' ]; then touch '" + started + "'; sleep 30; fi; printf '%s\\n' '" + plugin + "'"
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": Source{Kind: "command", Command: command}}}})
	if _, err := m.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(command))
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := m.RefreshCommandSources(ctx); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted command did not start")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled command reported success")
		}
	// runCommand has a 2s WaitDelay for inherited output pipes. Allow that
	// bounded drain plus scheduler/registry cleanup under a parallel build.
	case <-time.After(5 * time.Second):
		t.Fatal("canceled command did not stop")
	}
	after, _ := os.ReadFile(filepath.Join(m.pluginDir, "installed_plugins.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("canceled command published registry")
	}
}

func TestCommandRefreshSkipsLinkInsideItsProducer(t *testing.T) {
	m, market, producer, command, _ := linkFixture(t)
	counter := filepath.Join(t.TempDir(), "runs")
	command = "printf x >> '" + counter + "'; " + command
	writeTestJSON(t, filepath.Join(market, ".claude-plugin", "marketplace.json"), map[string]any{"name": "acme", "owner": map[string]string{"name": "Test"}, "plugins": []any{map[string]any{"name": "alpha", "source": Source{Kind: "command", Command: command, Mode: "link"}}}})
	sum := sha256.Sum256([]byte(command))
	if _, err := m.Install(t.Context(), "alpha", InstallOptions{AcceptCommands: []string{hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(counter, nil, 0600); err != nil {
		t.Fatal(err)
	}
	m.opts.WorkDir = producer
	report, err := m.RefreshCommandSources(t.Context())
	if err != nil || len(report.Updated)+len(report.Errors) != 0 {
		t.Fatalf("own producer refresh=%+v %v", report, err)
	}
	data, _ := os.ReadFile(counter)
	if len(data) != 0 {
		t.Fatal("unloaded link source command was executed")
	}
}
