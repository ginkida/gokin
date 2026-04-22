package ui

import (
	"strings"
	"testing"
	"time"
)

func TestRenderLiveActivityCard_ShowsCurrentWorkAndHints(t *testing.T) {
	m := NewModel()
	m.width = 110
	m.state = StateProcessing
	m.currentTool = "read"
	m.currentToolInfo = "internal/ui/tui.go"
	m.toolStartTime = time.Now().Add(-2 * time.Second)
	m.currentModel = "kimi-for-coding"
	m.runtimeStatus.Provider = "kimi"
	m.lastToolOutputIndex = 0
	m.activityFeed.recentLog = []string{"Reading internal/ui/tui.go -> 240 lines"}

	view := stripAnsi(m.renderLiveActivityCard())
	for _, want := range []string{
		"Now",
		"Doing",
		"Read",
		"internal/ui/tui.go",
		"Recent",
		"240 lines",
		"Esc cancel",
		"e last tool output",
		"kimi",
		"for-coding",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("live activity card missing %q:\n%s", want, view)
		}
	}
}

func TestRenderLiveActivityCard_ShowsLiveActivityHintForHiddenFeed(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.state = StateProcessing
	m.currentTool = "bash"
	m.currentToolInfo = "$ go test ./internal/ui"
	m.toolStartTime = time.Now().Add(-3 * time.Second)
	m.activeToolCalls = []activeToolCall{
		{name: "bash", info: "$ go test ./internal/ui", startTime: time.Now().Add(-3 * time.Second)},
		{name: "grep", info: "StatusUpdateMsg", startTime: time.Now().Add(-2 * time.Second)},
	}
	m.activityFeed.entries = []ActivityFeedEntry{
		{ID: "1", Type: ActivityTypeTool, Name: "bash", Description: "Running tests", Status: ActivityRunning, StartTime: time.Now().Add(-3 * time.Second)},
		{ID: "2", Type: ActivityTypeTool, Name: "grep", Description: "Searching retries", Status: ActivityRunning, StartTime: time.Now().Add(-2 * time.Second)},
	}
	m.activityFeed.visible = false

	view := stripAnsi(m.renderLiveActivityCard())
	if !strings.Contains(view, "2 tools in flight") {
		t.Fatalf("expected parallel tool summary, got:\n%s", view)
	}
	if !strings.Contains(view, "Ctrl+O live activity") {
		t.Fatalf("expected live activity hint when feed is hidden, got:\n%s", view)
	}
}

func TestActivityFeedPanel_AutoShowsForParallelActivity(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())

	p.AddEntry(ActivityFeedEntry{
		ID:        "tool-1",
		Type:      ActivityTypeTool,
		Name:      "read",
		Status:    ActivityRunning,
		StartTime: time.Now(),
	})
	if p.IsVisible() {
		t.Fatal("single tool should stay quiet")
	}

	p.AddEntry(ActivityFeedEntry{
		ID:        "tool-2",
		Type:      ActivityTypeTool,
		Name:      "grep",
		Status:    ActivityRunning,
		StartTime: time.Now(),
	})
	if !p.IsVisible() {
		t.Fatal("parallel activity should auto-show the feed")
	}
}

func TestRenderEngineStatus_UsesRichRuntimeStates(t *testing.T) {
	m := NewModel()
	m.state = StateProcessing
	m.currentTool = "read"
	m.activeToolCalls = []activeToolCall{{name: "read"}, {name: "grep"}}

	status := stripAnsi(m.renderEngineStatus())
	if !strings.Contains(status, "RUN READ ×2") {
		t.Fatalf("expected running status with parallel count, got %q", status)
	}

	m.currentTool = ""
	m.activeToolCalls = nil
	m.state = StateStreaming
	m.responseToolCount = 3
	status = stripAnsi(m.renderEngineStatus())
	if !strings.Contains(status, "WRITING · 3 tools") {
		t.Fatalf("expected streaming status with tool count, got %q", status)
	}

	m.state = StateProcessing
	m.responseToolCount = 0
	m.retryAttempt = 1
	m.retryMax = 3
	status = stripAnsi(m.renderEngineStatus())
	if !strings.Contains(status, "RETRY 1/3") {
		t.Fatalf("expected retry status, got %q", status)
	}
}
