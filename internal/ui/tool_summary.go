package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// conciseToolTarget returns the short subject for the parenthesized part of a
// tool line — "Read(credentials.go)", "Bash(go build ./...)". Deliberately
// terse: the basename for file tools, the meaningful command for bash. The full
// path/command is no longer repeated on a separate result row, so one clean
// subject per line is enough.
func conciseToolTarget(name, info string) string {
	info = strings.TrimSpace(info)
	if info == "" {
		return ""
	}
	switch strings.ToLower(strings.ReplaceAll(name, "-", "_")) {
	case "read", "write", "edit", "delete", "mkdir", "run_tests", "verify_code":
		base := filepath.Base(info)
		// Strip a trailing " (N lines)"-style annotation some info strings carry.
		if i := strings.LastIndex(base, " ("); i > 0 {
			base = base[:i]
		}
		return base
	case "bash", "test", "build":
		return compactInline(stripBashPlumbing(info), 48)
	default:
		return compactInline(info, 44)
	}
}

// stripBashPlumbing trims the noisy shell scaffolding (a leading `cd … &&`, a
// trailing `2>&1` / `| head` / `> /dev/null` capture) so the command's intent
// shows on the one-line display. The model still ran the full command — this
// only affects the label.
func stripBashPlumbing(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	// Some info strings carry a display "$ " prefix (extractToolInfoFromArgs) —
	// without stripping it the `cd … &&` check below never matches and the
	// whole plumbing survives ("Bash($ cd /long/path && go test -...)").
	cmd = strings.TrimSpace(strings.TrimPrefix(cmd, "$"))
	if strings.HasPrefix(cmd, "cd ") {
		if i := strings.Index(cmd, "&&"); i >= 0 {
			cmd = strings.TrimSpace(cmd[i+2:])
		}
	}
	cut := len(cmd)
	for _, marker := range []string{" 2>&1", " | head", " | tail", " >/dev/null", " > /dev/null", " 2>/dev/null"} {
		if i := strings.Index(cmd, marker); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(cmd[:cut])
}

// editDiffDisplay is the edit tool's display-diff payload, stashed on the
// Model by the ToolResultMsg handler and consumed by
// handleToolResultWithStatus within the same Update dispatch.
type editDiffDisplay struct {
	Text    string
	Added   int
	Removed int
}

// renderEditDiffBody renders the Claude-Code-style edit result body: an
// "Added N lines, removed M lines" header followed by the numbered ±hunk
// lines, colored by marker (+ green, - red, context dim).
func renderEditDiffBody(d *editDiffDisplay) string {
	header := lipgloss.NewStyle().Foreground(ColorMuted).
		Render(fmt.Sprintf("Added %s, removed %s",
			pluralCount(d.Added, "line", "lines"), pluralCount(d.Removed, "line", "lines")))

	addStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	delStyle := lipgloss.NewStyle().Foreground(ColorError)
	ctxStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var sb strings.Builder
	sb.WriteString(header)
	for line := range strings.SplitSeq(d.Text, "\n") {
		if line == "" {
			continue
		}
		sb.WriteByte('\n')
		switch line[0] {
		case '+':
			sb.WriteString(addStyle.Render(line))
		case '-':
			sb.WriteString(delStyle.Render(line))
		default:
			sb.WriteString(ctxStyle.Render(line))
		}
	}
	return sb.String()
}

// stripModelFacingContext removes machine-facing blocks from a tool result
// before it reaches ANY user-facing surface (result card body, expand view,
// activity-feed summary). These blocks are load-bearing for the MODEL —
// `[context:predicted]`/`[context]` enrichment hints (executor step 12.9c)
// and the edit tool's "no verification read needed" instruction — but read
// as machine noise in the user's scrollback. The model-facing stream is
// untouched; this only affects rendering (the same discipline as the
// dedup-stub marker path in handleToolResultWithStatus).
func stripModelFacingContext(content string) string {
	if content == "" || (!strings.Contains(content, "[context") && !strings.Contains(content, "Updated region (")) {
		return content // fast path — nothing to strip
	}
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		t := strings.TrimSpace(line)
		// Enrichment hints are single logical lines prefixed with [context…].
		if strings.HasPrefix(t, "[context]") || strings.HasPrefix(t, "[context:") {
			continue
		}
		// The edit-region header carries a model-facing parenthetical
		// ("already written to disk — no verification read needed") — keep
		// the useful header, drop the instruction aimed at the model.
		if strings.HasPrefix(t, "Updated region (") && strings.HasSuffix(t, ":") {
			out = append(out, "Updated region:")
			continue
		}
		out = append(out, line)
	}
	// Collapse the doubled blank line a dropped trailing block leaves behind.
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// toolOutcomeSummary returns the COUNT-ONLY outcome for a tool's merged line
// ("175 lines", "4 matches", "updated") — never the path/command, which already
// appears in the parenthesized target. Empty when there's nothing to add.
func toolOutcomeSummary(name, content string) string {
	switch strings.ToLower(strings.ReplaceAll(name, "-", "_")) {
	case "read":
		if n := displayLineCount(content); n > 0 {
			return pluralLines(n)
		}
		return ""
	case "bash", "test", "build", "run_tests", "verify_code":
		if strings.TrimSpace(content) == "(no matches)" {
			return "no matches"
		}
		if n := displayLineCount(content); n > 0 {
			return pluralLines(n)
		}
		return "completed"
	case "grep", "glob", "file_search", "code_search":
		if n := displayLineCount(content); n > 0 {
			return pluralCount(n, "match", "matches")
		}
		return "no matches"
	case "edit":
		return "updated"
	case "write":
		return "written"
	case "delete":
		return "deleted"
	case "mkdir":
		return "created"
	case "move":
		return "moved"
	case "copy":
		return "copied"
	}
	return ""
}

// summarizeSubAgentTask turns a sub-agent's dispatch prompt into a one-line
// description for the activity feed. Prompts can be paragraphs long; the
// feed only has one row per agent, so we pick the first sentence / line and
// cap to a screen-friendly length.
//
// Falls back to "Sub-agent: <type>" when the prompt is empty — spawn sites
// that haven't been updated, or agents spawned via legacy paths. Better to
// show *something* than a blank row.
func summarizeSubAgentTask(prompt, agentType string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		if agentType == "" {
			return "Sub-agent"
		}
		return "Sub-agent: " + agentType
	}

	// First meaningful line. Prompts often open with "You are a …" system
	// preamble — cheap heuristic: skip that framing and go for the task.
	var first string
	for line := range strings.SplitSeq(prompt, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "You are ") {
			continue
		}
		first = line
		break
	}
	if first == "" {
		// Everything was blank or preamble — fall back to the raw first line
		// so the row isn't empty.
		first = strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	}

	// Truncate at first sentence-ending punctuation (once we have enough
	// content to not chop mid-verb).
	if idx := strings.IndexAny(first, ".?!"); idx > 20 {
		first = first[:idx]
	}

	// Hard cap so the feed row doesn't wrap or blow past terminal width.
	const maxLen = 70
	if runes := []rune(first); len(runes) > maxLen {
		first = string(runes[:maxLen-1]) + "…"
	}
	if agentType != "" {
		return agentType + " · " + first
	}
	return first
}

