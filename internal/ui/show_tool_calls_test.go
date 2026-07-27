package ui

import (
	"strings"
	"testing"
	"time"
)

// show_tool_calls gates the transcript tool rows (the merged ▪ Name(target)
// lines emitted on tool completion). Status-bar spinner, activity feed and
// timing bookkeeping are unaffected — only the scrollback rows.
func TestShowToolCallsToggleSuppressesTranscriptRows(t *testing.T) {
	m := *NewModel()
	m.width = 100

	run := func(m Model, path string) Model {
		next, _ := m.Update(ToolCallMsg{Name: "write", Args: map[string]any{"file_path": path}})
		m = next.(Model)
		next, _ = m.Update(ToolResultMsg{Name: "write", Args: map[string]any{"file_path": path}, Content: "Created new file: " + path + " (100 bytes)"})
		return next.(Model)
	}

	// Default: rows render.
	m = run(m, "/w/x.go")
	if got := stripAnsi(m.output.state.content.String()); !strings.Contains(got, "x.go") {
		t.Fatalf("default showToolCalls=true should render the tool row:\n%s", got)
	}

	// Disabled: no new row.
	m.SetShowToolCalls(false)
	m = run(m, "/w/y.go")
	if got := stripAnsi(m.output.state.content.String()); strings.Contains(got, "y.go") {
		t.Fatalf("showToolCalls=false should suppress the tool row:\n%s", got)
	}
	if m.lastToolOutputIndex < 0 {
		t.Fatal("hidden tool row should still preserve its output for Ctrl+E")
	}
	entry := m.toolOutput.GetEntry(m.lastToolOutputIndex)
	if entry == nil || entry.ToolName != "write" || !strings.Contains(entry.FullContent, "y.go") {
		t.Fatalf("latest hidden tool output was not preserved: %#v", entry)
	}
}

// The edit display-diff stash (pendingEditDiff) is consumed by the renderer —
// with tool rows hidden it must be dropped, not leaked into the next visible
// tool result.
func TestShowToolCallsDisabledDropsPendingEditDiff(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.SetShowToolCalls(false)

	next, _ := m.Update(ToolCallMsg{Name: "edit", Args: map[string]any{"file_path": "/w/e.go"}})
	m = next.(Model)
	next, _ = m.Update(ToolResultMsg{
		Name:    "edit",
		Args:    map[string]any{"file_path": "/w/e.go"},
		Content: "Edited /w/e.go",
		Diff:    "@@ -1 +1 @@\n-old\n+new",
	})
	m = next.(Model)
	if m.pendingEditDiff != nil {
		t.Fatal("pendingEditDiff should be dropped when tool rows are hidden")
	}
}

func TestShowToolCallsDisabledDropsBufferedAggregate(t *testing.T) {
	m := *NewModel()
	m.width = 100

	m.handleToolResultWithStatus(strings.Repeat("line\n", 20), "read", "/w/x.go", time.Now(), false, "")
	if len(m.pendingToolLines) == 0 {
		t.Fatal("precondition: collapsed read should be buffered")
	}

	m.SetShowToolCalls(false)
	m.flushPendingToolLines()
	if len(m.pendingToolLines) != 0 {
		t.Fatal("disabling tool rows should discard buffered aggregates")
	}
	if got := stripAnsi(m.output.state.content.String()); strings.Contains(got, "x.go") {
		t.Fatalf("buffered tool row leaked after disabling showToolCalls:\n%s", got)
	}
}

// Hiding tool rows is a NOISE preference, not an error-suppression one: a
// FAILED tool must stay visible. Without this, `/set toolcalls off` silently
// swallowed every tool failure — the transcript showed nothing, and the only
// remaining surface (the activity feed) is hidden behind Ctrl+O.
func TestShowToolCallsDisabledStillSurfacesFailures(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.SetShowToolCalls(false)

	next, _ := m.Update(ToolCallMsg{Name: "edit", Args: map[string]any{"file_path": "/w/broken.go"}})
	m = next.(Model)
	next, _ = m.Update(ToolResultMsg{
		Name:   "edit",
		Args:   map[string]any{"file_path": "/w/broken.go"},
		Failed: true,
		Error:  "old_string not found in file",
	})
	m = next.(Model)

	got := stripAnsi(m.output.state.content.String())
	if !strings.Contains(got, "broken.go") {
		t.Fatalf("failed tool must stay visible with rows hidden:\n%s", got)
	}
	if !strings.Contains(got, "not found") {
		t.Fatalf("failure reason must reach the transcript:\n%s", got)
	}
}
