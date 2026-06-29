package agent

import (
	"strings"
	"testing"

	"gokin/internal/tools"
)

func TestSubAgentToolOutcome(t *testing.T) {
	tests := []struct {
		name        string
		result      tools.ToolResult
		wantSuccess bool
		wantSummary string
	}{
		{"success multi-line counts lines", tools.ToolResult{Success: true, Content: "line1\nline2\nline3"}, true, "3 lines"},
		{"success single line shown", tools.ToolResult{Success: true, Content: "Successfully replaced 1 occurrence"}, true, "Successfully replaced 1 occurrence"},
		{"success empty content", tools.ToolResult{Success: true, Content: "  "}, true, "done"},
		{"failure uses error", tools.ToolResult{Success: false, Error: "file not found: x.go"}, false, "file not found: x.go"},
		{"failure empty error falls back to content", tools.ToolResult{Success: false, Content: "boom\nmore detail"}, false, "boom"},
		{"failure all empty", tools.ToolResult{Success: false}, false, "failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, summary := subAgentToolOutcome(tt.result)
			if ok != tt.wantSuccess {
				t.Fatalf("success = %v, want %v", ok, tt.wantSuccess)
			}
			if summary != tt.wantSummary {
				t.Fatalf("summary = %q, want %q", summary, tt.wantSummary)
			}
		})
	}
}

func TestSubAgentToolOutcome_TruncatesLongSingleLine(t *testing.T) {
	long := strings.Repeat("x", 300) // single line, no newline
	_, summary := subAgentToolOutcome(tools.ToolResult{Success: true, Content: long})
	if r := []rune(summary); len(r) != subAgentOutcomeMaxRunes {
		t.Fatalf("summary rune len = %d, want %d", len(r), subAgentOutcomeMaxRunes)
	}
	if !strings.HasSuffix(summary, "…") {
		t.Fatalf("truncated summary must end with ellipsis: %q", summary)
	}
}

func TestTruncateRunesEllipsis_RuneSafe(t *testing.T) {
	// Multi-byte runes must never be split (no panic, exact rune count).
	got := truncateRunesEllipsis(strings.Repeat("я", 100), 10)
	if r := []rune(got); len(r) != 10 {
		t.Fatalf("rune len = %d, want 10 (%q)", len(r), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("want ellipsis suffix: %q", got)
	}
	// Short input is returned unchanged.
	if truncateRunesEllipsis("ok", 10) != "ok" {
		t.Fatal("short input must be unchanged")
	}
	if truncateRunesEllipsis("anything", 0) != "" {
		t.Fatal("n<=0 must return empty")
	}
}
