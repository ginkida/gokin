package goals

import (
	"strings"
	"testing"
	"time"
)

func TestGoalNeedsIndependentVerdictAndRetainsLastAchievement(t *testing.T) {
	now := time.Now()
	state, err := Set(State{}, "all required tests pass", now)
	if err != nil {
		t.Fatal(err)
	}
	state, err = Apply(state, Verdict{Verdict: "not_met", Reason: "The integration test has not run."}, true, 20, 5, now.Add(time.Second))
	if err != nil || state.Current.Status != Active || state.LastAchieved != nil {
		t.Fatal("partial work achieved the goal", err)
	}
	state, err = Apply(state, Verdict{Verdict: "met", Reason: "Both unit and integration test commands exited zero."}, true, 40, 10, now.Add(2*time.Second))
	if err != nil || state.Current.Status != Achieved || state.Current.Turns != 2 || state.Current.InputTokens != 60 {
		t.Fatal("verified completion missing", err)
	}
	state, err = Set(state, "a new complete condition", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	state = Clear(state, "user", now.Add(4*time.Second))
	if state.LastAchieved == nil || state.LastAchieved.Condition != "all required tests pass" {
		t.Fatal("clearing a replacement lost the last achievement")
	}
}

func TestGoalStallGuardKeepsConditionAndResumeResetsBaseline(t *testing.T) {
	state, err := Set(State{}, strings.Repeat("я", 4000), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < NoProgressLimit; i++ {
		state, err = Apply(state, Verdict{Verdict: "not_met", Reason: "Run the required check."}, false, 10, 2, time.Now())
		if err != nil {
			t.Fatal(err)
		}
	}
	if state.Current.Status != Active || !state.Current.Suspended {
		t.Fatal("stall either cleared the goal or kept spinning")
	}
	old := state.Current.ID
	state = Resume(state, time.Now())
	if state.Current.ID != old || state.Current.Suspended || state.Current.Turns != 0 || state.Current.InputTokens != 0 {
		t.Fatal("resume did not reset the measurement baseline")
	}
	if _, err := Set(state, strings.Repeat("я", 4001), time.Now()); err == nil {
		t.Fatal("oversized condition accepted")
	}
}

func TestGoalVerdictRejectsAmbiguousOrUnstructuredCompletion(t *testing.T) {
	for _, input := range []string{
		`{"verdict":"not_met","verdict":"met","reason":"claim"}`,
		`{"verdict":"met","reason":""}`,
		`{"verdict":"met","reason":"claim","tools":["write"]}`,
		`{"verdict":"met","reason":"claim"} {"verdict":"not_met"}`,
		"```json\n{\"verdict\":\"met\",\"reason\":\"claim\"}\n```",
		`{"verdict":"yes","reason":"claim"}`,
	} {
		if _, err := ParseVerdict(input); err == nil {
			t.Fatalf("accepted ambiguous completion: %s", input)
		}
	}
	if verdict, err := ParseVerdict(`{"verdict":"met","reason":"The observed command exited zero."}`); err != nil || verdict.Verdict != "met" {
		t.Fatal(verdict, err)
	}
}
