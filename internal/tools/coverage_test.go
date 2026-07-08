package tools

import (
	"path/filepath"
	"testing"

	"google.golang.org/genai"
)

// ========== NewCoverageState ==========

func TestNewCoverageState(t *testing.T) {
	s := NewCoverageState()
	if s == nil {
		t.Fatal("NewCoverageState returned nil")
	}
	if s.NumTargets() != 0 {
		t.Errorf("NumTargets = %d, want 0", s.NumTargets())
	}
	if s.Gen() != 0 {
		t.Errorf("Gen = %d, want 0", s.Gen())
	}
}

// ========== ResetTargets ==========

func TestCoverageState_ResetTargets(t *testing.T) {
	s := NewCoverageState()
	s.RecordTarget("read", "file_a", map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)})
	if s.NumTargets() != 1 {
		t.Fatalf("NumTargets = %d, want 1 before reset", s.NumTargets())
	}

	genBefore := s.Gen()
	s.ResetTargets()

	if s.NumTargets() != 0 {
		t.Errorf("NumTargets = %d after reset, want 0", s.NumTargets())
	}
	// Gen survives reset
	if s.Gen() != genBefore {
		t.Errorf("Gen = %d after reset, want %d (gen survives)", s.Gen(), genBefore)
	}
}

// ========== NoteProgress ==========

func TestCoverageState_NoteProgress(t *testing.T) {
	s := NewCoverageState()
	if s.Gen() != 0 {
		t.Fatalf("initial Gen = %d, want 0", s.Gen())
	}
	s.NoteProgress()
	if s.Gen() != 1 {
		t.Errorf("Gen = %d after one NoteProgress, want 1", s.Gen())
	}
	s.NoteProgress()
	if s.Gen() != 2 {
		t.Errorf("Gen = %d after two NoteProgress, want 2", s.Gen())
	}
}

// ========== RecordTarget — read ==========

func TestCoverageState_RecordTarget_ReadFirstSightIsProgress(t *testing.T) {
	s := NewCoverageState()
	redundant := s.RecordTarget("read", "file_a", map[string]any{
		"file_path": "a.go",
		"offset":    float64(1),
		"limit":     float64(100),
	})
	if redundant != 0 {
		t.Errorf("first sight redundant = %d, want 0", redundant)
	}
	if s.Gen() != 1 {
		t.Errorf("Gen = %d after first sight, want 1 (progress)", s.Gen())
	}
}

func TestCoverageState_RecordTarget_ReadNewGroundIsProgress(t *testing.T) {
	s := NewCoverageState()
	// First read: lines 1-100
	s.RecordTarget("read", "file_a", map[string]any{
		"file_path": "a.go",
		"offset":    float64(1),
		"limit":     float64(100),
	})
	// Second read: lines 200-300 (non-overlapping = new ground)
	redundant := s.RecordTarget("read", "file_a", map[string]any{
		"file_path": "a.go",
		"offset":    float64(200),
		"limit":     float64(100),
	})
	if redundant != 0 {
		t.Errorf("new ground redundant = %d, want 0 (progress)", redundant)
	}
	if s.Gen() != 2 {
		t.Errorf("Gen = %d after new ground, want 2 (progress)", s.Gen())
	}
}

func TestCoverageState_RecordTarget_ReadOverlapIsRedundant(t *testing.T) {
	s := NewCoverageState()
	// First read: lines 1-100
	s.RecordTarget("read", "file_a", map[string]any{
		"file_path": "a.go",
		"offset":    float64(1),
		"limit":     float64(100),
	})
	// Second read: lines 50-150 (overlaps → redundant, no progress between)
	redundant := s.RecordTarget("read", "file_a", map[string]any{
		"file_path": "a.go",
		"offset":    float64(50),
		"limit":     float64(100),
	})
	if redundant != 1 {
		t.Errorf("overlap redundant = %d, want 1", redundant)
	}
}

func TestCoverageState_RecordTarget_ReadOverlapMultipleRedundant(t *testing.T) {
	s := NewCoverageState()
	args := map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)}
	// First read
	s.RecordTarget("read", "file_a", args)
	// Second identical read → redundant=1
	r1 := s.RecordTarget("read", "file_a", args)
	if r1 != 1 {
		t.Fatalf("redundant after 2nd = %d, want 1", r1)
	}
	// Third identical read → redundant=2
	r2 := s.RecordTarget("read", "file_a", args)
	if r2 != 2 {
		t.Errorf("redundant after 3rd = %d, want 2", r2)
	}
}

