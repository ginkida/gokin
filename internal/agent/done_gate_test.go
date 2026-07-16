package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/donegate"
	"gokin/internal/tools"
)

// TestRecordToolExecution_PopulatesTrackers pins the load-bearing population
// path that arms both the verify-nudge and the sub-agent done-gate. Before this
// wiring a.toolsUsed was never populated (AddToolUsed had no callers), so both
// were effectively dead. recordToolExecution at the executeToolWithReflection
// chokepoint is the single live writer.
func TestRecordToolExecution_PopulatesTrackers(t *testing.T) {
	a := &Agent{workDir: "/tmp/gokin-test"}
	abs := func(p string) string { return "/tmp/gokin-test/" + p }

	a.recordToolExecution("read", map[string]any{"file_path": abs("a.go")}, tools.ToolResult{Success: true})  // read-only: tracked, not touched
	a.recordToolExecution("edit", map[string]any{"file_path": abs("b.go")}, tools.ToolResult{Success: false}) // failed mutation: tracked, not touched
	a.recordToolExecution("edit", map[string]any{"file_path": abs("b.go")}, tools.ToolResult{Success: true})  // success: touch b.go, gen 1
	a.recordToolExecution("write", map[string]any{"file_path": abs("b.go")}, tools.ToolResult{Success: true}) // re-edit dup: not re-touched, gen 2
	a.recordToolExecution("write", map[string]any{"file_path": abs("c.go")}, tools.ToolResult{Success: true}) // success: touch c.go, gen 3

	if got := a.GetToolsUsed(); len(got) != 5 {
		t.Fatalf("toolsUsed = %v, want 5 entries (every attempted call, success or fail)", got)
	}
	if got := a.GetTouchedPaths(); !reflect.DeepEqual(got, []string{"b.go", "c.go"}) {
		t.Fatalf("touched = %v, want [b.go c.go] (only successful mutations, deduped)", got)
	}
	// Re-edit of an already-touched file still bumps the generation so the gate
	// re-checks it — the count-only fingerprint would have missed gen 2.
	if gen := a.mutationGeneration(); gen != 3 {
		t.Fatalf("mutationGen = %d, want 3 (each successful mutation incl. re-edit bumps)", gen)
	}
}

func TestRecordToolExecution_MergesDeclaredAndBatchPaths(t *testing.T) {
	a := &Agent{workDir: "/tmp/gokin-test"}
	abs := func(p string) string { return "/tmp/gokin-test/" + p }

	// Pattern refactors have no concrete path argument; the tool reports the
	// files it actually changed in written_paths.
	a.recordToolExecution("refactor", map[string]any{"pattern": "**/*.go"}, tools.NewSuccessResultWithData("ok", map[string]any{
		"written_paths": []any{abs("a.go"), abs("b.go")},
	}))
	// Batch paths may arrive both in the call's list argument and in the result.
	// A duplicate from either source must still be recorded only once.
	a.recordToolExecution("batch", map[string]any{
		"files": []any{abs("c.go"), abs("a.go")},
	}, tools.NewSuccessResultWithData("ok", map[string]any{
		"written_paths": []string{abs("d.go"), abs("a.go")},
	}))

	if got := a.GetTouchedPaths(); !reflect.DeepEqual(got, []string{"a.go", "b.go", "c.go", "d.go"}) {
		t.Fatalf("touched = %v, want declared and batch paths merged and deduped", got)
	}
	if gen := a.mutationGeneration(); gen != 2 {
		t.Fatalf("mutationGen = %d, want 2 successful mutation calls", gen)
	}
}

func TestAgentDoneGateMaxFixes_Caps(t *testing.T) {
	cases := []struct {
		attempts int
		want     int
	}{
		{-1, 0}, {0, 0}, {1, 1}, {2, 2}, {3, agentDoneGateMaxFixCap}, {99, agentDoneGateMaxFixCap},
	}
	for _, c := range cases {
		if got := agentDoneGateMaxFixes(&donegate.Policy{AutoFixAttempts: c.attempts}); got != c.want {
			t.Errorf("agentDoneGateMaxFixes(AutoFixAttempts=%d) = %d, want %d", c.attempts, got, c.want)
		}
	}
}

