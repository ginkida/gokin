package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
)

type fakeAppForHooks struct {
	*fakeAppForMCP
	mgr *hooks.Manager
}

func TestHooksCommandClaudeSettingsDiagnosticsAndDisablePolicy(t *testing.T) {
	manager := hooks.NewManager(true, t.TempDir())
	user := t.TempDir()
	manager.ConfigureClaudeSettings("", user, false)
	app := &fakeAppForHooks{fakeAppForMCP: &fakeAppForMCP{}, mgr: manager}
	path := filepath.Join(user, "settings.json")
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0","async":"invalid"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := (&HooksCommand{}).Execute(t.Context(), nil, app)
	if err != nil || !strings.Contains(out, "settings hooks could not be loaded") || !strings.Contains(out, "async must be a boolean") {
		t.Fatalf("invalid settings diagnostic=%q %v", out, err)
	}
	if err := os.WriteFile(path, []byte(`{"disableAllHooks":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = (&HooksCommand{}).Execute(t.Context(), nil, app)
	if err != nil || !strings.Contains(out, "disabled by disableAllHooks") {
		t.Fatalf("disable policy diagnostic=%q %v", out, err)
	}
}

func (f *fakeAppForHooks) GetHooksManager() *hooks.Manager { return f.mgr }

func TestHooksCommand_NilAndEmpty(t *testing.T) {
	cmd := &HooksCommand{}

	out, err := cmd.Execute(context.Background(), nil, &fakeAppForHooks{fakeAppForMCP: &fakeAppForMCP{}})
	if err != nil || !strings.Contains(out, "unavailable") {
		t.Fatalf("nil manager: %q, %v", out, err)
	}

	mgr := hooks.NewManager(true, t.TempDir())
	out, err = cmd.Execute(context.Background(), nil, &fakeAppForHooks{fakeAppForMCP: &fakeAppForMCP{}, mgr: mgr})
	if err != nil || !strings.Contains(out, "No hooks configured") {
		t.Fatalf("empty manager: %q, %v", out, err)
	}
}

func TestHooksCommand_ListShowsSourceAndSemantics(t *testing.T) {
	mgr := hooks.NewManager(true, t.TempDir())
	mgr.AddHook(&hooks.Hook{
		Name: "no-prod-writes", Type: hooks.PreTool, ToolName: "write",
		Command: "./guard.sh", Enabled: true, FailOnError: true, Source: "project",
	})
	mgr.AddHook(&hooks.Hook{
		Name: "done-gate", Type: hooks.Stop,
		Command: "go test ./...", Enabled: true, FailOnError: true, Source: "config",
	})
	mgr.AddHook(&hooks.Hook{
		Name: "disabled-one", Type: hooks.PostTool,
		Command: "echo x", Enabled: false,
	})

	cmd := &HooksCommand{}
	out, err := cmd.Execute(context.Background(), nil, &fakeAppForHooks{fakeAppForMCP: &fakeAppForMCP{}, mgr: mgr})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, needle := range []string{
		"3 configured",
		"no-prod-writes", "[project]", "blocks the tool call on failure",
		"done-gate", "[config]", "asks the agent to continue on failure",
		"○ disabled-one",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("list missing %q:\n%s", needle, out)
		}
	}
}

func TestHooksCommandManagedOnlyMarksExcludedNativeHooks(t *testing.T) {
	manager := hooks.NewManager(true, t.TempDir())
	managed := t.TempDir()
	manager.ConfigureClaudeManagedSettings(managed)
	path := filepath.Join(managed, "managed-settings.json")
	if err := os.WriteFile(path, []byte(`{"allowManagedHooksOnly":true,"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.AddHook(&hooks.Hook{Name: "excluded", Type: hooks.PreTool, Enabled: true, Command: "exit 0"})
	app := &fakeAppForHooks{fakeAppForMCP: &fakeAppForMCP{}, mgr: manager}
	out, err := (&HooksCommand{}).Execute(t.Context(), nil, app)
	if err != nil || !strings.Contains(out, "managed hooks only (allowManagedHooksOnly)") || !strings.Contains(out, "○ excluded") || !strings.Contains(out, "claude:managed:") {
		t.Fatalf("managed display=%q %v", out, err)
	}
}

func TestHooksCommandReportsExplicitPluginWithoutCallingItManaged(t *testing.T) {
	manager := hooks.NewManager(false, t.TempDir())
	plugin, user := t.TempDir(), t.TempDir()
	manager.ConfigureClaudeSettings("", user, false)
	manager.ConfigureClaudePlugins([]string{plugin})
	path := filepath.Join(plugin, "hooks", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"exit 0"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := (&HooksCommand{}).Execute(t.Context(), nil, &fakeAppForHooks{fakeAppForMCP: &fakeAppForMCP{}, mgr: manager})
	if err != nil || !strings.Contains(out, "session plugin hooks active; automatic hooks disabled") || !strings.Contains(out, "claude:plugin:") || strings.Contains(out, "managed hooks active") {
		t.Fatalf("plugin diagnostics=%q %v", out, err)
	}
}
