package permission

import (
	"context"
	"strings"
	"testing"
)

func TestDontAskAllowsOnlyPreapprovedAndReadOnlyCalls(t *testing.T) {
	rules := DefaultRules()
	rules.SetPolicy("write", LevelAsk)
	rules.SetPolicy("edit", LevelAllow)
	rules.SetPolicy("delete", LevelDeny)
	manager := NewManager(rules, true)
	manager.SetDontAsk(true)

	prompts := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		prompts++
		return DecisionAllow, nil
	})

	assertDecision := func(tool string, args map[string]any, wantAllowed bool, reason string) {
		t.Helper()
		response, err := manager.Check(context.Background(), tool, args)
		if err != nil {
			t.Fatalf("%s check returned an interactive error: %v", tool, err)
		}
		if response == nil || response.Allowed != wantAllowed {
			t.Fatalf("%s response = %+v, want allowed=%v", tool, response, wantAllowed)
		}
		if reason != "" && !strings.Contains(response.Reason, reason) {
			t.Fatalf("%s reason = %q, want substring %q", tool, response.Reason, reason)
		}
	}

	assertDecision("write", map[string]any{"file_path": "main.go"}, false, "dontAsk")
	assertDecision("edit", map[string]any{"file_path": "main.go"}, true, "")
	assertDecision("delete", map[string]any{"path": "main.go"}, false, "configuration")
	assertDecision("bash", map[string]any{"command": "git status --short && git diff --stat"}, true, "")
	assertDecision("bash", map[string]any{"command": "touch generated.txt"}, false, "dontAsk")
	if prompts != 0 {
		t.Fatalf("dontAsk opened %d interactive prompts", prompts)
	}
}

func TestDontAskHonorsRunAndSkillGrantsButNotElevatedBash(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	manager.SetDontAsk(true)
	manager.SetRunToolRules([]string{"write", "bash"}, nil)

	response, err := manager.Check(
		context.Background(), "write",
		map[string]any{"file_path": "generated.go"},
	)
	if err != nil || response == nil || !response.Allowed {
		t.Fatalf("run-preapproved write = %+v, %v", response, err)
	}

	response, err = manager.CheckWithTemporaryToolRules(
		context.Background(), "edit",
		map[string]any{"file_path": "generated.go"},
		[]string{"edit"},
		nil,
	)
	if err != nil || response == nil || !response.Allowed {
		t.Fatalf("skill-preapproved edit = %+v, %v", response, err)
	}

	response, err = manager.Check(
		context.Background(), "bash",
		map[string]any{"command": "git push --force origin main"},
	)
	if err != nil || response == nil || response.Allowed ||
		!strings.Contains(response.Reason, "dontAsk") {
		t.Fatalf("elevated Bash escaped dontAsk floor: %+v, %v", response, err)
	}
}

func TestDontAskStatePropagatesToExistingScopedManager(t *testing.T) {
	base := NewManager(DefaultRules(), true)
	scoped := base.WithPolicyOverrides(nil)
	prompts := 0
	base.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		prompts++
		return DecisionAllow, nil
	})
	base.SetDontAsk(true)

	response, err := scoped.Check(
		context.Background(), "write",
		map[string]any{"file_path": "blocked.go"},
	)
	if err != nil || response == nil || response.Allowed ||
		!scoped.IsDontAsk() {
		t.Fatalf("scoped dontAsk response = %+v, %v enabled=%v",
			response, err, scoped.IsDontAsk())
	}
	if prompts != 0 {
		t.Fatalf("scoped dontAsk opened %d interactive prompts", prompts)
	}
}

func TestDontAskToggleClearsReusableSessionAuthority(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	args := map[string]any{"file_path": "main.go"}
	manager.RememberWithArgs("write", args, DecisionAllowSession)
	manager.SetDontAsk(true)

	response, err := manager.Check(context.Background(), "write", args)
	if err != nil || response == nil || response.Allowed {
		t.Fatalf("stale session approval survived dontAsk transition: %+v, %v", response, err)
	}
}
