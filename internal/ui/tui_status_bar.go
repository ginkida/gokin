package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// statusBarProjectPath returns the project directory for the status bar,
// with the home prefix collapsed to "~". Returns "" when the path is empty
// (nothing to show) so callers can skip the segment entirely.
//
// We use a 36-rune soft cap: long enough for ~/src/very/nested/mono-repo
// to fit unabbreviated, short enough that a deep nested path (e.g.
// ~/work/clients/acme/infra/services/api/internal/handlers) gets middle-
// truncated instead of shoving everything else off the right side.
func statusBarProjectPath(workDir string) string {
	pretty := prettyPath(workDir)
	if pretty == "" || pretty == "." {
		return ""
	}
	return shortenPath(pretty, 36)
}

// getStatusBarLayout determines the appropriate layout based on terminal width.
func (m Model) getStatusBarLayout() StatusBarLayout {
	switch {
	case m.width >= 120:
		return StatusBarLayoutFull
	case m.width >= 80:
		return StatusBarLayoutMedium
	case m.width >= 60:
		return StatusBarLayoutCompact
	default:
		return StatusBarLayoutMinimal
	}
}

// safePadding calculates padding ensuring it's never negative.
func safePadding(available, left, right int) int {
	padding := available - left - right
	if padding < 1 {
		return 1
	}
	return padding
}

// renderStatusBar renders the enhanced status bar with adaptive layout.
func (m Model) renderStatusBar() string {
	layout := m.getStatusBarLayout()

	switch layout {
	case StatusBarLayoutMinimal:
		return m.renderStatusBarMinimal()
	case StatusBarLayoutCompact:
		return m.renderStatusBarCompact()
	case StatusBarLayoutMedium:
		return m.renderStatusBarMedium()
	default:
		return m.renderStatusBarFull()
	}
}

// renderStatusBarMinimal renders a minimal status bar for very narrow terminals (< 60 chars).
// Shows compact core reliability fields.
func (m Model) renderStatusBarMinimal() string {
	parts := m.minimalStatusSegments()
	return strings.Join(parts, " ")
}

// renderStatusBarCompact renders a compact status bar for narrow terminals (60-79 chars).
// Shows all mandatory fields in short form.
func (m Model) renderStatusBarCompact() string {
	left := joinStatusSegments(m.compactStatusSegments())
	var requiredRight []string
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		requiredRight = append(requiredRight, scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent())))
	}

	return renderFittedStatusLine(m.width, left, m.statusBarHintSegments(true), requiredRight)
}

func (m Model) compactStatusSegments() []string {
	var parts []string
	parts = append(parts, m.safetyModeSegments(false)...)
	if m.queuedPending > 0 {
		parts = append(parts, fmt.Sprintf("📥%d", m.queuedPending))
	}
	if strings.HasPrefix(strings.ToLower(m.runtimeStatus.Mode), "degraded") {
		parts = append(parts, "mode:degraded")
	}

	if m.shouldShowBreaker() {
		parts = append(parts, "breaker:"+shortBreakerState(m.runtimeStatus.RequestBreaker)+"/"+shortBreakerState(m.runtimeStatus.StepBreaker))
	}

	provider := m.runtimeStatus.Provider
	if provider == "" {
		provider = "ready"
	}
	if runes := []rune(provider); len(runes) > 12 {
		provider = string(runes[:12])
	}
	parts = append(parts, provider)

	tokens, maxTokens := m.getTokenCounts()
	if m.showTokens && maxTokens > 0 {
		pct := m.getContextPercent()
		color := contextUrgencyColor(pct)
		hint := contextUrgencyHint(pct)
		label := formatAbsoluteTokens(tokens, maxTokens, m.getOutputTokens())
		if label == "" {
			label = fmt.Sprintf("ctx:%.0f%%", pct*100)
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(color).Render(label+hint))
	} else {
		parts = append(parts, "ctx:n/a")
	}

	return parts
}

