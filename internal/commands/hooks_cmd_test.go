package commands

import (
	"context"
	"strings"
	"testing"

	"gokin/internal/hooks"
)

type fakeAppForHooks struct {
	*fakeAppForMCP
	mgr *hooks.Manager
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
