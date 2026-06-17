package tools

import (
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// Within-turn re-coverage guard.
//
// The exact-pattern stagnation check in executeLoop only catches IDENTICAL
// batches — a loop that perturbs args every call (drifting read offset,
// rotating grep pattern) makes a fresh stagnationFingerprint each round and
// escapes it. This tracker is the executor-side mirror of the agent-level
// distinct-target coverage guard (v0.87.0, agent.go recordCoverageLocked):
// per-target and progress-gated, so paging, distinct files, and read→edit
// cycles never trip it — only an uninterrupted rotation over the same ground
// (the actual loop signature) climbs to the budget.
//
// The tracker lives as a LOCAL in executeLoop (within-turn by construction,
// single goroutine — no locking), so it needs no reset wiring: a fresh
// Execute gets a fresh tracker.

// maxCoverageRecoveryAttempts is how many graceful hints a re-coverage loop
// gets before the turn hard-aborts. Mirrors the stagnation recovery's
// hint-then-abort shape (read-only loops are always side-effect free to skip,
// so hints are cheap). The coverage CORE (span/target/record/state) lives in
// coverage.go, shared with the agent loop (Tier-4 slice 1).
const maxCoverageRecoveryAttempts = 2

// Budgets are consecutive no-progress re-coverages before a trip. Same values
// as the agent guard's normal-mode redundancyBudget (read = 8, grep = 8/2) so
// the two guards' documented semantics stay aligned. Package vars (not
// consts) so tests can tighten them to drive the abort path within
// maxIterations — same shared-with-test pattern as broadLoopExplorationTools.
var (
	execCoverageReadBudget = 8
	execCoverageGrepBudget = 4
)

func executorRedundancyBudget(tool string) int {
	if tool == "grep" {
		return execCoverageGrepBudget
	}
	return execCoverageReadBudget
}

type executorCoverageTracker struct {
	workDir string
	state   *CoverageState // shared coverage core (coverage.go)
	trips   int            // graceful interventions fired this turn
}

func newExecutorCoverageTracker(workDir string) *executorCoverageTracker {
	return &executorCoverageTracker{
		workDir: strings.TrimSpace(workDir),
		state:   NewCoverageState(),
	}
}

// coverageOffender describes the first call in a batch that exhausted its
// redundancy budget.
type coverageOffender struct {
	name      string
	target    string
	redundant int
}

// observeBatch records coverage for one round's calls and returns the first
// offender whose consecutive no-progress redundancy reached its budget, or
// nil. Must only be called for batches that will actually execute — skipped
// batches (kimi budget / stagnation recovery) must not leave ghost coverage.
func (t *executorCoverageTracker) observeBatch(calls []*genai.FunctionCall) *coverageOffender {
	var offender *coverageOffender
	for _, fc := range calls {
		if fc == nil {
			continue
		}
		switch fc.Name {
		case "read", "grep":
			redundant, target, tracked := t.record(fc.Name, fc.Args)
			if tracked && offender == nil && redundant >= executorRedundancyBudget(fc.Name) {
				offender = &coverageOffender{name: fc.Name, target: target, redundant: redundant}
			}
		default:
			// Any non-read/grep call (edit, bash, glob, todo, …) proves the
			// model is not in a tight read/grep rotation — breaks every
			// target's redundancy chain.
			t.state.NoteProgress()
		}
	}
	return offender
}

// record mirrors the agent guard's recordCoverageLocked: a revisit is
// redundant only when it re-covers already-seen ground for that target AND
// gen has not advanced since the target's previous visit. First sight of a
// target — or new ground in a known file — is progress and bumps gen; any
// progress between visits resets the target's chain.
func (t *executorCoverageTracker) record(name string, args map[string]any) (redundant int, target string, tracked bool) {
	target, ok := CanonTarget(name, args, t.workDir)
	if !ok {
		return 0, "", false
	}
	return t.state.RecordTarget(name, target, args), target, true
}

// resetWindow clears all per-target state after an intervention so the model
// gets a fresh window — and so spans recorded for the skipped batch can't
// poison the post-recovery retry. gen and trips survive.
func (t *executorCoverageTracker) resetWindow() {
	t.state.ResetTargets()
}

// buildCoverageRecoveryResults skips the batch with a re-coverage hint —
// side-effect free by construction (only read/grep can be offenders, and the
// whole batch is replaced, never partially executed).
func (e *Executor) buildCoverageRecoveryResults(calls []*genai.FunctionCall, off *coverageOffender) []*genai.FunctionResponse {
	results := make([]*genai.FunctionResponse, len(calls))
	for i, call := range calls {
		toolName := "tool"
		callID := ""
		if call != nil {
			toolName = call.Name
			callID = call.ID
		}
		result := NewErrorResult(buildCoverageRecoveryMessage(off))
		results[i] = &genai.FunctionResponse{
			ID:       callID,
			Name:     toolName,
			Response: result.ToMap(),
		}
	}
	return results
}

// buildCoverageRecoveryMessage keeps the "Loop guard:" prefix the UI
// classifier matches on, and the "Do not call it again" phrase shared with
// the stagnation recovery hints.
func buildCoverageRecoveryMessage(off *coverageOffender) string {
	switch off.name {
	case "read":
		return fmt.Sprintf(
			"Loop guard: %s has been re-read %d times in a row with shifting offsets and nothing else happening in between. Do not call it again — this file's content is already in the conversation above; reuse it. If what you need is not there, it is not in this file: read a DIFFERENT file or proceed with your best available evidence.",
			off.target, off.redundant,
		)
	default: // grep
		return fmt.Sprintf(
			"Loop guard: %s has been re-searched %d times in a row with rotating patterns and nothing else happening in between. Do not call it again — synthesize from the results already above, or open a specific file with read. If no pattern matched, what you are looking for is likely not there.",
			off.target, off.redundant,
		)
	}
}
