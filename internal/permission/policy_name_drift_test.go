package permission_test

import (
	"sort"
	"testing"

	"gokin/internal/config"
	"gokin/internal/permission"
	"gokin/internal/tools"
	"gokin/internal/toolschema"
)

// The permission table is the safety layer, so a name in it that matches no
// tool is worse than dead weight: it reads as coverage. The mirror image —
// a real tool entered under a slightly wrong name — silently falls through to
// DefaultPolicy instead of the level someone deliberately wrote down, and
// nothing anywhere fails. This test is an external package on purpose:
// internal/tools imports internal/permission, so only a *_test package can
// hold both the registry and the table.
func TestDefaultRulesNameOnlyRegisteredTools(t *testing.T) {
	known := map[string]bool{}
	for _, tool := range tools.DefaultRegistry(t.TempDir()).List() {
		known[tool.Name()] = true
	}
	for _, name := range tools.DefaultLazyRegistry(t.TempDir()).Names() {
		known[name] = true
	}
	if len(known) == 0 {
		t.Fatal("registry produced no tool names")
	}
	// Deferred publication adds virtual control tools without putting them in
	// execution registries. Verify their actual owner-bound lookup rather than
	// exempting arbitrary names from the drift check.
	registry := tools.DefaultRegistry(t.TempDir())
	ctx := tools.ContextWithDeferredCatalog(t.Context(), registry, registry.GeminiTools())
	for _, envelope := range toolschema.New(true).Publish(registry.GeminiTools()) {
		for _, declaration := range envelope.FunctionDeclarations {
			if known[declaration.Name] {
				continue
			}
			virtual, ok := tools.DeferredMetaTool(ctx, registry, declaration.Name)
			if !ok || virtual.Declaration().Name != declaration.Name {
				t.Fatalf("unimplemented virtual tool %q", declaration.Name)
			}
			known[declaration.Name] = true
		}
	}

	cfg := config.DefaultConfig().Permission
	for source, rules := range map[string]*permission.Rules{
		"DefaultRules":  permission.DefaultRules(),
		"DefaultConfig": permission.NewRulesFromConfig(cfg.DefaultPolicy, cfg.Rules),
	} {
		var phantom []string
		for name := range rules.ToolPolicies {
			if !known[name] {
				phantom = append(phantom, name)
			}
		}
		sort.Strings(phantom)
		if len(phantom) > 0 {
			t.Errorf("%s names unregistered tools: %v", source, phantom)
		}
		if policy, present := rules.ToolPolicies["tool_search"]; !present || policy != permission.LevelAllow {
			t.Errorf("%s must allow discovery explicitly", source)
		}
		if policy, present := rules.ToolPolicies["use_tool"]; !present || policy != permission.LevelAsk {
			t.Errorf("%s must give use_tool a neutral ask policy; only its resolved target grants authority", source)
		}
	}
}
