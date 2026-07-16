package agent

import (
	"os"
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

// TestAllSpawnPathsUseFewShot guards the v0.86.5 fix: the v0.86.1 wiring only
// reached the synchronous Spawn/SpawnWithContext paths; the three async paths
// (SpawnAsync, SpawnAsyncWithStreaming, SpawnMultiple) still called agent.Run
// with the raw prompt — so they wrote learning (recordAgentExecutionLearning)
// but never read it back, the exact write-only asymmetry #11 was meant to close.
// This is a source-level invariant: every agent.Run call site in runner_spawn.go
// must route its prompt through withFewShot. (Resume paths in runner_resume.go
// are intentionally excluded — they replay an existing task/checkpoint, so
// "similar past tasks" would be noise.) Cheap regression for "new spawn path
// forgot the helper".
func TestAllSpawnPathsUseFewShot(t *testing.T) {
	src, err := os.ReadFile("runner_spawn.go")
	if err != nil {
		t.Fatalf("read runner_spawn.go: %v", err)
	}
	calls := 0
	for line := range strings.SplitSeq(string(src), "\n") {
		// Some goroutines predeclare result so their panic-recovery defer can
		// publish/finalize that same value; accept both := and = assignments.
		// Synchronous paths intentionally use runAgentWithPanicRecovery so a
		// panic after a stateful tool still publishes retry provenance.
		isDirectRun := strings.Contains(line, ".Run(")
		isContainedSyncRun := strings.Contains(line, "runAgentWithPanicRecovery(")
		if (!isDirectRun && !isContainedSyncRun) || !strings.Contains(line, "result, err") {
			continue
		}
		calls++
		if !strings.Contains(line, "withFewShot") {
			t.Errorf("spawn path calls agent.Run without withFewShot:\n  %s", strings.TrimSpace(line))
		}
	}
	if calls < 5 {
		t.Fatalf("expected ≥5 spawn agent.Run call sites, found %d — did the scan break?", calls)
	}
}
