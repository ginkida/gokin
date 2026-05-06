package ui

import (
	"strings"
	"testing"
	"time"
)

// ── GenerateResultSummary ────────────────────────────────────────────────────

func TestGenerateResultSummary_Read(t *testing.T) {
	// Multiple lines — counts newlines
	if got := GenerateResultSummary("read", "line1\nline2\nline3"); got != "2 lines" {
		t.Errorf("read 3 lines (2 newlines): %q", got)
	}
	// Single line (no newlines) falls through to default ""
	if got := GenerateResultSummary("read", "just one line"); got != "" {
		t.Errorf("read 1 line (no newline): %q", got)
	}
	// Empty result
	if got := GenerateResultSummary("read", ""); got != "" {
		t.Errorf("read empty: %q", got)
	}
}

func TestGenerateResultSummary_Glob(t *testing.T) {
	if got := GenerateResultSummary("glob", "a.go\nb.go\nc.go"); got != "3 files" {
		t.Errorf("glob 3: %q", got)
	}
	if got := GenerateResultSummary("glob", ""); got != "0 files" {
		t.Errorf("glob empty: %q", got)
	}
	if got := GenerateResultSummary("glob", "single.go"); got != "1 files" {
		t.Errorf("glob single: %q", got)
	}
}

func TestGenerateResultSummary_Grep(t *testing.T) {
	if got := GenerateResultSummary("grep", "a.go:1:hit\nb.go:2:hit"); got != "2 matches" {
		t.Errorf("grep 2: %q", got)
	}
	if got := GenerateResultSummary("grep", ""); got != "0 matches" {
		t.Errorf("grep empty: %q", got)
	}
}

func TestGenerateResultSummary_Bash(t *testing.T) {
	// Multi-line output
	if got := GenerateResultSummary("bash", "line1\nline2\nline3"); got != "3 lines output" {
		t.Errorf("bash multi-line: %q", got)
	}
	// Single line — inline
	if got := GenerateResultSummary("bash", "ok"); got != "ok" {
		t.Errorf("bash single: %q", got)
	}
	// Empty output
	if got := GenerateResultSummary("bash", ""); got != "done" {
		t.Errorf("bash empty: %q", got)
	}
	// Long single line gets truncated
	long := strings.Repeat("x", 50)
	got := GenerateResultSummary("bash", long)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("bash long single line should be truncated: %q", got)
	}
	if len([]rune(got)) > 32 {
		t.Errorf("bash long single line too long: %d runes", len([]rune(got)))
	}
}

func TestGenerateResultSummary_EditWrite(t *testing.T) {
	if got := GenerateResultSummary("edit", "anything"); got != "applied" {
		t.Errorf("edit: %q", got)
	}
	// Write with newlines — counts newlines
	if got := GenerateResultSummary("write", "a\nb\nc"); got != "2 lines written" {
		t.Errorf("write 3 lines (2 newlines): %q", got)
	}
	// Write without newlines
	if got := GenerateResultSummary("write", "short"); got != "written" {
		t.Errorf("write no newlines: %q", got)
	}
}

func TestGenerateResultSummary_Unknown(t *testing.T) {
	if got := GenerateResultSummary("unknown_tool", "any result"); got != "" {
		t.Errorf("unknown: %q", got)
	}
}

// ── formatToolActivity ───────────────────────────────────────────────────────

func TestFormatToolActivity_ReadWriteEdit(t *testing.T) {
	if got := formatToolActivity("read", map[string]any{"file_path": "/tmp/foo.go"}); !strings.Contains(got, "foo.go") {
		t.Errorf("read: %q", got)
	}
	if got := formatToolActivity("write", map[string]any{"file_path": "/tmp/out.go", "content": "hello"}); !strings.Contains(got, "out.go") {
		t.Errorf("write: %q", got)
	}
	if got := formatToolActivity("edit", map[string]any{"file_path": "/tmp/src.go"}); !strings.Contains(got, "src.go") {
		t.Errorf("edit: %q", got)
	}
}

func TestFormatToolActivity_DeleteMkdir(t *testing.T) {
	if got := formatToolActivity("delete", map[string]any{"path": "/tmp/junk"}); !strings.Contains(got, "junk") {
		t.Errorf("delete: %q", got)
	}
	if got := formatToolActivity("mkdir", map[string]any{"path": "/tmp/newdir"}); !strings.Contains(got, "newdir") {
		t.Errorf("mkdir: %q", got)
	}
}

func TestFormatToolActivity_BashGrep(t *testing.T) {
	got := formatToolActivity("bash", map[string]any{"command": "go test ./..."})
	if !strings.Contains(got, "go test") {
		t.Errorf("bash: %q", got)
	}
	got = formatToolActivity("grep", map[string]any{"pattern": "TODO"})
	if !strings.Contains(got, "TODO") {
		t.Errorf("grep: %q", got)
	}
}

func TestFormatToolActivity_WebFetchSearch(t *testing.T) {
	got := formatToolActivity("web_fetch", map[string]any{"url": "https://example.com"})
	if !strings.Contains(got, "example.com") {
		t.Errorf("web_fetch: %q", got)
	}
	got = formatToolActivity("web_search", map[string]any{"query": "golang goroutines"})
	if !strings.Contains(got, "golang goroutines") {
		t.Errorf("web_search: %q", got)
	}
}

