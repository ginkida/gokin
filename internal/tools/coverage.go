package tools

import (
	"path/filepath"
	"strings"
)

// Shared read/grep distinct-target coverage core, used by BOTH agentic loops:
// the foreground executor's within-turn guard (executor_coverage.go) and the
// sub-agent loop's cross-turn guard (agent.go recordCoverageLocked). Extracted
// from two near-verbatim copies (Tier-4 slice 1) so a coverage-logic fix is a
// one-place change — the donegate/IsWriteTool precedent.
//
// Pure + state-agnostic: CoverageState carries NO lock — the CALLER owns
// synchronization (the executor is single-goroutine; the agent holds
// callHistoryMu). The canonical TARGET string is computed by the caller
// (executor: local CanonTarget; agent: donegate.NormalizeTouchedPaths) and
// passed into RecordTarget, so this core never imports donegate and stays in
// the tools leaf package (no import cycle).

// CoverageSpan is a half-open [start,end) line range a read covered.
type CoverageSpan struct{ start, end int }

// coverageSpanRing bounds per-target span memory — covering more than this many
// distinct sections of one file is genuine wide exploration, not a loop.
const coverageSpanRing = 8

// targetCoverage records what ground has been covered for one canonical target.
type targetCoverage struct {
	redundant int            // consecutive no-progress re-coverages of THIS target
	lastGen   int            // CoverageState.gen as of this target's previous visit
	spans     []CoverageSpan // read only: recent covered ranges (ring, cap coverageSpanRing)
}

// CoverageState tracks per-target re-coverage progress. NOT safe for concurrent
// use — the caller synchronizes (matches both call sites' existing contracts).
type CoverageState struct {
	targets map[string]*targetCoverage
	gen     int // bumped on any progress signal; gates redundancy counting
}

// NewCoverageState returns a fresh tracker (empty targets, gen 0).
func NewCoverageState() *CoverageState {
	return &CoverageState{targets: make(map[string]*targetCoverage)}
}

// ResetTargets clears per-target state but KEEPS gen — used after an
// intervention so a fresh window starts and the skipped batch's spans can't
// poison the retry (the executor's resetWindow; the agent's windowed reset).
func (c *CoverageState) ResetTargets() {
	c.targets = make(map[string]*targetCoverage)
}

// NoteProgress bumps the progress generation: any non-read/grep tool call proves
// the model is not in a tight read/grep rotation, breaking every target's
// redundancy chain.
func (c *CoverageState) NoteProgress() {
	c.gen++
}

// NumTargets reports how many distinct targets currently carry coverage state.
// Read-only introspection for tests/diagnostics; the caller still synchronizes.
func (c *CoverageState) NumTargets() int { return len(c.targets) }

// Gen reports the current progress generation. Read-only introspection.
func (c *CoverageState) Gen() int { return c.gen }

// RecordTarget updates coverage for a read/grep call against an ALREADY-canonical
// target and returns that target's consecutive no-progress redundancy count. A
// revisit is redundant only when it re-covers ground already seen for that
// target AND gen has not advanced since the target's previous visit; first sight
// of a target — or new ground in a known file — is progress and bumps gen, which
// resets the target's chain.
func (c *CoverageState) RecordTarget(name, canonicalTarget string, args map[string]any) int {
	ckey := name + "\x00" + canonicalTarget
	tc := c.targets[ckey]
	isNewTarget := tc == nil
	if isNewTarget {
		tc = &targetCoverage{}
		c.targets[ckey] = tc
	}

	reCovers := false
	progress := isNewTarget
	switch name {
	case "read":
		span := ReadSpan(args)
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
		if len(tc.spans) > coverageSpanRing {
			tc.spans = append([]CoverageSpan(nil), tc.spans[len(tc.spans)-coverageSpanRing:]...)
		}
	case "grep":
		reCovers = !isNewTarget
	}

	if progress {
		c.gen++
	}
	switch {
	case reCovers && tc.lastGen == c.gen:
		tc.redundant++ // re-covering with zero progress since the last visit
	case reCovers:
		tc.redundant = 0 // progress happened in between — a legitimate re-check
	}
	tc.lastGen = c.gen
	return tc.redundant
}

// ReadSpan derives the [start,end) line range a read covers, mirroring the read
// tool's defaults (offset 1, limit DefaultReadLimit — NOT DefaultChunkSize,
// which is only the >10MB chunked path's default; an under-sized default makes
// the guard blind to offset drift inside the already-read tail of a default read).
func ReadSpan(args map[string]any) CoverageSpan {
	start := max(GetIntDefault(args, "offset", 1), 0)
	width := GetIntDefault(args, "limit", DefaultReadLimit)
	if width <= 0 {
		width = DefaultReadLimit
	}
	return CoverageSpan{start: start, end: start + width}
}

// CanonTarget canonicalizes a read/grep call to the GROUND it covers, dropping
// the perturbable args (read offset/limit, grep pattern), via local path
// canonicalization (join+clean). Returns ok=false for calls it can't
// canonicalize. The agent computes its own donegate-based target instead — both
// feed RecordTarget, which takes the already-canonical string.
func CanonTarget(name string, args map[string]any, workDir string) (string, bool) {
	switch name {
	case "read":
		p, _ := GetString(args, "file_path")
		if strings.TrimSpace(p) == "" {
			return "", false
		}
		return canonCoveragePath(p, workDir), true
	case "grep":
		if p, ok := args["path"].(string); ok && strings.TrimSpace(p) != "" {
			return canonCoveragePath(p, workDir), true
		}
		// Path-less grep searches the whole workspace — collapse to one scope so
		// rotating patterns over the default scope are still seen as re-coverage.
		return "<scope>", true
	}
	return "", false
}

func canonCoveragePath(p, workDir string) string {
	p = strings.TrimSpace(p)
	workDir = strings.TrimSpace(workDir)
	if !filepath.IsAbs(p) && workDir != "" {
		p = filepath.Join(workDir, p)
	}
	return filepath.Clean(p)
}
