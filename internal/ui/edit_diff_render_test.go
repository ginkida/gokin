package ui

import (
	"strings"
	"testing"
)

// An edit result carrying a display-diff renders the Claude-Code shape:
// "Added N lines, removed M lines" + colored ±hunks — and the model-facing
// "Updated region" snippet body is NOT shown to the user.
func TestToolResultMsg_EditDiffRendersClaudeCodeStyle(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.state = StateProcessing

	m.handleMessageTypes(ToolCallMsg{Name: "edit", Args: map[string]any{"file_path": "/w/f.go"}})
	m.handleMessageTypes(ToolResultMsg{
		Name:        "edit",
		Args:        map[string]any{"file_path": "/w/f.go"},
		Content:     "Replaced 1 occurrence in /w/f.go\n\nUpdated region (already written to disk — no verification read needed):\n  10│ new line",
		Diff:        "-   10  old line\n+   10  new line",
		DiffAdded:   1,
		DiffRemoved: 1,
	})

	rendered := stripAnsi(m.output.state.content.String())
	if !strings.Contains(rendered, "Added 1 line, removed 1 line") {
		t.Fatalf("diff header missing:\n%s", rendered)
	}
	if !strings.Contains(rendered, "+   10  new line") || !strings.Contains(rendered, "-   10  old line") {
		t.Fatalf("±hunk lines missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "Updated region") {
		t.Fatalf("model-facing region snippet must not render when a diff is shown:\n%s", rendered)
	}
	if m.pendingEditDiff != nil {
		t.Fatal("stash must be consumed")
	}
}

// Without a diff payload the edit body renders as before (regression guard).
func TestToolResultMsg_EditWithoutDiffKeepsLegacyBody(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.state = StateProcessing

	m.handleMessageTypes(ToolCallMsg{Name: "edit", Args: map[string]any{"file_path": "/w/f.go"}})
	m.handleMessageTypes(ToolResultMsg{
		Name:    "edit",
		Args:    map[string]any{"file_path": "/w/f.go"},
		Content: "Replaced 1 occurrence in /w/f.go\n\nUpdated region (already written to disk — no verification read needed):\n  10│ new line",
	})

	rendered := stripAnsi(m.output.state.content.String())
	if !strings.Contains(rendered, "Updated region:") {
		t.Fatalf("legacy body (cleaned header) must render without a diff payload:\n%s", rendered)
	}
}
