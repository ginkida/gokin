package agent

import (
	"fmt"
	"strings"

	"gokin/internal/tools"
)

// subAgentToolOutcome derives a short, honest one-line outcome from a finished
// sub-agent tool result. It feeds the UI's merged tool line
// (▪ <type> Tool(target) · <outcome>): success drives the ✓/✗ coloring, and the
// summary is a COUNT/first-line value so it never repeats the path or command
// (those already appear in the parenthesized target).
//
// It lives in the agent package — which holds the FULL result — so line counts
// are accurate without an agent→ui import. It is deliberately GENERIC (no
// per-tool switch) so it cannot drift from ui.toolOutcomeSummary; the UI keeps
// the tidy per-tool phrasing for the foreground, and this gives a faithful,
// never-misleading summary for sub-agents.
func subAgentToolOutcome(r tools.ToolResult) (success bool, summary string) {
	if !r.Success {
		msg := firstNonEmptyLine(r.Error)
		if msg == "" {
			msg = firstNonEmptyLine(r.Content)
		}
		if msg == "" {
			msg = "failed"
		}
		return false, truncateRunesEllipsis(msg, subAgentOutcomeMaxRunes)
	}

	content := strings.TrimSpace(r.Content)
	if content == "" {
		return true, "done"
	}
	// Multi-line output: report the size honestly ("175 lines") rather than
	// echoing a single line that may not represent the whole result.
	if n := strings.Count(content, "\n"); n >= 1 {
		return true, fmt.Sprintf("%d lines", n+1)
	}
	// Single line: show it (a short status like "Successfully replaced …").
	return true, truncateRunesEllipsis(content, subAgentOutcomeMaxRunes)
}

const subAgentOutcomeMaxRunes = 72

// firstNonEmptyLine returns the first non-blank line of s, trimmed.
func firstNonEmptyLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// truncateRunesEllipsis trims s to at most n runes (rune-safe — never slices a
// multi-byte sequence), appending "…" when it cuts.
func truncateRunesEllipsis(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(runes[:n-1]) + "…"
}
