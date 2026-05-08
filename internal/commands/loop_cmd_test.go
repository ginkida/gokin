package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/loops"
)

// fakeLoopMgr is a minimal LoopManager double for shape-only command
// tests. Records the last Add call so we can assert "interval was
// parsed" vs "fell through to self-paced" without spinning up a real
// Manager + Storage.
type fakeLoopMgr struct {
	loops       []*loops.Loop
	addedTask   string
	addedMode   loops.Mode
	addedSecs   int64
	addCalls    int
	removed     string
	getReturns  *loops.Loop // when non-nil, Get returns this regardless of ID
}

func (f *fakeLoopMgr) Add(task string, mode loops.Mode, intervalSeconds int64, opts ...loops.AddOption) (*loops.Loop, error) {
	f.addCalls++
	f.addedTask = task
	f.addedMode = mode
	f.addedSecs = intervalSeconds
	l := &loops.Loop{ID: "loop-fake0001", Task: task, Mode: mode, IntervalSeconds: intervalSeconds, Status: loops.StatusRunning}
	f.loops = append(f.loops, l)
	return l, nil
}

func (f *fakeLoopMgr) List() []*loops.Loop                         { return f.loops }
func (f *fakeLoopMgr) Active() []*loops.Loop                       { return f.loops }
func (f *fakeLoopMgr) Get(id string) (*loops.Loop, bool) {
	if f.getReturns != nil {
		return f.getReturns, true
	}
	return nil, false
}
func (f *fakeLoopMgr) Stop(id string) error                        { return nil }
func (f *fakeLoopMgr) Pause(id string) error                       { return nil }
func (f *fakeLoopMgr) Resume(id string) error                      { return nil }
func (f *fakeLoopMgr) FireNow(id string) error                     { return nil }
func (f *fakeLoopMgr) Remove(id string) error                      { f.removed = id; return nil }

// TestParseLoopInterval covers the accept and reject paths of the
// shorthand parser. Pinned in tests so future "be more lenient"
// changes can't silently re-introduce the multi-unit silent
// fallthrough bug (e.g. "1h30m foo" mistakenly treated as a self-paced
// loop with task "1h30m foo").
func TestParseLoopInterval(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		secs  int64
	}{
		{"30s", true, 30},
		{"5m", true, 300},
		{"1h", true, 3600},
		{"2d", true, 172800},
		{"1h30m", false, 0},  // multi-unit not accepted
		{"5", false, 0},       // missing unit
		{"5x", false, 0},      // unknown unit
		{"-5m", false, 0},     // negative
		{"0m", false, 0},      // zero rejected
		{"", false, 0},        // empty
		{"foo", false, 0},     // not a shape at all
	}
	for _, tc := range cases {
		secs, ok := parseLoopInterval(tc.in)
		if ok != tc.ok || secs != tc.secs {
			t.Errorf("parseLoopInterval(%q) = (%d, %v), want (%d, %v)",
				tc.in, secs, ok, tc.secs, tc.ok)
		}
	}
}

// TestLoopCommand_RejectsLooksLikeIntervalGarbage: previously the
// command silently fell through to self-paced mode when the first arg
// looked like an interval but didn't parse. That doubled some users'
// LLM bills (a "/loop 1h30m run tests" became a self-paced loop with
// the malformed interval as part of the task description). The new
// guard rejects with an actionable error so the user sees the typo.
func TestLoopCommand_RejectsLooksLikeIntervalGarbage(t *testing.T) {
	mgr := &fakeLoopMgr{}
	cmd := &LoopCommand{}
	out, err := cmd.executeWithMgr(context.Background(), mgr, []string{"1h30m", "run", "tests"})
	if err != nil {
		t.Fatalf("executeWithMgr: %v", err)
	}
	if !strings.Contains(out, "not recognized") {
		t.Errorf("expected rejection message, got: %s", out)
	}
	if mgr.addCalls != 0 {
		t.Errorf("Add called %d times; expected 0 (garbage interval should not start a loop)", mgr.addCalls)
	}
}