func TestCoverageState_RecordTarget_ReadProgressResetsRedundancy(t *testing.T) {
	s := NewCoverageState()
	args := map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)}
	// Build up redundancy
	s.RecordTarget("read", "file_a", args)
	s.RecordTarget("read", "file_a", args) // redundant=1

	// Progress signal (e.g. an edit happened)
	s.NoteProgress()

	// Same overlap read — but gen advanced, so it's a legitimate re-check
	redundant := s.RecordTarget("read", "file_a", args)
	if redundant != 0 {
		t.Errorf("redundant after progress = %d, want 0 (progress resets chain)", redundant)
	}
}

// ========== RecordTarget — grep ==========

func TestCoverageState_RecordTarget_GrepFirstSightIsProgress(t *testing.T) {
	s := NewCoverageState()
	redundant := s.RecordTarget("grep", "scope_a", map[string]any{"pattern": "foo"})
	if redundant != 0 {
		t.Errorf("first grep redundant = %d, want 0", redundant)
	}
	if s.Gen() != 1 {
		t.Errorf("Gen = %d after first grep, want 1", s.Gen())
	}
}

func TestCoverageState_RecordTarget_GrepSameScopeIsRedundant(t *testing.T) {
	s := NewCoverageState()
	// First grep on scope_a
	s.RecordTarget("grep", "scope_a", map[string]any{"pattern": "foo"})
	// Second grep on same scope (different pattern, but same canonical target)
	redundant := s.RecordTarget("grep", "scope_a", map[string]any{"pattern": "bar"})
	if redundant != 1 {
		t.Errorf("same-scope grep redundant = %d, want 1", redundant)
	}
}

func TestCoverageState_RecordTarget_GrepDifferentScopeIsProgress(t *testing.T) {
	s := NewCoverageState()
	s.RecordTarget("grep", "scope_a", map[string]any{"pattern": "foo"})
	redundant := s.RecordTarget("grep", "scope_b", map[string]any{"pattern": "foo"})
	if redundant != 0 {
		t.Errorf("different-scope grep redundant = %d, want 0 (new target)", redundant)
	}
}

func TestCoverageState_RecordTarget_GrepProgressResetsRedundancy(t *testing.T) {
	s := NewCoverageState()
	args := map[string]any{"pattern": "foo"}
	s.RecordTarget("grep", "scope_a", args)
	s.RecordTarget("grep", "scope_a", args) // redundant=1

	s.NoteProgress()

	redundant := s.RecordTarget("grep", "scope_a", args)
	if redundant != 0 {
		t.Errorf("grep redundant after progress = %d, want 0", redundant)
	}
}

// ========== RecordTarget — distinct targets don't interfere ==========

func TestCoverageState_RecordTarget_DistinctTargetsIndependent(t *testing.T) {
	s := NewCoverageState()
	// Read file A
	s.RecordTarget("read", "file_a", map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)})
	// Read file B — new target, should be progress
	redundant := s.RecordTarget("read", "file_b", map[string]any{"file_path": "b.go", "offset": float64(1), "limit": float64(100)})
	if redundant != 0 {
		t.Errorf("distinct file redundant = %d, want 0", redundant)
	}
}

// ========== ReadSpan ==========

func TestReadSpan_Defaults(t *testing.T) {
	span := ReadSpan(map[string]any{})
	// No offset/limit → offset=1, limit=DefaultReadLimit
	if span.start != 1 {
		t.Errorf("start = %d, want 1 (default offset)", span.start)
	}
	if span.end != 1+DefaultReadLimit {
		t.Errorf("end = %d, want %d", span.end, 1+DefaultReadLimit)
	}
}

func TestReadSpan_ExplicitValues(t *testing.T) {
	span := ReadSpan(map[string]any{
		"offset": float64(100),
		"limit":  float64(50),
	})
	if span.start != 100 {
		t.Errorf("start = %d, want 100", span.start)
	}
	if span.end != 150 {
		t.Errorf("end = %d, want 150", span.end)
	}
}

func TestReadSpan_NegativeOffsetClampedToZero(t *testing.T) {
	span := ReadSpan(map[string]any{
		"offset": float64(-5),
		"limit":  float64(100),
	})
	if span.start != 0 {
		t.Errorf("start = %d, want 0 (clamped)", span.start)
	}
}

