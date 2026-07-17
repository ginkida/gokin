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

// A genuinely-identical todo loop earns a graceful, actionable hint (budget 2,
// hint-then-abort like edit — NOT a hard turn-kill, and NOT force-finalize).
func TestStagnationRecovery_TodoIsHintEligibleNotFinalize(t *testing.T) {
	if got := maxStagnationRecoveryAttempts("todo"); got != 2 {
		t.Fatalf("maxStagnationRecoveryAttempts(todo) = %d, want 2", got)
	}
	todoCall := []*genai.FunctionCall{{Name: "todo", Args: map[string]any{"todos": []any{}}}}
	if !shouldAttemptStagnationRecovery(todoCall, 0) || !shouldAttemptStagnationRecovery(todoCall, 1) {
		t.Fatal("todo must earn its 2 hints")
	}
	if shouldAttemptStagnationRecovery(todoCall, 2) {
		t.Fatal("todo budget must be bounded — attempt 2 aborts (no force-finalize)")
	}
	// Must NOT be read-only (no force-finalize phase, same as edit).
	_, readOnly, ok := stagnationHintBudget(todoCall)
	if !ok || readOnly {
		t.Fatalf("todo hint budget = readOnly:%v ok:%v, want ok + NOT readOnly", readOnly, ok)
	}

	msg := buildStagnationRecoveryMessage("todo", todoCall[0].Args, 5)
	if !strings.Contains(msg, "Do not call todo again") || !strings.Contains(msg, "DO the next task") {
		t.Fatalf("todo recovery hint should push execution, got: %q", msg)
	}
}
