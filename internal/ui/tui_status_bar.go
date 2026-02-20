package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

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
	mode := "normal"
	if strings.HasPrefix(strings.ToLower(m.runtimeStatus.Mode), "degraded") {
		mode = "degraded"
	}
	parts = append(parts, "mode:"+mode)

	if m.hasActivePlanStatus() {
		step := fmt.Sprintf("%d/%d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
		parts = append(parts, "step:"+step)
	}

	if m.shouldShowBreaker() {
		parts = append(parts, "breaker:"+shortBreakerState(m.runtimeStatus.RequestBreaker)+"/"+shortBreakerState(m.runtimeStatus.StepBreaker))
	}

	provider := m.runtimeStatus.Provider
	if provider == "" {
		provider = "?"
	}
	if len(provider) > 12 {
		provider = provider[:12]
	}
	parts = append(parts, "provider:"+provider)

	if pct := m.getContextPercent(); pct > 0 {
		color := contextUrgencyColor(pct)
		hint := contextUrgencyHint(pct)
		parts = append(parts, lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("ctx:%.0f%%", pct)+hint))
	} else {
		parts = append(parts, "ctx:n/a")
	}

	return parts
}

func (m Model) minimalStatusSegments() []string {
	var parts []string
	mode := "normal"
	if strings.HasPrefix(strings.ToLower(m.runtimeStatus.Mode), "degraded") {
		mode = "degraded"
	}
	parts = append(parts, "mode:"+mode)

	if m.hasActivePlanStatus() {
		parts = append(parts, fmt.Sprintf("step:%d/%d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps))
	}

	if m.shouldShowBreaker() {
		parts = append(parts, "breaker:"+shortBreakerState(m.runtimeStatus.RequestBreaker)+"/"+shortBreakerState(m.runtimeStatus.StepBreaker))
	}

	provider := m.runtimeStatus.Provider
	if provider == "" {
		provider = "?"
	}
	if len(provider) > 10 {
		provider = provider[:10]
	}
	parts = append(parts, "provider:"+provider)

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

	// Scroll indicator on the right
	var right string
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		right = scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent()))
	}

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

	left := strings.Join(leftParts, " · ")
	right := strings.Join(rightParts, " · ")

	padding := safePadding(m.width, lipgloss.Width(left), lipgloss.Width(right))
	return left + strings.Repeat(" ", padding) + right
}

func (m Model) baseStatusSegments(withContextBar bool) []string {
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	modeStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
	degradedStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	providerStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	breakerStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	heartbeatStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	var parts []string
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
	} else {
		parts = append(parts, modeStyle.Render("mode:normal"))
	}

	if m.hasActivePlanStatus() {
		activeStep := fmt.Sprintf("plan:%d/%d", m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
		parts = append(parts, dimStyle.Render(activeStep))
	}
	if withContextBar && m.hasActivePlanStatus() && m.planProgress.CurrentTitle != "" {
		title := m.planProgress.CurrentTitle
		if len(title) > 24 {
			title = title[:21] + "..."
		}
		parts = append(parts, dimStyle.Render("task:"+title))
	}

	if m.shouldShowBreaker() {
		reqBreaker := shortBreakerState(m.runtimeStatus.RequestBreaker)
		stepBreaker := shortBreakerState(m.runtimeStatus.StepBreaker)
		parts = append(parts, breakerStyle.Render(fmt.Sprintf("breaker:req=%s,step=%s", reqBreaker, stepBreaker)))
	}

	provider := m.runtimeStatus.Provider
	if provider == "" {
		provider = "unknown"
	}
	parts = append(parts, providerStyle.Render("provider:"+provider))

	tokenText := m.formatTokenStatus(withContextBar)
	if tokenText != "" {
		parts = append(parts, tokenText)
	}

	// Plan mode indicator
	if m.planningModeEnabled {
		planStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		parts = append(parts, planStyle.Render("PLAN"))
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

	return parts
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
	if strings.HasPrefix(strings.ToLower(m.runtimeStatus.Mode), "degraded") {
		return true
	}
	if m.hasActivePlanStatus() {
		return true
	}
	return m.state == StateProcessing || m.state == StateStreaming
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
	if withContextBar && pct > 0 {
		bar := renderContextBar(pct, 8)
		if m.tokenUsage != nil && m.tokenUsage.MaxTokens > 0 {
			abs := lipgloss.NewStyle().Foreground(ColorMuted).Render(
				fmt.Sprintf("tok:%d/%d", m.tokenUsage.Tokens, m.tokenUsage.MaxTokens))
			return bar + " " + abs
		}
		return bar
	}
	if m.tokenUsage != nil && m.tokenUsage.MaxTokens > 0 {
		color := contextUrgencyColor(pct)
		return lipgloss.NewStyle().Foreground(color).Render(
			fmt.Sprintf("tok:%d/%d", m.tokenUsage.Tokens, m.tokenUsage.MaxTokens))
	}
	if pct > 0 {
		color := contextUrgencyColor(pct)
		return lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("tok:%.0f%%", pct))
	}
	return ""
}

// contextUrgencyColor returns a color based on context usage percentage.
func contextUrgencyColor(pct float64) lipgloss.Color {
	switch {
	case pct > 95:
		return ColorError // Red — critical
	case pct > 80:
		return lipgloss.Color("#F97316") // Orange 500 — high
	case pct > 60:
		return ColorWarning // Amber — elevated
	default:
		return ColorSuccess // Green — healthy
	}
}

// contextUrgencyHint returns a short hint when context is getting full.
func contextUrgencyHint(pct float64) string {
	switch {
	case pct > 95:
		return " /compact now"
	case pct > 80:
		return " compact soon"
	default:
		return ""
	}
}

// renderContextBar returns a visual progress bar for context usage.
func renderContextBar(pct float64, barWidth int) string {
	if pct <= 0 {
		return ""
	}

	filled := int(pct / 100.0 * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	barColor := contextUrgencyColor(pct)

	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))
	pctStyle := lipgloss.NewStyle().Foreground(barColor)

	bar := filledStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", barWidth-filled))

	var label string
	switch {
	case pct < 1:
		label = "<1%"
	case pct < 10:
		label = fmt.Sprintf("%.1f%%", pct)
	default:
		label = fmt.Sprintf("%.0f%%", pct)
	}

	hint := contextUrgencyHint(pct)
	if hint != "" {
		return bar + " " + pctStyle.Render(label) + " " + lipgloss.NewStyle().Foreground(barColor).Bold(true).Render(hint)
	}
	return bar + " " + pctStyle.Render(label)
}

// getContextPercent returns the context usage percentage from available sources.
func (m Model) getContextPercent() float64 {
	if m.tokenUsagePercent > 0 {
		return m.tokenUsagePercent
	}
	if m.showTokens && m.tokenUsage != nil {
		p := m.tokenUsage.PercentUsed
		// Normalize: some legacy updates may already provide 0..100.
		if p > 1.0 {
			return p
		}
		return p * 100
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
		if len(bestAction) > 25 {
			bestAction = bestAction[:22] + "..."
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
	case StateQuestionPrompt:
		add("↑↓", "Navigate")
		add("Enter", "Confirm")
		add("esc", "Cancel")
	case StateModelSelector:
		add("↑↓", "Navigate")
		add("Enter", "Select")
		add("esc", "Cancel")
	case StateInput:
		// Only show when output is non-empty (user has been interacting)
		if !m.output.IsEmpty() {
			add("?", "Shortcuts")
			add("Ctrl+P", "Commands")
			if !m.output.IsAtBottom() {
				add("PgDn", "Scroll")
			}
		}
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