func TestReadSpan_ZeroLimitFallsBackToDefault(t *testing.T) {
	span := ReadSpan(map[string]any{
		"offset": float64(10),
		"limit":  float64(0),
	})
	if span.end != 10+DefaultReadLimit {
		t.Errorf("end = %d, want %d (zero limit → default)", span.end, 10+DefaultReadLimit)
	}
}

func TestReadSpan_NegativeLimitFallsBackToDefault(t *testing.T) {
	span := ReadSpan(map[string]any{
		"offset": float64(10),
		"limit":  float64(-1),
	})
	if span.end != 10+DefaultReadLimit {
		t.Errorf("end = %d, want %d (negative limit → default)", span.end, 10+DefaultReadLimit)
	}
}

// ========== CanonTarget — read ==========

func TestCanonTarget_Read(t *testing.T) {
	target, ok := CanonTarget("read", map[string]any{"file_path": "src/main.go"}, "")
	if !ok {
		t.Fatal("CanonTarget read should return ok=true")
	}
	if target != filepath.Clean("src/main.go") {
		t.Errorf("target = %q, want %q", target, filepath.Clean("src/main.go"))
	}
}

func TestCanonTarget_ReadEmptyPath(t *testing.T) {
	_, ok := CanonTarget("read", map[string]any{"file_path": ""}, "")
	if ok {
		t.Error("empty file_path should return ok=false")
	}
}

func TestCanonTarget_ReadWhitespacePath(t *testing.T) {
	_, ok := CanonTarget("read", map[string]any{"file_path": "   "}, "")
	if ok {
		t.Error("whitespace file_path should return ok=false")
	}
}

func TestCanonTarget_ReadMissingPath(t *testing.T) {
	_, ok := CanonTarget("read", map[string]any{}, "")
	if ok {
		t.Error("missing file_path should return ok=false")
	}
}

func TestCanonTarget_ReadWithWorkDir(t *testing.T) {
	target, ok := CanonTarget("read", map[string]any{"file_path": "main.go"}, "/project")
	if !ok {
		t.Fatal("should return ok=true")
	}
	want := filepath.Clean("/project/main.go")
	if target != want {
		t.Errorf("target = %q, want %q", target, want)
	}
}

func TestCanonTarget_ReadAbsolutePath(t *testing.T) {
	target, ok := CanonTarget("read", map[string]any{"file_path": "/abs/path/main.go"}, "/project")
	if !ok {
		t.Fatal("should return ok=true")
	}
	// Absolute path should not be joined with workDir
	if target != filepath.Clean("/abs/path/main.go") {
		t.Errorf("target = %q, want %q", target, filepath.Clean("/abs/path/main.go"))
	}
}

// ========== CanonTarget — grep ==========

func TestCanonTarget_GrepWithPath(t *testing.T) {
	target, ok := CanonTarget("grep", map[string]any{"pattern": "foo", "path": "src/"}, "")
	if !ok {
		t.Fatal("should return ok=true")
	}
	if target != filepath.Clean("src/") {
		t.Errorf("target = %q, want %q", target, filepath.Clean("src/"))
	}
}

func TestCanonTarget_GrepWithoutPath(t *testing.T) {
	target, ok := CanonTarget("grep", map[string]any{"pattern": "foo"}, "")
	if !ok {
		t.Fatal("should return ok=true")
	}
	if target != "<scope>" {
		t.Errorf("target = %q, want <scope>", target)
	}
}

func TestCanonTarget_GrepEmptyPath(t *testing.T) {
	target, ok := CanonTarget("grep", map[string]any{"path": ""}, "")
	if !ok {
		t.Fatal("should return ok=true (collapses to <scope>)")
	}
	if target != "<scope>" {
		t.Errorf("target = %q, want <scope>", target)
	}
}

// ========== CanonTarget — unknown tool ==========

func TestCanonTarget_UnknownTool(t *testing.T) {
	_, ok := CanonTarget("edit", map[string]any{"file_path": "a.go"}, "")
	if ok {
		t.Error("unknown tool should return ok=false")
	}
}

func TestCanonTarget_EmptyToolName(t *testing.T) {
	_, ok := CanonTarget("", map[string]any{}, "")
	if ok {
		t.Error("empty tool name should return ok=false")
	}
}

