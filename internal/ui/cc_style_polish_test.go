package ui

import (
	"strings"
	"testing"
	"time"
)

// TestMarkdownHeadingStripsHashMarkers — rendered headings drop the literal #/##
// markup (it's markup, not content); bold+color carries the hierarchy.
func TestMarkdownHeadingStripsHashMarkers(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	out := stripAnsi(p.renderMarkdownLine("## Section Title"))
	if strings.Contains(out, "#") {
		t.Errorf("heading should not render literal # markers: %q", out)
	}
	if !strings.Contains(out, "Section Title") {
		t.Errorf("heading text missing: %q", out)
	}
}

// TestToolExecutingBlockUsesSharedBullet — every tool uses the one ⏺ bullet
// (calm aligned column) rather than a per-tool glyph confetti.
func TestToolExecutingBlockUsesSharedBullet(t *testing.T) {
	s := DefaultStyles()
	for _, name := range []string{"read", "bash", "edit", "grep"} {
		out := stripAnsi(s.FormatToolExecutingBlock(name, map[string]any{"file_path": "x.go"}))
		if !strings.Contains(out, toolBullet) {
			t.Errorf("%s executing block should use the shared bullet %q: %q", name, toolBullet, out)
		}
	}
	// The old per-tool punctuation glyphs ($ for bash) must no longer lead the line.
	bash := strings.TrimSpace(stripAnsi(s.FormatToolExecutingBlock("bash", map[string]any{"command": "go build"})))
	if strings.HasPrefix(bash, "$") {
		t.Errorf("bash should no longer lead with $: %q", bash)
	}
}

// TestEngineStatusNoDotDuringWork — the WRITING work state drops the redundant
// leading ●/○ (the live card already shows an animated spinner for the state).
func TestEngineStatusNoDotDuringWork(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	out := stripAnsi(m.renderEngineStatus())
	if !strings.Contains(out, "WRITING") {
		t.Fatalf("expected WRITING badge: %q", out)
	}
	if strings.ContainsAny(out, "●○") {
		t.Errorf("WRITING badge should have no leading dot: %q", out)
	}
}

// TestInterruptCueSurvivesNarrowWidth — Esc cancellation is cued even on the
// tightest (minimal) layout, where it used to be absent entirely.
func TestInterruptCueSurvivesNarrowWidth(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.runtimeStatus.Provider = "glm"
	m.width = 50 // minimal layout (< 60)
	if got := stripAnsi(m.renderStatusBar()); !strings.Contains(got, "esc") {
		t.Errorf("narrow busy status bar should still cue esc: %q", got)
	}
}

// TestLiveCardShowsElapsed — a streaming card past the 3s threshold shows the
// elapsed clock (the "is it hung or working?" signal), not just sub-states.
func TestLiveCardShowsElapsed(t *testing.T) {
	m := NewModel()
	m.state = StateStreaming
	m.streamStartTime = time.Now().Add(-6 * time.Second)
	line := stripAnsi(m.liveActivityCurrentLine(ActivityFeedSnapshot{}))
	if !strings.Contains(line, " · ") {
		t.Errorf("streaming card past 3s should show elapsed: %q", line)
	}
}

// TestLiveCardSurfacesCurrentActivity — the agent's specific activity label
// reaches the card (the actual renderer during processing) rather than no-op'ing.
func TestLiveCardSurfacesCurrentActivity(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	m.streamStartTime = time.Now()
	m.currentActivity = "Implementing backup/restore"
	got := stripAnsi(m.liveActivityCurrentLine(ActivityFeedSnapshot{}))
	if !strings.Contains(got, "Implementing backup/restore") {
		t.Errorf("card should surface the currentActivity label: %q", got)
	}
}
