package ui

import (
	"errors"
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
// preview entirely. The body is silent (no expand hint inline — the `e`
// shortcut is documented in the shortcuts overlay and contextual hints).
// Users who want to see the file content press `e`. The full content
// stays in the tool-output entry store, so `e` can still reveal it.
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
	// And the quiet more-lines marker (no keyboard instruction — that's
	// in the shortcuts overlay).
	if !strings.Contains(rendered, "more line") {
		t.Fatalf("bash preview should include 'N more lines' marker:\n%s", rendered)
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
	// Compact mode collapses silently — body is empty, no inline content.
	if strings.Contains(rendered, "line 1") {
		t.Fatalf("compact mode should not inline content:\n%s", rendered)
	}
	// Header must still land.
	if !strings.Contains(rendered, "Bash") {
		t.Fatalf("compact mode should still show ✓ tool header:\n%s", rendered)
	}
}

func TestHandleToolResultWithStatus_FailedUsesErrorBlock(t *testing.T) {
	m := NewModel()

	m.handleToolResultWithStatus("stderr line\nmore detail", "bash", "go test ./...", time.Now().Add(-500*time.Millisecond), true, "exit status 1")
	rendered := stripAnsi(m.output.state.content.String())

	if !strings.Contains(rendered, "✗ bash") {
		t.Fatalf("failed tool should render error block:\n%s", rendered)
	}
	if !strings.Contains(rendered, "exit status 1") {
		t.Fatalf("failed tool should include error detail:\n%s", rendered)
	}
	if strings.Contains(rendered, "✓ Bash") {
		t.Fatalf("failed tool must not render success block:\n%s", rendered)
	}
}

func TestToolResultMsg_FailedMarksActivityFeedEntryFailed(t *testing.T) {
	m := NewModel()
	m.width = 100

	updated, _ := m.Update(ToolCallMsg{
		Name: "bash",
		Args: map[string]any{"command": "go test ./..."},
	})
	mm := updated.(Model)
	updated, _ = mm.Update(ToolResultMsg{
		Name:   "bash",
		Failed: true,
		Error:  "exit status 1",
	})
	mm = updated.(Model)

	if len(mm.activityFeed.entries) != 1 {
		t.Fatalf("activity entries = %d, want 1", len(mm.activityFeed.entries))
	}
	entry := mm.activityFeed.entries[0]
	if entry.Status != ActivityFailed {
		t.Fatalf("activity status = %v, want ActivityFailed", entry.Status)
	}
	if !strings.Contains(entry.ResultSummary, "exit status 1") {
		t.Fatalf("activity summary = %q, want error detail", entry.ResultSummary)
	}
	if mm.responseToolFailures != 1 {
		t.Fatalf("responseToolFailures = %d, want 1", mm.responseToolFailures)
	}
}

func TestToolResultMsg_MatchesParallelSameNameToolByArgs(t *testing.T) {
	m := NewModel()
	m.width = 100

	updated, _ := m.Update(ToolCallMsg{
		Name: "read",
		Args: map[string]any{"file_path": "internal/ui/first.go"},
	})
	mm := updated.(Model)
	updated, _ = mm.Update(ToolCallMsg{
		Name: "read",
		Args: map[string]any{"file_path": "internal/ui/second.go"},
	})
	mm = updated.(Model)
	updated, _ = mm.Update(ToolResultMsg{
		Name:    "read",
		Args:    map[string]any{"file_path": "internal/ui/second.go"},
		Content: "package ui\n",
	})
	mm = updated.(Model)

	if len(mm.activeToolCalls) != 1 {
		t.Fatalf("active tool calls = %d, want 1", len(mm.activeToolCalls))
	}
	if got := mm.activeToolCalls[0].info; !strings.Contains(got, "first.go") {
		t.Fatalf("remaining active tool info = %q, want first read still running", got)
	}
	if len(mm.activityFeed.entries) != 2 {
		t.Fatalf("activity entries = %d, want 2", len(mm.activityFeed.entries))
	}
	if mm.activityFeed.entries[0].Status != ActivityRunning {
		t.Fatalf("first read status = %v, want running", mm.activityFeed.entries[0].Status)
	}
	if mm.activityFeed.entries[1].Status != ActivityCompleted {
		t.Fatalf("second read status = %v, want completed", mm.activityFeed.entries[1].Status)
	}

	rendered := stripAnsi(mm.output.state.content.String())
	if !strings.Contains(rendered, "second.go") {
		t.Fatalf("completed result should use second read info:\n%s", rendered)
	}
}

func TestResponseMetadataAfterDoneKeepsToolTimingBreakdown(t *testing.T) {
	m := NewModel()
	m.responseToolDuration = 1500 * time.Millisecond
	m.responseToolCount = 2
	m.responseToolFailures = 1

	updated, _ := m.Update(ResponseDoneMsg{})
	mm := updated.(Model)
	if mm.responseToolCount != 2 {
		t.Fatalf("ResponseDone reset tool count before metadata, got %d", mm.responseToolCount)
	}

	updated, _ = mm.Update(ResponseMetadataMsg{Duration: 3 * time.Second})
	mm = updated.(Model)
	rendered := stripAnsi(mm.output.state.content.String())

	if !strings.Contains(rendered, "thinking 1.5s") || !strings.Contains(rendered, "tools 1.5s (2 tools · 1 failed)") {
		t.Fatalf("metadata footer missing timing breakdown:\n%s", rendered)
	}
	if mm.responseToolCount != 0 || mm.responseToolDuration != 0 || mm.responseToolFailures != 0 {
		t.Fatalf("metadata should reset tool timing, count=%d failures=%d duration=%v", mm.responseToolCount, mm.responseToolFailures, mm.responseToolDuration)
	}
}

func TestResponseMetadataShowsFailedToolCount(t *testing.T) {
	m := NewModel()
	m.responseToolDuration = 2 * time.Second
	m.responseToolCount = 3
	m.responseToolFailures = 1

	footer := stripAnsi(m.renderResponseMetadata(ResponseMetadataMsg{Duration: 5 * time.Second}))
	if !strings.Contains(footer, "tools 2.0s (3 tools · 1 failed)") {
		t.Fatalf("metadata footer missing failed tool count:\n%s", footer)
	}
}

func TestFormatToolRunSummary(t *testing.T) {
	cases := []struct {
		count       int
		failures    int
		includeUsed bool
		want        string
	}{
		{count: 0, want: ""},
		{count: 1, want: "1 tool"},
		{count: 2, includeUsed: true, want: "2 tools used"},
		{count: 3, failures: 1, want: "3 tools · 1 failed"},
		{count: 3, failures: 1, includeUsed: true, want: "3 tools used · 1 failed"},
	}

	for _, c := range cases {
		if got := formatToolRunSummary(c.count, c.failures, c.includeUsed); got != c.want {
			t.Fatalf("formatToolRunSummary(%d,%d,%v) = %q, want %q", c.count, c.failures, c.includeUsed, got, c.want)
		}
	}
}

func TestErrorSettlesActiveToolCalls(t *testing.T) {
	m := NewModel()
	m.width = 100

	updated, _ := m.Update(ToolCallMsg{
		Name: "bash",
		Args: map[string]any{"command": "go test ./..."},
	})
	mm := updated.(Model)
	updated, _ = mm.Update(ErrorMsg(errors.New("request failed")))
	mm = updated.(Model)

	if len(mm.activeToolCalls) != 0 {
		t.Fatalf("active tool calls not cleared: %d", len(mm.activeToolCalls))
	}
	if mm.activityFeed.HasActiveEntries() {
		t.Fatal("activity feed still has active entries after error")
	}
	if len(mm.activityFeed.entries) != 1 || mm.activityFeed.entries[0].Status != ActivityFailed {
		t.Fatalf("activity entry not marked failed: %+v", mm.activityFeed.entries)
	}
	if !strings.Contains(mm.activityFeed.entries[0].ResultSummary, "request failed") {
		t.Fatalf("activity summary = %q, want request error", mm.activityFeed.entries[0].ResultSummary)
	}
}

func TestResponseDoneSettlesDanglingToolCalls(t *testing.T) {
	m := NewModel()
	m.width = 100

	updated, _ := m.Update(ToolCallMsg{
		Name: "read",
		Args: map[string]any{"file_path": "internal/ui/tui.go"},
	})
	mm := updated.(Model)
	updated, _ = mm.Update(ResponseDoneMsg{})
	mm = updated.(Model)

	if len(mm.activeToolCalls) != 0 {
		t.Fatalf("active tool calls not cleared: %d", len(mm.activeToolCalls))
	}
	if mm.activityFeed.HasActiveEntries() {
		t.Fatal("activity feed still has active entries after ResponseDone")
	}
	if len(mm.activityFeed.entries) != 1 || mm.activityFeed.entries[0].Status != ActivityFailed {
		t.Fatalf("dangling tool should be marked failed, entries=%+v", mm.activityFeed.entries)
	}
}