// TestLoopCommand_AcceptsSingleUnitInterval: positive control for the
// rejection above — the same path with a clean interval still creates
// the loop.
func TestLoopCommand_AcceptsSingleUnitInterval(t *testing.T) {
	mgr := &fakeLoopMgr{}
	cmd := &LoopCommand{}
	_, err := cmd.executeWithMgr(context.Background(), mgr, []string{"5m", "run", "tests"})
	if err != nil {
		t.Fatalf("executeWithMgr: %v", err)
	}
	if mgr.addCalls != 1 {
		t.Fatalf("Add called %d times; expected 1", mgr.addCalls)
	}
	if mgr.addedMode != loops.ModeInterval {
		t.Errorf("mode = %s, want interval", mgr.addedMode)
	}
	if mgr.addedSecs != 300 {
		t.Errorf("seconds = %d, want 300", mgr.addedSecs)
	}
	if mgr.addedTask != "run tests" {
		t.Errorf("task = %q, want %q", mgr.addedTask, "run tests")
	}
}

// TestLoopCommand_OutputRendersFullMarkdown: /loop output <id>
// renders the loop's full markdown including iteration summaries
// (full text, not the 80-char truncation that /loop status uses for
// inline display). Pinned in tests so a future "let's truncate
// output too" change can't silently regress.
func TestLoopCommand_OutputRendersFullMarkdown(t *testing.T) {
	now := time.Now()
	mgr := &fakeLoopMgr{
		loops: []*loops.Loop{{
			ID:           "loop-out",
			Task:         "Refactor user service",
			Mode:         loops.ModeSelfPaced,
			Status:       loops.StatusRunning,
			CreatedAt:    now,
			UpdateMemory: true,
			IterationCount: 1,
			SuccessCount:   1,
			Iterations: []loops.Iteration{
				{N: 1, StartedAt: now, Duration: time.Second, Summary: "Identified the cause", OK: true},
			},
		}},
	}
	// Get returns the first loop regardless of ID.
	mgr.getReturns = mgr.loops[0]

	cmd := &LoopCommand{}
	out, err := cmd.executeWithMgr(context.Background(), mgr, []string{"output", "loop-out"})
	if err != nil {
		t.Fatalf("executeWithMgr: %v", err)
	}

	for _, want := range []string{
		"# Loop loop-out",
		"Refactor user service",
		"Identified the cause",
		".gokin/loops/loop-out.md",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q. Got:\n%s", want, out)
		}
	}
}

// TestLoopCommand_OutputUnknownID: /loop output on a missing ID
// returns a friendly "not found" message rather than crashing.
func TestLoopCommand_OutputUnknownID(t *testing.T) {
	mgr := &fakeLoopMgr{} // empty; Get always returns (nil, false)
	cmd := &LoopCommand{}
	out, err := cmd.executeWithMgr(context.Background(), mgr, []string{"output", "loop-missing"})
	if err != nil {
		t.Fatalf("executeWithMgr: %v", err)
	}
	if !strings.Contains(out, "not found") {
		t.Errorf("expected 'not found' message, got: %s", out)
	}
}

// TestLoopCommand_FallsThroughToSelfPacedForRealTasks: confirms that a
// task starting with a number like "5 failing tests" doesn't trigger
// the rejection — only fully-alphanumeric "looks-like-interval" tokens
// should. The intervalShapeRe is anchored: `^[0-9]+[a-zA-Z]+$`, so a
// number-then-space is fine.
func TestLoopCommand_FallsThroughToSelfPacedForRealTasks(t *testing.T) {
	mgr := &fakeLoopMgr{}
	cmd := &LoopCommand{}
	out, err := cmd.executeWithMgr(context.Background(), mgr, []string{"check", "the", "deploy"})
	if err != nil {
		t.Fatalf("executeWithMgr: %v", err)
	}
	if mgr.addCalls != 1 {
		t.Fatalf("Add called %d times; expected 1; output=%s", mgr.addCalls, out)
	}
	if mgr.addedMode != loops.ModeSelfPaced {
		t.Errorf("mode = %s, want self-paced", mgr.addedMode)
	}
	if mgr.addedTask != "check the deploy" {
		t.Errorf("task = %q, want %q", mgr.addedTask, "check the deploy")
	}
}
