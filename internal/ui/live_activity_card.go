package ui

import (
	"fmt"
	"strings"
	"time"

	"gokin/internal/format"

	"github.com/charmbracelet/lipgloss"
)

// Visual design of the live activity indicator.
//
// Design target: the card adds what the status bar can't show — the specific
// action happening right now — and nothing else. Provider, model, state
// (WRITING / WORKING / IDLE), the context-usage bar, and the "esc Interrupt"
// key hint are all permanently visible in the bottom status bar. Repeating
// them here just steals vertical space.
//
// Current shape (big Live Activity panel closed):
//
//	▎ Writing: - `tui.go` — Model struct fields
//	▎ ↳ Editing tui_status_bar.go → applied (63ms)
//	▎ → Plan step 3/8
//
// The colored left-edge bar (▎) IS the state indicator — its hue matches
// the status bar's state chip, so state reads from colour, not repeated
// text. When the big Live Activity panel is open (Ctrl+O), the card
// collapses to just the current-action line; Recent/Next are already in
// the panel below.
//
// Earlier iterations had a bordered "chip" (● WRITING), repeated meta
// (kimi · kimi-for-coding), and an "Esc cancel" footer — every one of
// those duplicated something the status bar already shows. Removed.
func (m Model) shouldShowLiveActivityCard() bool {
	if m.isModalState() {
		return false
	}
	if m.state == StateProcessing || m.state == StateStreaming {
		return true
	}
	if len(m.backgroundTasks) > 0 {
		return true
	}
	return m.activityFeed != nil && m.activityFeed.HasActiveEntries()
}

// spinnerFramesCard is the braille-dot animation used as an inline leading
// indicator on the first line of the card when the agent is actively
// working. We pick the frame from a monotonic wall-clock source so the
// animation is consistent across every re-render within the same tick.
var spinnerFramesCard = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m Model) renderLiveActivityCard() string {
	if !m.shouldShowLiveActivityCard() {
		return ""
	}

	width := m.width - 2
	if width < 40 {
		width = 40
	}

	accent := m.liveActivityAccentColor()
	barStyle := lipgloss.NewStyle().Foreground(accent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	snapshot := ActivityFeedSnapshot{}
	if m.activityFeed != nil {
		snapshot = m.activityFeed.Snapshot(4, 2)
	}

	// When the big Live Activity panel (Ctrl+O) is open, collapse to just
	// the current-action line — Recent/Next live in the panel below.
	feedOpen := m.activityFeed != nil && m.activityFeed.IsVisible()

	// Line 1: the current action — no state badge, no meta. State comes
	// from the accent color on the left bar; state word + provider + model
	// are already permanent fixtures in the bottom status bar.
	current := m.liveActivityCurrentLine(snapshot)
	if current == "" {
		// Nothing specific to show and the card would otherwise be empty —
		// better to render nothing than a bare accent bar with no content.
		return ""
	}

	// Pick the leading glyph for the first line: animated braille spinner
	// while the agent is working (Processing/Streaming), static bar (▎)
	// otherwise. The spinner replaces — not complements — the bar, so the
	// user sees "⠋ Writing: …" on one line instead of a separate spinner
	// line below the card. Same colour scheme (state accent) either way.
	firstGlyph := "▎"
	if m.state == StateProcessing || m.state == StateStreaming {
		elapsed := time.Since(m.streamStartTime)
		frameIdx := int(elapsed.Milliseconds()/80) % len(spinnerFramesCard)
		firstGlyph = spinnerFramesCard[frameIdx]
	}

	// Prefix every line with a coloured leading glyph. First line uses the
	// animated spinner (when active); subsequent lines keep the static bar
	// so the animation doesn't become visually noisy.
	var out strings.Builder
	out.WriteString(barStyle.Render(firstGlyph) + " ")
	out.WriteString(valueStyle.Render(truncateRunes(current, width-2)))

	// Todo progress: when the agent is working through a plan (todo list),
	// surface the currently-active step (or next pending) so users see
	// "we're on step 3 of 7" instead of having to guess from tool calls.
	// The line lives below the current-action row because it's a durable
	// orientation signal, not a transient one.
	if todo := m.liveActivityTodoLine(); todo != "" {
		out.WriteByte('\n')
		out.WriteString(barStyle.Render("▎") + " " +
			valueStyle.Render(truncateRunes(todo, width-2)))
	}

	if !feedOpen {
		// Recent: one line of the most recent completed activity, prefixed
		// with "↳ " so it reads as a trailing consequence of the current
		// action rather than a competing primary signal.
		if recent := m.liveActivityRecentLine(snapshot); recent != "" {
			out.WriteByte('\n')
			out.WriteString(barStyle.Render("▎") + " " +
				dimStyle.Render("↳ ") +
				valueStyle.Render(truncateRunes(recent, width-4)))
		}
		// Next: only surface truly new signal (retries, rate limits,
		// plan progression, parallel work). Generic status echoes are
		// dropped inside liveActivityNextLine.
		if next := m.liveActivityNextLine(snapshot); next != "" {
			out.WriteByte('\n')
			out.WriteString(barStyle.Render("▎") + " " +
				dimStyle.Render("→ ") +
				valueStyle.Render(truncateRunes(next, width-4)))
		}
	}

	return out.String()
}

func (m Model) liveActivityAccentColor() lipgloss.Color {
	switch {
	case m.retryAttempt > 0 || (!m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil)):
		return ColorWarning
	case m.streamIdleMsg != "":
		return ColorInfo
	case m.currentTool != "":
		return GetToolIconColor(m.currentTool)
	case m.responseToolFailures > 0:
		return ColorWarning
	case m.state == StateStreaming:
		return ColorSuccess
	default:
		return ColorGradient2
	}
}