func (m Model) minimalStatusSegments() []string {
	var parts []string
	parts = append(parts, m.safetyModeSegments(true)...)
	if strings.HasPrefix(strings.ToLower(m.runtimeStatus.Mode), "degraded") {
		parts = append(parts, "mode:degraded")
	}

	if m.shouldShowBreaker() {
		parts = append(parts, "breaker:"+shortBreakerState(m.runtimeStatus.RequestBreaker)+"/"+shortBreakerState(m.runtimeStatus.StepBreaker))
	}

	provider := m.runtimeStatus.Provider
	if provider == "" {
		provider = "ready"
	}
	if runes := []rune(provider); len(runes) > 10 {
		provider = string(runes[:10])
	}
	parts = append(parts, provider)

	_, maxTokens := m.getTokenCounts()
	if m.showTokens && maxTokens > 0 {
		pct := m.getContextPercent()
		color := contextUrgencyColor(pct)
		parts = append(parts, lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("ctx:%.0f%%", pct*100)))
	}

	return parts
}

func (m Model) safetyModeSegments(minimal bool) []string {
	var parts []string
	if !m.permissionsEnabled {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("YOLO"))
	}
	if !m.sandboxEnabled {
		label := "!SANDBOX"
		if minimal {
			label = "!SBX"
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render(label))
	}
	return parts
}

// renderStatusBarMedium renders a medium status bar for standard terminals (80-119 chars).
// Shows mandatory fields plus warnings.
func (m Model) renderStatusBarMedium() string {
	left := joinStatusSegments(m.baseStatusSegments(true))

	// Right side: scroll indicator and urgent runtime health. Session cost is
	// deliberately kept out of chrome to avoid low-value visual noise.
	var requiredRight []string
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		requiredRight = append(requiredRight, scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent())))
	}

	return renderFittedStatusLine(m.width, left, m.statusBarHintSegments(false), requiredRight)
}

// renderStatusBarFull renders the full status bar for wide terminals (>= 120 chars).
// Shows full Status Bar 2.0 with all reliability and runtime details.
func (m Model) renderStatusBarFull() string {
	leftParts := m.baseStatusSegments(true)
	if m.currentModel != "" {
		leftParts = append(leftParts, lipgloss.NewStyle().Foreground(ColorDim).Render(shortenModelName(m.currentModel)))
	}

	var requiredRight []string

	// Retry indicator (important — shows active retries)
	if m.retryAttempt > 0 && m.retryMax > 0 {
		retryStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		requiredRight = append(requiredRight, retryStyle.Render(fmt.Sprintf("↻ %d/%d", m.retryAttempt, m.retryMax)))
	} else if !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil) {
		wait := time.Until(m.rateLimitWaitUntil).Round(time.Second)
		waitStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		requiredRight = append(requiredRight, waitStyle.Render(fmt.Sprintf("⏳ Rate Limit %s", wait)))
	}

	// Scroll indicator
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		requiredRight = append(requiredRight, scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent())))
	}

	// MCP health (only when unhealthy)
	if m.mcpTotal > 0 && m.mcpHealthy < m.mcpTotal {
		mcpColor := ColorWarning
		if m.mcpHealthy == 0 {
			mcpColor = ColorError
		}
		requiredRight = append(requiredRight, lipgloss.NewStyle().Foreground(mcpColor).Render(fmt.Sprintf("MCP %d/%d", m.mcpHealthy, m.mcpTotal)))
	}

	left := joinStatusSegments(leftParts)
	return renderFittedStatusLine(m.width, left, m.statusBarHintSegments(false), requiredRight)
}

// statusSeparator returns the muted "│" used to delimit status-bar segments.
//
// Style is computed on every call so the separator follows ApplyTheme — do
// not cache as a package-level var.
func statusSeparator() string {
	return lipgloss.NewStyle().Foreground(ColorDim).Render(" │ ")
}

