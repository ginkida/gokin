package permission

import (
	"context"
	"reflect"
	"testing"
)

func TestParseTemporaryToolGrantListPreservesScopedBashRules(t *testing.T) {
	got, err := ParseTemporaryToolGrantList(
		"Read, Grep Bash(git status --short) Bash(git diff *) Read",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"read", "grep", "bash(git status --short)", "bash(git diff *)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grants = %#v, want %#v", got, want)
	}
}

func TestTemporaryToolGrantPatternAndPolicyFloors(t *testing.T) {
	ctx := context.Background()
	rules := DefaultRules()
	manager := NewManager(rules, true)

	promptCount := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		promptCount++
		return DecisionAllow, nil
	})
	grants := []string{"write", "bash(git status *)"}

	if response, err := manager.CheckWithTemporaryToolGrants(
		ctx, "write", map[string]any{"file_path": "main.go"}, grants,
	); err != nil || !response.Allowed || promptCount != 0 {
		t.Fatalf("temporary write grant = %+v, %v; prompts=%d", response, err, promptCount)
	}
	if response, err := manager.CheckWithTemporaryToolGrants(
		ctx, "bash", map[string]any{"command": "git status --short"}, grants,
	); err != nil || !response.Allowed || promptCount != 0 {
		t.Fatalf("matching bash grant = %+v, %v; prompts=%d", response, err, promptCount)
	}
	if response, err := manager.CheckWithTemporaryToolGrants(
		ctx, "bash", map[string]any{"command": "go test ./..."}, grants,
	); err != nil || !response.Allowed || promptCount != 1 {
		t.Fatalf("nonmatching bash grant = %+v, %v; prompts=%d", response, err, promptCount)
	}

	// A broad grant cannot bypass the elevated-action confirmation floor.
	if response, err := manager.CheckWithTemporaryToolGrants(
		ctx, "bash", map[string]any{"command": "git reset --hard HEAD~1"}, []string{"bash"},
	); err != nil || !response.Allowed || promptCount != 2 {
		t.Fatalf("elevated bash grant = %+v, %v; prompts=%d", response, err, promptCount)
	}

	// An explicit deny remains stronger than a temporary grant.
	rules.SetPolicy("write", LevelDeny)
	manager.SetRules(rules)
	if response, err := manager.CheckWithTemporaryToolGrants(
		ctx, "write", map[string]any{"file_path": "denied.go"}, []string{"write"},
	); err != nil || response.Allowed {
		t.Fatalf("denied temporary grant = %+v, %v", response, err)
	}
}

func TestTemporaryToolGrantHonorsParentSessionDeny(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	args := map[string]any{"file_path": "main.go"}
	manager.RememberWithArgs(
		"write",
		args,
		DecisionDenySession,
	)
	response, err := manager.CheckWithTemporaryToolGrants(
		context.Background(),
		"write",
		args,
		[]string{"write"},
	)
	if err != nil || response.Allowed || response.Decision != DecisionDenySession {
		t.Fatalf("session-denied temporary write grant = %+v, %v", response, err)
	}
}

