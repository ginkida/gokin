package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPermissionPromptNumberedOptions — the permission prompt uses the numbered
// `> 1. … 2. … 3.` list its sibling prompts use, not a flat y/a/n letter row.
func TestPermissionPromptNumberedOptions(t *testing.T) {
	m := Model{
		width:       100,
		permRequest: &PermissionRequestMsg{ToolName: "bash", RiskLevel: "high", Args: map[string]any{"command": "ls"}},
	}
	got := stripAnsi(m.renderPermissionPrompt())
	for _, want := range []string{"> 1. Allow once", "2. Allow for session", "3. Deny"} {
		if !strings.Contains(got, want) {
			t.Errorf("permission prompt missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "y Allow") {
		t.Errorf("permission prompt should not keep the dense y/a/n letter row:\n%s", got)
	}
}

// TestPermissionNumberKeyDecides — pressing the displayed number applies that
// decision (so the numbers aren't decorative); y/a/n still work via the same path.
func TestPermissionNumberKeyDecides(t *testing.T) {
	var got PermissionDecision
	var gotID string
	decided := false
	m := NewModel()
	m.SetPermissionCallback(func(id string, d PermissionDecision) { gotID = id; got = d; decided = true })
	m.permRequest = &PermissionRequestMsg{ID: "req-1", ToolName: "bash", RiskLevel: "high"}
	m.state = StatePermissionPrompt

	_ = m.handlePermissionPromptKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("3")})
	if !decided || got != PermissionDeny {
		t.Errorf("pressing 3 should Deny: decided=%v decision=%v", decided, got)
	}
	if gotID != "req-1" {
		t.Errorf("callback should receive the displayed request's ID, got %q", gotID)
	}
}

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