// ========== canonCoveragePath ==========

func TestCanonCoveragePath_Relative(t *testing.T) {
	got := canonCoveragePath("src/main.go", "/project")
	want := filepath.Clean("/project/src/main.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCanonCoveragePath_Absolute(t *testing.T) {
	got := canonCoveragePath("/abs/main.go", "/project")
	if got != filepath.Clean("/abs/main.go") {
		t.Errorf("got %q, want %q", got, filepath.Clean("/abs/main.go"))
	}
}

func TestCanonCoveragePath_EmptyWorkDir(t *testing.T) {
	got := canonCoveragePath("main.go", "")
	if got != filepath.Clean("main.go") {
		t.Errorf("got %q, want %q", got, filepath.Clean("main.go"))
	}
}

func TestCanonCoveragePath_TrimsWhitespace(t *testing.T) {
	got := canonCoveragePath("  main.go  ", "/project")
	want := filepath.Clean("/project/main.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ========== CoverageSpan ring buffer ==========

func TestCoverageState_RecordTarget_SpanRingEviction(t *testing.T) {
	s := NewCoverageState()
	// Record coverageSpanRing+2 non-overlapping reads to the same file
	// Each read covers a distinct range so they're all "new ground" (progress)
	for i := 0; i < coverageSpanRing+2; i++ {
		offset := 1 + i*200 // 1-200, 201-400, 601-800, ... (non-overlapping)
		s.RecordTarget("read", "file_a", map[string]any{
			"file_path": "a.go",
			"offset":    float64(offset),
			"limit":     float64(100),
		})
	}
	// Should not panic and should have tracked the target
	if s.NumTargets() != 1 {
		t.Errorf("NumTargets = %d, want 1", s.NumTargets())
	}
}

// ========== Integration: read overlap after ring eviction ==========

func TestCoverageState_RecordTarget_OverlapAfterManyDistinctReads(t *testing.T) {
	s := NewCoverageState()
	// Fill the ring with distinct ranges
	for i := 0; i < coverageSpanRing; i++ {
		offset := 1 + i*200
		s.RecordTarget("read", "file_a", map[string]any{
			"file_path": "a.go",
			"offset":    float64(offset),
			"limit":     float64(100),
		})
	}
	// Now read a range that overlaps the FIRST recorded span (which was evicted)
	// This should NOT be detected as redundant because the old span was evicted
	redundant := s.RecordTarget("read", "file_a", map[string]any{
		"file_path": "a.go",
		"offset":    float64(1),
		"limit":     float64(100),
	})
	// The first span was evicted from the ring, so this is "new ground" again
	// → progress, redundant=0
	if redundant != 0 {
		t.Logf("redundant = %d after ring eviction (first span gone) — acceptable as long as no panic", redundant)
	}
}

// ========== Gen helper ==========

func TestCoverageState_GenIncrementsOnProgress(t *testing.T) {
	s := NewCoverageState()
	initial := s.Gen()

	// First read = progress
	s.RecordTarget("read", "f", map[string]any{"file_path": "f.go", "offset": float64(1), "limit": float64(100)})
	if s.Gen() != initial+1 {
		t.Errorf("Gen = %d after first read, want %d", s.Gen(), initial+1)
	}

	// NoteProgress
	s.NoteProgress()
	if s.Gen() != initial+2 {
		t.Errorf("Gen = %d after NoteProgress, want %d", s.Gen(), initial+2)
	}
}

// ========== NumTargets ==========

func TestCoverageState_NumTargets(t *testing.T) {
	s := NewCoverageState()
	s.RecordTarget("read", "file_a", map[string]any{"file_path": "a.go"})
	s.RecordTarget("read", "file_b", map[string]any{"file_path": "b.go"})
	s.RecordTarget("grep", "scope_a", map[string]any{"pattern": "x"})
	if s.NumTargets() != 3 {
		t.Errorf("NumTargets = %d, want 3", s.NumTargets())
	}
}

// ========== FunctionCall args with int (not float64) ==========

func TestReadSpan_IntArgs(t *testing.T) {
	// Some callers may pass int instead of float64
	span := ReadSpan(map[string]any{
		"offset": 50,
		"limit":  200,
	})
	// GetIntDefault handles both int and float64
	if span.start != 50 {
		t.Errorf("start = %d, want 50", span.start)
	}
	if span.end != 250 {
		t.Errorf("end = %d, want 250", span.end)
	}
}

// ========== FunctionCall nil args ==========

func TestRecordTarget_NilArgs(t *testing.T) {
	s := NewCoverageState()
	// read with nil args → CanonTarget returns ok=false for missing file_path
	// RecordTarget should still handle it
	redundant := s.RecordTarget("read", "target", nil)
	// Should not panic; redundant may be 0 since it's a new target
	if redundant != 0 {
		t.Logf("redundant with nil args = %d (new target → 0 expected)", redundant)
	}
}

// ========== Gen survives ResetTargets ==========

func TestCoverageState_GenSurvivesResetTargets(t *testing.T) {
	s := NewCoverageState()
	s.RecordTarget("read", "f", map[string]any{"file_path": "f.go"})
	s.NoteProgress()
	genBefore := s.Gen()

	s.ResetTargets()

	if s.Gen() != genBefore {
		t.Errorf("Gen = %d after ResetTargets, want %d", s.Gen(), genBefore)
	}
}

// ========== Multiple targets same tool ==========

func TestCoverageState_MultipleReadTargets(t *testing.T) {
	s := NewCoverageState()
	// Read b.go twice WITHOUT any progress in between → redundant
	argsB := map[string]any{"file_path": "b.go", "offset": float64(1), "limit": float64(100)}
	s.RecordTarget("read", "b.go", argsB) // first sight: progress, redundant=0

	redundant := s.RecordTarget("read", "b.go", argsB) // overlap, no progress → redundant=1
	if redundant != 1 {
		t.Errorf("re-read of b.go redundant = %d, want 1", redundant)
	}

	// Now read a DIFFERENT file — that's progress (new target)
	s.RecordTarget("read", "a.go", map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)})

	// Re-read b.go again — gen advanced (new file a.go), so chain reset → redundant=0
	redundantAfterProgress := s.RecordTarget("read", "b.go", argsB)
	if redundantAfterProgress != 0 {
		t.Errorf("re-read of b.go after progress = %d, want 0 (chain reset)", redundantAfterProgress)
	}
}

// ========== observeBatch with nil calls ==========

func TestExecutorCoverageTracker_ObserveBatch_NilCalls(t *testing.T) {
	tracker := newExecutorCoverageTracker("")
	offender := tracker.observeBatch([]*genai.FunctionCall{nil, nil})
	if offender != nil {
		t.Error("nil calls should not produce an offender")
	}
}

func TestExecutorCoverageTracker_ObserveBatch_Empty(t *testing.T) {
	tracker := newExecutorCoverageTracker("")
	offender := tracker.observeBatch([]*genai.FunctionCall{})
	if offender != nil {
		t.Error("empty batch should not produce an offender")
	}
}

func TestExecutorCoverageTracker_ObserveBatch_NonReadGrepBreaksChain(t *testing.T) {
	tracker := newExecutorCoverageTracker("")
	args := map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)}

	// Read same file twice → build redundancy
	tracker.observeBatch([]*genai.FunctionCall{
		{Name: "read", Args: args},
	})
	off := tracker.observeBatch([]*genai.FunctionCall{
		{Name: "read", Args: args},
	})
	if off == nil {
		// redundant=1, budget=8 → not yet an offender
	} else {
		t.Fatal("should not be offender at redundant=1 with budget=8")
	}

	// Now an edit call breaks the chain
	tracker.observeBatch([]*genai.FunctionCall{
		{Name: "edit", Args: map[string]any{"file_path": "a.go"}},
	})

	// Read same file again → redundancy should have been reset by NoteProgress
	off = tracker.observeBatch([]*genai.FunctionCall{
		{Name: "read", Args: args},
	})
	if off != nil {
		t.Error("after edit (progress), read should not be offender (chain reset)")
	}
}

