package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// --- Claude-Code-style live-activity detail toggle (Ctrl+O). ---
// Default = minimal: ONE dim line during streaming, feed suppressed. Ctrl+O —
// including DURING Processing/Streaming (the whole point) — expands to the
// full card + feed and back.

func newStreamingModelWithTodo() Model {
	m := *NewModel()
	m.width = 120
	m.height = 40
	m.state = StateStreaming
	m.currentResponseBuf.WriteString("working on the thing")
	m.todoItems = []string{
		"[x] Done step",
		"[/] Current step",
		"[ ] Next step",
	}
	return m
}

// TestLiveActivity_MinimalByDefault: with the default (collapsed) verbosity the
// card is exactly ONE line — no todo row, no next row — the unobtrusive shape.
func TestLiveActivity_MinimalByDefault(t *testing.T) {
	m := newStreamingModelWithTodo()
	if m.liveDetailExpanded {
		t.Fatal("liveDetailExpanded must default to false")
	}

	view := stripAnsi(m.renderLiveActivityCard(false))
	if view == "" {
		t.Fatal("minimal card must still render the one-line indicator")
	}
	if strings.Contains(view, "\n") {
		t.Fatalf("minimal card must be ONE line, got:\n%s", view)
	}
	if strings.Contains(view, "Current step") {
		t.Fatalf("minimal card must not show the todo row, got:\n%s", view)
	}
}

// TestLiveActivity_CtrlOTogglesDuringStreaming: the toggle must work while the
// agent streams (pre-fix Ctrl+O was gated to StateInput — the user literally
// could not open the detail during work), through the FULL Update path, and the
// keystroke must be consumed (never also reach the compose textarea).
func TestLiveActivity_CtrlOTogglesDuringStreaming(t *testing.T) {
	m := newStreamingModelWithTodo()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m2 := updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+O must be consumed (non-nil cmd) so it can't leak into the textarea")
	}
	if !m2.liveDetailExpanded {
		t.Fatal("Ctrl+O during streaming must expand the live detail")
	}

	// Expanded: the todo row is visible now.
	view := stripAnsi(m2.renderLiveActivityCard(false))
	if !strings.Contains(view, "Current step") {
		t.Fatalf("expanded card must show the todo row, got:\n%s", view)
	}

	// Toggle back → minimal again.
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m3 := updated.(Model)
	if m3.liveDetailExpanded {
		t.Fatal("second Ctrl+O must collapse back to minimal")
	}
	view = stripAnsi(m3.renderLiveActivityCard(false))
	if strings.Contains(view, "\n") {
		t.Fatalf("collapsed card must be ONE line again, got:\n%s", view)
	}
}

// TestLiveActivity_FeedGatedOnDetail: the activity feed panel renders only in
// detailed mode — in minimal mode the whole live surface is the one-line card.
func TestLiveActivity_FeedGatedOnDetail(t *testing.T) {
	m := *NewModel()
	m.width = 110
	m.height = 40
	m.state = StateProcessing
	m.processingLabel = "Agent: explore"
	m.activityFeed.AddEntry(feedEntry("tool-1", ActivityRunning))
	m.activityFeed.visible = true

	view := renderToPlain(m.View())
	if strings.Contains(view, "Live Activity") {
		t.Fatalf("feed panel must be suppressed in minimal mode:\n%.600s", view)
	}

	m.liveDetailExpanded = true
	view = renderToPlain(m.View())
	if !strings.Contains(view, "Live Activity") {
		t.Fatalf("feed panel must render in detailed mode:\n%.600s", view)
	}
}

// TestLiveActivity_StatusBarHintFollowsState: during work the status bar offers
// the toggle hint, and the label flips with the state (detail ⇄ minimal).
func TestLiveActivity_StatusBarHintFollowsState(t *testing.T) {
	m := *NewModel()
	m.state = StateStreaming

	hints := m.contextualShortcutHintPairs()
	if len(hints) != 1 || hints[0].key != "ctrl+o" || hints[0].desc != "detail" {
		t.Fatalf("minimal mode should hint 'ctrl+o detail', got %v", hints)
	}

	m.liveDetailExpanded = true
	hints = m.contextualShortcutHintPairs()
	if len(hints) != 1 || hints[0].desc != "minimal" {
		t.Fatalf("detailed mode should hint 'ctrl+o minimal', got %v", hints)
	}
}

// TestLiveActivity_CtrlOStillWorksIdle: the toggle keeps working in StateInput
// (the pre-existing binding surface).
func TestLiveActivity_CtrlOStillWorksIdle(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.state = StateInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m2 := updated.(Model)
	if !m2.liveDetailExpanded {
		t.Fatal("Ctrl+O in StateInput must toggle live detail")
	}
}
