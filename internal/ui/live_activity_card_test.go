package ui

import (
	"strings"
	"testing"
	"time"
)

// TestRenderLiveActivityCard_ShowsCurrentWorkWithoutStatusBarDup pins the
// dedupe contract: the compact card must NOT repeat provider/model, the
// state word (WRITING/RUNNING/WORKING), or the "Esc cancel" hint — all
// three are permanently visible in the bottom status bar. The card's one
// unique job is showing what the agent is doing right now. State is
// carried by the left-edge bar's colour, not by repeated text.
func TestRenderLiveActivityCard_ShowsCurrentWorkWithoutStatusBarDup(t *testing.T) {
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

	// Card must surface the current action + a recent-log echo.
	for _, want := range []string{
		"Read",               // tool name, capitalized
		"internal/ui/tui.go", // tool target
		"240 lines",          // recent-log snippet
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("card missing %q:\n%s", want, view)
		}
	}

	// Card must NOT repeat status-bar content.
	for _, dup := range []string{
		"RUNNING",    // state word — status bar shows ○ RUNNING
		"WRITING",    //   ditto
		"Esc cancel", // status bar shows "esc Interrupt"
		"kimi-for-coding", // model name — status bar shows it
	} {
		if strings.Contains(view, dup) {
			t.Errorf("card duplicates status bar content %q:\n%s", dup, view)
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
	// Compact form surfaces parallel work in the Next row.
	if !strings.Contains(view, "in flight") && !strings.Contains(view, "parallel") {
		t.Fatalf("expected parallel tool signal (in flight / parallel), got:\n%s", view)
	}
}

// TestRenderLiveActivityCard_CollapsesWhenFeedOpen: when the big Live
// Activity panel is open, the card must collapse to the single
// current-action line — Recent/Next duplicate what the big panel shows.
func TestRenderLiveActivityCard_CollapsesWhenFeedOpen(t *testing.T) {
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
	m.activityFeed.visible = true // <-- the key toggle

	view := stripAnsi(m.renderLiveActivityCard())

	// Must still show the current action.
	if !strings.Contains(view, "internal/ui/tui.go") {
		t.Errorf("collapsed card must still show current action: %q", view)
	}

	// Must NOT show Recent (duplicates big panel).
	if strings.Contains(view, "↳ ") {
		t.Errorf("collapsed card leaked Recent row: %q", view)
	}

	// Exactly 1 line (header only) — Recent/Next/footer all suppressed.
	lineCount := strings.Count(view, "\n") + 1
	if lineCount > 1 {
		t.Errorf("collapsed card has %d lines, want 1:\n%s", lineCount, view)
	}
}

// TestRenderLiveActivityCard_PureBulletSnippetFallsBackToGeneric: when the
// stream snippet is just a bullet marker (the model emitted "- " and no
// content yet), we must not render "Writing: " with a dangling space.
// Falls through to "Writing response" instead.
func TestRenderLiveActivityCard_PureBulletSnippetFallsBackToGeneric(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.state = StateStreaming
	// lastStreamSnippet reads from the response buffer; seed it with a
	// lone bullet marker like the model emits when it hasn't produced
	// content yet.
	m.currentResponseBuf.WriteString("- ")
	m.responseToolCount = 0

	line := m.liveActivityCurrentLine(ActivityFeedSnapshot{})
	if strings.HasSuffix(line, ": ") || strings.HasSuffix(line, ":") {
		t.Errorf("pure-bullet snippet produced dangling label: %q", line)
	}
	if !strings.Contains(line, "Writing response") {
		t.Errorf("expected fallback to 'Writing response', got: %q", line)
	}
}

// TestCleanStreamSnippet pins the snippet-cleanup that prevents visual
// collisions like "Writing: - `tui.go`" where the model-emitted snippet
// starts with its own markdown bullet. Stripping one leading list marker
// gives clean output.
func TestCleanStreamSnippet(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"- `tui.go` — fields", "`tui.go` — fields"},
		{"* list item", "list item"},
		{"• bullet item", "bullet item"},
		{"1. numbered", "numbered"},
		{"12. numbered", "numbered"},
		{"   - padded bullet", "padded bullet"},
		{"plain text", "plain text"},
		{"`code`", "`code`"},       // no leading bullet to strip
		{"-not a bullet", "-not a bullet"}, // no space after dash
		{"", ""},
		// Bare bullet markers (content hasn't arrived yet) — drop entirely
		// so caller falls back to a generic label. lastStreamSnippet does
		// TrimSpace, so we see "-" / "*" / "•", not "- " / "* " / "• ".
		{"-", ""},
		{"*", ""},
		{"•", ""},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			if got := cleanStreamSnippet(c.in); got != c.want {
				t.Errorf("cleanStreamSnippet(%q) = %q, want %q", c.in, got, c.want)
			}
		})
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