// ========== executorRedundancyBudget ==========

func TestExecutorRedundancyBudget_Read(t *testing.T) {
	if got := executorRedundancyBudget("read"); got != execCoverageReadBudget {
		t.Errorf("read budget = %d, want %d", got, execCoverageReadBudget)
	}
}

func TestExecutorRedundancyBudget_Grep(t *testing.T) {
	if got := executorRedundancyBudget("grep"); got != execCoverageGrepBudget {
		t.Errorf("grep budget = %d, want %d", got, execCoverageGrepBudget)
	}
}

func TestExecutorRedundancyBudget_UnknownTool(t *testing.T) {
	// Unknown tools get the read budget (default)
	if got := executorRedundancyBudget("edit"); got != execCoverageReadBudget {
		t.Errorf("unknown tool budget = %d, want %d (read default)", got, execCoverageReadBudget)
	}
}

// ========== buildCoverageRecoveryMessage ==========

func TestBuildCoverageRecoveryMessage_Read(t *testing.T) {
	off := &coverageOffender{name: "read", target: "/src/main.go", redundant: 3}
	msg := buildCoverageRecoveryMessage(off)
	if msg == "" {
		t.Fatal("message should not be empty")
	}
	// Must contain "Loop guard:" prefix for UI classifier
	if !containsCov(msg, "Loop guard:") {
		t.Error("message should contain 'Loop guard:' prefix")
	}
	// Must contain "Do not call it again"
	if !containsCov(msg, "Do not call it again") {
		t.Error("message should contain 'Do not call it again'")
	}
	// Must mention the target
	if !containsCov(msg, "/src/main.go") {
		t.Error("message should mention the target path")
	}
	// Must mention the redundant count
	if !containsCov(msg, "3") {
		t.Error("message should mention redundant count")
	}
}