func TestRunToolRulesApplyAcrossPermissionModesAndPatterns(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(DefaultRules(), true)
	allows, err := ParseTemporaryToolGrantList("Write Bash(git status *) Bash")
	if err != nil {
		t.Fatal(err)
	}
	denies, err := ParseTemporaryToolDenyList("Bash(git push *) mcp__*")
	if err != nil {
		t.Fatal(err)
	}
	manager.SetRunToolRules(allows, denies)

	promptCount := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		promptCount++
		return DecisionAllow, nil
	})
	if response, err := manager.Check(ctx, "write", map[string]any{"file_path": "main.go"}); err != nil || !response.Allowed || promptCount != 0 {
		t.Fatalf("run allow write = %+v, %v prompts=%d", response, err, promptCount)
	}
	if response, err := manager.Check(ctx, "bash", map[string]any{"command": "git status --short"}); err != nil || !response.Allowed || promptCount != 0 {
		t.Fatalf("run allow bash = %+v, %v prompts=%d", response, err, promptCount)
	}
	if response, err := manager.Check(ctx, "bash", map[string]any{"command": "git push origin main"}); err != nil || response.Allowed {
		t.Fatalf("scoped run deny = %+v, %v", response, err)
	}
	if response, err := manager.Check(ctx, "mcp__github__create_pr", nil); err != nil || response.Allowed {
		t.Fatalf("wildcard run deny = %+v, %v", response, err)
	}

	// bypassPermissions skips prompts, not an explicit run deny supplied by the
	// same invocation.
	manager.SetEnabled(false)
	if response, err := manager.Check(ctx, "bash", map[string]any{"command": "git push origin main"}); err != nil || response.Allowed {
		t.Fatalf("bypass overrode run deny = %+v, %v", response, err)
	}
}

func TestRunAllowPreservesElevatedBashAndSessionDenyFloors(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	manager.SetRunToolRules([]string{"bash", "write"}, nil)
	promptCount := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		promptCount++
		return DecisionAllow, nil
	})

	response, err := manager.Check(
		context.Background(), "bash",
		map[string]any{"command": "git reset --hard HEAD~1"},
	)
	if err != nil || !response.Allowed || promptCount != 1 {
		t.Fatalf("elevated run allow = %+v, %v prompts=%d", response, err, promptCount)
	}

	args := map[string]any{"file_path": "denied.go"}
	manager.RememberWithArgs("write", args, DecisionDenySession)
	response, err = manager.Check(context.Background(), "write", args)
	if err != nil || response.Allowed || response.Decision != DecisionDenySession {
		t.Fatalf("session deny lost to run allow = %+v, %v", response, err)
	}
}

func TestRunDenyWinsOverTurnScopedSkillGrant(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	manager.SetRunToolRules(nil, []string{"write"})

	response, err := manager.CheckWithTemporaryToolGrants(
		context.Background(),
		"write",
		map[string]any{"file_path": "blocked.go"},
		[]string{"write"},
	)
	if err != nil || response == nil || response.Allowed ||
		response.Decision != DecisionDeny {
		t.Fatalf("skill grant overrode run deny: response=%+v err=%v", response, err)
	}
}

func TestTurnScopedSkillDenyWinsOverGrantAndBypass(t *testing.T) {
	manager := NewManager(DefaultRules(), false)
	response, err := manager.CheckWithTemporaryToolRules(
		context.Background(),
		"bash",
		map[string]any{"command": "git push origin main"},
		[]string{"bash"},
		[]string{"bash(git push *)"},
	)
	if err != nil || response == nil || response.Allowed ||
		response.Decision != DecisionDeny {
		t.Fatalf("skill deny lost to grant/bypass: response=%+v err=%v", response, err)
	}

	response, err = manager.CheckWithTemporaryToolRules(
		context.Background(),
		"bash",
		map[string]any{"command": "git status --short"},
		[]string{"bash"},
		[]string{"bash(git push *)"},
	)
	if err != nil || response == nil || !response.Allowed {
		t.Fatalf("nonmatching scoped deny blocked call: response=%+v err=%v", response, err)
	}
}

func TestToolDenyRuleMatchesName(t *testing.T) {
	tests := []struct {
		rule string
		tool string
		want bool
	}{
		{rule: "write", tool: "write", want: true},
		{rule: "mcp__*", tool: "mcp__github__create_pr", want: true},
		{rule: "*", tool: "bash", want: true},
		{rule: "bash(git push *)", tool: "bash", want: false},
		{rule: "read", tool: "write", want: false},
	}
	for _, test := range tests {
		if got := ToolDenyRuleMatchesName(test.rule, test.tool); got != test.want {
			t.Errorf("ToolDenyRuleMatchesName(%q, %q) = %v, want %v",
				test.rule, test.tool, got, test.want)
		}
	}
}
