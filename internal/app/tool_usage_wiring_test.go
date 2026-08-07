package app

import (
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/toolusage"
)

// A nil ledger is a safe no-op on every method, which is what makes an unwired
// ledger dangerous: it would record nothing, report every tool as never used,
// and look exactly like a working instrument. These tests hold the wiring, not
// the ledger — the ledger has its own package tests.
func TestToolUsageRecordsFromBothSinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_usage.json")
	application := &App{
		phaseMetrics: NewPhaseMetrics(),
		toolMetrics:  NewToolMetrics(),
		toolUsage:    toolusage.NewLedger(path),
	}

	// Foreground executor path.
	application.recordToolPhaseOutcome("read", 10*time.Millisecond, true)

	// Sub-agent path. Sub-agent tool calls never reach recordToolPhaseOutcome,
	// so a tool used only through delegation would otherwise read as dead.
	application.handleSubAgentActivity("agent-1", "general", "task", "go_search",
		nil, "tool_end", true, "found 3")

	counts := application.toolUsage.Snapshot()
	if counts["read"] != 1 {
		t.Fatalf("foreground sink did not record: %v", counts)
	}
	if counts["go_search"] != 1 {
		t.Fatalf("sub-agent sink did not record: %v", counts)
	}
}

// The counter answers "has this ever been reached for", which /clear must not
// be able to erase — that is precisely what the in-memory collector already did.
func TestToolUsageSurvivesClearConversation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_usage.json")
	application := &App{
		phaseMetrics: NewPhaseMetrics(),
		toolMetrics:  NewToolMetrics(),
		toolUsage:    toolusage.NewLedger(path),
	}
	application.recordToolPhaseOutcome("repl_exec", time.Millisecond, true)

	application.toolMetrics.Reset()

	if got := application.toolUsage.Snapshot()["repl_exec"]; got != 1 {
		t.Fatalf("lifetime count = %d after session metrics reset, want 1", got)
	}
	if len(application.toolMetrics.Snapshot()) != 0 {
		t.Fatal("session metrics should have been cleared")
	}
}
