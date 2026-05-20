package ui

import (
	"fmt"
	"strings"
)

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
		if strings.Contains(content, "(no matches)") {
			return "no matches"
		}
		if lineCount > 0 {
			if detail != "" {
				return fmt.Sprintf("%d files in %s", lineCount, detail)
			}
			return fmt.Sprintf("%d files", lineCount)
		}
		return "no matches"
	case "grep", "file_search", "code_search":
		if lineCount > 0 {
			if detail != "" {
				return fmt.Sprintf("%d matches for %s", lineCount, detail)
			}
			return fmt.Sprintf("%d matches", lineCount)
		}
		return "no matches"
	case "bash", "test", "build":
		// Codex-style: command first ("what was run") with line count in
		// parens. Body preview is collapsed by default (see
		// collapsedByDefault in tui.go) so this title line carries all
		// the visible signal — leading with the command makes a stack of
		// 5 bash calls scan as a column of operations, not "200 lines
		// from … 13 lines from … 4 lines from …".
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
		if detail != "" {
			return fmt.Sprintf("updated %s", detail)
		}
		return "updated"
	case "write":
		if detail != "" {
			return fmt.Sprintf("wrote %s", detail)
		}
		return "written"
	case "tree", "list_dir", "list_files":
		if strings.Contains(content, "(empty)") {
			return "empty"
		}
		if lineCount > 0 {
			if detail != "" {
				return fmt.Sprintf("%d items in %s", lineCount, detail)
			}
			return fmt.Sprintf("%d items", lineCount)
		}
	case "web_fetch":
		if detail != "" {
			return fmt.Sprintf("fetched %s", detail)
		}
		return "fetched"
	case "web_search":
		if len(content) > 0 {
			resultCount := strings.Count(content, "http")
			if resultCount > 0 {
				if detail != "" {
					return fmt.Sprintf("%d results for %s", resultCount, detail)
				}
				return fmt.Sprintf("%d results", resultCount)
			}
		}
		if detail != "" {
			return fmt.Sprintf("done for %s", detail)
		}
		return "done"
	case "ask_user", "ask_question":
		return "answered"
	}

	return detail
}

// pluralLines renders a line-count with grammatical agreement: "1 line"
// vs "N lines". Used by the codex-style read/bash summaries so a single
// stdout line of output doesn't read as "1 lines" — a small thing, but
// the summary is the most-read part of the chat stream.
func pluralLines(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
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
