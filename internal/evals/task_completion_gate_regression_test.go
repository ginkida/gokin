package evals

import "testing"

// A successful agent process is not the same thing as a completed task. The
// CLI can exit zero after producing no usable answer (empty output, the smart
// fallback marker, or the explicit empty-model placeholder). These shapes are
// already rejected by the task_completed metric; the scenario status must use
// the same ground truth or green verification can turn a non-answer into a
// false "passed" eval.
func TestScenarioPassedRejectsSuccessfulProcessWithoutDeliveredAnswer(t *testing.T) {
	for _, output := range []string{
		"",
		"   ",
		"[Auto] no response available",
		"[Model returned an empty response — try rephrasing your request]",
	} {
		result := Result{
			Agent:        CommandResult{Success: true, OutputPreview: output},
			Verification: []CommandResult{{Success: true}},
			Metrics:      map[string]bool{"task_completed": false},
		}
		if scenarioPassed(result) {
			t.Fatalf("scenarioPassed accepted non-answer %q with green verification", output)
		}
	}
}