func (m Model) liveActivityCurrentLine(snapshot ActivityFeedSnapshot) string {
	if m.currentTool != "" {
		title := displayToolName(m.currentTool)
		if active := len(m.activeToolCalls); active > 1 {
			title = fmt.Sprintf("%s · %d in flight", title, active)
		}
		if m.currentToolInfo != "" {
			title += " — " + m.currentToolInfo
		}
		if !m.toolStartTime.IsZero() {
			title += " · " + format.Duration(time.Since(m.toolStartTime))
		}
		return title
	}

	if m.state == StateStreaming {
		// Clean the snippet: strip leading bullets/dashes/stars from the
		// model's markdown so "Writing: - `tui.go`" doesn't read as a
		// double-dash. Cleaning may reduce a pure-bullet snippet to "" — in
		// that case we fall through to the generic "Writing response" label
		// rather than render "Writing: " with a dangling space.
		cleaned := ""
		if raw := m.lastStreamSnippet(); raw != "" {
			cleaned = cleanStreamSnippet(raw)
		}
		switch {
		case cleaned != "":
			return "Writing: " + cleaned
		case m.responseToolCount > 0:
			return "Writing response · " + formatToolRunSummary(m.responseToolCount, m.responseToolFailures, true)
		default:
			return "Writing response"
		}
	}

	if label := strings.TrimSpace(m.processingLabel); label != "" {
		// Append elapsed wall-clock once we're past the "barely started"
		// threshold. Users ask "has it really been thinking for a while?" —
		// this answers it without waiting for a tool call to land. Matches
		// the behaviour the old spinner-status block provided before the
		// card took over rendering.
		if elapsed := time.Since(m.streamStartTime); elapsed >= 3*time.Second {
			label += " · " + format.Duration(elapsed)
		}
		return label
	}

	if len(m.backgroundTasks) > 0 {
		return m.formatBackgroundTaskStatus(len(m.backgroundTasks))
	}

	if snapshot.RunningAgents > 0 || snapshot.RunningTools > 0 {
		return fmt.Sprintf("%d agents · %d tools", snapshot.RunningAgents, snapshot.RunningTools)
	}

	return ""
}

