package agent

import (
	"strings"
	"testing"
	"time"
)

type fewShotFakeStore struct{ ctx string }

func (f *fewShotFakeStore) LearnFromSuccess(taskType, prompt, agentType, output string, d time.Duration, tokens int) error {
	return nil
}
func (f *fewShotFakeStore) GetSimilarExamples(prompt string, limit int) []TaskExampleSummary {
	return nil
}
func (f *fewShotFakeStore) GetExamplesForContext(taskType, prompt string, limit int) string {
	return f.ctx
}

// TestWithFewShot pins the v0.86.1 fix: the ExampleStore was write-only;
// withFewShot reads relevant past successes back into the spawn prompt. No store
// or no matching examples → the prompt is unchanged (common no-op case).
func TestWithFewShot(t *testing.T) {
	r := &Runner{}

	// No example store → prompt unchanged.
	if got := r.withFewShot(runnerAgentDeps{}, "general", "do X"); got != "do X" {
		t.Errorf("nil store: got %q, want unchanged", got)
	}

	// Store returns no examples (no tag overlap) → unchanged.
	deps := runnerAgentDeps{exampleStore: &fewShotFakeStore{ctx: ""}}
	if got := r.withFewShot(deps, "general", "do X"); got != "do X" {
		t.Errorf("empty examples: got %q, want unchanged", got)
	}

	// Store returns examples → prepended, original task preserved at the end.
	deps = runnerAgentDeps{exampleStore: &fewShotFakeStore{ctx: "## Similar Past Tasks\n\n### Example A\n..."}}
	got := r.withFewShot(deps, "general", "do the thing")
	if !strings.Contains(got, "## Similar Past Tasks") {
		t.Errorf("examples not injected: %q", got)
	}
	if !strings.HasSuffix(got, "do the thing") {
		t.Errorf("original task not preserved at the end: %q", got)
	}
	if !strings.Contains(got, "Now handle this request:") {
		t.Errorf("missing separator between examples and task: %q", got)
	}
}