// joinStatusSegments joins non-empty segments with statusSeparator(). Empty
// strings are skipped so an unset cell doesn't produce a "foo │  │ bar" gap.
func joinStatusSegments(segments []string) string {
	var nonEmpty []string
	for _, s := range segments {
		if s == "" {
			continue
		}
		nonEmpty = append(nonEmpty, s)
	}
	if len(nonEmpty) == 0 {
		return ""
	}
	return strings.Join(nonEmpty, statusSeparator())
}

func renderFittedStatusLine(width int, left string, optionalRight, requiredRight []string) string {
	optional := append([]string(nil), optionalRight...)
	for {
		rightParts := append([]string(nil), optional...)
		rightParts = append(rightParts, requiredRight...)
		right := joinStatusSegments(rightParts)
		if width <= 0 || right == "" || lipgloss.Width(left)+lipgloss.Width(right)+1 <= width || len(optional) == 0 {
			padding := safePadding(width, lipgloss.Width(left), lipgloss.Width(right))
			return left + strings.Repeat(" ", padding) + right
		}
		optional = optional[:len(optional)-1]
	}
}

type shortcutHint struct {
	key  string
	desc string
}

func (m Model) statusBarHintSegments(compact bool) []string {
	style := lipgloss.NewStyle().Foreground(ColorDim)
	var parts []string

	if m.state == StateInput {
		if len(m.todoItems) > 0 && !m.todosVisible {
			if compact {
				parts = append(parts, style.Render(fmt.Sprintf("C-t tasks %d", len(m.todoItems))))
			} else {
				parts = append(parts, style.Render(fmt.Sprintf("Ctrl+T tasks %d", len(m.todoItems))))
			}
		}
		if m.activityFeed != nil && !m.activityFeed.IsVisible() && m.activityFeed.HasActiveEntries() {
			if compact {
				parts = append(parts, style.Render("C-o activity"))
			} else {
				parts = append(parts, style.Render("Ctrl+O activity"))
			}
		}
	}

	for _, hint := range m.contextualShortcutHintPairs() {
		key := hint.key
		if compact {
			key = compactKeyLabel(key)
		}
		parts = append(parts, style.Render(key+" "+hint.desc))
	}

	return parts
}

func compactKeyLabel(key string) string {
	switch key {
	case "Ctrl+T":
		return "C-t"
	case "Ctrl+O":
		return "C-o"
	default:
		return key
	}
}

