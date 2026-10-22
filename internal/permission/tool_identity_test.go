package permission

import (
	"context"
	"testing"
)

func TestMCPSourceIdentityDenyWinsOverAliasAllowAndSkillGrant(t *testing.T) {
	const alias = "mcp__plugin_suite_db__long_alias_0123"
	const source = "mcp__plugin_suite_db__a_much_longer_original_tool_name_than_a_provider_can_accept"
	ctx := ContextWithToolIdentity(t.Context(), alias, source)
	rules := DefaultRules()
	rules.SetPolicy(alias, LevelAllow)
	rules.SetPolicy(source, LevelDeny)
	manager := NewManager(rules, true)
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) {
		t.Fatal("hard source deny prompted")
		return DecisionAllow, nil
	})
	response, err := manager.CheckWithTemporaryToolGrants(ctx, alias, nil, []string{alias, source})
	if err != nil || response.Allowed {
		t.Fatalf("source deny lost: %+v %v", response, err)
	}
	if !TemporaryToolRulesDenyWithIdentity(ctx, []string{source}, alias, nil, "") {
		t.Fatal("source skill deny did not match shortened alias")
	}
	manager.SetRunToolRules(nil, []string{source})
	manager.SetEnabled(false)
	if response, err := manager.Check(ctx, alias, nil); err != nil || response.Allowed {
		t.Fatalf("bypass erased source run deny: %+v %v", response, err)
	}
}

func TestMCPIdentitySessionApprovalCannotTravelToAnotherSource(t *testing.T) {
	const alias = "shared_alias"
	manager := NewManager(DefaultRules(), true)
	prompts := 0
	manager.SetPromptHandler(func(context.Context, *Request) (Decision, error) { prompts++; return DecisionAllowSession, nil })
	for _, source := range []string{"mcp__one__query", "mcp__one__query", "mcp__two__query"} {
		response, err := manager.Check(ContextWithToolIdentity(t.Context(), alias, source), alias, nil)
		if err != nil || !response.Allowed {
			t.Fatalf("permission=%+v %v", response, err)
		}
	}
	if prompts != 2 {
		t.Fatalf("approval crossed source boundary: prompts=%d", prompts)
	}
}
