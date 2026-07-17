package tools

import (
	"testing"

	"google.golang.org/genai"
)

// v0.100.99: the recovery whack-a-mole is closed at the root — every
// side-effect-free tool (IsParallelSafeTool) plus the idempotent state/query
// tools earn graceful recovery (hints + force-finalize) on a truly-identical
// loop, instead of a hard turn-kill. Field reports hit it as read-only
// inspection, todo, memorize, then `memory` (a memory-update task looping on
// the kv-store tool). Recovery never re-executes the tool, so the only tools
// that keep the immediate hard abort are genuine mutations.
func TestStagnationRecovery_IdempotentToolsAreRecoverySafe(t *testing.T) {
	// The reported tool + its adjacent idempotent state/query siblings.
	recoverable := []string{
		"memory", "memorize", "skill", "mcp_admin", "todo",
		// Side-effect-free (IsParallelSafeTool) — sampled.
		"read", "grep", "git_status", "go_search", "review_changes", "web_fetch",
		// Idempotent inspection not in parallelSafeTools.
		"check_impact", "go_diagnostics",
	}
	for _, tool := range recoverable {
		if !isStagnationRecoverySafe(tool) {
			t.Errorf("isStagnationRecoverySafe(%s) = false, want true", tool)
		}
		if got := maxStagnationRecoveryAttempts(tool); got < 2 {
			t.Errorf("maxStagnationRecoveryAttempts(%s) = %d, want >=2 (graceful recovery)", tool, got)
		}
		call := []*genai.FunctionCall{{Name: tool, Args: map[string]any{"x": 1}}}
		if !shouldAttemptStagnationRecovery(call, 0) {
			t.Errorf("%s must earn a recovery hint at attempt 0", tool)
		}
		// All recovery-safe tools except edit are force-finalize eligible.
		if _, readOnly, ok := stagnationHintBudget(call); !ok || !readOnly {
			t.Errorf("%s hint budget = readOnly:%v ok:%v, want ok + readOnly (force-finalize)", tool, readOnly, ok)
		}
	}

	// Genuine mutations keep the immediate hard abort (recovery could mask a
	// broken mutating loop / a dishonest success claim).
	for _, tool := range []string{"write", "delete", "move", "copy", "git_commit", "git_add", "ssh", "refactor", "batch"} {
		if isStagnationRecoverySafe(tool) {
			t.Errorf("%s must NOT be recovery-safe (genuine mutation)", tool)
		}
		if got := maxStagnationRecoveryAttempts(tool); got != 0 {
			t.Errorf("maxStagnationRecoveryAttempts(%s) = %d, want 0 (hard abort backstop)", tool, got)
		}
	}

	// edit stays the special case: 1 hint, NO force-finalize.
	if got := maxStagnationRecoveryAttempts("edit"); got != 1 {
		t.Errorf("maxStagnationRecoveryAttempts(edit) = %d, want 1", got)
	}
	editCall := []*genai.FunctionCall{{Name: "edit", Args: map[string]any{"file_path": "a.go", "old_string": "x"}}}
	if _, editReadOnly, _ := stagnationHintBudget(editCall); editReadOnly {
		t.Error("edit must stay force-finalize EXCLUDED")
	}
}