// TestRunDoneGateAtBreak_SkipGuards covers the cheap early-out branches that
// must hold so a gate can never brick an agent: disabled config and
// nothing-mutated both skip without ever building/running checks.
func TestRunDoneGateAtBreak_SkipGuards(t *testing.T) {
	var output strings.Builder
	fixes, gen := 0, -1
	ctx := context.Background()

	a := &Agent{workDir: "/tmp/x"}
	if got := a.runDoneGateAtBreak(ctx, "fix the bug", &output, &fixes, &gen); got != agentGateSkipped {
		t.Fatalf("nil policy: got %v, want agentGateSkipped", got)
	}

	a.SetDoneGatePolicy(donegate.Policy{Enabled: false})
	if got := a.runDoneGateAtBreak(ctx, "fix the bug", &output, &fixes, &gen); got != agentGateSkipped {
		t.Fatalf("disabled policy: got %v, want agentGateSkipped", got)
	}

	a.SetDoneGatePolicy(donegate.Policy{Enabled: true, AutoFixAttempts: 1})
	if got := a.runDoneGateAtBreak(ctx, "fix the bug", &output, &fixes, &gen); got != agentGateSkipped {
		t.Fatalf("nothing mutated: got %v, want agentGateSkipped", got)
	}
}

// TestInjectClaimCorrectionIfNeeded_Guards pins the conservative pre-conditions
// (disabled, empty text, no mutations) so the git-ground-truth check never runs
// for agents that didn't change code.
func TestInjectClaimCorrectionIfNeeded_Guards(t *testing.T) {
	a := &Agent{workDir: "/tmp/x"}
	if a.injectClaimCorrectionIfNeeded("I changed foo.go") {
		t.Fatal("nil policy must not inject a correction")
	}
	a.SetDoneGatePolicy(donegate.Policy{Enabled: true})
	if a.injectClaimCorrectionIfNeeded("   ") {
		t.Fatal("empty final text must not inject a correction")
	}
	if a.injectClaimCorrectionIfNeeded("I changed foo.go") {
		t.Fatal("no touched paths must not inject a correction")
	}
}

// TestNeedsVerificationNudge_UsesCanonicalMutationSet locks the verify-nudge to
// donegate.IsMutationTool so it can't drift again — copy/mkdir count as
// mutations (the old hardcoded set omitted them and carried non-existent
// multiedit/atomicwrite entries instead).
func TestNeedsVerificationNudge_UsesCanonicalMutationSet(t *testing.T) {
	mut := &Agent{workDir: "/tmp/x"}
	mut.toolsUsed = []string{"copy"} // mutation via copy, no verification
	if !mut.needsVerificationNudge() {
		t.Fatal("copy (a mutation) without verification should need a nudge")
	}

	verified := &Agent{workDir: "/tmp/x"}
	verified.toolsUsed = []string{"mkdir", "bash"} // mutation + a verification command
	if verified.needsVerificationNudge() {
		t.Fatal("mkdir + bash (verification) should not need a nudge")
	}

	readOnly := &Agent{workDir: "/tmp/x"}
	readOnly.toolsUsed = []string{"read", "grep"}
	if readOnly.needsVerificationNudge() {
		t.Fatal("read-only run should not need a nudge")
	}
}

// TestRunnerSetDoneGateConfig_MapsPolicy verifies the config->policy mapping for
// sub-agents: never fail-closed, invalid mode falls back to strict, and an empty
// CheckTimeout defaults.
func TestRunnerSetDoneGateConfig_MapsPolicy(t *testing.T) {
	r := &Runner{}
	r.SetDoneGateConfig(config.DoneGateConfig{
		Enabled:         true,
		Mode:            "normal",
		AutoFixAttempts: 4,
		CheckTimeout:    90 * time.Second,
	})
	p := r.doneGatePolicy
	if p == nil {
		t.Fatal("policy not set")
	}
	if !p.Enabled || p.Mode != "normal" || p.FailClosed || p.AutoFixAttempts != 4 || p.CheckTimeout != 90*time.Second {
		t.Fatalf("policy mapping wrong: %#v", p)
	}

	r.SetDoneGateConfig(config.DoneGateConfig{Enabled: true, Mode: "bogus"})
	p = r.doneGatePolicy
	if p.Mode != "strict" {
		t.Fatalf("invalid mode should fall back to strict, got %q", p.Mode)
	}
	if p.CheckTimeout != donegate.DefaultCheckTimeout {
		t.Fatalf("empty CheckTimeout should default to %v, got %v", donegate.DefaultCheckTimeout, p.CheckTimeout)
	}
	if p.FailClosed {
		t.Fatal("sub-agent policy must never be fail-closed")
	}
}
