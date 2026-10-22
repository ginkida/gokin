package permission

import (
	"context"
	"testing"
)

func TestHookDecisionPreservesDeniesAndBindsToEffectiveArguments(t *testing.T) {
	rules := DefaultRules()
	rules.SetPolicy("bash", LevelAsk)
	manager := NewManager(rules, true)
	prompts := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) { prompts++; return DecisionAllow, nil })
	args := map[string]any{"command": "printf value"}
	ctx := ContextWithHookDecision(t.Context(), "bash", args, "allow")
	response, err := manager.CheckWithTemporaryToolRules(ctx, "bash", args, nil, nil)
	if err != nil || !response.Allowed || prompts != 0 {
		t.Fatalf("allow=%+v err=%v prompts=%d", response, err, prompts)
	}
	changed := map[string]any{"command": "printf other"}
	response, err = manager.CheckWithTemporaryToolRules(ctx, "bash", changed, nil, nil)
	if err != nil || !response.Allowed || prompts != 1 {
		t.Fatalf("changed=%+v err=%v prompts=%d", response, err, prompts)
	}
	response, err = manager.CheckWithTemporaryToolRules(ctx, "bash", args, nil, []string{"bash"})
	if err != nil || response.Allowed {
		t.Fatalf("skill deny overridden: %+v %v", response, err)
	}
	rules.SetPolicy("bash", LevelDeny)
	manager.SetRules(rules)
	response, err = manager.CheckWithTemporaryToolRules(ctx, "bash", args, nil, nil)
	if err != nil || response.Allowed {
		t.Fatalf("config deny overridden: %+v %v", response, err)
	}
}

func TestHookAskOverridesReadOnlyPolicyAndDontAskCannotApproveIt(t *testing.T) {
	manager := NewManager(DefaultRules(), true)
	prompts := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) { prompts++; return DecisionAllow, nil })
	args := map[string]any{"file_path": "file.go"}
	ctx := ContextWithHookDecision(t.Context(), "read", args, "ask")
	response, err := manager.CheckWithTemporaryToolRules(ctx, "read", args, []string{"read"}, nil)
	if err != nil || !response.Allowed || prompts != 1 {
		t.Fatalf("ask=%+v err=%v prompts=%d", response, err, prompts)
	}
	manager.SetDontAsk(true)
	response, err = manager.Check(ctx, "read", args)
	if err != nil || response.Allowed || prompts != 1 {
		t.Fatalf("dontAsk=%+v err=%v prompts=%d", response, err, prompts)
	}
}
