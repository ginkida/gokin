package permission_test

import (
	"sort"
	"testing"

	"gokin/internal/permission"
	"gokin/internal/tools"
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

	var phantom []string
	for name := range permission.DefaultRules().ToolPolicies {
		if !known[name] {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Fatalf("DefaultRules names tools that are not registered: %v", phantom)
	}
}
