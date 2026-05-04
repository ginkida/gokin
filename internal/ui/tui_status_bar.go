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
	left := strings.Join(m.compactStatusSegments(), " · ")
	right := ""
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		right = scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent()))
	}

	padding := safePadding(m.width, lipgloss.Width(left), lipgloss.Width(right))
	return left + strings.Repeat(" ", padding) + right
}

func (m Model) compactStatusSegments() []string {
	var parts []string
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

	if pct := m.getContextPercent(); pct > 0 {
		color := contextUrgencyColor(pct)
		hint := contextUrgencyHint(pct)
		tokens, maxTokens := m.getTokenCounts()
		label := formatAbsoluteTokens(tokens, maxTokens, m.getOutputTokens())
		if label == "" {
			label = fmt.Sprintf("ctx:%.0f%%", pct)
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(color).Render(label+hint))
	} else {
		parts = append(parts, "ctx:n/a")
	}

	return parts
}

func (m Model) minimalStatusSegments() []string {
	var parts []string
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

	if pct := m.getContextPercent(); pct > 0 {
		color := contextUrgencyColor(pct)
		parts = append(parts, lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("ctx:%.0f%%", pct)))
	}

	return parts
}

// renderStatusBarMedium renders a medium status bar for standard terminals (80-119 chars).
// Shows mandatory fields plus warnings.
func (m Model) renderStatusBarMedium() string {
	left := strings.Join(m.baseStatusSegments(true), " · ")

	// Right side: cost + scroll indicator
	var rightParts []string
	if m.sessionCost > 0 {
		costStr := fmt.Sprintf("$%.2f", m.sessionCost)
		if m.sessionCost < 0.01 {
			costStr = fmt.Sprintf("$%.4f", m.sessionCost)
		}
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(ColorDim).Render(costStr))
	}
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		rightParts = append(rightParts, scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent())))
	}
	right := strings.Join(rightParts, " · ")

	padding := safePadding(m.width, lipgloss.Width(left), lipgloss.Width(right))
	return left + strings.Repeat(" ", padding) + right
}

// renderStatusBarFull renders the full status bar for wide terminals (>= 120 chars).
// Shows full Status Bar 2.0 with all reliability and runtime details.
func (m Model) renderStatusBarFull() string {
	leftParts := m.baseStatusSegments(true)
	if m.currentModel != "" {
		leftParts = append(leftParts, lipgloss.NewStyle().Foreground(ColorDim).Render(shortenModelName(m.currentModel)))
	}

	var rightParts []string

	// Retry indicator (important — shows active retries)
	if m.retryAttempt > 0 && m.retryMax > 0 {
		retryStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		rightParts = append(rightParts, retryStyle.Render(fmt.Sprintf("↻ %d/%d", m.retryAttempt, m.retryMax)))
	} else if !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil) {
		wait := time.Until(m.rateLimitWaitUntil).Round(time.Second)
		waitStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		rightParts = append(rightParts, waitStyle.Render(fmt.Sprintf("⏳ Rate Limit %s", wait)))
	}

	// Scroll indicator
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		rightParts = append(rightParts, scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent())))
	}

	// MCP health (only when unhealthy)
	if m.mcpTotal > 0 && m.mcpHealthy < m.mcpTotal {
		mcpColor := ColorWarning
		if m.mcpHealthy == 0 {
			mcpColor = ColorError
		}
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(mcpColor).Render(fmt.Sprintf("MCP %d/%d", m.mcpHealthy, m.mcpTotal)))
	}

	// Session cost in status bar
	if m.sessionCost > 0 {
		costStr := fmt.Sprintf("$%.2f", m.sessionCost)
		if m.sessionCost < 0.01 {
			costStr = fmt.Sprintf("$%.4f", m.sessionCost)
		}
		rightParts = append(rightParts, lipgloss.NewStyle().Foreground(ColorDim).Render(costStr))
	}

	left := strings.Join(leftParts, " · ")
	right := strings.Join(rightParts, " · ")

	padding := safePadding(m.width, lipgloss.Width(left), lipgloss.Width(right))
	return left + strings.Repeat(" ", padding) + right
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
		status = "RUN " + strings.ToUpper(m.currentTool)
		if active := len(m.activeToolCalls); active > 1 {
			status += fmt.Sprintf(" ×%d", active)
		}
		color = GetToolIconColor(m.currentTool)
	case m.state == StateStreaming:
		status = "WRITING"
		if m.responseToolCount > 0 {
			status += fmt.Sprintf(" · %d tools", m.responseToolCount)
		}
		color = ColorSuccess
		icon = MessageIcons["pending"]
	case m.planProgressMode && m.planProgress != nil && m.planProgress.TotalSteps > 0:
		status = fmt.Sprintf("PLAN %d/%d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
		color = ColorInfo
	case m.state == StateProcessing:
		status = "THINKING"
		if strings.Contains(strings.ToLower(m.processingLabel), "agent") {
			status = "AGENT LOOP"
		}
		color = ColorSecondary
	default:
		status = "IDLE"
	}

	return engineStyle.Foreground(color).Render(icon + " " + status)
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
	pct := m.getContextPercent()
	if pct <= 0 {
		return ""
	}
	tokens, maxTokens := m.getTokenCounts()
	if withContextBar {
		return renderContextBar(pct, 8, tokens, maxTokens, m.getOutputTokens())
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
	if tokens > 0 && maxTokens > 0 {
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
func contextUrgencyColor(pct float64) lipgloss.Color {
	switch {
	case pct > 0.95:
		return ColorError // Red — critical
	case pct > 0.80:
		return lipgloss.Color("#F97316") // Orange 500 — high
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
	if pct <= 0 {
		return ""
	}

	filled := int(pct * float64(barWidth))
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
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))
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

// contextualShortcutHints returns a compact line of shortcuts relevant to current state.
func (m Model) contextualShortcutHints() string {
	keyStyle := lipgloss.NewStyle().Foreground(ColorMuted).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(ColorDim)
	sep := descStyle.Render(" · ")

	var parts []string
	add := func(key, desc string) {
		parts = append(parts, keyStyle.Render(key)+" "+descStyle.Render(desc))
	}

	switch m.state {
	case StatePermissionPrompt:
		add("y", "Allow")
		add("a", "Always")
		add("n", "Deny")
		add("esc", "Cancel")
	case StatePlanApproval:
		add("y", "Approve")
		add("n", "Reject")
		add("m", "Modify")
		add("↑↓", "Navigate")
	case StateDiffPreview:
		add("Enter", "Accept")
		add("e", "Edit")
		add("n", "Reject")
		add("q", "Close")
	case StateMultiDiffPreview:
		add("y", "Apply all")
		add("n", "Reject all")
		add("Tab", "Switch pane")
		add("↑↓", "Browse")
	case StateQuestionPrompt:
		add("↑↓", "Navigate")
		add("Enter", "Confirm")
		add("esc", "Cancel")
	case StateModelSelector:
		add("↑↓", "Navigate")
		add("Enter", "Select")
		add("esc", "Cancel")
	case StateProcessing, StateStreaming:
		add("esc", "Interrupt")
	default:
		return ""
	}

	if len(parts) == 0 {
		return ""
	}

	return strings.Join(parts, sep)
}

// shortenModelName returns a shortened model name.
func shortenModelName(name string) string {
	name = strings.ReplaceAll(name, "gemini-", "")
	name = strings.ReplaceAll(name, "-preview", "")
	name = strings.ReplaceAll(name, "-latest", "")
	return name
}