func (m Model) baseStatusSegments(withContextBar bool) []string {
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	degradedStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	providerStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	heartbeatStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	var parts []string
	// Project path is always useful as the first anchor — you instantly
	// see which repo this session is bound to without scrolling up to the
	// welcome banner. Kept dim so it doesn't fight for attention with the
	// live status cells. Always ~/-collapsed when inside $HOME; shortened
	// only when long enough to eat the bar (>36 runes ≈ "~/a/very/long/path").
	if dir := statusBarProjectPath(m.workDir); dir != "" {
		parts = append(parts, dimStyle.Render(dir))
	}
	if !m.permissionsEnabled {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("YOLO"))
	}
	if !m.sandboxEnabled {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render("!SANDBOX"))
	}
	if m.queuedPending > 0 {
		parts = append(parts, providerStyle.Render(fmt.Sprintf("📥 %d queued", m.queuedPending)))
	}

	mode := "normal"
	if m.runtimeStatus.Mode != "" {
		mode = m.runtimeStatus.Mode
	}
	if strings.HasPrefix(mode, "degraded") {
		modeText := m.degradedModeLabel(withContextBar)
		parts = append(parts, degradedStyle.Render(modeText))
	}

	if m.hasActivePlanStatus() {
		planStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
		activeStep := fmt.Sprintf("%s %d/%d", MessageIcons["info"], m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
		if m.planProgress.CurrentTitle != "" {
			title := m.planProgress.CurrentTitle
			if runes := []rune(title); len(runes) > 20 {
				title = string(runes[:17]) + "..."
			}
			activeStep += " " + title
		}
		parts = append(parts, planStyle.Render(activeStep))
	}
	if m.shouldShowBreaker() {
		reqBreaker := shortBreakerState(m.runtimeStatus.RequestBreaker)
		stepBreaker := shortBreakerState(m.runtimeStatus.StepBreaker)
		if reqBreaker == "open" || stepBreaker == "open" {
			parts = append(parts, lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render(MessageIcons["error"]+" circuit open"))
		} else if reqBreaker == "half" || stepBreaker == "half" {
			parts = append(parts, lipgloss.NewStyle().Foreground(ColorWarning).Render(MessageIcons["warning"]+" recovering"))
		}
	}

	provider := m.runtimeStatus.Provider
	if provider == "" {
		provider = "unknown"
	}
	parts = append(parts, providerStyle.Render(provider))

	tokenText := m.formatTokenStatus(withContextBar)
	if tokenText != "" {
		parts = append(parts, tokenText)
	}

	// Plan mode indicator
	if m.planningModeEnabled {
		planStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
		parts = append(parts, planStyle.Render(MessageIcons["info"]+" plan mode"))
	}

	// Background tasks
	if bgCount := len(m.backgroundTasks); bgCount > 0 {
		parts = append(parts, dimStyle.Render(m.formatBackgroundTaskStatus(bgCount)))
	}

	if m.shouldShowHeartbeat() {
		hb := "heartbeat:waiting"
		if m.runtimeStatus.HasHeartbeat {
			hb = "heartbeat:" + m.runtimeStatus.HeartbeatAge.Round(time.Second).String()
		}
		parts = append(parts, heartbeatStyle.Render(hb))
	}

	// Live Engine Status
	engineStatus := m.renderEngineStatus()
	if engineStatus != "" {
		parts = append(parts, engineStatus)
	}

	return parts
}

// renderEngineStatus returns a compact status indicator for the AI engine.
func (m Model) renderEngineStatus() string {
	engineStyle := lipgloss.NewStyle().Bold(true)

	var status string
	color := ColorMuted
	icon := MessageIcons["active"]

	switch {
	case !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil):
		// Bare "RATE LIMIT" was uninformative — users asked "of what?
		// for how long?" The provider slot already shows which backend
		// is active; add a countdown so the user sees when it'll resume.
		// time.Duration.String() renders like "42s" / "1m35s" — short
		// enough for the status-bar cell, long enough to answer "how
		// much longer?". Falls back to the bare label if the deadline
		// has already passed (shouldn't happen — the switch guard
		// filters it out — but we avoid "resumes in -3s" just in case).
		wait := time.Until(m.rateLimitWaitUntil).Round(time.Second)
		if wait > 0 {
			status = fmt.Sprintf("RATE LIMIT · resumes in %s", wait)
		} else {
			status = "RATE LIMIT"
		}
		color = ColorWarning
		icon = MessageIcons["warning"]
	case m.retryAttempt > 0 && m.retryMax > 0:
		status = fmt.Sprintf("RETRY %d/%d", m.retryAttempt, m.retryMax)
		color = ColorWarning
		icon = MessageIcons["warning"]
	case m.state == StateQuestionPrompt || m.state == StatePermissionPrompt || m.state == StatePlanApproval:
		status = "WAITING"
		color = ColorWarning
	case m.currentTool != "":
		status = "RUN " + statusToolLabel(m.currentTool)
		if active := len(m.activeToolCalls); active > 1 {
			status += fmt.Sprintf(" ×%d", active)
		}
		color = GetToolIconColor(m.currentTool)
	case m.state == StateStreaming:
		status = "WRITING"
		if m.responseToolCount > 0 {
			status += statusSeparator() + formatToolRunSummary(m.responseToolCount, m.responseToolFailures, false)
		}
		if m.responseToolFailures > 0 {
			color = ColorWarning
			icon = MessageIcons["warning"]
		} else {
			color = ColorSuccess
			icon = MessageIcons["pending"]
		}
	case m.planProgressMode && m.planProgress != nil && m.planProgress.TotalSteps > 0:
		status = fmt.Sprintf("PLAN %d/%d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
		color = ColorInfo
	case m.state == StateProcessing:
		status = processingStatusLabel(m.processingLabel)
		if status == "" {
			status = "THINKING"
		}
		color = ColorSecondary
	default:
		status = "IDLE"
	}

	return engineStyle.Foreground(color).Render(icon + " " + status)
}