func TestFormatToolActivity_GitBranch(t *testing.T) {
	got := formatToolActivity("git_branch", map[string]any{"action": "create", "name": "feature"})
	if !strings.Contains(got, "create") || !strings.Contains(got, "feature") {
		t.Errorf("git_branch: %q", got)
	}
	// No args fallback
	got = formatToolActivity("git_branch", nil)
	if got != "Branch operation" {
		t.Errorf("git_branch no args: %q", got)
	}
}

func TestFormatToolActivity_GitLog(t *testing.T) {
	got := formatToolActivity("git_log", map[string]any{"file": "/src/main.go"})
	if !strings.Contains(got, "main.go") {
		t.Errorf("git_log file: %q", got)
	}
	got = formatToolActivity("git_log", map[string]any{"grep": "fix:"})
	if !strings.Contains(got, "fix:") {
		t.Errorf("git_log grep: %q", got)
	}
	got = formatToolActivity("git_log", nil)
	if got != "Reading git history" {
		t.Errorf("git_log fallback: %q", got)
	}
}

func TestFormatToolActivity_ListDirTree(t *testing.T) {
	got := formatToolActivity("list_dir", map[string]any{"directory_path": "/src"})
	if !strings.Contains(got, "src") {
		t.Errorf("list_dir: %q", got)
	}
	got = formatToolActivity("tree", nil)
	if got != "Listing directory" {
		t.Errorf("tree no path: %q", got)
	}
}

func TestFormatToolActivity_Task(t *testing.T) {
	got := formatToolActivity("task", map[string]any{"description": "Search for bugs"})
	if got != "Search for bugs" {
		t.Errorf("task description: %q", got)
	}
	got = formatToolActivity("task", map[string]any{"prompt": "Find all TODO comments"})
	if got != "Find all TODO comments" {
		t.Errorf("task prompt: %q", got)
	}
}

func TestFormatToolActivity_UnknownFallback(t *testing.T) {
	if got := formatToolActivity("my_custom_tool", nil); got != "my_custom_tool" {
		t.Errorf("unknown tool: %q", got)
	}
}

// ── ActivityFeedPanel methods (0% coverage) ──────────────────────────────────

func TestActivityFeedPanel_TickAndToggle(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())

	if p.IsVisible() {
		t.Fatal("should start hidden")
	}

	p.Toggle()
	if !p.IsVisible() {
		t.Error("Toggle() should show panel")
	}
	p.Toggle()
	if p.IsVisible() {
		t.Error("second Toggle() should hide panel")
	}

	// Tick should not panic and should advance frame
	p.Tick()
	p.Tick()
	p.Tick()
	// No assertion beyond "does not panic"
}

func TestActivityFeedPanel_UpdateSubAgentProgress(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "explore", "Search codebase")

	p.UpdateSubAgentProgress("a1", 0.5, 3, 6)

	state := p.GetSubAgentState("a1")
	if state == nil {
		t.Fatal("state should exist")
	}
	if state.Progress != 0.5 {
		t.Errorf("progress = %v, want 0.5", state.Progress)
	}
	if state.CurrentStep != 3 {
		t.Errorf("currentStep = %v, want 3", state.CurrentStep)
	}
	if state.TotalSteps != 6 {
		t.Errorf("totalSteps = %v, want 6", state.TotalSteps)
	}

	// Unknown agent ID should not panic
	p.UpdateSubAgentProgress("nonexistent", 1.0, 10, 10)
}

func TestActivityFeedPanel_CompleteSubAgent_Success(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "explore", "Search codebase")

	if !p.HasActiveEntries() {
		t.Error("should have active entries after StartSubAgent")
	}

	p.CompleteSubAgent("a1", true, "found 12 results")

	state := p.GetSubAgentState("a1")
	if state != nil {
		t.Error("state should be removed after CompleteSubAgent")
	}
}

func TestActivityFeedPanel_CompleteSubAgent_Failure(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a2", "general", "Do something")
	p.CompleteSubAgent("a2", false, "timed out")

	// After completion, agent state should be cleared
	if p.GetSubAgentState("a2") != nil {
		t.Error("state should be removed after failed CompleteSubAgent")
	}
}

func TestActivityFeedPanel_CompleteSubAgent_UnknownID(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	// Should not panic for unknown agent
	p.CompleteSubAgent("ghost", true, "")
}

func TestActivityFeedPanel_Clear(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "explore", "Find bugs")
	p.AddEntry(ActivityFeedEntry{
		ID:     "t1",
		Type:   ActivityTypeTool,
		Name:   "read",
		Status: ActivityRunning,
	})

	p.Clear()

	if p.HasActiveEntries() {
		t.Error("should have no active entries after Clear")
	}
	if p.GetSubAgentState("a1") != nil {
		t.Error("sub-agent state should be cleared")
	}
}

// ── Snapshot ─────────────────────────────────────────────────────────────────

func TestActivityFeedPanel_Snapshot(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "explore", "Search")
	p.AddEntry(ActivityFeedEntry{
		ID:        "t1",
		Type:      ActivityTypeTool,
		Name:      "read",
		Status:    ActivityRunning,
		StartTime: time.Now(),
	})

	snap := p.Snapshot(5, 3)
	if snap.RunningAgents != 1 {
		t.Errorf("RunningAgents = %d, want 1", snap.RunningAgents)
	}
	if snap.RunningTools != 1 {
		t.Errorf("RunningTools = %d, want 1", snap.RunningTools)
	}
}
