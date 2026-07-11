package loops

import (
	"context"
	"strings"
	"testing"
	"time"
)

// --- parseHandoff -----------------------------------------------------------

func TestParseHandoff_BasicBlock(t *testing.T) {
	output := "I refactored the parser and added tests.\n\n" +
		"HANDOFF:\n" +
		"- DONE: parser refactor + tests in parse_test.go\n" +
		"- NEXT: wire the new parser into cmd/serve\n" +
		"- BLOCKER: none\n" +
		"\n" +
		"Refactored the parser; wiring comes next."

	got := parseHandoff(output)
	want := "- DONE: parser refactor + tests in parse_test.go\n" +
		"- NEXT: wire the new parser into cmd/serve\n" +
		"- BLOCKER: none"
	if got != want {
		t.Fatalf("parseHandoff:\n got: %q\nwant: %q", got, want)
	}
}

func TestParseHandoff_SameLineOneLiner(t *testing.T) {
	got := parseHandoff("prose\n\nHANDOFF: everything done, verify with go test next\n\nsummary.")
	if got != "everything done, verify with go test next" {
		t.Fatalf("one-line handoff = %q", got)
	}
}

func TestParseHandoff_StopsAtBlankLine(t *testing.T) {
	got := parseHandoff("HANDOFF:\n- NEXT: step\n\nThis summary sentence must not leak into the handoff.")
	if strings.Contains(got, "summary sentence") {
		t.Fatalf("handoff leaked past the blank line: %q", got)
	}
}

func TestParseHandoff_ExcludesDoneMarker(t *testing.T) {
	got := parseHandoff("HANDOFF:\n- DONE: all of it\ndone.")
	if strings.Contains(strings.ToLower(got), "done.") && strings.HasSuffix(strings.ToLower(got), "done.") {
		t.Fatalf("self-termination marker must not be part of the working state: %q", got)
	}
	if !strings.Contains(got, "all of it") {
		t.Fatalf("real content lost: %q", got)
	}
}

func TestParseHandoff_CaseInsensitiveAndLastWins(t *testing.T) {
	output := "handoff: stale early state\n\nmore work happened\n\nHandoff: fresh final state\n\nsummary."
	got := parseHandoff(output)
	if got != "fresh final state" {
		t.Fatalf("last handoff must win, got %q", got)
	}
}

func TestParseHandoff_AbsentReturnsEmpty(t *testing.T) {
	if got := parseHandoff("just prose, no block\n\nsummary."); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := parseHandoff(""); got != "" {
		t.Fatalf("expected empty for empty output, got %q", got)
	}
}

