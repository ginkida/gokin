package agent

import (
	"context"
	"testing"

	"gokin/internal/permission"
)

func TestApprovedPlanStepPermissionsAreScopedNotDisabled(t *testing.T) {
	rules := permission.DefaultRules()
	rules.SetPolicy("ssh", permission.LevelAsk)
	rules.SetPolicy("mcp_admin", permission.LevelAsk)
	base := permission.NewManager(rules, true)
	prompts := 0
	base.SetPromptHandler(func(context.Context, *permission.Request) (permission.Decision, error) {
		prompts++
		return permission.DecisionDeny, nil
	})

	scoped := approvedPlanStepPermissions(base)
	if scoped == nil || !scoped.IsEnabled() {
		t.Fatal("approved plan step disabled the permission system")
	}
	for _, call := range []struct {
		name string
		args map[string]any
	}{
		{name: "write", args: map[string]any{"file_path": "local.go"}},
		{name: "run_tests", args: map[string]any{"package": "./..."}},
	} {
		resp, err := scoped.Check(context.Background(), call.name, call.args)
		if err != nil || !resp.Allowed {
			t.Fatalf("local plan capability %s = %+v, %v", call.name, resp, err)
		}
	}
	if prompts != 0 {
		t.Fatalf("local plan operations unexpectedly prompted %d times", prompts)
	}

	for _, name := range []string{"bash", "ssh", "mcp_admin", "git_commit", "delete"} {
		resp, err := scoped.Check(context.Background(), name, map[string]any{"command": "danger"})
		if err != nil {
			t.Fatalf("%s check: %v", name, err)
		}
		if resp.Allowed {
			t.Errorf("approved plan step broadened %s authority: %+v", name, resp)
		}
	}
	if prompts != 5 {
		t.Fatalf("unscoped operations did not retain prompt policy; prompts=%d", prompts)
	}
}

func TestApprovedPlanStepPermissionsPreserveBaseHardDeny(t *testing.T) {
	rules := permission.DefaultRules()
	for _, tool := range []string{"write", "edit", "run_tests", "bash"} {
		rules.SetPolicy(tool, permission.LevelDeny)
	}
	scoped := approvedPlanStepPermissions(permission.NewManager(rules, true))

	for _, tool := range []string{"write", "edit", "run_tests", "bash"} {
		resp, err := scoped.Check(context.Background(), tool, map[string]any{
			"file_path": "local.go",
			"command":   "rm local.go",
		})
		if err != nil {
			t.Fatalf("%s check: %v", tool, err)
		}
		if resp.Allowed {
			t.Fatalf("approved plan step weakened explicit deny for %s", tool)
		}
	}
}

func TestApprovedPlanStepPermissionsPreserveExplicitYOLO(t *testing.T) {
	base := permission.NewManager(nil, false)
	scoped := approvedPlanStepPermissions(base)
	resp, err := scoped.Check(context.Background(), "ssh", map[string]any{"host": "example"})
	if err != nil || !resp.Allowed {
		t.Fatalf("explicitly disabled permissions were not preserved: %+v, %v", resp, err)
	}
}
