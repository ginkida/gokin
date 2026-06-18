package tools

import (
	"fmt"
	"strings"
)

// Incomplete-work continuation: the fix for "the model narrates 'Продолжаю…'
// then stops mid-task and the user has to type 'continue'." An agentic loop ends
// the turn as soon as the model returns text with no tool calls — but if the
// model's OWN todo list still has unfinished items, it isn't actually done; it
// just announced the next step without taking it. The loop should nudge it to
// ACT and keep going instead of ending.

// MaxIncompleteWorkContinuations bounds consecutive nudges where the model
// narrated WITHOUT acting (no new tool ran since the last nudge). A model that
// keeps describing the next step but never calls a tool is stuck — stop after
// this many and hand control back. A model steadily completing todos runs a tool
// between nudges, which resets the counter (see each loop's progress check), so
// genuine multi-step work is never capped by this.
const MaxIncompleteWorkContinuations = 3

// IncompleteTodoSummary inspects the registry's todo tool and returns the count
// of not-completed items (pending + in_progress) plus a short, capped human
// summary of them. Returns (0, "") when there is no todo tool, no list, or every
// item is completed — i.e. the model is genuinely finished and the loop should
// end normally. Safe on a nil registry / missing tool (returns 0).
func IncompleteTodoSummary(registry ToolRegistry) (int, string) {
	if registry == nil {
		return 0, ""
	}
	tool, ok := registry.Get("todo")
	if !ok {
		return 0, ""
	}
	tt, ok := tool.(*TodoTool)
	if !ok || tt == nil {
		return 0, ""
	}

	items := tt.GetItems()
	count := 0
	var lines []string
	for _, it := range items {
		if it.Status == "completed" {
			continue
		}
		count++
		if len(lines) < 5 {
			marker := "•"
			if it.Status == "in_progress" {
				marker = "▶"
			}
			lines = append(lines, fmt.Sprintf("  %s %s", marker, it.Content))
		}
	}
	if count == 0 {
		return 0, ""
	}
	summary := strings.Join(lines, "\n")
	if count > len(lines) {
		summary += fmt.Sprintf("\n  … and %d more", count-len(lines))
	}
	return count, summary
}

// IncompleteWorkContinuationPrompt is the firm user-role nudge appended to
// history when the model stopped with unfinished todos. It insists the model
// ACT (call the next tool) rather than describe — the failure mode is the model
// announcing intent without taking the action.
func IncompleteWorkContinuationPrompt(count int, summary string) string {
	return fmt.Sprintf(
		"Your task list still has %d unfinished item(s):\n%s\n\nThe task is NOT complete — keep going. Do the next step NOW by calling the appropriate tool; do not just describe what you will do. When everything is genuinely finished, call todo to mark the items completed.",
		count, summary,
	)
}
