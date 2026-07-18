package app

import (
	"encoding/json"
	"strings"
	"testing"

	"gokin/internal/plan"
)

func TestSelectPlanProofStep_RequestedStepWins(t *testing.T) {
	steps := []*plan.Step{
		{ID: 1, Status: plan.StatusCompleted},
		{ID: 2, Status: plan.StatusInProgress},
	}
	got := selectPlanProofStep(steps, 2, 1)
	if got == nil || got.ID != 1 {
		t.Fatalf("selectPlanProofStep(current=2, requested=1) = %+v, want step 1", got)
	}
}

func TestSelectPlanProofStep_RequestedMissingReturnsNil(t *testing.T) {
	steps := []*plan.Step{
		{ID: 1, Status: plan.StatusInProgress},
	}
	if got := selectPlanProofStep(steps, 1, 99); got != nil {
		t.Fatalf("requested-missing must return nil, got %+v", got)
	}
}

func TestSelectPlanProofStep_CurrentStepUsedWhenNoRequest(t *testing.T) {
	steps := []*plan.Step{
		{ID: 1, Status: plan.StatusInProgress},
		{ID: 2, Status: plan.StatusCompleted, Evidence: []string{"diff"}},
	}
	got := selectPlanProofStep(steps, 2, 0)
	if got == nil || got.ID != 2 {
		t.Fatalf("current step should win over status/evidence tiers, got %+v", got)
	}
}

func TestSelectPlanProofStep_CurrentMissingFallsToActiveStatus(t *testing.T) {
	steps := []*plan.Step{
		{ID: 1, Status: plan.StatusCompleted, Evidence: []string{"diff"}},
		{ID: 2, Status: plan.StatusPaused},
	}
	got := selectPlanProofStep(steps, 99, 0)
	if got == nil || got.ID != 2 {
		t.Fatalf("missing current must fall through to active-status tier, got %+v", got)
	}
}

func TestSelectPlanProofStep_ActiveStatusPicksFirstMatch(t *testing.T) {
	steps := []*plan.Step{
		{ID: 1, Status: plan.StatusFailed},
		{ID: 2, Status: plan.StatusInProgress},
		nil,
		{ID: 3, Status: plan.StatusPaused},
	}
	got := selectPlanProofStep(steps, 0, 0)
	if got == nil || got.ID != 1 {
		t.Fatalf("status tier scans in slice order, want first active step (1), got %+v", got)
	}
}

func TestSelectPlanProofStep_EvidencePicksLastMatch(t *testing.T) {
	steps := []*plan.Step{
		{ID: 1, Status: plan.StatusCompleted, Evidence: []string{"old"}},
		nil,
		{ID: 2, Status: plan.StatusCompleted, VerificationNote: "verified"},
	}
	got := selectPlanProofStep(steps, 0, 0)
	if got == nil || got.ID != 2 {
		t.Fatalf("evidence tier scans in reverse, want last evidenced step (2), got %+v", got)
	}
}

func TestSelectPlanProofStep_WhitespaceNoteDoesNotCountAsEvidence(t *testing.T) {
	steps := []*plan.Step{
		{ID: 1, Status: plan.StatusCompleted, Evidence: []string{"real"}},
		{ID: 2, Status: plan.StatusCompleted, VerificationNote: "   "},
	}
	got := selectPlanProofStep(steps, 0, 0)
	if got == nil || got.ID != 1 {
		t.Fatalf("whitespace-only note must be ignored, want step 1, got %+v", got)
	}
}

func TestSelectPlanProofStep_FallbackFirstNonNil(t *testing.T) {
	steps := []*plan.Step{
		nil,
		{ID: 5, Status: plan.StatusPending},
		{ID: 6, Status: plan.StatusPending},
	}
	got := selectPlanProofStep(steps, 0, 0)
	if got == nil || got.ID != 5 {
		t.Fatalf("final fallback must return first non-nil step, got %+v", got)
	}
}

func TestSelectPlanProofStep_EmptyOrAllNil(t *testing.T) {
	if got := selectPlanProofStep(nil, 0, 0); got != nil {
		t.Fatalf("nil slice must return nil, got %+v", got)
	}
	if got := selectPlanProofStep([]*plan.Step{nil, nil}, 0, 0); got != nil {
		t.Fatalf("all-nil slice must return nil, got %+v", got)
	}
}

func TestPrettyProofJSON_RejectsNonObjects(t *testing.T) {
	for _, in := range []string{"", "   \n\t ", "{not json", "[1,2]", "42", `"str"`} {
		if got := prettyProofJSON(in); got != "" {
			t.Errorf("prettyProofJSON(%q) = %q, want empty", in, got)
		}
	}
}

func TestPrettyProofJSON_NullPassesThrough(t *testing.T) {
	// "null" unmarshals into a nil map without error and re-marshals as "null".
	if got := prettyProofJSON("null"); got != "null" {
		t.Errorf("prettyProofJSON(null) = %q, want %q", got, "null")
	}
}

func TestPrettyProofJSON_ValidObjectIndented(t *testing.T) {
	raw := `{"b":1,"a":"x"}`
	got := prettyProofJSON(raw)
	if got == "" {
		t.Fatal("valid object must not be rejected")
	}
	if !strings.Contains(got, "\n") {
		t.Errorf("output must be multi-line (indented), got %q", got)
	}
	// Map keys are sorted by encoding/json; both must survive a round-trip.
	var roundTrip map[string]any
	if err := json.Unmarshal([]byte(got), &roundTrip); err != nil {
		t.Fatalf("output must remain valid JSON: %v", err)
	}
	if roundTrip["a"] != "x" || roundTrip["b"] != float64(1) {
		t.Errorf("round-trip mismatch: %+v", roundTrip)
	}
}
