package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestActivityFeedUpsertReevaluatesVisibilityAndTerminalEventsAreIdempotent(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.AddEntry(ActivityFeedEntry{ID: "reused", Type: ActivityTypeTool, Name: "read", Status: ActivityCompleted})
	p.AddEntry(ActivityFeedEntry{ID: "other", Type: ActivityTypeTool, Name: "write", Status: ActivityRunning})
	if p.IsVisible() {
		t.Fatal("one running operation should not auto-open the feed")
	}
	p.AddEntry(ActivityFeedEntry{ID: "reused", Type: ActivityTypeTool, Name: "read", Status: ActivityRunning})
	if !p.IsVisible() {
		t.Fatal("upsert to a second running operation did not auto-open the feed")
	}

	p.CompleteEntry("reused", true, "done")
	logCount := len(p.recentLog)
	description := p.entries[p.activeEntries["reused"]].Description
	p.CompleteEntry("reused", true, "done")
	if len(p.recentLog) != logCount || p.entries[p.activeEntries["reused"]].Description != description {
		t.Fatalf("duplicate tool completion rewrote visible history: logs=%v entry=%+v", p.recentLog, p.entries[p.activeEntries["reused"]])
	}

	agents := NewActivityFeedPanel(DefaultStyles())
	agents.StartSubAgent("agent-1", "review", "first description")
	started := agents.GetSubAgentState("agent-1").StartTime
	agents.StartSubAgent("agent-1", "review", "updated description")
	if len(agents.entries) != 1 || !agents.GetSubAgentState("agent-1").StartTime.Equal(started) {
		t.Fatalf("duplicate agent start created a second lifecycle: entries=%d state=%+v", len(agents.entries), agents.GetSubAgentState("agent-1"))
	}
	if got := agents.entries[0].Description; got != "updated description" {
		t.Fatalf("agent replay did not refresh descriptive state: %q", got)
	}
	agents.CompleteSubAgent("agent-1", true, "reviewed")
	logCount = len(agents.recentLog)
	agents.CompleteSubAgent("agent-1", true, "reviewed")
	if len(agents.recentLog) != logCount {
		t.Fatalf("duplicate agent completion appended history twice: %v", agents.recentLog)
	}
}

func TestContextObservatoryDegenerateHeightUsesOneTruthfulRecoveryRow(t *testing.T) {
	critical := NewContextObservatoryPanel(DefaultStyles())
	critical.Show()
	critical.UpdateHealth(ContextHealthMsg{TotalTokens: 95, MaxTokens: 100, PruningAlert: "Pruning required"})
	for height := 1; height < 7; height++ {
		view := critical.View(100, height)
		plain := stripAnsi(view)
		if got := lipgloss.Height(view); got > height {
			t.Fatalf("height=%d rendered %d rows:\n%s", height, got, plain)
		}
		for _, want := range []string{"Esc/Ctrl+H close", "Pruning required"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d tiny observatory lost %q: %q", height, want, plain)
			}
		}
	}

	empty := NewContextObservatoryPanel(DefaultStyles())
	empty.Show()
	if view := stripAnsi(empty.View(80, 1)); !strings.Contains(view, "no data yet") || !strings.Contains(view, "close") {
		t.Fatalf("tiny empty state is not truthful/actionable: %q", view)
	}
}

func TestAgentTreeIrreducibleHeightDisclosesFoldedTasksInTitle(t *testing.T) {
	p := NewAgentTreePanel(DefaultStyles())
	nodes := make([]AgentTreeNode, 8)
	for i := range nodes {
		nodes[i] = AgentTreeNode{ID: string(rune('a' + i)), AgentType: "worker", Description: "task", Status: "running"}
	}
	p.UpdateTree(nodes)

	view := p.View(70, 4)
	plain := stripAnsi(view)
	for _, want := range []string{"Ctrl+A hide", "7 folded", "[worker]"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("irreducible tree silently hid work; missing %q:\n%s", want, plain)
		}
	}
	if got := lipgloss.Height(view); got > 4 {
		t.Fatalf("irreducible tree rendered %d rows:\n%s", got, plain)
	}
	if tiny := stripAnsi(p.View(70, 1)); !strings.Contains(tiny, "8 tasks") || !strings.Contains(tiny, "Ctrl+A hide") {
		t.Fatalf("one-row tree hid total/recovery: %q", tiny)
	}
}

