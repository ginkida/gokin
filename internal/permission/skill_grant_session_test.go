package permission

import (
	"context"
	"testing"
)

// A skill grant is turn-scoped; the user's "Deny for session" is not. The grant
// branch delegates to a per-call scoped manager, and that scope used to get a
// brand-new session cache — so the decision was written to a throwaway and the
// very next identical call asked again.
func TestSkillGrantKeepsSessionDecisionOnTheParentManager(t *testing.T) {
	rules := DefaultRules()
	rules.SetPolicy("bash", LevelAsk)
	manager := NewManager(rules, true)

	prompts := 0
	manager.SetPromptHandler(func(_ context.Context, _ *Request) (Decision, error) {
		prompts++
		return DecisionDenySession, nil
	})

	ctx := context.Background()
	args := map[string]any{"command": "sudo rm -rf /tmp/scratch"}
	grants := []string{"bash"}

	first, err := manager.CheckWithTemporaryToolGrants(ctx, "bash", args, grants)
	if err != nil {
		t.Fatal(err)
	}
	if first.Allowed {
		t.Fatalf("elevated call was allowed despite a session deny: %+v", first)
	}

	// The same call under the same grant must be answered from the remembered
	// decision, not by asking the user a second time.
	second, err := manager.CheckWithTemporaryToolGrants(ctx, "bash", args, grants)
	if err != nil {
		t.Fatal(err)
	}
	if second.Allowed {
		t.Fatalf("session deny did not stick: %+v", second)
	}
	if prompts != 1 {
		t.Fatalf("prompted %d times, want 1 — the session decision was discarded", prompts)
	}

	// It is the SESSION's decision, so it also applies without the skill grant.
	third, err := manager.Check(ctx, "bash", args)
	if err != nil {
		t.Fatal(err)
	}
	if third.Allowed || prompts != 1 {
		t.Fatalf("decision did not reach the parent manager: %+v prompts=%d", third, prompts)
	}
}
