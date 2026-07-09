package ui

import (
	"strings"
	"testing"
	"time"
)

// --- Round 9 UX fixes: plan-panel linger, userHidden latches, MCP badge. ---

func newTestPlanPanel() *PlanProgressPanel {
	p := NewPlanProgressPanel(DefaultStyles())
	p.StartPlan("plan-1", "Test plan", "desc", []PlanStepInfo{
		{ID: 1, Title: "step one", Description: "d1"},
		{ID: 2, Title: "step two", Description: "d2"},
	})
	return p
}

// TestPlanPanel_AutoHidesAfterLinger pins the round-9 fix for
// plan-panel-never-hides: before it, the ONLY visible=false write was dead
// code (Hide/EndPlanExecution/PlanCompleteMsg had zero callers/senders), so a
// finished plan's bordered box stayed pinned — elapsed ticking up — for the
// rest of the session.
func TestPlanPanel_AutoHidesAfterLinger(t *testing.T) {
	p := newTestPlanPanel()
	p.StartStep(1)
	p.CompleteStep(1, "", "")
	p.StartStep(2)
	p.CompleteStep(2, "", "")

	p.EndPlan()
	if p.View(100) == "" {
		t.Fatal("panel must keep rendering during the linger window")
	}

	// Simulate the linger window passing.
	p.finishedAt = time.Now().Add(-planPanelLingerAfterDone - time.Second)
	if got := p.View(100); got != "" {
		t.Fatalf("panel must self-hide after the linger window, got:\n%s", got)
	}
	if got := p.ViewCompact(); got != "" {
		t.Fatalf("compact view must self-hide too, got:\n%s", got)
	}
}

// TestPlanPanel_ElapsedFrozenAfterEnd: a finished plan must not keep counting
// up — the header clock freezes at the real duration.
func TestPlanPanel_ElapsedFrozenAfterEnd(t *testing.T) {
	p := newTestPlanPanel()
	// Plan ran 30s and JUST finished (linger window active): the header clock
	// must show the frozen 30s duration, not keep counting from startedAt.
	p.startedAt = time.Now().Add(-30 * time.Second)
	p.EndPlan()

	view := stripAnsi(p.View(100))
	if view == "" {
		t.Fatal("panel must render during the linger window")
	}
	if !strings.Contains(view, "30.0s") && !strings.Contains(view, "30.1s") {
		t.Fatalf("elapsed should be frozen at ~30s, view:\n%s",
			strings.SplitN(view, "\n", 3)[0])
	}
}

// TestPlanPanel_StartStepReopensAfterTerminalStamp: a plan that resumes
// (replan/retry after a premature terminal signal) must cancel the pending
// auto-hide.
func TestPlanPanel_StartStepReopensAfterTerminalStamp(t *testing.T) {
	p := newTestPlanPanel()
	p.EndPlan()
	if p.finishedAt.IsZero() {
		t.Fatal("EndPlan must stamp finishedAt")
	}
	p.StartStep(2)
	if !p.finishedAt.IsZero() {
		t.Fatal("a step starting must clear the terminal stamp — the plan is alive again")
	}
}

// TestPlanPanel_ToggleRePins: an explicit Ctrl+X (Toggle) clears the linger
// stamp — user intent overrides auto-hide, mirroring the agent tree idiom.
func TestPlanPanel_ToggleRePins(t *testing.T) {
	p := newTestPlanPanel()
	p.EndPlan()
	p.Toggle()
	if !p.finishedAt.IsZero() {
		t.Fatal("explicit Toggle must clear the linger stamp")
	}
}

// TestPlanPanel_EndPlanIdempotent: repeated terminal messages must not extend
// the linger window.
func TestPlanPanel_EndPlanIdempotent(t *testing.T) {
	p := newTestPlanPanel()
	p.EndPlan()
	first := p.finishedAt
	time.Sleep(2 * time.Millisecond)
	p.EndPlan()
	if !p.finishedAt.Equal(first) {
		t.Fatal("second EndPlan must not re-stamp finishedAt")
	}
}

// TestPlanProgressMsg_WholePlanCompletedStartsLinger wires the whole chain
// through Update: the final step's "completed" message (Completed ==
// TotalSteps) must stamp the panel's linger; an INTERMEDIATE completed step
// must not.
func TestPlanProgressMsg_WholePlanCompletedStartsLinger(t *testing.T) {
	m := *NewModel()
	m.width = 100
	m.planProgressPanel.StartPlan("p1", "Wired plan", "", []PlanStepInfo{
		{ID: 1, Title: "one"}, {ID: 2, Title: "two"},
	})

	updated, _ := m.Update(PlanProgressMsg{
		PlanID: "p1", CurrentStepID: 1, TotalSteps: 2, Completed: 1, Status: "completed",
	})
	m2 := updated.(Model)
	if !m2.planProgressPanel.finishedAt.IsZero() {
		t.Fatal("intermediate step completion must NOT start the linger")
	}

	updated, _ = m2.Update(PlanProgressMsg{
		PlanID: "p1", CurrentStepID: 2, TotalSteps: 2, Completed: 2, Status: "completed",
	})
	m3 := updated.(Model)
	if m3.planProgressPanel.finishedAt.IsZero() {
		t.Fatal("final step completion (Completed == TotalSteps) must start the linger")
	}
}

