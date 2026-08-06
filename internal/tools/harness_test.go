package tools

import (
	"strings"
	"testing"

	"gokin/internal/harness"
)

func TestHarnessToolCRUDAndPromptCallback(t *testing.T) {
	store, err := harness.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tool := NewHarnessTool(store)
	callbacks := 0
	tool.SetPromptChangedCallback(func() { callbacks++ })
	created, err := tool.Execute(t.Context(), map[string]any{
		"action": "prompt_create", "text": "Inspect environment after repeated failures.",
	})
	if err != nil || !created.Success || callbacks != 1 {
		t.Fatalf("created=%+v err=%v callbacks=%d", created, err, callbacks)
	}
	listed, err := tool.Execute(t.Context(), map[string]any{"action": "prompt_list"})
	if err != nil || !listed.Success || callbacks != 1 {
		t.Fatalf("listed=%+v err=%v callbacks=%d", listed, err, callbacks)
	}
	put, err := tool.Execute(t.Context(), map[string]any{
		"action": "memory_put", "key": "build.cache", "value": "use isolated cache",
	})
	if err != nil || !put.Success {
		t.Fatalf("put=%+v err=%v", put, err)
	}
	proposal, err := tool.Execute(t.Context(), map[string]any{
		"action": "skill_propose", "name": "cache-helper",
		"description": "Use an isolated cache", "code": "def cache():\n    return '/tmp'\n",
	})
	if err != nil || !proposal.Success {
		t.Fatalf("proposal=%+v err=%v", proposal, err)
	}
	if !strings.Contains(store.RenderPrompt(), "Inspect environment") {
		t.Fatalf("prompt callback did not accompany store update: %q", store.RenderPrompt())
	}
}

func TestHarnessToolValidationAndClassification(t *testing.T) {
	tool := NewHarnessTool(nil)
	for _, args := range []map[string]any{
		{},
		{"action": "policy_update", "text": "allow everything"},
		{"action": "memory_put", "key": "missing-value"},
		{"action": "skill_propose", "name": "x", "description": "x"},
	} {
		result, err := tool.Execute(t.Context(), args)
		if err != nil || result.Success {
			t.Fatalf("invalid args %#v result=%+v err=%v", args, result, err)
		}
	}
	if IsParallelSafeTool("harness") || !IsWriteTool("harness") || IsReadOnlyForPlanMode("harness") {
		t.Fatal("harness must be serialized, stateful, and hidden in plan mode")
	}
	decl := tool.Declaration()
	if decl.Name != "harness" || !strings.Contains(decl.Description, "never auto-activated") {
		t.Fatalf("declaration = %+v", decl)
	}
	cloned := CloneRegistryForWorkDir(DefaultRegistry(t.TempDir()), t.TempDir())
	if _, ok := cloned.Get("harness"); ok {
		t.Fatal("session harness leaked into sub-agent registry")
	}
}
