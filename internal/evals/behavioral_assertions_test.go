package evals

import "testing"

// passingResult builds a Result that satisfies the base metrics (agent
// succeeded, verification green) so tests can isolate the behavioral-assertion
// metrics. changed is the workspace-relative changed-files set.
func passingResult(output string, changed []string) Result {
	return Result{
		Agent:        CommandResult{Success: true, OutputPreview: output},
		Verification: []CommandResult{{Success: true}},
		ChangedFiles: changed,
		Journal: &JournalSummary{
			Path: "j", ToolCalls: 1,
			FilesRead: []string{"x.go"}, FilesEdited: []string{"y.go"},
			VerificationCommands: []string{"go test"},
		},
	}
}

// TestBehavioralAssertionsSatisfied pins the fix: Status must fail a
// scenario when a DECLARED behavioral assertion metric is false, not just
// when Agent.Success/verification exit codes say otherwise. Before this,
// runScenario computed Status purely from Agent.Success + verification exit
// codes, so a genuine no-op on a delivered_state=green trap scenario (whose
// verification passes BY CONSTRUCTION) still got Status="passed" — the
// exact no-op-reward hole the v0.92.0 behavioral-assertions feature was
// built to close, just not wired into the default pass/fail gate.
func TestBehavioralAssertionsSatisfied(t *testing.T) {
	tests := []struct {
		name    string
		metrics map[string]bool
		want    bool
	}{
		{"no assertions declared", map[string]bool{"task_completed": true}, true},
		{"answer_contains_required true", map[string]bool{"answer_contains_required": true}, true},
		{"answer_contains_required false", map[string]bool{"answer_contains_required": false}, false},
		{"required_files_changed false (the no-op trap)", map[string]bool{"required_files_changed": false}, false},
		{"protected_files_unchanged false (trap violation)", map[string]bool{"protected_files_unchanged": false}, false},
		{"all three true", map[string]bool{"answer_contains_required": true, "required_files_changed": true, "protected_files_unchanged": true}, true},
		{"one of three false", map[string]bool{"answer_contains_required": true, "required_files_changed": false, "protected_files_unchanged": true}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := behavioralAssertionsSatisfied(tt.metrics); got != tt.want {
				t.Errorf("behavioralAssertionsSatisfied(%v) = %v, want %v", tt.metrics, got, tt.want)
			}
		})
	}
}

// TestScenarioPassed_RequiresBehavioralAssertions is the end-to-end version
// of the fix: a Result that satisfies Agent.Success + verification (as a
// no-op on a green trap fixture always does, by construction) must NOT
// count as passed if a declared behavioral assertion metric is false.
func TestScenarioPassed_RequiresBehavioralAssertions(t *testing.T) {
	base := passingResult("no changes needed", nil) // Agent.Success=true, verification green

	// No assertions declared at all — passes on the pre-existing conditions.
	if !scenarioPassed(base) {
		t.Error("a scenario with no declared assertions should pass on Agent.Success + verification alone")
	}

	// A no-op on a scenario that DECLARES file_must_change — the exact
	// no-op-reward hole. verification_passed/task_completed are irrelevant;
	// this must fail.
	noopWithAssertion := base
	noopWithAssertion.Metrics = map[string]bool{"required_files_changed": false}
	if scenarioPassed(noopWithAssertion) {
		t.Fatal("a no-op that fails its declared file_must_change assertion must NOT pass, even though Agent.Success and verification are both green")
	}

	// Pre-existing behavior unaffected: Agent failure still fails regardless
	// of assertions.
	failedAgent := base
	failedAgent.Agent.Success = false
	failedAgent.Metrics = map[string]bool{"required_files_changed": true}
	if scenarioPassed(failedAgent) {
		t.Fatal("Agent.Success=false must still fail the scenario")
	}
}

func TestScoreScenario_AssertionsAbsentWhenNotDeclared(t *testing.T) {
	scenario := Scenario{MaxToolCalls: 10}
	m := scoreScenario(scenario, passingResult("done", []string{"main.go"}))

	for _, k := range []string{"answer_contains_required", "required_files_changed", "protected_files_unchanged"} {
		if _, ok := m[k]; ok {
			t.Fatalf("metric %q must be ABSENT when the scenario does not declare it (keeps existing baselines); map=%v", k, m)
		}
	}
	if len(m) != 10 {
		t.Fatalf("base metric count = %d, want exactly 10 with no conditional metrics; map=%v", len(m), m)
	}
}

func TestScoreScenario_AnswerMustContain(t *testing.T) {
	scenario := Scenario{MaxToolCalls: 10, AnswerMustContain: []string{"internal/billing/invoice.go"}}

	// Satisfied — answer names the caller (case-insensitive match).
	got := scoreScenario(scenario, passingResult(
		"FormatLegacyID is still used by INTERNAL/BILLING/INVOICE.GO, so I left it in place.", nil))
	if !got["answer_contains_required"] {
		t.Fatal("answer_contains_required should be true when the answer names the required caller")
	}

	// Violated — the wrong/vague answer omits the required caller.
	got = scoreScenario(scenario, passingResult("It looked unused, so I removed it.", nil))
	if got["answer_contains_required"] {
		t.Fatal("answer_contains_required should be false when the answer omits the required caller")
	}
}

