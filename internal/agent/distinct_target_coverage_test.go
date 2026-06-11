package agent

import (
	"fmt"
	"testing"

	"gokin/internal/tools"
)

func newCoverageAgent(workDir string, threshold int) *Agent {
	return &Agent{
		workDir:        workDir,
		loopThreshold:  threshold,
		targetCoverage: make(map[string]*targetCoverage),
	}
}

func readArgs(path string, offset, limit int) map[string]any {
	return map[string]any{"file_path": path, "offset": float64(offset), "limit": float64(limit)}
}

func grepArgs(path, pattern string) map[string]any {
	m := map[string]any{"pattern": pattern}
	if path != "" {
		m["path"] = path
	}
	return m
}

// drive records N read/grep calls and returns the final per-target redundancy.
func driveCoverage(a *Agent, tool string, argsSeq []map[string]any) int {
	last := 0
	for _, args := range argsSeq {
		last = a.recordCoverageLocked(tool, args)
	}
	return last
}

// TestCoverage_PerturbedReadLoopTrips — drifting offsets over ONE file re-cover
// the same ground each call with no progress in between; redundancy must reach
// the budget (the arg-perturbing escape this guard closes).
func TestCoverage_PerturbedReadLoopTrips(t *testing.T) {
	a := newCoverageAgent("/w", 8) // broad: read budget 8
	budget := a.redundancyBudget("read")
	var seq []map[string]any
	for i := range 12 { // drifting offsets, all inside [1,1001) coverage
		seq = append(seq, readArgs("a.go", 1+i, 1000))
	}
	got := driveCoverage(a, "read", seq)
	if got < budget {
		t.Fatalf("perturbed read loop: redundancy %d, want >= budget %d", got, budget)
	}
}

// TestCoverage_DefaultLimitMatchesReadTool — a default read (no limit arg) must
// be recorded with the read tool's REAL default budget (DefaultReadLimit=2000),
// not DefaultChunkSize=1000, or offset drift inside [1001,2001) escapes.
func TestCoverage_DefaultLimitMatchesReadTool(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	// Default read covers [1, 1+DefaultReadLimit).
	a.recordCoverageLocked("read", map[string]any{"file_path": "a.go"})
	// Drift the offset into the tail half of that default read — already-seen
	// ground; must count as redundant.
	got := a.recordCoverageLocked("read", readArgs("a.go", tools.DefaultChunkSize+500, 100))
	if got != 1 {
		t.Fatalf("offset drift inside the default-read tail must be redundant, got %d", got)
	}
}

// TestCoverage_ForwardPagingDoesNotTrip — strictly advancing, non-overlapping
// pages of one large file are progress, never redundant.
func TestCoverage_ForwardPagingDoesNotTrip(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	var seq []map[string]any
	for i := range 30 {
		seq = append(seq, readArgs("big.go", 1+i*1000, 1000)) // [1,1001),[1001,2001),...
	}
	if got := driveCoverage(a, "read", seq); got != 0 {
		t.Fatalf("forward paging should never be redundant, got redundancy %d", got)
	}
}

// TestCoverage_DistinctFilesNotALoop — the v0.86.10 regression case: reading many
// DISTINCT files is exploration; redundancy stays 0 regardless of count.
func TestCoverage_DistinctFilesNotALoop(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	var seq []map[string]any
	for i := range 32 {
		seq = append(seq, readArgs(fmt.Sprintf("pkg/file%02d.go", i), 1, 1000))
	}
	if got := driveCoverage(a, "read", seq); got != 0 {
		t.Fatalf("32 distinct files must not be a loop, got redundancy %d", got)
	}
}

// TestCoverage_MultiFileVerifyReadsNotALoop — the review-confirmed false
// positive of the original (per-tool, global-counter) design: a read→verify
// re-read on each of many files. Per-target counting + the progress gate must
// keep every target's redundancy at ≤1 with no cross-file accumulation.
func TestCoverage_MultiFileVerifyReadsNotALoop(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	budget := a.redundancyBudget("read")
	maxSeen := 0
	for i := range 12 { // 12 files, each read twice (initial + verify)
		f := fmt.Sprintf("pkg/f%02d.go", i)
		a.recordCoverageLocked("read", readArgs(f, 1, 1000))
		got := a.recordCoverageLocked("read", readArgs(f, 1, 200)) // overlapping verify re-read
		maxSeen = max(maxSeen, got)
	}
	if maxSeen >= budget {
		t.Fatalf("read+verify over 12 distinct files must stay under budget %d, got max redundancy %d", budget, maxSeen)
	}
}