// TestPlanApprovalRebuilt — the plan-approval prompt is now lipgloss-rendered
// like its siblings: numbered options with a `> ` marker, no `(y)` decoration,
// no `•` separators, and no background-fill selection highlight.
func TestPlanApprovalRebuilt(t *testing.T) {
	steps := []PlanStepInfo{
		{ID: 1, Title: "Extract SessionStore", Description: "move the map + mutex out"},
		{ID: 2, Title: "Wire DI", Description: "pass the store through the constructor"},
	}
	m := Model{
		width:       80,
		planRequest: &PlanApprovalRequestMsg{Title: "Refactor the auth layer", Description: "Splits the store from the client", Steps: steps},
		styles:      DefaultStyles(),
	}
	raw := m.renderPlanApproval()
	got := stripAnsi(raw)

	for _, want := range []string{"Plan Approval", "Step 1: Extract SessionStore", "> 1. Approve", "2. Reject", "3. Request changes"} {
		if !strings.Contains(got, want) {
			t.Errorf("plan approval missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "(y)") || strings.Contains(got, "•") {
		t.Errorf("plan approval should drop the (y) decoration + • separators:\n%s", got)
	}
	if strings.Contains(raw, "\x1b[48") {
		t.Errorf("selected option should not use a Background fill")
	}

	// Feedback sub-mode still renders the input + a back hint.
	m.planFeedbackMode = true
	m.planFeedbackInput = NewInputModel(m.styles, m.workDir)
	if fb := stripAnsi(m.renderPlanApproval()); !strings.Contains(fb, "Enter your feedback") {
		t.Errorf("feedback mode should show the feedback prompt:\n%s", fb)
	}
}

// TestInlineCodeNoDoubleSpaceGap — inline `code` sits tight to its words; the
// old Padding(0,1) inserted a literal double-space gap on each side.
func TestInlineCodeNoDoubleSpaceGap(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	out := stripAnsi(p.renderMarkdownLine("use the `foo()` function here"))
	if strings.Contains(out, "  ") {
		t.Errorf("inline code should sit tight (no double-space gap): %q", out)
	}
	if !strings.Contains(out, "foo()") {
		t.Errorf("inline code text missing: %q", out)
	}
}

// TestListBulletsAreOneCalmMarker — unordered lists use ONE dim bullet at every
// depth (nesting by indent), not a cycle of 6 bold geometric glyphs.
func TestListBulletsAreOneCalmMarker(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	top, ok := p.renderListItem("- top")
	if !ok {
		t.Fatal("'- top' should parse as a list item")
	}
	nested, ok := p.renderListItem("  - nested")
	if !ok {
		t.Fatal("'  - nested' should parse as a list item")
	}
	top, nested = stripAnsi(top), stripAnsi(nested)
	if !strings.Contains(top, "• top") {
		t.Errorf("top-level should use the one bullet: %q", top)
	}
	if !strings.HasPrefix(nested, "  • ") {
		t.Errorf("nested should be indent + same bullet: %q", nested)
	}
	for _, g := range []string{"●", "○", "■", "□", "◆", "◇"} {
		if strings.Contains(top+nested, g) {
			t.Errorf("list should not use the geometric confetti glyph %q", g)
		}
	}
}

// TestCodeFenceHasNoFullWidthRule — the code-block fence is a quiet dim label,
// not the full-width `─` rules the project removed elsewhere.
func TestCodeFenceHasNoFullWidthRule(t *testing.T) {
	p := NewMarkdownStreamParser(DefaultStyles())
	top := stripAnsi(p.RenderCodeFenceTop(RenderedBlock{Language: "go"}, 100))
	if strings.Contains(top, "────") {
		t.Errorf("code fence top should not draw a full-width rule: %q", top)
	}
	if !strings.Contains(top, "go") {
		t.Errorf("code fence should still show the dim language label: %q", top)
	}
	if bot := p.RenderCodeFenceBottom(100); bot != "" {
		t.Errorf("code fence bottom should emit nothing, got %q", bot)
	}
}

// TestToolExecutingBlockUsesSharedBullet — every tool uses the one shared dim ▪ marker
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

// TestToolResultMergedLine_NoDuplication pins the user-driven redesign end to
// end: a completed tool is ONE line "▪ Name(target) · outcome" — the tool name
// and the path/command each appear exactly once (no separate call row + result
// row that repeated them), and shell plumbing is stripped.
func TestToolResultMergedLine_NoDuplication(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.handleToolResultWithInfo(strings.Repeat("x\n", 175), "read",
		"~/projects/glm/internal/shared/credentials.go", time.Now().Add(-50*time.Millisecond))
	m.flushPendingToolLines() // buffered by aggregation; a single entry flushes as the legacy line
	rendered := stripAnsi(m.output.state.content.String())

	if !strings.Contains(rendered, "Read(credentials.go)") {
		t.Errorf("want merged Read(credentials.go):\n%s", rendered)
	}
	if !strings.Contains(rendered, "175 lines") {
		t.Errorf("want outcome '175 lines':\n%s", rendered)
	}
	if n := strings.Count(rendered, "Read"); n != 1 {
		t.Errorf("tool name should appear ONCE (no call+result repeat), got %d:\n%s", n, rendered)
	}

	// Bash: plumbing stripped, command + name not repeated.
	m2 := NewModel()
	m2.width = 100
	m2.handleToolResultWithInfo("ok\n", "bash",
		"go build ./... 2>&1 | head -40", time.Now().Add(-1900*time.Millisecond))
	m2.flushPendingToolLines()
	r2 := stripAnsi(m2.output.state.content.String())
	if !strings.Contains(r2, "Bash(go build ./...)") {
		t.Errorf("bash plumbing should strip to Bash(go build ./...):\n%s", r2)
	}
	if strings.Contains(r2, "2>&1") || strings.Contains(r2, "head -40") {
		t.Errorf("shell plumbing should not be shown:\n%s", r2)
	}
	if n := strings.Count(r2, "Bash"); n != 1 {
		t.Errorf("Bash should appear ONCE, got %d:\n%s", n, r2)
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
