package tools

import (
	"fmt"
	"path/filepath"
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

type execCoverSpan struct{ start, end int }

// execTargetCoverage records what ground has been covered for one canonical
// target within the current turn.
type execTargetCoverage struct {
	redundant int            // consecutive no-progress re-coverages of THIS target
	lastGen   int            // tracker gen as of this target's previous visit
	spans     []execCoverSpan // read only: recent covered ranges (ring, cap execCoverageSpanRing)
}

const (
	// execCoverageSpanRing bounds the per-target span memory — covering more
	// than this many distinct sections of one file is genuine wide
	// exploration, not a loop. Matches the agent guard's coverageSpanRing.
	execCoverageSpanRing = 8

	// maxCoverageRecoveryAttempts is how many graceful hints a re-coverage
	// loop gets before the turn hard-aborts. Mirrors the stagnation
	// recovery's hint-then-abort shape (read-only loops are always
	// side-effect free to skip, so hints are cheap).
	maxCoverageRecoveryAttempts = 2
)

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
	targets map[string]*execTargetCoverage
	gen     int // bumped on any progress signal; gates redundancy counting
	trips   int // graceful interventions fired this turn
}

func newExecutorCoverageTracker(workDir string) *executorCoverageTracker {
	return &executorCoverageTracker{
		workDir: strings.TrimSpace(workDir),
		targets: make(map[string]*execTargetCoverage),
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
			t.gen++
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
	target, ok := t.coverageTarget(name, args)
	if !ok {
		return 0, "", false
	}
	ckey := name + "\x00" + target
	tc := t.targets[ckey]
	isNewTarget := tc == nil
	if isNewTarget {
		tc = &execTargetCoverage{}
		t.targets[ckey] = tc
	}

	reCovers := false
	progress := isNewTarget
	switch name {
	case "read":
		span := execReadSpan(args)
		for _, s := range tc.spans {
			if span.start < s.end && s.start < span.end { // overlaps already-covered ground
				reCovers = true
				break
			}
		}
		if !reCovers {
			progress = true // new ground in a known file is progress too
		}
		tc.spans = append(tc.spans, span)
		if len(tc.spans) > execCoverageSpanRing {
			tc.spans = append([]execCoverSpan(nil), tc.spans[len(tc.spans)-execCoverageSpanRing:]...)
		}
	case "grep":
		reCovers = !isNewTarget
	}

	if progress {
		t.gen++
	}
	switch {
	case reCovers && tc.lastGen == t.gen:
		tc.redundant++ // re-covering with zero progress since the last visit
	case reCovers:
		tc.redundant = 0 // progress happened in between — a legitimate re-check
	}
	tc.lastGen = t.gen
	return tc.redundant, target, true
}

// resetWindow clears all per-target state after an intervention so the model
// gets a fresh window — and so spans recorded for the skipped batch can't
// poison the post-recovery retry. gen and trips survive.
func (t *executorCoverageTracker) resetWindow() {
	t.targets = make(map[string]*execTargetCoverage)
}

// coverageTarget canonicalizes a read/grep call to the GROUND it covers,
// dropping the perturbable args (read offset/limit, grep pattern). Deliberately
// separate from stagnationFingerprint, which MUST keep distinct offsets
// distinct so paging is never mistaken for stagnation.
func (t *executorCoverageTracker) coverageTarget(name string, args map[string]any) (string, bool) {
	switch name {
	case "read":
		p, _ := GetString(args, "file_path")
		if strings.TrimSpace(p) == "" {
			return "", false
		}
		return t.canonPath(p), true
	case "grep":
		if p, ok := args["path"].(string); ok && strings.TrimSpace(p) != "" {
			return t.canonPath(p), true
		}
		// Path-less grep searches the whole workspace — collapse to one scope
		// so rotating patterns over the default scope are still re-coverage.
		return "<scope>", true
	}
	return "", false
}

func (t *executorCoverageTracker) canonPath(p string) string {
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) && t.workDir != "" {
		p = filepath.Join(t.workDir, p)
	}
	return filepath.Clean(p)
}

// execReadSpan derives the [start,end) line range a read call covers,
// mirroring the read tool's defaults (offset default 1, limit default
// DefaultReadLimit — NOT DefaultChunkSize; see the agent guard's readSpan).
func execReadSpan(args map[string]any) execCoverSpan {
	start := max(GetIntDefault(args, "offset", 1), 0)
	width := GetIntDefault(args, "limit", DefaultReadLimit)
	if width <= 0 {
		width = DefaultReadLimit
	}
	return execCoverSpan{start: start, end: start + width}
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