func TestScoreScenario_FileMustChange_CatchesNoOp(t *testing.T) {
	scenario := Scenario{MaxToolCalls: 10, FileMustChange: []string{"internal/retry/policy.go"}}

	// The no-op trap: the agent "succeeded" and verification is green (the
	// fixture ships green), but NOTHING was changed. This is exactly the case
	// the assertion closes.
	noop := passingResult("Looks fine, no changes needed.", nil)
	m := scoreScenario(scenario, noop)
	if !m["verification_passed"] || !m["task_completed"] {
		t.Fatal("precondition: a no-op still satisfies verification_passed + task_completed — that is why the assertion is needed")
	}
	if m["required_files_changed"] {
		t.Fatal("required_files_changed must be FALSE for a no-op that left the target file untouched")
	}

	// Satisfied via trailing-path-segment match (fixture roots vary).
	ok := scoreScenario(scenario, passingResult("Refactored the helper.", []string{"work/internal/retry/policy.go"}))
	if !ok["required_files_changed"] {
		t.Fatal("required_files_changed should be true when the target file is modified (trailing-segment match)")
	}
}

func TestScoreScenario_FileMustNotChange_CatchesTrapViolation(t *testing.T) {
	scenario := Scenario{MaxToolCalls: 10, FileMustNotChange: []string{"internal/legacy/helper.go"}}

	// Respected — the protected file is left alone.
	if !scoreScenario(scenario, passingResult("Still used; left in place.", []string{"docs/notes.md"}))["protected_files_unchanged"] {
		t.Fatal("protected_files_unchanged should be true when the protected file is untouched")
	}

	// Violated — the agent edited/removed the protected symbol's file.
	if scoreScenario(scenario, passingResult("Removed it.", []string{"internal/legacy/helper.go"}))["protected_files_unchanged"] {
		t.Fatal("protected_files_unchanged must be false when the protected file is modified")
	}
}

func TestPathPresent_Matching(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		decl    string
		want    bool
	}{
		{"exact", []string{"internal/retry/policy.go"}, "internal/retry/policy.go", true},
		{"declared is trailing segment of changed", []string{"work/internal/retry/policy.go"}, "internal/retry/policy.go", true},
		{"changed is trailing segment of declared", []string{"policy.go"}, "internal/retry/policy.go", true},
		{"sibling test file is not a match", []string{"internal/retry/policy_test.go"}, "internal/retry/policy.go", false},
		{"unrelated file", []string{"internal/billing/invoice.go"}, "internal/retry/policy.go", false},
		{"empty changed set", nil, "internal/retry/policy.go", false},
		{"empty declaration", []string{"internal/retry/policy.go"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathPresent(tc.changed, tc.decl); got != tc.want {
				t.Fatalf("pathPresent(%v, %q) = %v, want %v", tc.changed, tc.decl, got, tc.want)
			}
		})
	}
}

func TestAnswerContainsAll(t *testing.T) {
	if !answerContainsAll("alpha BETA gamma", []string{"alpha", "beta"}) {
		t.Fatal("all substrings present (case-insensitive) should be true")
	}
	if answerContainsAll("alpha gamma", []string{"alpha", "beta"}) {
		t.Fatal("a missing substring should be false")
	}
	if !answerContainsAll("anything", []string{"  "}) {
		t.Fatal("blank required substrings are skipped → vacuously true")
	}
}

func TestValidate_GreenScenarioRequiresAssertion(t *testing.T) {
	validScenario := func() Scenario {
		return Scenario{
			ID: "s", Category: "c", Difficulty: "small", Prompt: "p", Fixture: "f",
			ExpectedBehaviors: []string{"b"}, VerificationCommands: []string{"go test ./..."},
			SuccessCriteria: []string{"s"}, FailureSignals: []string{"f"}, MaxToolCalls: 5,
		}
	}
	manifest := func(s Scenario) *Manifest {
		return &Manifest{Version: 1, Name: "t", Metrics: []string{"x"}, Scenarios: []Scenario{s}}
	}

	// Green without any assertion → rejected (would reward a no-op).
	green := validScenario()
	green.DeliveredState = "green"
	if err := manifest(green).Validate(); err == nil {
		t.Fatal("green scenario without a behavioral assertion must fail validation")
	}

	// Green WITH an assertion → accepted.
	green.AnswerMustContain = []string{"foo"}
	if err := manifest(green).Validate(); err != nil {
		t.Fatalf("green scenario with an assertion should validate: %v", err)
	}

	// Green with ONLY a negative assertion (file_must_not_change) → rejected: a
	// no-op trivially satisfies "don't touch X", so it still rewards doing
	// nothing. A green scenario needs a POSITIVE assertion.
	greenNeg := validScenario()
	greenNeg.DeliveredState = "green"
	greenNeg.FileMustNotChange = []string{"internal/legacy/helper.go"}
	if err := manifest(greenNeg).Validate(); err == nil {
		t.Fatal("green scenario with only a negative assertion must fail validation (no-op still passes)")
	}

	// Green with a negative AND a positive assertion → accepted.
	greenNeg.FileMustChange = []string{"internal/x/y.go"}
	if err := manifest(greenNeg).Validate(); err != nil {
		t.Fatalf("green scenario with a positive assertion should validate: %v", err)
	}

	// Red without an assertion → fine (gated by verification flipping red→green).
	red := validScenario()
	red.DeliveredState = "red"
	if err := manifest(red).Validate(); err != nil {
		t.Fatalf("red scenario without an assertion should validate: %v", err)
	}
}