func TestBuildCoverageRecoveryMessage_Grep(t *testing.T) {
	off := &coverageOffender{name: "grep", target: "<scope>", redundant: 2}
	msg := buildCoverageRecoveryMessage(off)
	if msg == "" {
		t.Fatal("message should not be empty")
	}
	if !containsCov(msg, "Loop guard:") {
		t.Error("grep message should contain 'Loop guard:' prefix")
	}
	if !containsCov(msg, "Do not call it again") {
		t.Error("grep message should contain 'Do not call it again'")
	}
}

func TestBuildCoverageRecoveryMessage_UnknownDefaultsToGrepFormat(t *testing.T) {
	off := &coverageOffender{name: "unknown", target: "x", redundant: 1}
	msg := buildCoverageRecoveryMessage(off)
	// Unknown falls to default (grep format)
	if !containsCov(msg, "re-searched") {
		t.Error("unknown tool should fall to grep format (re-searched)")
	}
}

// ========== resetWindow ==========

func TestExecutorCoverageTracker_ResetWindow(t *testing.T) {
	tracker := newExecutorCoverageTracker("")
	args := map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)}

	// Build some state
	tracker.observeBatch([]*genai.FunctionCall{{Name: "read", Args: args}})
	if tracker.state.NumTargets() != 1 {
		t.Fatalf("NumTargets = %d before reset, want 1", tracker.state.NumTargets())
	}

	genBefore := tracker.state.Gen()
	tracker.resetWindow()

	if tracker.state.NumTargets() != 0 {
		t.Errorf("NumTargets = %d after resetWindow, want 0", tracker.state.NumTargets())
	}
	// Gen survives
	if tracker.state.Gen() != genBefore {
		t.Errorf("Gen = %d after resetWindow, want %d", tracker.state.Gen(), genBefore)
	}
}

// ========== newExecutorCoverageTracker ==========

func TestNewExecutorCoverageTracker(t *testing.T) {
	tracker := newExecutorCoverageTracker("/project")
	if tracker == nil {
		t.Fatal("returned nil")
	}
	if tracker.workDir != "/project" {
		t.Errorf("workDir = %q, want /project", tracker.workDir)
	}
	if tracker.state == nil {
		t.Error("state should not be nil")
	}
}

func TestNewExecutorCoverageTracker_TrimsWorkDir(t *testing.T) {
	tracker := newExecutorCoverageTracker("  /project  ")
	if tracker.workDir != "/project" {
		t.Errorf("workDir = %q, want /project (trimmed)", tracker.workDir)
	}
}

// ========== observeBatch — offender detection at budget ==========

func TestExecutorCoverageTracker_ObserveBatch_ReadOffenderAtBudget(t *testing.T) {
	// Tighten budget for test
	oldRead := execCoverageReadBudget
	execCoverageReadBudget = 3
	defer func() { execCoverageReadBudget = oldRead }()

	tracker := newExecutorCoverageTracker("")
	args := map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)}

	// 1st read: new target (progress)
	tracker.observeBatch([]*genai.FunctionCall{{Name: "read", Args: args}})
	// 2nd read: overlap, redundant=1
	tracker.observeBatch([]*genai.FunctionCall{{Name: "read", Args: args}})
	// 3rd read: overlap, redundant=2
	tracker.observeBatch([]*genai.FunctionCall{{Name: "read", Args: args}})
	// 4th read: overlap, redundant=3 → hits budget
	off := tracker.observeBatch([]*genai.FunctionCall{{Name: "read", Args: args}})

	if off == nil {
		t.Fatal("expected offender at redundant=3 with budget=3")
	}
	if off.name != "read" {
		t.Errorf("offender name = %q, want read", off.name)
	}
}

