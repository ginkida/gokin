package tools

import (
	"context"
	"strings"
	"testing"
)

// TestTodoTool_SingleInProgressEnforcement pins the Claude Code invariant:
// at most ONE todo may be in_progress at a time. Multiple in_progress
// markers produce ambiguous UX (which task is the agent actually working
// on?) and let a model pretend it's multitasking when tool dispatch is
// serial. Reject hard.
func TestTodoTool_SingleInProgressEnforcement(t *testing.T) {
	cases := []struct {
		name      string
		statuses  []string
		wantError bool
	}{
		// Zero in_progress is fine (all pending at start, or all complete at end).
		{"all_pending", []string{"pending", "pending", "pending"}, false},
		{"all_completed", []string{"completed", "completed", "completed"}, false},
		{"mix_no_in_progress", []string{"completed", "pending", "pending"}, false},

		// Exactly one in_progress — the normal working state.
		{"one_in_progress", []string{"completed", "in_progress", "pending"}, false},
		{"only_in_progress", []string{"in_progress"}, false},

		// Multiple in_progress — REJECT.
		{"two_in_progress", []string{"in_progress", "in_progress"}, true},
		{"three_in_progress", []string{"in_progress", "in_progress", "in_progress"}, true},
		{"mixed_two_in_progress", []string{"completed", "in_progress", "in_progress", "pending"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewTodoTool()
			todos := buildTodoArgs(tc.statuses)

			result, err := tool.Execute(context.Background(), todos)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			if tc.wantError {
				if result.Success {
					t.Errorf("expected rejection for %v; got success", tc.statuses)
				}
				if !strings.Contains(result.Error, "in_progress") {
					t.Errorf("rejection should explain the constraint, got: %q", result.Error)
				}
				if !strings.Contains(result.Error, "one") {
					t.Errorf("rejection should name the 'exactly one' rule, got: %q", result.Error)
				}
			} else {
				if !result.Success {
					t.Errorf("expected success for %v; got error: %q", tc.statuses, result.Error)
				}
			}
		})
	}
}

// TestTodoTool_RejectionDoesNotMutateState ensures a rejected call doesn't
// leave the internal list in a partial state — the agent must be able to
// retry cleanly with the full corrected list.
func TestTodoTool_RejectionDoesNotMutateState(t *testing.T) {
	tool := NewTodoTool()

	// Seed with a valid single-in-progress list.
	good := buildTodoArgs([]string{"completed", "in_progress", "pending"})
	result, _ := tool.Execute(context.Background(), good)
	if !result.Success {
		t.Fatalf("seed failed: %q", result.Error)
	}
	before := tool.GetItems()
	if len(before) != 3 {
		t.Fatalf("expected 3 items after seed; got %d", len(before))
	}

	// Attempt a bad update with two in_progress — must be rejected AND
	// must leave `before` intact.
	bad := buildTodoArgs([]string{"in_progress", "in_progress"})
	result, _ = tool.Execute(context.Background(), bad)
	if result.Success {
		t.Fatal("bad update should have been rejected")
	}

	after := tool.GetItems()
	if len(after) != len(before) {
		t.Errorf("rejected update mutated list length: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if before[i].Content != after[i].Content || before[i].Status != after[i].Status {
			t.Errorf("rejected update mutated item %d: before=%+v after=%+v", i, before[i], after[i])
		}
	}
}

// buildTodoArgs is a terse helper for the status-matrix table test. Creates
// a todos args payload matching the shape Execute expects.
func buildTodoArgs(statuses []string) map[string]any {
	items := make([]any, 0, len(statuses))
	for i, s := range statuses {
		items = append(items, map[string]any{
			"content":     "task " + string(rune('A'+i)),
			"active_form": "doing task " + string(rune('A'+i)),
			"status":      s,
		})
	}
	return map[string]any{"todos": items}
}
