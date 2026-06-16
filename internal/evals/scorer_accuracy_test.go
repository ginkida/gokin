package evals

import (
	"strings"
	"testing"
)

// TestFalseFileClaims_IgnoresProseAndURLs pins the noise-sensitivity fix: a
// "false file claim" is a hallucinated workspace PATH, not a prose word that
// ends in a code extension (Node.js) or a URL host (pkg.go.dev/...).
func TestFalseFileClaims_IgnoresProseAndURLs(t *testing.T) {
	changed := []string{"internal/config/duration.go"}
	// Journal read paths are ABSOLUTE (the agent resolves them); changed paths
	// and the answer cite them workspace-RELATIVE — the metric must match across
	// that mismatch via trailing-segment matching.
	read := []string{"/private/var/folders/xx/T/gokin-evals-123/scn/glm/internal/billing/invoice.go"}

	clean := []string{
		"I used Node.js with React.js and a bit of Vue.js for the UI.",
		"See https://pkg.go.dev/encoding/json and golang.org/x/tools for docs.",
		"Fixed the bug in internal/config/duration.go after reading internal/billing/invoice.go.",
		"The package.json and go.mod were untouched; ran go test ./... to confirm.",
	}
	for _, out := range clean {
		if got := falseFileClaims(out, changed, read); len(got) != 0 {
			t.Errorf("expected no false claims for %q, got %v", out, got)
		}
	}

	// A genuine hallucination — a workspace path neither changed nor read —
	// must still be flagged. Basename-only matching must NOT excuse a
	// wrong-directory hallucination of a real file's name.
	if got := falseFileClaims("the caller is in internal/ghost/fake.go", changed, read); len(got) != 1 || got[0] != "internal/ghost/fake.go" {
		t.Errorf("real hallucinated path must be flagged, got %v", got)
	}
	if got := falseFileClaims("see wrong/dir/invoice.go", changed, read); len(got) != 1 {
		t.Errorf("wrong-directory copy of a real basename must still be flagged, got %v", got)
	}
}

// TestMentionsVerification_RequiresRealSignal pins the false-positive fix: bare
// "test"/"build" in prose must NOT count as verification.
func TestMentionsVerification_RequiresRealSignal(t *testing.T) {
	noVerify := []string{
		"The test was failing because of an off-by-one.",
		"I'll build a fix for the rate limiter.",
		"This is a tricky bug to test in isolation.",
	}
	for _, out := range noVerify {
		if mentionsVerification(out, nil) {
			t.Errorf("prose mentioning test/build but no verification should be false: %q", out)
		}
	}

	yesVerify := []string{
		"Ran go test ./internal/config — all tests pass.",
		"Verified the fix; go build ./... succeeds.",
		"I re-ran the suite and the tests passed.",
	}
	for _, out := range yesVerify {
		if !mentionsVerification(out, nil) {
			t.Errorf("real verification report should be true: %q", out)
		}
	}

	// Falls back to the scenario's declared verification commands.
	if !mentionsVerification("I checked it with node --test test/x.test.js", []string{"node --test test/x.test.js"}) {
		t.Error("explicit verification command in the answer should match")
	}
}

// TestAgentDeliveredAnswer_RejectsNonAnswers pins the task_completed fix: an
// exit-0 run with an empty / [Auto] / placeholder stdout is NOT a completed task.
func TestAgentDeliveredAnswer_RejectsNonAnswers(t *testing.T) {
	ok := func(success bool, out string) bool {
		return agentDeliveredAnswer(Result{Agent: CommandResult{Success: success, OutputPreview: out}})
	}
	if !ok(true, "Fixed the off-by-one in offset.go; go test passes.") {
		t.Error("a real answer with exit 0 must count as completed")
	}
	if ok(false, "real answer but the process failed") {
		t.Error("non-zero exit must not count")
	}
	if ok(true, "   \n  ") {
		t.Error("empty/whitespace stdout must not count")
	}
	if ok(true, "[Auto] I've completed the requested operations. The results should be shown above.") {
		t.Error("[Auto] smart-fallback non-answer must not count")
	}
	if ok(true, "⚠ Model returned an empty response after several attempts.") {
		t.Error("empty-response placeholder must not count")
	}
}

// TestTrimPreview_KeepsTailConclusion pins the truncation fix: a long answer's
// conclusion (at the tail) must survive so answer_contains_required and
// mentions-verification can see it.
func TestTrimPreview_KeepsTailConclusion(t *testing.T) {
	body := strings.Repeat("exploration narration. ", 800) // well over the rune cap
	answer := "START. " + body + " CONCLUSION: the caller is internal/billing/invoice.go"

	got := trimPreview(answer, outputPreviewLimit)
	if len([]rune(got)) <= len([]rune(answer)) && !strings.Contains(got, "truncated") {
		t.Fatal("precondition: the answer should be long enough to truncate")
	}
	if !strings.Contains(got, "internal/billing/invoice.go") {
		t.Error("trimPreview must keep the TAIL conclusion (the named file)")
	}
	if !strings.Contains(got, "START") {
		t.Error("trimPreview should also keep the head")
	}
}