func processingStatusLabel(label string) string {
	lower := strings.ToLower(strings.TrimSpace(label))
	switch {
	case lower == "":
		return ""
	case strings.Contains(lower, "quality gate"):
		return "VERIFY"
	case strings.Contains(lower, "self-review") || strings.Contains(lower, "review"):
		return "REVIEW"
	case strings.Contains(lower, "auto-fix") || strings.Contains(lower, "autofix"):
		return "AUTOFIX"
	case strings.Contains(lower, "agent"):
		return "AGENT LOOP"
	default:
		return "THINKING"
	}
}

func (m Model) hasActivePlanStatus() bool {
	if m.planProgress == nil {
		return false
	}
	if m.planProgress.TotalSteps <= 0 {
		return false
	}
	switch m.planProgress.Status {
	case "in_progress", "paused":
		return true
	default:
		return m.planProgress.Completed < m.planProgress.TotalSteps
	}
}

func (m Model) shouldShowBreaker() bool {
	req := strings.ToLower(m.runtimeStatus.RequestBreaker)
	step := strings.ToLower(m.runtimeStatus.StepBreaker)
	isClosedOrEmpty := func(s string) bool {
		return s == "" || s == "closed" || s == "n/a"
	}
	return !isClosedOrEmpty(req) || !isClosedOrEmpty(step)
}

func (m Model) shouldShowHeartbeat() bool {
	return strings.HasPrefix(strings.ToLower(m.runtimeStatus.Mode), "degraded")
}

func shortBreakerState(state string) string {
	switch strings.ToLower(state) {
	case "open":
		return "open"
	case "half_open":
		return "half"
	case "closed":
		return "closed"
	default:
		if state == "" {
			return "n/a"
		}
		return state
	}
}

func (m Model) formatTokenStatus(withContextBar bool) string {
	tokens, maxTokens := m.getTokenCounts()
	if !m.showTokens || maxTokens <= 0 {
		return ""
	}
	pct := m.getContextPercent()
	if withContextBar {
		barWidth := 8
		if m.width >= 120 {
			barWidth = 16
		} else if m.width >= 80 {
			barWidth = 12
		}
		return renderContextBar(pct, barWidth, tokens, maxTokens, m.getOutputTokens())
	}
	color := contextUrgencyColor(pct)
	if label := formatAbsoluteTokens(tokens, maxTokens, m.getOutputTokens()); label != "" {
		return lipgloss.NewStyle().Foreground(color).Render(label)
	}
	return lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("tok:%.0f%%", pct*100))
}

// getTokenCounts returns the current and max token counts if available.
func (m Model) getTokenCounts() (tokens int, maxTokens int) {
	if m.tokenUsage != nil {
		return m.tokenUsage.Tokens, m.tokenUsage.MaxTokens
	}
	return 0, 0
}

// getOutputTokens returns the estimated output tokens for the current response.
func (m Model) getOutputTokens() int {
	if m.tokenUsage != nil {
		return m.tokenUsage.OutputTokens
	}
	return 0
}

// formatAbsoluteTokens returns "45.0K/128.0K" if absolute counts are available,
// optionally appending "+1.2K" for output tokens.
func formatAbsoluteTokens(tokens, maxTokens, outputTokens int) string {
	if tokens >= 0 && maxTokens > 0 {
		label := fmt.Sprintf("%s/%s", formatTokens(tokens), formatTokens(maxTokens))
		if outputTokens > 0 {
			label += fmt.Sprintf(" +%s", formatTokens(outputTokens))
		}
		return label
	}
	return ""
}

