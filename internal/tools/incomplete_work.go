package tools

import (
	"context"
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
	return incompleteTodoSummary(registry, false)
}

func incompleteTodoSummary(registry ToolRegistry, skipVerification bool) (int, string) {
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
		if it.Status == "completed" || skipVerification && isVerificationTodo(it.Content) {
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

// Only clearly verification-only tasks may be omitted. Unknown or mixed tasks
// still need continuation: permission limits on checks cannot abandon edits.
func isVerificationTodo(content string) bool {
	content = strings.ToLower(strings.Join(strings.Fields(content), " "))
	switch content {
	case "run tests", "run the tests", "run test suite", "run verification", "run checks",
		"run lint", "run build", "verify code", "verify changes", "verify build", "validate changes",
		"check formatting", "build project", "test changes", "lint code", "run lint for changed files":
		return true
	}
	// Only complete known commands qualify. Arbitrary arguments or prose may
	// contain implementation work, even after a recognizable command prefix.
	switch strings.TrimPrefix(content, "run ") {
	case "go test", "go test ./...", "go build", "go build ./...", "go vet", "go vet ./...",
		"cargo test", "cargo build", "cargo check", "cargo clippy", "pytest",
		"npm test", "npm run test", "npm run build", "npm run lint", "pnpm test", "yarn test", "bun test",
		"make test", "make check", "make build", "make lint":
		return true
	}
	return false
}

// IncompleteWorkDecision is the result of the shared todo-continuation gate
// (DecideIncompleteWorkContinuation). It carries the decision plus the UPDATED
// counter values the caller stores back — but NOT any side effect (history
// append, warning toast, locking, carried text): those stay with the caller
// because they differ between the foreground executor and the sub-agent loop.
type IncompleteWorkDecision struct {
	Continue  bool   // append IncompleteWorkContinuationPrompt and re-loop
	Exhausted bool   // had unfinished todos but the continuation budget is spent (caller logs)
	Count     int    // unfinished-item count (for the prompt + logging)
	Summary   string // capped human summary of the unfinished items (for the prompt)
	Stuck     int    // updated value to store into the caller's incompleteWorkStuck
	LastNudge int    // updated value to store into the caller's toolsUsedAtLastIncompleteNudge
}

// DecideIncompleteWorkContinuation is the byte-behavior-identical decision core
// shared by executor.executeLoop and agent.executeLoop (Tier-4 unification): the
// model returned text with no tool calls — should the loop NUDGE it to keep going
// because its OWN todo list is unfinished, or finish? It performs the
// max_tokens-skip, unfinished-todo check, progress reset, budget check, and
// counter math exactly as the two former inline copies did, and returns the
// decision + updated counters. It touches NO history/locks/callbacks — the caller
// owns those, so the two loops keep their distinct side effects.
//
//   - isMaxTokens: resp.FinishReason == MaxTokens (skip — that has its own
//     continuation, so don't double-continue).
//   - toolsRun: tools run so far this request (executor: len(toolsUsed);
//     agent: a.ToolsUsedCount()). Read ONCE and reused for the progress compare
//     AND the next LastNudge — observationally identical to the old double-read
//     (no tool runs between) and free of its benign TOCTOU.
//   - actionMode: false ONLY for an interactive foreground turn the user is
//     DISCUSSING/analyzing (the discuss-mode gate, not yet confirmed). Then
//     pending todos are INTENTIONAL — the user wants to discuss before
//     implementing — so the nudge is SUPPRESSED (it would shove the model into
//     the very implementation the discuss-mode gate is holding back). Autonomous
//     paths (sub-agents, /loop) and the headless executor always pass true.
//   - ctx: when headless verification is refused or unavailable, omit only
//     clearly verification-only todos. Implementation todos still continue.
func DecideIncompleteWorkContinuation(ctx context.Context, registry ToolRegistry, isMaxTokens bool, toolsRun, lastNudge, stuck int, actionMode bool) IncompleteWorkDecision {
	if isMaxTokens {
		return IncompleteWorkDecision{Stuck: stuck, LastNudge: lastNudge}
	}
	// Discussion turn: don't nudge "act now" — unfinished todos are expected
	// (the user is analyzing, not asking to implement). Counters unchanged, same
	// shape as the max_tokens short-circuit.
	if !actionMode {
		return IncompleteWorkDecision{Stuck: stuck, LastNudge: lastNudge}
	}
	n, summary := incompleteTodoSummary(registry, HeadlessPermissionDenied(ctx) || HeadlessVerificationUnavailable(ctx))
	if n == 0 {
		return IncompleteWorkDecision{Stuck: stuck, LastNudge: lastNudge}
	}
	// A tool ran since the last nudge → progress; reset the stuck counter so a
	// model steadily completing todos is never capped.
	if toolsRun > lastNudge {
		stuck = 0
	}
	if stuck < MaxIncompleteWorkContinuations {
		stuck++
		return IncompleteWorkDecision{
			Continue: true, Count: n, Summary: summary,
			Stuck: stuck, LastNudge: toolsRun,
		}
	}
	// Budget spent on a model narrating without acting — finish; counters stay.
	return IncompleteWorkDecision{
		Exhausted: true, Count: n, Summary: summary,
		Stuck: stuck, LastNudge: lastNudge,
	}
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
