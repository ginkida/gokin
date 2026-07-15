package ui

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func treeNodes(statuses ...string) []AgentTreeNode {
	nodes := make([]AgentTreeNode, 0, len(statuses))
	for i, s := range statuses {
		nodes = append(nodes, AgentTreeNode{ID: string(rune('a' + i)), Description: "task", Status: s})
	}
	return nodes
}

func TestAgentTreeSummaryKeepsBlockedSkippedAndUnknownDistinct(t *testing.T) {
	p := NewAgentTreePanel(nil)
	p.UpdateTree(treeNodes("pending", "ready", "blocked", "skipped", "mystery"))

	view := renderToPlain(p.View(100))
	for _, want := range []string{"2 pending", "1 blocked", "1 skipped", "1 unknown", "?"} {
		if !strings.Contains(view, want) {
			t.Fatalf("agent summary collapsed distinct status %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "5 pending") {
		t.Fatalf("agent summary presented non-pending work as pending:\n%s", view)
	}
}

func TestAgentTreeCopiesAndSanitizesSnapshot(t *testing.T) {
	nodes := []AgentTreeNode{{
		ID:           "agent-1",
		AgentType:    "review\x1b]0;hijack\aagent",
		Description:  "inspect\nforged row",
		Status:       " RUNNING ",
		Dependencies: []string{"parent"},
	}}
	p := NewAgentTreePanel(nil)
	p.UpdateTree(nodes)
	p.Toggle()
	nodes[0].Description = "mutated"
	nodes[0].Dependencies[0] = "mutated-parent"

	p.mu.RLock()
	stored := p.nodes[0]
	p.mu.RUnlock()
	if stored.Description != "inspect forged row" || stored.Status != "running" || stored.Dependencies[0] != "parent" {
		t.Fatalf("agent snapshot was aliased or not normalized: %+v", stored)
	}
	view := p.View(80)
	plain := renderToPlain(view)
	if strings.Contains(view, "\x1b]") || strings.Contains(plain, "hijack") || strings.Contains(plain, "mutated") || strings.Contains(plain, "forged\nrow") {
		t.Fatalf("agent snapshot rendered unsafe/mutated data:\n%s", plain)
	}
}

func TestAgentTreeIndeterminateProgressFitsEveryNarrowWidth(t *testing.T) {
	p := NewAgentTreePanel(nil)
	p.UpdateTree([]AgentTreeNode{{
		AgentType:   strings.Repeat("very-long-agent-type", 4),
		Description: strings.Repeat("description ", 8),
		Status:      "running",
		Progress:    math.NaN(),
		ToolsUsed:   1,
		CurrentTool: strings.Repeat("tool", 20),
	}})
	p.Toggle()

	for width := 1; width <= 32; width++ {
		view := p.View(width)
		for row, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width=%d row=%d overflow=%d: %q", width, row, got, renderToPlain(line))
			}
		}
	}
}

func TestAgentTreeCompactHeightKeepsActiveTaskAndHonestFold(t *testing.T) {
	p := NewAgentTreePanel(nil)
	nodes := []AgentTreeNode{
		{AgentType: "done", Description: "old one", Status: "completed"},
		{AgentType: "done", Description: "old two", Status: "completed"},
		{AgentType: "waiting", Description: "queued one", Status: "pending"},
		{AgentType: "waiting", Description: "queued two", Status: "pending"},
		{AgentType: "waiting", Description: "queued three", Status: "blocked"},
		{AgentType: "waiting", Description: "queued four", Status: "ready"},
		{AgentType: "unknown", Description: "future state", Status: "future"},
		{AgentType: "critical", Description: "current work", Status: "running", CurrentTool: "write_file"},
	}
	p.UpdateTree(nodes)

	view := p.View(70, 8)
	plain := renderToPlain(view)
	if got, limit := lipgloss.Height(view), agentTreePanelHeightBudget(8); got > limit {
		t.Fatalf("compact tree height=%d, want <=%d:\n%s", got, limit, plain)
	}
	for _, want := range []string{"[critical]", "→ write_file", "Ctrl+A hide", "7 row(s) folded"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("compact tree lost priority signal %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "[done]") || strings.Contains(plain, "[waiting]") {
		t.Fatalf("compact tree rendered lower-priority work ahead of the active task:\n%s", plain)
	}
}

func TestAgentTreeIrreducibleHeightKeepsRecoveryBeforeThought(t *testing.T) {
	p := NewAgentTreePanel(nil)
	p.UpdateTree([]AgentTreeNode{{
		AgentType:   "worker",
		Description: "repairing state",
		Status:      "running",
		Thought:     "speculative detail",
		Reflection:  "retry with safe fallback",
	}})
	p.Toggle()

	view := p.View(64, 7)
	plain := renderToPlain(view)
	if got, limit := lipgloss.Height(view), agentTreePanelHeightBudget(7); got > limit {
		t.Fatalf("irreducible tree height=%d, want <=%d:\n%s", got, limit, plain)
	}
	for _, want := range []string{"[worker]", "RECOVERY", "retry with safe fallback", "Ctrl+A hide"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("irreducible tree lost recovery signal %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "speculative detail") {
		t.Fatalf("thought prose displaced higher-priority recovery guidance:\n%s", plain)
	}
}

func TestAgentTreeFitsEveryTinyHeight(t *testing.T) {
	p := NewAgentTreePanel(nil)
	p.UpdateTree(treeNodes("running", "pending"))

	for height := 1; height <= 8; height++ {
		view := p.View(30, height)
		if got, limit := lipgloss.Height(view), agentTreePanelHeightBudget(height); got > limit {
			t.Fatalf("height=%d rendered %d rows, want <=%d:\n%s", height, got, limit, renderToPlain(view))
		}
	}
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
	m.liveDetailExpanded = true // the feed renders only in detailed mode (Ctrl+O)

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