func TestParseHandoff_BoundedRuneSafe(t *testing.T) {
	// Cyrillic content (2-byte runes) far beyond the cap — the bound must cut
	// by RUNES, never mid-rune (the bounded-model-input class).
	huge := "HANDOFF: " + strings.Repeat("работа ", MaxHandoffRunes)
	got := parseHandoff(huge)
	if n := len([]rune(got)); n > MaxHandoffRunes+1 { // +1 for the ellipsis
		t.Fatalf("handoff not bounded: %d runes", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated handoff should be ellipsized, got tail %q", got[len(got)-12:])
	}
}

// --- latestHandoff -----------------------------------------------------------

func TestLatestHandoff_SkipsHandoffLessNewest(t *testing.T) {
	iters := []Iteration{
		{N: 1, Handoff: "old state"},
		{N: 2, Handoff: "current state"},
		{N: 3, Transient: true}, // provider hiccup, no output — must not erase state
	}
	h, n := latestHandoff(iters)
	if h != "current state" || n != 2 {
		t.Fatalf("latestHandoff = (%q, %d), want (current state, 2)", h, n)
	}
}

// --- summarizeOutput ----------------------------------------------------------

func TestSummarizeOutput_TrailingHandoffBlockIsNotTheSummary(t *testing.T) {
	output := "Implemented the config loader.\n\nHANDOFF:\n- NEXT: add validation"
	got := summarizeOutput(output, "fallback")
	if strings.Contains(strings.ToUpper(got), "HANDOFF") {
		t.Fatalf("a trailing HANDOFF block must not become the toast summary, got %q", got)
	}
	if !strings.Contains(got, "config loader") {
		t.Fatalf("summary should be the preceding prose paragraph, got %q", got)
	}
}

// --- capFilesTouched ----------------------------------------------------------

func TestCapFilesTouched(t *testing.T) {
	var many []string
	for i := 0; i < MaxFilesTouchedPerIteration+5; i++ {
		many = append(many, "file"+strings.Repeat("x", i)+".go")
	}
	got := capFilesTouched(many)
	if len(got) != MaxFilesTouchedPerIteration {
		t.Fatalf("cap = %d, want %d", len(got), MaxFilesTouchedPerIteration)
	}
	if got := capFilesTouched(nil); got != nil {
		t.Fatalf("nil in, nil out — got %v", got)
	}
}

// --- BuildIterationPrompt -----------------------------------------------------

func loopWithIterations(task string, iters ...Iteration) *Loop {
	l := &Loop{ID: "loop-x", Task: task, Mode: ModeSelfPaced, Status: StatusRunning, CreatedAt: time.Now()}
	for _, it := range iters {
		l.Iterations = append(l.Iterations, it)
		l.IterationCount++
		if it.OK {
			l.SuccessCount++
		} else {
			l.FailureCount++
		}
	}
	return l
}

func TestBuildIterationPrompt_InjectsLatestHandoffVerbatim(t *testing.T) {
	l := loopWithIterations("fix the flaky auth tests",
		Iteration{N: 1, OK: true, StartedAt: time.Now(), Summary: "started", Handoff: "- DONE: reproduced flake\n- NEXT: fix token expiry race in auth_test.go"},
	)
	prompt := BuildIterationPrompt(l)
	if !strings.Contains(prompt, "- NEXT: fix token expiry race in auth_test.go") {
		t.Fatalf("handoff must be injected verbatim:\n%s", prompt)
	}
	if !strings.Contains(prompt, "working state from iteration #1") {
		t.Fatalf("handoff attribution missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "don't re-derive or redo") {
		t.Fatalf("continue-instruction missing:\n%s", prompt)
	}
}

func TestBuildIterationPrompt_CutoffNoteWhenPreviousFailedNonTransient(t *testing.T) {
	l := loopWithIterations("implement the exporter",
		Iteration{N: 1, OK: false, StartedAt: time.Now(), Summary: "iteration did not complete cleanly",
			Handoff: "- IN PROGRESS: writing exporter.go, csv branch done, json branch half-written"},
	)
	prompt := BuildIterationPrompt(l)
	if !strings.Contains(prompt, "did NOT finish cleanly") {
		t.Fatalf("cut-off continuation note missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "json branch half-written") {
		t.Fatalf("handoff from the FAILED iteration must still be injected:\n%s", prompt)
	}
}

func TestBuildIterationPrompt_TransientFailureDoesNotClaimCutoff(t *testing.T) {
	l := loopWithIterations("implement the exporter",
		Iteration{N: 1, OK: true, StartedAt: time.Now(), Summary: "good progress", Handoff: "- NEXT: tests"},
		Iteration{N: 2, OK: false, Transient: true, StartedAt: time.Now(), Summary: "provider overloaded"},
	)
	prompt := BuildIterationPrompt(l)
	if strings.Contains(prompt, "did NOT finish cleanly") {
		t.Fatalf("a transient provider failure is not a cut-off iteration:\n%s", prompt)
	}
	if !strings.Contains(prompt, "- NEXT: tests") {
		t.Fatalf("handoff must survive across a transient failure:\n%s", prompt)
	}
}

func TestBuildIterationPrompt_ActionCoachesHandoffMonitorDoesNot(t *testing.T) {
	action := BuildIterationPrompt(loopWithIterations("fix bugs in the app"))
	if !strings.Contains(action, "HANDOFF:") {
		t.Fatalf("action task must coach the HANDOFF block:\n%s", action)
	}
	monitor := BuildIterationPrompt(loopWithIterations("monitor the deploy status"))
	if strings.Contains(monitor, "HANDOFF:") {
		t.Fatalf("monitor task must not carry handoff coaching:\n%s", monitor)
	}
}

func TestBuildIterationPrompt_ShowsFilesTouchedInRecentContext(t *testing.T) {
	l := loopWithIterations("fix bugs",
		Iteration{N: 1, OK: true, MadeChanges: true, StartedAt: time.Now(), Summary: "fixed the parser",
			FilesTouched: []string{"internal/parse/parse.go", "internal/parse/parse_test.go"}},
	)
	prompt := BuildIterationPrompt(l)
	if !strings.Contains(prompt, "files: internal/parse/parse.go, internal/parse/parse_test.go") {
		t.Fatalf("files touched must anchor the recent context:\n%s", prompt)
	}
}

func TestRenderFilesTouchedInline_CapsDisplay(t *testing.T) {
	files := []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go"}
	got := renderFilesTouchedInline(files)
	if !strings.Contains(got, "+2 more") {
		t.Fatalf("display cap missing: %q", got)
	}
	if strings.Contains(got, "e.go") {
		t.Fatalf("beyond-cap files should be summarized, not listed: %q", got)
	}
}

// --- fireOne end-to-end --------------------------------------------------------

// TestFireOne_RecordsHandoffAndFilesTouched pins the full capture path: a
// spawner result carrying output-with-handoff + touched files must land on
// the recorded Iteration (handoff parsed, files capped), and the NEXT
// iteration's prompt must carry both forward.
func TestFireOne_RecordsHandoffAndFilesTouched(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("fix bugs in this app", ModeSelfPaced, 0)

	output := "Fixed the off-by-one.\n\n" +
		"HANDOFF:\n- DONE: off-by-one in pager.go\n- NEXT: the nil deref in cache.go\n\n" +
		"Fixed the pager bug; cache bug next."
	var files []string
	for i := 0; i < MaxFilesTouchedPerIteration+3; i++ {
		files = append(files, "pkg/file"+strings.Repeat("z", i)+".go")
	}
	spawn := func(ctx context.Context, prompt string) (SpawnResult, error) {
		return SpawnResult{Output: output, OK: true, MadeChanges: true, FilesTouched: files}, nil
	}
	r := NewRunner(mgr, spawn, (&fakeIdle{}).check)
	r.fireOne(context.Background(), l)

	got, _ := mgr.Get(l.ID)
	if got.IterationCount != 1 {
		t.Fatalf("iteration not recorded")
	}
	it := got.Iterations[0]
	if !strings.Contains(it.Handoff, "NEXT: the nil deref in cache.go") {
		t.Fatalf("handoff not captured on the iteration: %q", it.Handoff)
	}
	if len(it.FilesTouched) != MaxFilesTouchedPerIteration {
		t.Fatalf("files not capped: %d", len(it.FilesTouched))
	}
	if strings.Contains(strings.ToUpper(it.Summary), "HANDOFF") {
		t.Fatalf("summary polluted by the handoff block: %q", it.Summary)
	}

	next := BuildIterationPrompt(got)
	if !strings.Contains(next, "NEXT: the nil deref in cache.go") {
		t.Fatalf("next prompt must carry the handoff forward:\n%s", next)
	}
}

// --- markdown render ------------------------------------------------------------

func TestRenderLoopMarkdown_IncludesFilesAndHandoff(t *testing.T) {
	l := loopWithIterations("fix bugs",
		Iteration{N: 1, OK: true, StartedAt: time.Now(), Summary: "fixed the parser",
			FilesTouched: []string{"internal/parse/parse.go"},
			Handoff:      "- DONE: parser\n- NEXT: cache"},
	)
	md := RenderLoopMarkdown(l)
	if !strings.Contains(md, "Files: internal/parse/parse.go") {
		t.Fatalf("markdown must list files touched:\n%s", md)
	}
	if !strings.Contains(md, "> - DONE: parser") || !strings.Contains(md, "> - NEXT: cache") {
		t.Fatalf("markdown must blockquote the handoff:\n%s", md)
	}
}

// A bulleted handoff "- NEXT:" line must not shadow the real pacing hint —
// parseNextHint takes the first line-start "next:" match, and the handoff
// coaching deliberately prescribes BULLETED lines so plan-state lines never
// sit at line start.
func TestParseNextHint_BulletedHandoffNextIsNotAHint(t *testing.T) {
	out := "HANDOFF:\n- NEXT: fix the nil deref in cache.go\n\nnext: 30m\n\nsummary."
	if got := parseNextHint(out); got != "30m" {
		t.Fatalf("parseNextHint = %q, want the real pacing hint 30m", got)
	}
}