func TestLiveActivityCardFitsDegenerateWidthsAndIgnoresNonTodoRows(t *testing.T) {
	for width := 1; width <= 16; width++ {
		m := NewModel()
		m.width = width
		m.state = StateProcessing
		m.currentActivity = "Reviewing A👩‍💻BC"
		view := m.renderLiveActivityCard(false)
		for row, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width=%d row=%d overflow=%d: %q", width, row, got, stripAnsi(line))
			}
		}
	}

	m := NewModel()
	m.todoItems = []string{"", "not a checklist row", "[x] Done", "[/] Active", "[ ] Pending", "[ ]"}
	if got := m.liveActivityTodoLine(); !strings.Contains(got, "Step 2/3: Active") {
		t.Fatalf("non-todo/empty rows inflated plan position: %q", got)
	}
}

func TestPlanProgressTinyViewAndNotificationRemainTruthful(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Migration", "", []PlanStepInfo{{ID: 10, Title: "Prepare"}, {ID: 42, Title: "Apply"}})
	panel.StartStep(42)
	for height := 1; height < 8; height++ {
		view := panel.View(120, height)
		plain := stripAnsi(view)
		if got := lipgloss.Height(view); got > height {
			t.Fatalf("height=%d plan rendered %d rows: %q", height, got, plain)
		}
		for _, want := range []string{"resize for controls", "Step 42: Apply"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d tiny plan lost %q: %q", height, want, plain)
			}
		}
	}
	panel.PauseStep(42, "Review credentials")
	if view := stripAnsi(panel.View(120, 2)); !strings.Contains(view, "Plan paused") || !strings.Contains(view, "/resume-plan") {
		t.Fatalf("tiny paused plan lost recovery command: %q", view)
	}

	panel.steps[1].Title = "Apply\x1b]0;hijack\a\nforged"
	notice := panel.RenderStepNotification(42, PlanStepCompleted)
	plainNotice := stripAnsi(notice)
	if !strings.Contains(plainNotice, "[2/2]") || strings.Contains(plainNotice, "[42/2]") {
		t.Fatalf("notification used step ID as ordinal: %q", plainNotice)
	}
	if strings.Contains(notice, "\x1b]") || strings.Contains(plainNotice, "hijack") || strings.Contains(plainNotice, "forged\n") {
		t.Fatalf("notification rendered unsafe structural metadata: %q", plainNotice)
	}

	empty := NewPlanProgressPanel(DefaultStyles())
	empty.StartPlan("empty", "No-op", "", nil)
	if view := stripAnsi(empty.View(80, 1)); !strings.Contains(view, "no executable steps") || strings.Contains(view, "Plan complete") {
		t.Fatalf("tiny empty plan claimed ordinary completion: %q", view)
	}
}

func TestPlanAndTaskTimelineSanitizeRuntimeRowsAndPreserveUnicodeBounds(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Audit\x1b]0;title\a\nforged", "", []PlanStepInfo{{
		ID:          1,
		Title:       "Check A👍🏽BC",
		Description: "first\nsecond\x1b[2J",
	}})
	panel.StartStep(1)
	panel.SetCurrentTool("read\nforged", "A👩‍💻BC\x1b]0;tool\a")
	panel.PauseStep(1, "needs\nreview\x1b[2J")
	for width := 4; width <= 40; width++ {
		view := panel.View(width, 8)
		if strings.Contains(view, "\x1b]0;") || strings.Contains(view, "\x1b[2J") {
			t.Fatalf("width=%d terminal control reached plan panel: %q", width, view)
		}
		for row, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width=%d row=%d overflow=%d: %q", width, row, got, stripAnsi(line))
			}
		}
	}

	m := NewModel()
	start := m.renderTaskTimelineStart(1, "review\x1b]0;kind\a", "first\nforged\x1b[2J")
	progress := m.renderTaskTimelineProgress(.5, "A👩‍💻BC\nforged\x1b]0;msg\a")
	done := m.renderTaskTimelineDone(false, time.Second, errors.New("bad\nforged\x1b[2J"))
	for name, rendered := range map[string]string{"start": start, "progress": progress, "done": done} {
		if strings.Contains(rendered, "\x1b]0;") || strings.Contains(rendered, "\x1b[2J") {
			t.Fatalf("%s timeline row retained terminal control: %q", name, rendered)
		}
	}
	if strings.Count(stripAnsi(start), "\n") != 1 || strings.Contains(stripAnsi(progress), "\n") || strings.Contains(stripAnsi(done), "\n") {
		t.Fatalf("runtime text injected timeline structure: start=%q progress=%q done=%q", stripAnsi(start), stripAnsi(progress), stripAnsi(done))
	}
}
