package ui

import (
	"strings"
	"testing"
	"time"
)

func treeNodes(statuses ...string) []AgentTreeNode {
	nodes := make([]AgentTreeNode, 0, len(statuses))
	for i, s := range statuses {
		nodes = append(nodes, AgentTreeNode{ID: string(rune('a' + i)), Description: "task", Status: s})
	}
	return nodes
}

func TestAgentTree_AutoHidesAfterLinger(t *testing.T) {
	p := NewAgentTreePanel(nil)
	p.UpdateTree(treeNodes("running", "pending"))
	if !p.IsVisible() {
		t.Fatal("≥2 tasks must auto-show the panel")
	}
	if p.View(100) == "" {
		t.Fatal("live tree must render")
	}

	p.UpdateTree(treeNodes("completed", "failed"))
	if p.View(100) == "" {
		t.Fatal("freshly finished tree must linger so the user can read the final state")
	}

	// Simulate the linger window passing.
	p.mu.Lock()
	p.allDoneAt = time.Now().Add(-agentTreeLingerAfterDone - time.Second)
	p.mu.Unlock()
	if v := p.View(100); v != "" {
		t.Fatalf("finished tree must auto-hide after linger, got:\n%s", v)
	}

	// New live work clears the stamp and brings the panel back.
	p.UpdateTree(treeNodes("running", "pending"))
	if p.View(100) == "" {
		t.Fatal("new live tasks must clear the done-stamp and render again")
	}
}

func TestAgentTree_ManualToggleOverridesLinger(t *testing.T) {
	p := NewAgentTreePanel(nil)
	p.UpdateTree(treeNodes("completed", "completed"))
	p.mu.Lock()
	p.allDoneAt = time.Now().Add(-time.Minute)
	p.mu.Unlock()

	p.Toggle() // off
	p.Toggle() // explicitly back on — user wants to see the finished tree
	if p.View(100) == "" {
		t.Fatal("explicit toggle-on must override the auto-hide linger")
	}
	if !strings.Contains(p.View(100), "task") {
		t.Fatal("toggled-on view must render the tree content")
	}
}

func TestAgentTree_CompactModeFoldsTerminalAndQueued(t *testing.T) {
	p := NewAgentTreePanel(nil)
	statuses := []string{
		"completed", "completed", "completed", "completed", "completed",
		"running", "running",
		"pending", "pending", "pending", "pending", "pending", "pending", "pending", "pending", "pending",
	}
	p.UpdateTree(treeNodes(statuses...))

	view := renderToPlain(p.View(120))
	if got := strings.Count(view, "✓"); got != 0 {
		t.Fatalf("compact mode must fold completed rows, found %d:\n%s", got, view)
	}
	if !strings.Contains(view, "row(s) folded") {
		t.Fatalf("compact mode must disclose folded rows:\n%s", view)
	}
	// All running rows render; queued rows are budget-capped.
	lines := strings.Count(view, "\n") + 1
	if lines > 14 {
		t.Fatalf("compact tree is %d lines, want ≤14:\n%s", lines, view)
	}
}

func TestAgentTree_SmallTreeRendersFully(t *testing.T) {
	p := NewAgentTreePanel(nil)
	p.UpdateTree(treeNodes("completed", "running", "pending"))

	view := renderToPlain(p.View(120))
	if !strings.Contains(view, "✓") {
		t.Fatalf("small tree must render completed rows:\n%s", view)
	}
	if strings.Contains(view, "folded") {
		t.Fatalf("small tree must not fold:\n%s", view)
	}
}

// TestView_FeedSuppressedWhileTreeRenders pins the one-activity-surface rule:
// the feed and the tree both auto-show on sub-agent activity and showed every
// background agent twice when stacked.
func TestView_FeedSuppressedWhileTreeRenders(t *testing.T) {
	m := NewModel()
	m.width = 110
	m.height = 40
	m.state = StateInput

	// Tree with live tasks → renders.
	m.agentTreePanel.UpdateTree(treeNodes("running", "pending"))
	// Feed visible with an active entry → would render alone.
	m.activityFeed.AddEntry(feedEntry("tool-1", ActivityRunning))
	m.activityFeed.visible = true

	view := renderToPlain(m.View())
	if !strings.Contains(view, "Agent Orchestrator") {
		t.Fatalf("agent tree must render:\n%.600s", view)
	}
	if strings.Contains(view, "Live Activity") {
		t.Fatalf("activity feed must be suppressed while the tree renders:\n%.600s", view)
	}

	// Tree gone → feed comes back.
	m.agentTreePanel.UpdateTree(nil)
	view = renderToPlain(m.View())
	if !strings.Contains(view, "Live Activity") {
		t.Fatalf("feed must render once the tree is gone:\n%.600s", view)
	}
}
