package ui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func assertPanelFitsWidth(t *testing.T, view string, width int) {
	t.Helper()
	for row, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("row %d width=%d, want <=%d:\n%s", row, got, width, stripAnsi(view))
		}
	}
}

func TestAgentTreeExtremeContentFitsAndKeepsPrimarySignal(t *testing.T) {
	panel := NewAgentTreePanel(DefaultStyles())
	panel.UpdateTree([]AgentTreeNode{
		{
			ID:          "one",
			AgentType:   "",
			Description: "Investigating\n" + strings.Repeat("请求失败 and more context ", 12),
			Thought:     "first thought\nsecond thought " + strings.Repeat("🙂", 20),
			Reflection:  "retrying\nwith a safer approach " + strings.Repeat("detail ", 20),
			Status:      "running",
			Depth:       100,
			Progress:    2.5,
			ToolsUsed:   3,
			CurrentTool: "grep\nTODO",
		},
		{ID: "two", AgentType: "review", Description: "Review", Status: "pending"},
	})

	for _, width := range []int{1, 4, 12, 30, 60} {
		view := panel.View(width)
		assertPanelFitsWidth(t, view, width)
		if strings.Contains(stripAnsi(view), "2562047h") {
			t.Fatalf("width=%d zero StartTime rendered absurd duration:\n%s", width, stripAnsi(view))
		}
	}
	wide := stripAnsi(panel.View(60))
	for _, want := range []string{"[agent]", "→ grep TODO", "RECOVERY"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("agent tree lost primary signal %q:\n%s", want, wide)
		}
	}
	if strings.Count(wide, "first thought") != 1 || strings.Contains(wide, "first thought\nsecond thought") {
		t.Fatalf("agent thought embedded a structural newline:\n%s", wide)
	}
	if !strings.Contains(wide, "━━━━━━━━") {
		t.Fatalf("progress >100%% was not clamped to a complete bar:\n%s", wide)
	}
}

func TestAgentTreeSummaryIgnoresMissingAndFutureStartTimes(t *testing.T) {
	panel := NewAgentTreePanel(DefaultStyles())
	panel.nodes = []AgentTreeNode{
		{Status: "running"},
		{Status: "running", StartTime: time.Now().Add(time.Minute)},
	}
	if summary := panel.buildSummary(); strings.Contains(summary, "2562047h") || strings.Contains(summary, "-") {
		t.Fatalf("invalid start times leaked into summary: %q", summary)
	}
}

func TestAgentTreeCapsLargeRunningSetAndDisclosesFold(t *testing.T) {
	panel := NewAgentTreePanel(DefaultStyles())
	nodes := make([]AgentTreeNode, 20)
	for i := range nodes {
		nodes[i] = AgentTreeNode{ID: string(rune('a' + i)), AgentType: "worker", Description: "Parallel task", Status: "running", StartTime: time.Now()}
	}
	panel.UpdateTree(nodes)
	view := stripAnsi(panel.View(80))
	if shown := strings.Count(view, "[worker]"); shown != agentTreeFullRender {
		t.Fatalf("large running tree rendered %d task rows, want %d:\n%s", shown, agentTreeFullRender, view)
	}
	if !strings.Contains(view, "12 row(s) folded") {
		t.Fatalf("large running tree hid rows without disclosure:\n%s", view)
	}
}

func TestActivityFeedExtremeContentFitsAndNormalizesRows(t *testing.T) {
	panel := NewActivityFeedPanel(DefaultStyles())
	panel.ShowExplicit()
	panel.entries = []ActivityFeedEntry{{
		ID:          "tool",
		Type:        ActivityTypeTool,
		Name:        "custom\ntool",
		Description: "Running\n" + strings.Repeat("非常に長い説明 ", 20),
		Status:      ActivityRunning,
	}}
	panel.recentLog = []string{"first line\nsecond line " + strings.Repeat("log ", 30)}

	for _, width := range []int{1, 4, 12, 30, 60} {
		view := panel.View(width)
		assertPanelFitsWidth(t, view, width)
		if strings.Contains(stripAnsi(view), "2562047h") {
			t.Fatalf("width=%d zero StartTime rendered absurd duration:\n%s", width, stripAnsi(view))
		}
	}
	wide := stripAnsi(panel.View(60))
	for _, want := range []string{"custom tool", "Running 非常", "first line second line"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("activity feed lost normalized content %q:\n%s", want, wide)
		}
	}
}