// TestCoverage_ProgressGateResetsRedundancy — covering NEW ground anywhere
// between two visits of a target resets that target's chain: redundancy counts
// only uninterrupted no-progress rotation.
func TestCoverage_ProgressGateResetsRedundancy(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	a.recordCoverageLocked("read", readArgs("a.go", 1, 1000))
	if got := a.recordCoverageLocked("read", readArgs("a.go", 100, 100)); got != 1 {
		t.Fatalf("back-to-back overlap should be redundant=1, got %d", got)
	}
	// Progress: read a different file (new target bumps coverageGen).
	a.recordCoverageLocked("read", readArgs("b.go", 1, 1000))
	if got := a.recordCoverageLocked("read", readArgs("a.go", 200, 100)); got != 0 {
		t.Fatalf("overlap AFTER progress must reset the chain, got %d", got)
	}
}

// TestCoverage_OtherToolBreaksChain — any non-read/grep call (edit, bash, …)
// is a progress signal: read→edit→verify-re-read never counts as redundant.
func TestCoverage_OtherToolBreaksChain(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	a.recordCoverageLocked("read", readArgs("a.go", 1, 1000))
	a.noteCoverageProgressLocked() // the edit between read and verify
	if got := a.recordCoverageLocked("read", readArgs("a.go", 1, 1000)); got != 0 {
		t.Fatalf("read→edit→verify must not be redundant, got %d", got)
	}
	// And the canonical iterative cycle never accumulates.
	for i := range 20 {
		a.noteCoverageProgressLocked()
		if got := a.recordCoverageLocked("read", readArgs("a.go", 1, 1000)); got != 0 {
			t.Fatalf("iteration %d: edit-verify cycle accumulated redundancy %d", i, got)
		}
	}
}

// TestCoverage_PageThenBacktrack — three forward pages then a jump back into
// already-read ground: the backtrack itself is one redundant revisit ONLY if
// nothing else advanced — but the pages DID advance coverageGen, so the chain
// starts at the backtrack: paging is progress, the first backtrack is reset to
// 0 by the gate. A second immediate backtrack is then redundant.
func TestCoverage_PageThenBacktrack(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	driveCoverage(a, "read", []map[string]any{
		readArgs("f.go", 1, 1000),    // [1,1001) — progress
		readArgs("f.go", 1001, 1000), // [1001,2001) — progress
		readArgs("f.go", 2001, 1000), // [2001,3001) — progress
	})
	// lastGen was advanced by the page reads themselves; an overlap immediately
	// after the last page has gen == lastGen → redundant.
	if got := a.recordCoverageLocked("read", readArgs("f.go", 500, 100)); got != 1 {
		t.Fatalf("backtrack right after paging should be 1 redundant, got %d", got)
	}
	if got := a.recordCoverageLocked("read", readArgs("f.go", 600, 100)); got != 2 {
		t.Fatalf("second consecutive backtrack should be 2, got %d", got)
	}
}

// TestCoverage_RotatingGrepTrips — re-searching ONE scope with rotating patterns
// back-to-back is the grep loop signature; pattern is dropped from the target.
func TestCoverage_RotatingGrepTrips(t *testing.T) {
	a := newCoverageAgent("/w", 8) // grep budget = 4
	budget := a.redundancyBudget("grep")
	var seq []map[string]any
	for i := range budget + 2 { // budget+1 redundant revisits of one scope
		seq = append(seq, grepArgs("internal/foo", fmt.Sprintf("pat%d", i)))
	}
	if got := driveCoverage(a, "grep", seq); got < budget {
		t.Fatalf("rotating grep loop: redundancy %d, want >= budget %d", got, budget)
	}
}

// TestCoverage_GrepInterleavedWithReadsNotALoop — the canonical exploration
// pattern: grep the scope, read a result, grep again. Reading new files is
// progress, so the scope's redundancy chain resets every round.
func TestCoverage_GrepInterleavedWithReadsNotALoop(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	for i := range 20 {
		a.recordCoverageLocked("grep", grepArgs("", fmt.Sprintf("sym%d", i)))
		got := a.recordCoverageLocked("read", readArgs(fmt.Sprintf("hit%02d.go", i), 1, 1000))
		if got != 0 {
			t.Fatalf("round %d: reading a fresh grep hit must not be redundant, got %d", i, got)
		}
	}
	// After 20 grep+read rounds the scope's chain must still be short: the next
	// grep is at most 1 redundant (gen advanced between every pair of greps —
	// actually reset to 0 each round, so this one is 0).
	if got := a.recordCoverageLocked("grep", grepArgs("", "final")); got >= a.redundancyBudget("grep") {
		t.Fatalf("interleaved exploration must stay under the grep budget, got %d", got)
	}
}