// contextUrgencyColor returns a color based on context usage percentage.
// pct is a fraction in the range 0.0–1.0.
//
// Three-tier ramp (healthy → elevated → critical). The previous 4-tier ramp
// had a hard-coded #F97316 "orange" between amber and red that didn't fit
// the locked Graphite + violet palette; collapsing 80–95% into the amber
// tier keeps the signal honest without introducing an off-palette colour.
func contextUrgencyColor(pct float64) lipgloss.Color {
	switch {
	case pct > 0.95:
		return ColorError // Coral — critical
	case pct > 0.60:
		return ColorWarning // Amber — elevated
	default:
		return ColorSuccess // Green — healthy
	}
}

// contextUrgencyHint returns a short hint when context is getting full.
// pct is a fraction in the range 0.0–1.0.
func contextUrgencyHint(pct float64) string {
	switch {
	case pct > 0.95:
		return " /compact now"
	case pct > 0.80:
		return " compact soon"
	default:
		return ""
	}
}

// maxBarDivisor protects contextUrgencyColor math from a division-by-zero
// when maxTokens is 0 (e.g. before the first TokenUsageMsg lands). Returns
// 1 in that case so the downstream ratio is simply bounded to a small
// number and the colour calculation stays deterministic.
func maxBarDivisor(maxTokens int) int {
	if maxTokens <= 0 {
		return 1
	}
	return maxTokens
}

// renderContextBar returns a visual progress bar for context usage.
// pct is a fraction in the range 0.0–1.0 representing the already-confirmed
// input context. outputTokens is the live-streaming estimate of tokens
// generated by the current response; it's shown as a lighter extension
// of the bar so users can see the projected context after this turn lands.
// When absolute token counts are available, displays them alongside the bar.
func renderContextBar(pct float64, barWidth int, tokens int, maxTokens int, outputTokens int) string {
	if pct <= 0 && maxTokens <= 0 {
		return ""
	}
	// Defensive: this renders every frame and its no-panic invariant otherwise
	// lives entirely in the caller (barWidth ∈ {8,12,16}, pct ≥ 0). A negative
	// barWidth or pct would drive a strings.Repeat count negative → panic.
	if barWidth <= 0 {
		return ""
	}

	filled := int(pct * float64(barWidth))
	if filled < 0 {
		filled = 0
	}
	if filled > barWidth {
		filled = barWidth
	}
	if filled == 0 && pct > 0 {
		filled = 1
	}

	// Projected fill: how much additional bar width the pending output
	// tokens would consume once they land as input on the next turn. We
	// render this as a second, dimmer band so the user sees the bar
	// "extending" in real time during streaming — instead of the bar
	// standing still while the "+259" text ticks up next to it.
	projected := 0
	if maxTokens > 0 && outputTokens > 0 {
		projectedTotal := int(float64(tokens+outputTokens) / float64(maxTokens) * float64(barWidth))
		if projectedTotal > barWidth {
			projectedTotal = barWidth
		}
		if projectedTotal > filled {
			projected = projectedTotal - filled
		}
	}

	barColor := contextUrgencyColor(pct)
	projectedColor := contextUrgencyColor(
		float64(tokens+outputTokens) / float64(maxBarDivisor(maxTokens)),
	)

	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	projectedStyle := lipgloss.NewStyle().Foreground(projectedColor).Faint(true)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorDim)
	pctStyle := lipgloss.NewStyle().Foreground(barColor)

	bar := filledStyle.Render(strings.Repeat("█", filled)) +
		projectedStyle.Render(strings.Repeat("▓", projected)) +
		emptyStyle.Render(strings.Repeat("░", barWidth-filled-projected))

	label := formatAbsoluteTokens(tokens, maxTokens, outputTokens)
	if label == "" {
		displayPct := pct * 100
		switch {
		case displayPct < 1:
			label = "<1%"
		case displayPct < 10:
			label = fmt.Sprintf("%.1f%%", displayPct)
		default:
			label = fmt.Sprintf("%.0f%%", displayPct)
		}
	}

	hint := contextUrgencyHint(pct)
	if hint != "" {
		return bar + " " + pctStyle.Render(label) + " " + lipgloss.NewStyle().Foreground(barColor).Bold(true).Render(hint)
	}
	return bar + " " + pctStyle.Render(label)
}

