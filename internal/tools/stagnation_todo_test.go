package tools

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

// Field report (v0.100.95): a Kimi K3 turn that narrates and updates its
// checklist frequently hit `executor stagnation: tool pattern "todo:"
// repeated 5 times consecutively` — the whole turn killed with no recovery.
// Two defects: (1) todo had an EMPTY default fingerprint, so DISTINCT list
// updates (real progress — checking off a task, adding the next) all
// collapsed to one pattern; (2) todo had zero recovery budget, so even a
// genuinely-identical loop hard-aborted instead of getting a graceful hint.
func TestStagnationFingerprint_TodoKeysOnListContent(t *testing.T) {
	listA := map[string]any{"todos": []any{
		map[string]any{"content": "wire adapter", "status": "in_progress", "active_form": "Wiring"},
	}}
	listB := map[string]any{"todos": []any{
		map[string]any{"content": "wire adapter", "status": "completed", "active_form": "Wiring"},
		map[string]any{"content": "add tests", "status": "in_progress", "active_form": "Adding"},
	}}

	fpA := stagnationFingerprint("todo", listA)
	fpB := stagnationFingerprint("todo", listB)
	if fpA == "" || fpB == "" {
		t.Fatalf("todo fingerprints should key on content, got %q / %q", fpA, fpB)
	}
	if fpA == fpB {
		t.Fatalf("distinct todo updates collapsed to one fingerprint: %q", fpA)
	}
	// A truly-identical re-write must still collapse (that IS a loop).
	if again := stagnationFingerprint("todo", listA); again != fpA {
		t.Fatalf("identical todo produced different fingerprints: %q vs %q", fpA, again)
	}
}

// A genuinely-identical todo loop earns graceful hints AND a force-finalize
// phase (v0.100.96 field report round 2 — a model wrote the identical list 5×,
// ignored both hints, and hit the scary hard-abort error card). todo is a
// side-effect-free idempotent no-op, so "STOP re-planning, write your answer
// NOW" salvages the turn into an honest response instead of a dead error.
func TestStagnationRecovery_TodoIsHintPlusFinalizeEligible(t *testing.T) {
	if got := maxStagnationRecoveryAttempts("todo"); got != 2 {
		t.Fatalf("maxStagnationRecoveryAttempts(todo) = %d, want 2", got)
	}
	todoCall := []*genai.FunctionCall{{Name: "todo", Args: map[string]any{"todos": []any{}}}}
	// 2 hints + 2 finalize = recovers through attempt 3, aborts at 4.
	for attempt := 0; attempt < 4; attempt++ {
		if !shouldAttemptStagnationRecovery(todoCall, attempt) {
			t.Fatalf("todo must recover at attempt %d (2 hints + 2 finalize)", attempt)
		}
	}
	if shouldAttemptStagnationRecovery(todoCall, 4) {
		t.Fatal("todo budget must be bounded — attempt 4 aborts")
	}
	// Force-finalize eligible (unlike edit): the turn ends in an answer, not
	// a dead error card.
	_, readOnly, ok := stagnationHintBudget(todoCall)
	if !ok || !readOnly {
		t.Fatalf("todo hint budget = readOnly:%v ok:%v, want ok + readOnly (force-finalize)", readOnly, ok)
	}

	msg := buildStagnationRecoveryMessage("todo", todoCall[0].Args, 5)
	if !strings.Contains(msg, "Do not call todo again") || !strings.Contains(msg, "DO the next task") {
		t.Fatalf("todo recovery hint should push execution, got: %q", msg)
	}
	// edit stays hint-then-abort (no finalize) — the contrast that proves the
	// exclusion is edit-only.
	editCall := []*genai.FunctionCall{{Name: "edit", Args: map[string]any{"file_path": "a.go", "old_string": "x"}}}
	if _, editReadOnly, _ := stagnationHintBudget(editCall); editReadOnly {
		t.Fatal("edit must stay force-finalize-EXCLUDED (dishonest success claim risk)")
	}
}
