package ui

import "testing"

// TestAgentRecentTools_ClearedOnCompleteAndFailed (round 7) pins the fix for
// a stale sub-agent tool-trail leak: agentRecentTools/agentToolCount were
// previously reset ONLY on the next SubAgentActivityMsg{"start"} — a
// finished sub-agent's tool trail lingered until then, which could be much
// later or in an entirely UNRELATED later turn that spawns no sub-agent at
// all (readable via the fallback processing-indicator block whenever
// currentTool=="" during StateProcessing/StateStreaming). Fixed by clearing
// both fields on "complete" and "failed" too, symmetric with "start".
func TestAgentRecentTools_ClearedOnCompleteAndFailed(t *testing.T) {
	t.Run("complete", func(t *testing.T) {
		m := NewModel()
		m.handleMessageTypes(SubAgentActivityMsg{AgentID: "a1", AgentType: "explore", Status: "start"})
		m.handleMessageTypes(SubAgentActivityMsg{
			AgentID: "a1", AgentType: "explore", ToolName: "read",
			ToolArgs: map[string]any{"file_path": "auth.go"}, Status: "tool_start",
		})
		if len(m.agentRecentTools) == 0 || m.agentToolCount == 0 {
			t.Fatal("test setup invalid: tool_start should have populated agentRecentTools/agentToolCount")
		}

		m.handleMessageTypes(SubAgentActivityMsg{AgentID: "a1", AgentType: "explore", Status: "complete"})

		if len(m.agentRecentTools) != 0 {
			t.Fatalf("agentRecentTools not cleared after complete: %v", m.agentRecentTools)
		}
		if m.agentToolCount != 0 {
			t.Fatalf("agentToolCount not cleared after complete: %d", m.agentToolCount)
		}
	})

	t.Run("failed", func(t *testing.T) {
		m := NewModel()
		m.handleMessageTypes(SubAgentActivityMsg{AgentID: "a1", AgentType: "general", Status: "start"})
		m.handleMessageTypes(SubAgentActivityMsg{
			AgentID: "a1", AgentType: "general", ToolName: "bash",
			ToolArgs: map[string]any{"command": "go build ./..."}, Status: "tool_start",
		})
		if len(m.agentRecentTools) == 0 || m.agentToolCount == 0 {
			t.Fatal("test setup invalid: tool_start should have populated agentRecentTools/agentToolCount")
		}

		m.handleMessageTypes(SubAgentActivityMsg{AgentID: "a1", AgentType: "general", Status: "failed"})

		if len(m.agentRecentTools) != 0 {
			t.Fatalf("agentRecentTools not cleared after failed: %v", m.agentRecentTools)
		}
		if m.agentToolCount != 0 {
			t.Fatalf("agentToolCount not cleared after failed: %d", m.agentToolCount)
		}
	})
}

// TestAgentRecentTools_DoesNotLeakIntoUnrelatedLaterTurn is the end-to-end
// failure scenario: a finished sub-agent's tool trail must not resurface in
// the fallback processing-indicator's status string (the concrete symptom —
// "tool names from a prior, unrelated conversation turn" showing up as if
// they were live activity).
func TestAgentRecentTools_DoesNotLeakIntoUnrelatedLaterTurn(t *testing.T) {
	m := NewModel()
	m.handleMessageTypes(SubAgentActivityMsg{AgentID: "a1", AgentType: "explore", Status: "start"})
	m.handleMessageTypes(SubAgentActivityMsg{
		AgentID: "a1", AgentType: "explore", ToolName: "read",
		ToolArgs: map[string]any{"file_path": "secret_turn_marker.go"}, Status: "tool_start",
	})
	m.handleMessageTypes(SubAgentActivityMsg{AgentID: "a1", AgentType: "explore", Status: "complete"})

	// A later, unrelated turn that never spawns a sub-agent — currentTool
	// stays empty throughout, which is exactly the fallback block's render
	// condition (len(agentRecentTools) > 0 && currentTool == "").
	if len(m.agentRecentTools) != 0 {
		t.Fatalf("stale tool trail would leak into an unrelated later turn: %v", m.agentRecentTools)
	}
}