// getContextPercent returns the context usage percentage from available sources.
// All callers should expect a 0.0–1.0 fraction.
func (m Model) getContextPercent() float64 {
	if m.showTokens && m.tokenUsage != nil {
		return m.tokenUsage.PercentUsed
	}
	return 0
}

// formatBackgroundTaskStatus returns a compact status string for background tasks.
// Shows current action if a task has progress info, otherwise falls back to "N bg".
func (m Model) formatBackgroundTaskStatus(bgCount int) string {
	var bestAction string
	for _, t := range m.backgroundTasks {
		if t.CurrentAction != "" {
			bestAction = t.CurrentAction
			break
		}
	}
	if bestAction != "" {
		if runes := []rune(bestAction); len(runes) > 25 {
			bestAction = string(runes[:22]) + "..."
		}
		return bestAction
	}
	return fmt.Sprintf("%d bg", bgCount)
}

// degradedModeLabel returns a specific label for degraded mode based on runtime state.
func (m Model) degradedModeLabel(withDetail bool) string {
	rs := m.runtimeStatus

	// Determine the specific reason
	reqOpen := strings.EqualFold(rs.RequestBreaker, "open")
	stepOpen := strings.EqualFold(rs.StepBreaker, "open")
	reqHalf := strings.EqualFold(rs.RequestBreaker, "half_open")

	var reason string
	switch {
	case reqOpen && stepOpen:
		reason = "breakers-open"
	case reqOpen:
		reason = "req-breaker-open"
	case stepOpen:
		reason = "step-breaker-open"
	case reqHalf:
		reason = "recovering"
	case rs.ConsecutiveFailure > 0:
		reason = fmt.Sprintf("failures:%d", rs.ConsecutiveFailure)
	default:
		reason = "degraded"
	}

	if withDetail && rs.DegradedRemaining > 0 {
		return fmt.Sprintf("mode:%s(retry %s)", reason, rs.DegradedRemaining.Round(time.Second))
	}
	return "mode:" + reason
}

func (m Model) contextualShortcutHintPairs() []shortcutHint {
	switch m.state {
	case StatePermissionPrompt:
		return []shortcutHint{{"y", "Allow"}, {"a", "Always"}, {"n", "Deny"}, {"esc", "Cancel"}}
	case StatePlanApproval:
		return []shortcutHint{{"y", "Approve"}, {"n", "Reject"}, {"m", "Modify"}, {"↑↓", "Navigate"}}
	case StateDiffPreview:
		return []shortcutHint{{"Enter", "Accept"}, {"e", "Edit"}, {"n", "Reject"}, {"q", "Close"}}
	case StateMultiDiffPreview:
		return []shortcutHint{{"y", "Apply all"}, {"n", "Reject all"}, {"Tab", "Switch pane"}, {"↑↓", "Browse"}}
	case StateQuestionPrompt:
		return []shortcutHint{{"↑↓", "Navigate"}, {"Enter", "Confirm"}, {"esc", "Cancel"}}
	case StateModelSelector:
		return []shortcutHint{{"↑↓", "Navigate"}, {"Enter", "Select"}, {"esc", "Cancel"}}
	case StateProcessing, StateStreaming:
		return []shortcutHint{{"Esc", "interrupt"}}
	default:
		return nil
	}
}

// shortenModelName returns a shortened model name for status-bar display.
// Drops the noisy "-preview" / "-latest" version suffixes. The
// `"gemini-"` stripper that lived here pre-v0.65 was removed when the
// Gemini provider was deleted — no current model ID carries that
// prefix.
func shortenModelName(name string) string {
	name = strings.ReplaceAll(name, "-preview", "")
	name = strings.ReplaceAll(name, "-latest", "")
	return name
}
