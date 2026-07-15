package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/memory"

	"google.golang.org/genai"
)

func newGlobalScopeMemoryTool(t *testing.T) (*MemoryTool, *memory.Store) {
	t.Helper()
	store, err := memory.NewStore(t.TempDir(), t.TempDir(), 100)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tool := NewMemoryTool()
	tool.SetStore(store)
	return tool, store
}

func TestMemoryGlobalScopeRequiresExplicitOptIn(t *testing.T) {
	tool, store := newGlobalScopeMemoryTool(t)

	args := map[string]any{
		"action":  "remember",
		"scope":   "global",
		"key":     "editor-preference",
		"content": "Prefer compact diffs",
	}
	if err := tool.Validate(args); err != nil {
		t.Fatalf("structurally valid global request failed Validate: %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "memory.allow_global") {
		t.Fatalf("denied global remember = %+v, want loud opt-in failure", result)
	}
	if result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockPermission {
		t.Fatalf("denied global remember lost permission policy metadata: %+v", result.PolicyBlock)
	}
	if got := store.List(false); len(got) != 0 {
		t.Fatalf("denied global remember silently wrote %d entries: %+v", len(got), got)
	}
}

func TestMemoryGlobalScopeOptInStoresAndRetrievesGlobal(t *testing.T) {
	tool, store := newGlobalScopeMemoryTool(t)
	tool.SetAllowGlobal(true)

	remembered, err := tool.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"scope":   "global",
		"key":     "editor-preference",
		"content": "Prefer compact diffs",
	})
	if err != nil || !remembered.Success {
		t.Fatalf("remember global: result=%+v err=%v", remembered, err)
	}
	entries := store.List(false)
	if len(entries) != 1 || entries[0].Type != memory.MemoryGlobal {
		t.Fatalf("stored entries = %+v, want one real global entry", entries)
	}

	recalled, err := tool.Execute(context.Background(), map[string]any{
		"action":       "recall",
		"key":          "editor-preference",
		"project_only": false,
	})
	if err != nil || !recalled.Success || !strings.Contains(recalled.Content, "compact diffs") {
		t.Fatalf("recall opted-in global: result=%+v err=%v", recalled, err)
	}

	// scope is intentionally not a read filter. Reject it loudly instead of
	// silently returning project memories when the caller meant global recall.
	invalid, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"scope":  "global",
	})
	if err != nil {
		t.Fatal(err)
	}
	if invalid.Success || !strings.Contains(invalid.Error, "project_only=false") {
		t.Fatalf("read-time scope argument was silently ignored: %+v", invalid)
	}
}

func TestMemoryDefaultReadsCannotCrossProjectBoundary(t *testing.T) {
	tool, store := newGlobalScopeMemoryTool(t)
	global := memory.NewEntry("GLOBAL-SECRET-SENTINEL", memory.MemoryGlobal).WithKey("private-user-note")
	if err := store.Add(global); err != nil {
		t.Fatal(err)
	}

	// Omitted project_only is intentionally safe, including the exact-key fast
	// path (Store.Get itself would otherwise fall through to global scope).
	recalled, err := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"key":    "private-user-note",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(recalled.Content, "GLOBAL-SECRET-SENTINEL") {
		t.Fatalf("default recall leaked global content: %+v", recalled)
	}

	for _, action := range []string{"recall", "list"} {
		result, execErr := tool.Execute(context.Background(), map[string]any{
			"action":       action,
			"project_only": false,
		})
		if execErr != nil {
			t.Fatalf("%s: %v", action, execErr)
		}
		if result.Success || !strings.Contains(result.Error, "memory.allow_global") {
			t.Fatalf("%s project_only=false = %+v, want loud global denial", action, result)
		}
		if result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockPermission {
			t.Fatalf("%s denial lost permission policy metadata: %+v", action, result.PolicyBlock)
		}
	}
	result, execErr := tool.Execute(context.Background(), map[string]any{
		"action": "recall",
		"scope":  "global",
	})
	if execErr != nil {
		t.Fatal(execErr)
	}
	if result.Success || result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockPermission {
		t.Fatalf("scope=global recall did not fail closed: %+v", result)
	}

	for _, args := range []map[string]any{
		{"action": "forget", "key": "private-user-note"},
		{"action": "forget", "id": global.ID},
		// An ID miss must not fall through to Store.Remove's key lookup. That
		// used to delete the global entry by passing its key through the id field.
		{"action": "forget", "id": "private-user-note"},
		{"action": "feedback", "key": "private-user-note", "success": true},
		{"action": "feedback", "id": global.ID, "success": true},
	} {
		result, execErr := tool.Execute(context.Background(), args)
		if execErr != nil {
			t.Fatalf("%v: %v", args, execErr)
		}
		if result.Success {
			t.Fatalf("global mutation unexpectedly succeeded with opt-in off: args=%v result=%+v", args, result)
		}
	}
	if entry, ok := store.GetByID(global.ID); !ok || entry.Content != "GLOBAL-SECRET-SENTINEL" {
		t.Fatal("denied global mutation changed or removed the entry")
	}
}

func TestMemoryCloneSharesLiveGlobalPolicy(t *testing.T) {
	tool, _ := newGlobalScopeMemoryTool(t)
	tool.SetAllowGlobal(true)
	clone := CloneToolForWorkDir(tool, "").(*MemoryTool)

	tool.SetAllowGlobal(false)
	result, err := clone.Execute(context.Background(), map[string]any{
		"action":  "remember",
		"scope":   "global",
		"content": "must not survive revocation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "memory.allow_global") {
		t.Fatalf("pre-existing clone retained stale global access: %+v", result)
	}
	if result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockPermission {
		t.Fatalf("revoked clone denial lost permission policy metadata: %+v", result.PolicyBlock)
	}
}

func TestMemoryGlobalDenialReachesExecutorPolicyObserver(t *testing.T) {
	tool, _ := newGlobalScopeMemoryTool(t)
	registry := NewRegistry()
	if err := registry.Register(tool); err != nil {
		t.Fatal(err)
	}
	executor := NewExecutor(registry, nil, time.Second)
	var observed *PolicyBlock
	executor.SetHandler(&ExecutionHandler{
		OnToolPolicyBlocked: func(_ string, block PolicyBlock) {
			copy := block
			observed = &copy
		},
	})

	result := executor.executeTool(context.Background(), &genai.FunctionCall{
		Name: "memory",
		Args: map[string]any{
			"action":  "remember",
			"scope":   "global",
			"content": "must stay blocked",
		},
	})
	if result.Success || result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockPermission {
		t.Fatalf("executor result lost memory policy block: %+v", result)
	}
	if observed == nil || observed.Kind != PolicyBlockPermission {
		t.Fatalf("OnToolPolicyBlocked observed %+v, want permission block", observed)
	}
}