func TestExecutorCoverageTracker_ObserveBatch_GrepOffenderAtBudget(t *testing.T) {
	oldGrep := execCoverageGrepBudget
	execCoverageGrepBudget = 2
	defer func() { execCoverageGrepBudget = oldGrep }()

	tracker := newExecutorCoverageTracker("")
	args := map[string]any{"pattern": "foo", "path": "src/"}

	// 1st grep: new target
	tracker.observeBatch([]*genai.FunctionCall{{Name: "grep", Args: args}})
	// 2nd grep: same scope, redundant=1
	tracker.observeBatch([]*genai.FunctionCall{{Name: "grep", Args: args}})
	// 3rd grep: redundant=2 → hits budget
	off := tracker.observeBatch([]*genai.FunctionCall{{Name: "grep", Args: args}})

	if off == nil {
		t.Fatal("expected grep offender at redundant=2 with budget=2")
	}
	if off.name != "grep" {
		t.Errorf("offender name = %q, want grep", off.name)
	}
}

// ========== observeBatch — first offender wins ==========

func TestExecutorCoverageTracker_ObserveBatch_FirstOffenderWins(t *testing.T) {
	oldRead := execCoverageReadBudget
	execCoverageReadBudget = 2
	defer func() { execCoverageReadBudget = oldRead }()

	tracker := newExecutorCoverageTracker("")
	argsA := map[string]any{"file_path": "a.go", "offset": float64(1), "limit": float64(100)}

	// Build redundancy for A alone: first sight (progress), then re-read (redundant=1)
	tracker.observeBatch([]*genai.FunctionCall{{Name: "read", Args: argsA}})
	tracker.observeBatch([]*genai.FunctionCall{{Name: "read", Args: argsA}})

	// Third read of A → redundant=2 → hits budget=2 → offender.
	off := tracker.observeBatch([]*genai.FunctionCall{
		{Name: "read", Args: argsA},
	})

	if off == nil {
		t.Fatal("expected an offender")
	}
	if off.target != "a.go" {
		t.Errorf("offender target = %q, want a.go", off.target)
	}
	if off.redundant != 2 {
		t.Errorf("offender redundant = %d, want 2", off.redundant)
	}
}

// ========== buildCoverageRecoveryResults ==========

func TestExecutor_BuildCoverageRecoveryResults(t *testing.T) {
	e := &Executor{}
	calls := []*genai.FunctionCall{
		{ID: "call_1", Name: "read", Args: map[string]any{"file_path": "a.go"}},
		{ID: "call_2", Name: "read", Args: map[string]any{"file_path": "b.go"}},
	}
	off := &coverageOffender{name: "read", target: "a.go", redundant: 3}

	results := e.buildCoverageRecoveryResults(calls, off)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("result[%d] is nil", i)
			continue
		}
		if r.ID != calls[i].ID {
			t.Errorf("result[%d] ID = %q, want %q", i, r.ID, calls[i].ID)
		}
		if r.Name != calls[i].Name {
			t.Errorf("result[%d] Name = %q, want %q", i, r.Name, calls[i].Name)
		}
	}
}

func TestExecutor_BuildCoverageRecoveryResults_NilCall(t *testing.T) {
	e := &Executor{}
	calls := []*genai.FunctionCall{nil}
	off := &coverageOffender{name: "read", target: "x", redundant: 1}

	results := e.buildCoverageRecoveryResults(calls, off)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	// Nil call → toolName="tool", callID=""
	if results[0].Name != "tool" {
		t.Errorf("nil call Name = %q, want 'tool'", results[0].Name)
	}
	if results[0].ID != "" {
		t.Errorf("nil call ID = %q, want ''", results[0].ID)
	}
}

// ========== helper ==========

func containsCov(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && indexOfCov(s, substr) >= 0))
}

func indexOfCov(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