// generateToolResultSummary creates compact summaries based on tool type and content.
func generateToolResultSummary(toolName, content, detail string) string {
	normalizedTool := strings.ToLower(strings.ReplaceAll(toolName, "-", "_"))
	// For tools whose detail is a filesystem path, use the path-aware
	// shortener (handles ~/, keeps filename visible). For command/pattern
	// tools, generic head-tail truncation is fine.
	switch normalizedTool {
	case "read", "write", "edit", "delete", "glob", "grep":
		detail = shortenPath(detail, 40)
	default:
		detail = summarizeToolDetail(detail, 56)
	}
	lineCount := displayLineCount(content)

	switch normalizedTool {
	case "read":
		// Codex-style: path first ("what was read") with line count in
		// parens as supporting context. Old format was "N lines from
		// path" which led with the count — fine for technical density,
		// noisier in an exploration phase where the agent reads 10
		// files in a row and you just want a column of file paths.
		if detail != "" {
			if lineCount > 0 {
				return fmt.Sprintf("%s (%s)", detail, pluralLines(lineCount))
			}
			return detail
		}
		if lineCount > 0 {
			return pluralLines(lineCount)
		}
	case "glob":
		// Codex-style: pattern first, count in parens. Empty content or
		// the "(no matches)" sentinel both render as the pattern with
		// "(no matches)" suffix — the pattern is still the most useful
		// thing for the user to see at a glance.
		empty := lineCount == 0 || strings.Contains(content, "(no matches)")
		if empty {
			if detail != "" {
				return fmt.Sprintf("%s (no matches)", detail)
			}
			return "no matches"
		}
		if detail != "" {
			return fmt.Sprintf("%s (%s)", detail, pluralCount(lineCount, "match", "matches"))
		}
		return pluralCount(lineCount, "match", "matches")
	case "grep", "file_search", "code_search":
		// Codex-style: pattern first, count in parens.
		if lineCount == 0 {
			if detail != "" {
				return fmt.Sprintf("%s (no matches)", detail)
			}
			return "no matches"
		}
		if detail != "" {
			return fmt.Sprintf("%s (%s)", detail, pluralCount(lineCount, "match", "matches"))
		}
		return pluralCount(lineCount, "match", "matches")
	case "bash", "test", "build":
		// Codex-style: command first ("what was run") with line count in
		// parens. Body preview is collapsed by default (see
		// collapsedByDefault in tui.go) so this title line carries all
		// the visible signal — leading with the command makes a stack of
		// 5 bash calls scan as a column of operations, not "200 lines
		// from … 13 lines from … 4 lines from …".
		//
		// A benign no-match search (grep/rg via bash, exit 1) carries the
		// "(no matches)" sentinel — render it as such, not "1 line".
		if strings.TrimSpace(content) == "(no matches)" {
			if detail != "" {
				return fmt.Sprintf("%s (no matches)", detail)
			}
			return "no matches"
		}
		if detail != "" {
			if lineCount > 0 {
				return fmt.Sprintf("%s (%s)", detail, pluralLines(lineCount))
			}
			return detail
		}
		if lineCount > 0 {
			return pluralLines(lineCount) + " of output"
		}
		return "completed"
	case "edit":
		// Codex-style: the path *is* the action. Earlier "updated PATH"
		// was reading like a status report; in a column of edits the
		// "updated" word is pure repetition.
		if detail != "" {
			return detail
		}
		return "updated"
	case "write":
		if detail != "" {
			return detail
		}
		return "written"
	case "delete":
		if detail != "" {
			return detail
		}
		return "deleted"
	case "tree", "list_dir", "list_files":
		// Codex-style: dir first, item count in parens, "(empty)" suffix
		// for empty dirs (same shape as glob's "(no matches)").
		if strings.Contains(content, "(empty)") || lineCount == 0 {
			if detail != "" {
				return fmt.Sprintf("%s (empty)", detail)
			}
			return "empty"
		}
		if detail != "" {
			return fmt.Sprintf("%s (%s)", detail, pluralCount(lineCount, "item", "items"))
		}
		return pluralCount(lineCount, "item", "items")
	case "web_fetch":
		// Codex-style: the URL alone — "fetched" was status filler when
		// the URL already tells you what happened. Empty detail keeps
		// the legacy "fetched" so we don't render a blank string.
		if detail != "" {
			return detail
		}
		return "fetched"
	case "web_search":
		// Codex-style: query first, result count in parens. Result count
		// derived from "http" occurrences in body (heuristic preserved
		// from the previous implementation — search backends differ).
		resultCount := 0
		if len(content) > 0 {
			resultCount = strings.Count(content, "http")
		}
		if resultCount > 0 {
			if detail != "" {
				return fmt.Sprintf("%s (%s)", detail, pluralCount(resultCount, "result", "results"))
			}
			return pluralCount(resultCount, "result", "results")
		}
		if detail != "" {
			return fmt.Sprintf("%s (no results)", detail)
		}
		return "no results"
	case "ask_user", "ask_question":
		return "answered"
	}

	return detail
}

// pluralCount renders a count with grammatical agreement: "1 line" vs
// "N lines", "1 match" vs "N matches", etc. Used by the codex-style
// summaries so a single match doesn't read as "1 matches" — a small
// thing, but the summary is the most-read part of the chat stream.
func pluralCount(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// pluralLines is the line-count specialisation kept for callers that
// already use the "line/lines" wording (read, bash output, etc.).
func pluralLines(n int) string {
	return pluralCount(n, "line", "lines")
}

func displayLineCount(content string) int {
	trimmed := strings.TrimRight(content, "\n")
	if trimmed == "" {
		return 0
	}
	return strings.Count(trimmed, "\n") + 1
}

func summarizeToolDetail(detail string, maxLen int) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}

	detail = strings.Join(strings.Fields(detail), " ")
	if detail == "" {
		return ""
	}
	runes := []rune(detail)
	if len(runes) <= maxLen {
		return detail
	}

	headLen := (maxLen - 3) / 2
	tailLen := maxLen - 3 - headLen
	if headLen < 1 || tailLen < 1 {
		return string(runes[:maxLen])
	}
	return string(runes[:headLen]) + "..." + string(runes[len(runes)-tailLen:])
}