func TestActivityFeedProgressNormalizationIsMonotonicAndBounded(t *testing.T) {
	panel := NewActivityFeedPanel(DefaultStyles())
	panel.StartSubAgent("a", "explore", "Work")
	panel.UpdateSubAgentProgress("a", 3, 12, 5)
	state := panel.GetSubAgentState("a")
	if state.Progress != 1 || state.CurrentStep != 5 || state.TotalSteps != 5 {
		t.Fatalf("bounded progress=%v step=%d/%d", state.Progress, state.CurrentStep, state.TotalSteps)
	}
	panel.UpdateSubAgentProgress("a", math.NaN(), 0, 0)
	state = panel.GetSubAgentState("a")
	if state.Progress != -1 || state.CurrentStep != 5 || state.TotalSteps != 5 {
		t.Fatalf("NaN/empty update corrupted known state: progress=%v step=%d/%d", state.Progress, state.CurrentStep, state.TotalSteps)
	}
}

func TestActivityFeedEmptyStateFitsNarrowPanels(t *testing.T) {
	panel := NewActivityFeedPanel(DefaultStyles())
	panel.ShowExplicit()
	for _, width := range []int{1, 4, 8, 20} {
		assertPanelFitsWidth(t, panel.View(width), width)
	}
}

func TestActivityFeedDisclosesRunningRowsBeyondRenderCap(t *testing.T) {
	panel := NewActivityFeedPanel(DefaultStyles())
	panel.ShowExplicit()
	for i := range 8 {
		panel.entries = append(panel.entries, ActivityFeedEntry{
			ID:          string(rune('a' + i)),
			Type:        ActivityTypeTool,
			Name:        "tool",
			Description: "running task",
			Status:      ActivityRunning,
			StartTime:   time.Now(),
		})
	}
	view := stripAnsi(panel.View(80))
	if !strings.Contains(view, "… 3 more running activities") {
		t.Fatalf("feed silently hid active rows beyond its cap:\n%s", view)
	}
}

func TestActivityFeedCompactHeightPrioritizesLiveThenFailure(t *testing.T) {
	panel := NewActivityFeedPanel(DefaultStyles())
	panel.ShowExplicit()
	panel.entries = []ActivityFeedEntry{
		{ID: "done", Type: ActivityTypeTool, Name: "completed-tool", Description: "stale success", Status: ActivityCompleted},
		{ID: "failed", Type: ActivityTypeTool, Name: "failed-tool", Description: "permission denied", Status: ActivityFailed},
		{ID: "live", Type: ActivityTypeTool, Name: "live-tool", Description: "writing current file", Status: ActivityRunning},
	}

	view := panel.View(80, 7)
	plain := stripAnsi(view)
	if got, limit := lipgloss.Height(view), activityFeedPanelHeightBudget(7); got > limit {
		t.Fatalf("compact feed height=%d, want <=%d:\n%s", got, limit, plain)
	}
	for _, want := range []string{"live-tool", "writing current file", "failed-tool", "permission denied", "Ctrl+O minimal"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("compact feed lost priority signal %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "completed-tool") || strings.Contains(plain, "stale success") {
		t.Fatalf("stale success displaced an active operation or failure:\n%s", plain)
	}
}

func TestActivityFeedCompactHeightFoldsLiveRowsBeforeRecentLog(t *testing.T) {
	panel := NewActivityFeedPanel(DefaultStyles())
	panel.ShowExplicit()
	for i := range 8 {
		panel.entries = append(panel.entries, ActivityFeedEntry{
			ID:          fmt.Sprintf("run-%d", i),
			Type:        ActivityTypeTool,
			Name:        fmt.Sprintf("live-%d", i),
			Description: fmt.Sprintf("current operation %d", i),
			Status:      ActivityRunning,
		})
	}
	for i := range 3 {
		panel.recentLog = append(panel.recentLog, fmt.Sprintf("old log %d", i))
	}

	view := panel.View(90, 8)
	plain := stripAnsi(view)
	if got, limit := lipgloss.Height(view), activityFeedPanelHeightBudget(8); got > limit {
		t.Fatalf("compact feed height=%d, want <=%d:\n%s", got, limit, plain)
	}
	for _, want := range []string{"live-7", "live-6", "… 6 more running activities"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("compact feed lost newest live signal %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "old log") {
		t.Fatalf("recent log displaced current running work:\n%s", plain)
	}
}

func TestActivityFeedFitsEveryTinyHeight(t *testing.T) {
	panel := NewActivityFeedPanel(DefaultStyles())
	panel.ShowExplicit()
	for i := range 4 {
		panel.entries = append(panel.entries, ActivityFeedEntry{
			ID:     fmt.Sprintf("run-%d", i),
			Type:   ActivityTypeTool,
			Name:   "tool",
			Status: ActivityRunning,
		})
	}
	panel.recentLog = []string{"recent result"}

	for height := 1; height <= 8; height++ {
		view := panel.View(36, height)
		if got, limit := lipgloss.Height(view), activityFeedPanelHeightBudget(height); got > limit {
			t.Fatalf("height=%d rendered %d rows, want <=%d:\n%s", height, got, limit, stripAnsi(view))
		}
	}
}