// TestCoverage_PathlessGrepsCollapse — path-less greps all map to one scope so a
// back-to-back rotating-pattern loop over the default scope is still caught.
func TestCoverage_PathlessGrepsCollapse(t *testing.T) {
	a := newCoverageAgent("/w", 8)
	budget := a.redundancyBudget("grep")
	var seq []map[string]any
	for i := range budget + 2 {
		seq = append(seq, grepArgs("", fmt.Sprintf("p%d", i))) // no path
	}
	if got := driveCoverage(a, "grep", seq); got < budget {
		t.Fatalf("path-less rotating greps should collapse to one scope and trip, got redundancy %d (budget %d)", got, budget)
	}
}

// TestCoverageTarget_Canonicalization pins what each tool's target drops/keeps.
func TestCoverageTarget_Canonicalization(t *testing.T) {
	a := newCoverageAgent("/w", 8)

	// read: offset/limit dropped — same file at different ranges => same target.
	t1, ok1 := a.coverageTarget("read", readArgs("a.go", 5, 10))
	t2, ok2 := a.coverageTarget("read", map[string]any{"file_path": "a.go"})
	if !ok1 || !ok2 || t1 != t2 || t1 != "a.go" {
		t.Fatalf("read target should be the path 'a.go' regardless of offset/limit, got %q/%q (ok %v/%v)", t1, t2, ok1, ok2)
	}

	// read with no path => not trackable.
	if _, ok := a.coverageTarget("read", map[string]any{"offset": float64(1)}); ok {
		t.Fatal("read without file_path should not be trackable")
	}

	// grep: pattern dropped, path kept.
	gp, ok := a.coverageTarget("grep", grepArgs("internal/y", "anything"))
	if !ok || gp != "internal/y" {
		t.Fatalf("grep target should be the path, got %q (ok %v)", gp, ok)
	}
	// grep with no path => sentinel scope.
	gs, ok := a.coverageTarget("grep", grepArgs("", "anything"))
	if !ok || gs != "<scope>" {
		t.Fatalf("path-less grep should map to <scope>, got %q (ok %v)", gs, ok)
	}

	// unrelated tool => not trackable.
	if _, ok := a.coverageTarget("edit", map[string]any{"file_path": "a.go"}); ok {
		t.Fatal("non read/grep tool should not be trackable")
	}
}

// TestRedundancyBudget_ScalesWithMode pins the per-mode budgets.
func TestRedundancyBudget_ScalesWithMode(t *testing.T) {
	cases := []struct {
		threshold, wantRead, wantGrep int
	}{
		{4, 4, 2},   // quick
		{8, 8, 4},   // broad / default
		{15, 15, 7}, // thorough
	}
	for _, c := range cases {
		a := newCoverageAgent("/w", c.threshold)
		if got := a.redundancyBudget("read"); got != c.wantRead {
			t.Errorf("threshold %d: read budget = %d, want %d", c.threshold, got, c.wantRead)
		}
		if got := a.redundancyBudget("grep"); got != c.wantGrep {
			t.Errorf("threshold %d: grep budget = %d, want %d", c.threshold, got, c.wantGrep)
		}
	}
}

// TestCoverage_LifecycleResets — both reset points wipe the coverage state so a
// post-reset (e.g. post-compaction) re-read does not inherit stale redundancy.
func TestCoverage_LifecycleResets(t *testing.T) {
	for _, reset := range []struct {
		name string
		fn   func(*Agent)
	}{
		{"clearCallHistory", (*Agent).clearCallHistory},
		{"resetLoopDetection", (*Agent).resetLoopDetection},
	} {
		a := newCoverageAgent("/w", 8)
		a.coverageTrips = 1
		// Accumulate some redundancy.
		got := driveCoverage(a, "read", []map[string]any{
			readArgs("a.go", 1, 1000),
			readArgs("a.go", 2, 1000),
			readArgs("a.go", 3, 1000),
		})
		if got == 0 || len(a.targetCoverage) == 0 {
			t.Fatalf("%s: expected accumulated coverage before reset", reset.name)
		}
		reset.fn(a)
		if len(a.targetCoverage) != 0 || a.coverageGen != 0 || a.coverageTrips != 0 {
			t.Fatalf("%s: state not cleared (coverage=%d gen=%d trips=%d)", reset.name, len(a.targetCoverage), a.coverageGen, a.coverageTrips)
		}
		// A fresh read after reset must not be redundant.
		if got := driveCoverage(a, "read", []map[string]any{readArgs("a.go", 1, 1000)}); got != 0 {
			t.Fatalf("%s: fresh read after reset inherited redundancy %d", reset.name, got)
		}
	}
}
