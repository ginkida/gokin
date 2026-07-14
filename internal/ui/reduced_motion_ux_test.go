package ui

import (
	"strings"
	"testing"
	"time"
)

func TestReducedMotionPropagatesAndKeepsHousekeepingState(t *testing.T) {
	m := NewModel()
	m.SetReducedMotion(true)

	if !m.reducedMotion || !m.output.state.reducedMotion || !m.progressModel.reducedMotion ||
		!m.toolProgressBar.reducedMotion || !m.planProgressPanel.reducedMotion ||
		!m.activityFeed.reducedMotion || !m.agentTreePanel.reducedMotion {
		t.Fatal("reduced-motion setting did not reach every live surface")
	}
	m.planProgressPanel.Tick()
	m.activityFeed.Tick()
	m.agentTreePanel.Tick()
	_ = m.toolProgressBar.Tick()
	if m.planProgressPanel.frame != 0 || m.activityFeed.frame != 0 ||
		m.agentTreePanel.frame != 0 || m.toolProgressBar.frame != 0 {
		t.Fatal("a reduced-motion surface advanced its animation frame")
	}

	m.toolProgressBar.Show("download")
	m.toolProgressBar.lastUpdateTime = time.Now().Add(-progressBarStaleTimeout - time.Second)
	_ = m.toolProgressBar.Tick()
	if m.toolProgressBar.IsVisible() {
		t.Fatal("reduced motion disabled stale-progress housekeeping")
	}
}

func TestReducedMotionUsesStableSemanticIndicators(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateProcessing
	m.processingLabel = "Working"
	m.SetReducedMotion(true)

	live := stripAnsi(m.renderLiveActivityCard(false))
	if !strings.HasPrefix(live, "● ") || containsAnimatedSpinner(live) {
		t.Fatalf("live card retained animation: %q", live)
	}

	m.toolProgressBar.Show("download")
	tool := stripAnsi(m.toolProgressBar.View(80))
	if !strings.HasPrefix(tool, "● ") || containsAnimatedSpinner(tool) {
		t.Fatalf("tool progress retained animation: %q", tool)
	}

	m.progressModel.Start("Batch", 3)
	batch := stripAnsi(m.progressModel.View())
	if !strings.HasPrefix(batch, "● ") || containsAnimatedSpinner(batch) {
		t.Fatalf("batch progress retained animation: %q", batch)
	}

	m.planProgressPanel.StartPlan("p", "Plan", "", []PlanStepInfo{{ID: 1, Title: "Step"}})
	m.planProgressPanel.StartStep(1)
	plan := stripAnsi(m.planProgressPanel.View(80, 24))
	if !strings.Contains(plan, "● Step") || containsAnimatedSpinner(plan) {
		t.Fatalf("plan progress retained animation:\n%s", plan)
	}

	m.activityFeed.ShowExplicit()
	m.activityFeed.AddEntry(ActivityFeedEntry{ID: "a", Name: "read", Status: ActivityRunning})
	feed := stripAnsi(m.activityFeed.View(80))
	if !strings.Contains(feed, "● read") || containsAnimatedSpinner(feed) {
		t.Fatalf("activity feed retained animation:\n%s", feed)
	}

	m.agentTreePanel.UpdateTree([]AgentTreeNode{{ID: "1", AgentType: "worker", Status: "running"}, {ID: "2", AgentType: "reviewer", Status: "pending"}})
	tree := stripAnsi(m.agentTreePanel.View(80))
	if !strings.Contains(tree, "● [worker]") || containsAnimatedSpinner(tree) {
		t.Fatalf("agent tree retained animation:\n%s", tree)
	}

	m.todoItems = []string{"- [/] Review accessibility", "- [ ] Run tests", "- [x] Audit navigation"}
	tasks := stripAnsi(m.renderTodos())
	if !strings.Contains(tasks, "● Review accessibility") || containsAnimatedSpinner(tasks) {
		t.Fatalf("tasks panel retained animation:\n%s", tasks)
	}
}

func TestTasksPanelRestoresAnimationWhenReducedMotionIsDisabled(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.todoItems = []string{"- [/] Active task"}
	m.SetReducedMotion(false)

	tasks := stripAnsi(m.renderTodos())
	if !containsAnimatedSpinner(tasks) || strings.Contains(tasks, "● Active task") {
		t.Fatalf("normal-motion tasks lost their activity animation:\n%s", tasks)
	}
}

func TestReducedMotionConfigUpdateAppliesLive(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.todoItems = []string{"- [/] Live task"}
	if before := stripAnsi(m.renderTodos()); !containsAnimatedSpinner(before) {
		t.Fatalf("setup: normal-motion task is not animated:\n%s", before)
	}
	_ = m.handleMessageTypes(ConfigUpdateMsg{ReducedMotion: true})
	if !m.reducedMotion || !m.output.state.reducedMotion {
		t.Fatal("ConfigUpdateMsg did not apply reduced motion live")
	}
	if after := stripAnsi(m.renderTodos()); !strings.Contains(after, "● Live task") || containsAnimatedSpinner(after) {
		t.Fatalf("live config update did not settle task animation:\n%s", after)
	}
}

func TestReducedMotionCancelsInFlightSmoothScroll(t *testing.T) {
	m := NewOutputModel(DefaultStyles())
	m.state.scrolling = true
	m.state.scrollTarget = 20
	m.SetReducedMotion(true)
	if m.state.scrolling || !m.state.reducedMotion {
		t.Fatalf("smooth scroll remained active: scrolling=%v reduced=%v", m.state.scrolling, m.state.reducedMotion)
	}
}

func containsAnimatedSpinner(s string) bool {
	for _, glyph := range spinnerFramesCard {
		if strings.Contains(s, glyph) {
			return true
		}
	}
	return false
}
