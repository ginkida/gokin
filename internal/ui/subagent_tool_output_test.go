package ui

import (
	"strings"
	"testing"
)

// TestFormatAgentToolLine_SuccessAndFailure pins the merged line's shape: success
// shows the outcome summary (no ✗), failure shows ✗ + the error summary. No
// per-tool duration (it can't be correctly attributed for parallel tool batches).
func TestFormatAgentToolLine_SuccessAndFailure(t *testing.T) {
	s := DefaultStyles()

	ok := stripAnsi(s.FormatAgentToolLine("explore", "read", "auth.go", "175 lines", true))
	for _, want := range []string{"explore", "Read", "auth.go", "175 lines"} {
		if !strings.Contains(ok, want) {
			t.Fatalf("success line missing %q: %q", want, ok)
		}
	}
	if strings.Contains(ok, "✗") {
		t.Fatalf("success line must not show ✗: %q", ok)
	}

	fail := stripAnsi(s.FormatAgentToolLine("general", "bash", "go build", "exit code 1", false))
	for _, want := range []string{"general", "Bash", "go build", "exit code 1", "✗"} {
		if !strings.Contains(fail, want) {
			t.Fatalf("failure line missing %q: %q", want, fail)
		}
	}
}

// TestSubAgentToolEnd_RendersOutcome pins the core fix: a sub-agent tool now
// renders ONE merged line carrying its OUTCOME (✓ + summary) — previously
// tool_end appended nothing, so sub-agent work looked like "a tool, no output".
func TestSubAgentToolEnd_RendersOutcome(t *testing.T) {
	m := NewModel()
	m.handleMessageTypes(SubAgentActivityMsg{
		AgentID:   "a1",
		AgentType: "explore",
		ToolName:  "read",
		ToolArgs:  map[string]any{"file_path": "internal/app/auth.go"},
		Status:    "tool_end",
		Success:   true,
		Summary:   "175 lines",
	})
	rendered := stripAnsi(m.output.state.content.String())
	for _, want := range []string{"explore", "Read", "auth.go", "175 lines"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("tool_end line missing %q:\n%q", want, rendered)
		}
	}
}

// TestSubAgentToolStart_NoScrollbackLine pins that the contentless start row was
// removed — the live card/status bar show the running tool; scrollback only gets
// the merged result line on tool_end (matches foreground calm rendering).
func TestSubAgentToolStart_NoScrollbackLine(t *testing.T) {
	m := NewModel()
	before := m.output.state.content.String()
	m.handleMessageTypes(SubAgentActivityMsg{
		AgentID:   "a1",
		AgentType: "explore",
		ToolName:  "read",
		ToolArgs:  map[string]any{"file_path": "x.go"},
		Status:    "tool_start",
	})
	if after := m.output.state.content.String(); before != after {
		t.Fatalf("tool_start must not append a scrollback line; before=%q after=%q", before, after)
	}
}

// TestSubAgentToolEnd_FailureShowsError pins that a failed sub-agent tool shows
// ✗ plus the error summary (not a silent green line).
func TestSubAgentToolEnd_FailureShowsError(t *testing.T) {
	m := NewModel()
	m.handleMessageTypes(SubAgentActivityMsg{
		AgentID:   "a1",
		AgentType: "general",
		ToolName:  "bash",
		ToolArgs:  map[string]any{"command": "go build ./..."},
		Status:    "tool_end",
		Success:   false,
		Summary:   "exit code 1",
	})
	rendered := stripAnsi(m.output.state.content.String())
	for _, want := range []string{"general", "Bash", "✗", "exit code 1"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("failed tool_end line missing %q:\n%q", want, rendered)
		}
	}
}

// TestSubAgentStart_ForegroundTaskSurfaced pins the task-surfacing fix: a
// foreground sub-agent (no backgroundTasks entry) carries its dispatch prompt in
// msg.Task, which must now appear in the start line — previously it showed a
// bare "Agent started" with no context.
func TestSubAgentStart_ForegroundTaskSurfaced(t *testing.T) {
	m := NewModel()
	m.handleMessageTypes(SubAgentActivityMsg{
		AgentID:   "fg-1",
		AgentType: "general",
		Task:      "Find the rate-limit handler and add jitter",
		Status:    "start",
	})
	rendered := stripAnsi(m.output.state.content.String())
	if !strings.Contains(rendered, "rate-limit handler") {
		t.Fatalf("foreground sub-agent start must surface the dispatch task:\n%q", rendered)
	}
}