// --- userHidden latches ---

// TestActivityFeed_ExplicitHideSurvivesAutoShow pins the round-9 fix for
// auto-show-undoes-hide: hiding the feed (Ctrl+O) while agents run used to be
// undone by the very next AddEntry/StartSubAgent — the panel snapped back
// open seconds later, every time.
func TestActivityFeed_ExplicitHideSurvivesAutoShow(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())

	p.StartSubAgent("a1", "general", "task one")
	if !p.IsVisible() {
		t.Fatal("setup: StartSubAgent should auto-show the feed")
	}

	p.Toggle() // explicit hide
	if p.IsVisible() {
		t.Fatal("setup: Toggle should hide")
	}

	// Auto-show triggers must now stand down.
	p.StartSubAgent("a2", "general", "task two")
	if p.IsVisible() {
		t.Fatal("StartSubAgent overrode an explicit user hide")
	}
	p.AddEntry(ActivityFeedEntry{ID: "t1", Status: ActivityRunning})
	p.AddEntry(ActivityFeedEntry{ID: "t2", Status: ActivityRunning})
	if p.IsVisible() {
		t.Fatal("AddEntry auto-show overrode an explicit user hide")
	}

	// An explicit show re-arms auto-show management.
	p.Toggle()
	if !p.IsVisible() {
		t.Fatal("explicit Toggle should show again")
	}
	p.Toggle() // hide again
	p.Toggle() // show — userHidden must be cleared
	p.StartSubAgent("a3", "general", "task three")
	if !p.IsVisible() {
		t.Fatal("after an explicit show, auto-show should manage visibility again")
	}
}

// TestAgentTree_ExplicitHideSurvivesAutoShow: the same latch on the agent
// tree — UpdateTree's >=2-tasks auto-show must not override an explicit hide.
func TestAgentTree_ExplicitHideSurvivesAutoShow(t *testing.T) {
	p := NewAgentTreePanel(DefaultStyles())

	nodes := []AgentTreeNode{
		{ID: "t1", Status: "running"},
		{ID: "t2", Status: "running"},
	}
	p.UpdateTree(nodes)
	if !p.IsVisible() {
		t.Fatal("setup: >=2 tasks should auto-show the tree")
	}

	p.Toggle() // explicit hide
	p.UpdateTree(nodes)
	if p.IsVisible() {
		t.Fatal("UpdateTree auto-show overrode an explicit user hide")
	}

	p.Toggle() // explicit show re-arms
	p.Toggle() // hide
	p.Toggle() // show
	p.UpdateTree(nodes)
	if !p.IsVisible() {
		t.Fatal("after an explicit show, auto-show should manage visibility again")
	}
}

// --- MCP badge ---

// TestRuntimeStatus_FeedsMCPBadge pins the round-9 fix for
// dead-mcp-health-badge: Model.mcpHealthy/mcpTotal had ZERO writers since
// v0.69.3, so the "MCP N/M" status-bar segment could never render — even
// with every server down. The runtime snapshot now carries the counts.
func TestRuntimeStatus_FeedsMCPBadge(t *testing.T) {
	m := NewModel()
	m.handleRuntimeStatusMsg(RuntimeStatusMsg{Status: RuntimeStatusSnapshot{
		MCPHealthy: 1,
		MCPTotal:   2,
	}})
	if m.mcpHealthy != 1 || m.mcpTotal != 2 {
		t.Fatalf("snapshot not applied: healthy=%d total=%d", m.mcpHealthy, m.mcpTotal)
	}

	// The badge itself: renders only when something is unhealthy (calm UI),
	// which requires the fields to be fed at all.
	m.width = 130
	m.showTokens = false
	bar := stripAnsi(m.renderStatusBar())
	if !strings.Contains(bar, "MCP 1/2") {
		t.Fatalf("status bar should show the degraded MCP badge, got:\n%s", bar)
	}

	// All healthy → badge stays quiet.
	m.handleRuntimeStatusMsg(RuntimeStatusMsg{Status: RuntimeStatusSnapshot{
		MCPHealthy: 2,
		MCPTotal:   2,
	}})
	bar = stripAnsi(m.renderStatusBar())
	if strings.Contains(bar, "MCP") {
		t.Fatalf("healthy MCP must not render a badge, got:\n%s", bar)
	}
}