// liveActivityTodoLine surfaces the agent's current plan position as a
// single row in the live activity card — the user can see "which step"
// without opening the full todo panel (Ctrl+T). Returns "" when no todos
// exist or when none are actively or pending (all done).
//
// Priority picks, in order:
//  1. An in-progress step marked `[/]` (the agent just started this).
//  2. The next pending step marked `[ ]`.
//
// Format: "☐ Step 3/7: Refactor rate limit display" — position is the
// 1-based index of the chosen step among *all* items, not only pending.
// That way "Step 3/7" stays anchored to the plan even as earlier steps
// complete.
func (m Model) liveActivityTodoLine() string {
	if len(m.todoItems) == 0 {
		return ""
	}

	parseTodo := func(raw string) (marker, text string) {
		s := strings.TrimSpace(raw)
		if strings.HasPrefix(s, "- ") {
			s = strings.TrimSpace(s[2:])
		}
		if len(s) >= 3 && s[0] == '[' && s[2] == ']' {
			return string(s[1]), strings.TrimSpace(s[3:])
		}
		return "", s
	}

	total := len(m.todoItems)
	pickIdx := -1
	pickMarker := ""
	pickText := ""

	// First pass: in-progress. Second pass: next pending. Done items
	// ([x]) and empty lines skipped.
	for i, raw := range m.todoItems {
		marker, text := parseTodo(raw)
		if marker == "/" {
			pickIdx = i
			pickMarker = marker
			pickText = text
			break
		}
	}
	if pickIdx < 0 {
		for i, raw := range m.todoItems {
			marker, text := parseTodo(raw)
			if marker == " " && text != "" {
				pickIdx = i
				pickMarker = marker
				pickText = text
				break
			}
		}
	}
	if pickIdx < 0 {
		return ""
	}

	glyph := "☐"
	switch pickMarker {
	case "/":
		glyph = "◐" // partial / in-progress
	case " ":
		glyph = "☐" // empty box / pending
	}

	return fmt.Sprintf("%s Step %d/%d: %s", glyph, pickIdx+1, total, pickText)
}

// liveActivityRecentLine returns a single line of the most recent log entry,
// or "" when there's nothing worth echoing back. The previous version
// fell through to a bag of fallbacks (response tool count, agent tool list
// joined by arrows) that duplicated information already in the header, so
// we only surface genuinely new signal now.
func (m Model) liveActivityRecentLine(snapshot ActivityFeedSnapshot) string {
	if len(snapshot.RecentLog) > 0 {
		return snapshot.RecentLog[len(snapshot.RecentLog)-1]
	}
	return ""
}

// liveActivityNextLine is only rendered when it tells the user something
// they can't infer from the header. Generic "watching for next tool call"
// dropped — it was noise.
func (m Model) liveActivityNextLine(snapshot ActivityFeedSnapshot) string {
	switch {
	case !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil):
		return fmt.Sprintf("Auto-retry in %s", format.Duration(time.Until(m.rateLimitWaitUntil)))
	case m.retryAttempt > 0 && m.retryMax > 0:
		return fmt.Sprintf("Provider recovery %d/%d", m.retryAttempt, m.retryMax)
	case m.streamIdleMsg != "":
		return m.streamIdleMsg
	}

	if m.planProgressMode && m.planProgress != nil {
		title := strings.TrimSpace(m.planProgress.CurrentTitle)
		if title != "" {
			return title
		}
		return fmt.Sprintf("Plan step %d/%d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
	}

	if snapshot.RunningTools > 1 {
		return fmt.Sprintf("%d tools running in parallel", snapshot.RunningTools)
	}
	if snapshot.RunningAgents > 0 {
		return fmt.Sprintf("%d background agents working", snapshot.RunningAgents)
	}
	return ""
}

// cleanStreamSnippet removes leading markdown list markers (-, *, •, numbered
// bullets) and whitespace from a streaming snippet so the "Writing: X"
// header prefix doesn't collide with the snippet's own leading punctuation.
// Inline formatting (backticks, inline code) is preserved — that's real
// content signal.
//
// Returns "" for pure-marker inputs (the model emitted just "- " with no
// content yet) so the caller can fall back to a generic label instead of
// rendering "Writing: -" or "Writing: " with a dangling suffix.
func cleanStreamSnippet(snippet string) string {
	s := strings.TrimSpace(snippet)
	// A bare bullet character with no trailing content — drop. Upstream
	// lastStreamSnippet TrimSpace'd away the space after the marker, so
	// we see "-" / "*" / "•" rather than "- ".
	switch s {
	case "", "-", "*", "•":
		return ""
	}
	// Strip one leading bullet marker if present.
	switch {
	case strings.HasPrefix(s, "- "):
		s = s[2:]
	case strings.HasPrefix(s, "* "):
		s = s[2:]
	case strings.HasPrefix(s, "• "):
		s = strings.TrimSpace(s[len("• "):])
	}
	// Strip a leading numbered bullet "1. ", "12. " etc.
	if idx := strings.Index(s, ". "); idx > 0 && idx <= 3 {
		if allDigits(s[:idx]) {
			s = s[idx+2:]
		}
	}
	return strings.TrimSpace(s)
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func truncateRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes == 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}
