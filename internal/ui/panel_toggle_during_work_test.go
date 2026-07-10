package ui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// --- During-work panel toggles (the Ctrl+O bug class, siblings). ---
// The agent tree (Ctrl+A) and todos (Ctrl+T) are surfaces about work IN
// PROGRESS, but their toggles were gated to StateInput only — the user
// literally could not open them while agents actually ran. Both now follow
// the Ctrl+O/Ctrl+X pattern (StateInput || Processing || Streaming), stay
// ctrl-keys (can't fight type-ahead), and return keyConsumed (round-8 rule).

func runningTreeNode(id string) AgentTreeNode {
	return AgentTreeNode{
		ID:          id,
		AgentType:   "explore",
		Description: "scan the repo",
		Status:      "running",
		StartTime:   time.Now(),
	}
}

// TestCtrlATogglesAgentTreeDuringStreaming pins the fix: Ctrl+A must open the
// agent tree in the FULL View output while the agent streams — the exact
// moment the tree is most useful — and a second press must close it.
func TestCtrlATogglesAgentTreeDuringStreaming(t *testing.T) {
	m := *NewModel()
	m.width = 110
	m.height = 40
	m.state = StateStreaming
	m.currentResponseBuf.WriteString("streaming answer")
	// One running node: below the >=2 auto-show threshold, so ONLY the
	// explicit toggle can reveal it — isolating the keybinding under test.
	m.agentTreePanel.UpdateTree([]AgentTreeNode{runningTreeNode("a1")})
	if m.agentTreePanel.IsVisible() {
		t.Fatal("precondition: tree must not auto-show for a single node")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m2 := updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+A must be consumed (non-nil cmd) so it can't leak into the textarea")
	}
	if !m2.agentTreePanel.IsVisible() {
		t.Fatal("Ctrl+A during streaming must show the agent tree")
	}
	view := renderToPlain(m2.View())
	if !strings.Contains(view, "scan the repo") {
		t.Fatalf("agent tree must render in the streaming view after Ctrl+A, got:\n%.800s", view)
	}

	// Second press closes it again.
	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m3 := updated.(Model)
	if m3.agentTreePanel.IsVisible() {
		t.Fatal("second Ctrl+A must hide the agent tree")
	}
}

// TestCtrlTTogglesTodosDuringProcessing pins the sibling fix for the todo
// panel: the status bar advertises "Ctrl+T tasks N" during work, so the key
// must actually work there.
func TestCtrlTTogglesTodosDuringProcessing(t *testing.T) {
	m := *NewModel()
	m.width = 110
	m.height = 40
	m.state = StateProcessing
	m.processingLabel = "Thinking"
	m.todoItems = []string{"[/] Implement the feature", "[ ] Write tests"}
	if m.todosVisible {
		t.Fatal("precondition: todos hidden by default")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m2 := updated.(Model)
	if cmd == nil {
		t.Fatal("Ctrl+T must be consumed (non-nil cmd) so it can't leak into the textarea")
	}
	if !m2.todosVisible {
		t.Fatal("Ctrl+T during processing must show the todos")
	}
	view := renderToPlain(m2.View())
	if !strings.Contains(view, "Implement the feature") {
		t.Fatalf("todos must render in the processing view after Ctrl+T, got:\n%.800s", view)
	}

	updated, _ = m2.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	m3 := updated.(Model)
	if m3.todosVisible {
		t.Fatal("second Ctrl+T must hide the todos")
	}
}

// TestCtrlAStillWorksInInput guards the pre-existing surface: the extension
// to work states must not break the idle path.
func TestCtrlAStillWorksInInput(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.state = StateInput

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	m2 := updated.(Model)
	if !m2.agentTreePanel.IsVisible() {
		t.Fatal("Ctrl+A in StateInput must still toggle the agent tree")
	}
}
