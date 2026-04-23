package ui

import (
	"strings"
	"testing"
	"time"
)

func TestAppendThinkingStream_ShowsSingleThinkingHeader(t *testing.T) {
	output := NewOutputModel(DefaultStyles())

	output.AppendThinkingStream("Inspecting")
	output.AppendThinkingStream(" retry policy")
	output.EndThinking()

	rendered := stripAnsi(output.state.content.String())
	if strings.Count(rendered, "Thinking") != 1 {
		t.Fatalf("expected single thinking header, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "Inspecting retry policy") {
		t.Fatalf("expected streaming thinking content, got:\n%s", rendered)
	}
}

// TestHandleToolResultWithInfo_ReadCollapsesByDefault pins the Claude-Code-
// style collapse for Read: the ✓ success line (with path + line count +
// duration) already carries the signal, so we skip the inline head+tail
// preview entirely and show a single "⎿ press e to expand" hint. Users
// who want to see the file content press `e`. The full content stays in
// the tool-output entry store, so `e` can still reveal it.
func TestHandleToolResultWithInfo_ReadCollapsesByDefault(t *testing.T) {
	m := NewModel()
	content := strings.Join([]string{
		"package ui",
		"",
		"func alpha() {}",
		"func beta() {}",
		"func gamma() {}",
		"func delta() {}",
		"func epsilon() {}",
		"func zeta() {}",
		"func eta() {}",
		"func theta() {}",
		"func iota() {}",
		"func kappa() {}",
	}, "\n")

	m.handleToolResultWithInfo(content, "read", "internal/ui/output.go", time.Now().Add(-1500*time.Millisecond))

	rendered := stripAnsi(m.output.state.content.String())

	// Header must still land.
	for _, want := range []string{"Read", "12 lines", "output.go"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Read success header missing %q:\n%s", want, rendered)
		}
	}
	// Expand hint must be the compact form.
	if !strings.Contains(rendered, "press e to expand") {
		t.Fatalf("expected 'press e to expand' hint, got:\n%s", rendered)
	}
	// Inline content preview must NOT appear by default — Read is collapsed.
	for _, shouldNotLeak := range []string{"func alpha", "func kappa", "package ui"} {
		if strings.Contains(rendered, shouldNotLeak) {
			t.Fatalf("Read default should not inline content (%q):\n%s",
				shouldNotLeak, rendered)
		}
	}
	// Removed hints must stay removed.
	for _, removed := range []string{
		"Preview below - press e for full output",
		"[read: ",
		"Full output hidden - press e to expand",
	} {
		if strings.Contains(rendered, removed) {
			t.Fatalf("stale hint %q resurfaced:\n%s", removed, rendered)
		}
	}
}

// TestHandleToolResultWithInfo_BashKeepsPreview: non-Read tools (bash,
// grep, glob) still get the head+tail inline preview — their output is
// the primary signal, not the fact that they ran.
func TestHandleToolResultWithInfo_BashKeepsPreview(t *testing.T) {
	m := NewModel()
	content := strings.Join([]string{
		"line 1", "line 2", "line 3", "line 4", "line 5",
		"line 6", "line 7", "line 8", "line 9", "line 10",
		"line 11", "line 12",
	}, "\n")

	m.handleToolResultWithInfo(content, "bash", "go test ./internal/ui", time.Now().Add(-2*time.Second))
	rendered := stripAnsi(m.output.state.content.String())

	// Bash preview should include at least the first output line.
	if !strings.Contains(rendered, "line 1") {
		t.Fatalf("bash preview should include first output line:\n%s", rendered)
	}
	// And the friendly more-lines marker with expand hint.
	if !strings.Contains(rendered, "more") || !strings.Contains(rendered, "press e") {
		t.Fatalf("bash preview should include 'N more · press e' marker:\n%s", rendered)
	}
}

// TestHandleToolResultWithInfo_UsesExplicitCompactMode: the user-triggered
// global compact mode (toggled via `E` / Ctrl+E) still forces every tool
// — Read included, already collapsed, but also bash/grep/glob — into
// the minimal-hint form. Regression guard on the ToggleAll path.
func TestHandleToolResultWithInfo_UsesExplicitCompactMode(t *testing.T) {
	m := NewModel()
	content := strings.Join([]string{
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
		"line 6",
		"line 7",
		"line 8",
		"line 9",
		"line 10",
		"line 11",
		"line 12",
	}, "\n")

	// ToggleAll twice: first enters AllExpanded, second switches to
	// AllCollapsed (which is what CompactModeActive() reports true on).
	m.toolOutput.ToggleAll()
	m.toolOutput.ToggleAll()
	m.handleToolResultWithInfo(content, "bash", "go test ./internal/ui", time.Now().Add(-2*time.Second))

	rendered := stripAnsi(m.output.state.content.String())
	if !strings.Contains(rendered, "press e to expand") {
		t.Fatalf("compact mode should show 'press e to expand':\n%s", rendered)
	}
	// Must NOT inline the content.
	if strings.Contains(rendered, "line 1") {
		t.Fatalf("compact mode should not inline content:\n%s", rendered)
	}
}
