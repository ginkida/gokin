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
	if h := m.interruptHint(); h != "" {
		requiredRight = append(requiredRight, h)
	}

	return renderFittedStatusLine(m.width, left, m.statusBarHintSegments(true), requiredRight)
}

func (m Model) compactStatusSegments() []string {
	var parts []string
	parts = append(parts, m.safetyModeSegments(false)...)
	if m.queuedPending > 0 {
		parts = append(parts, fmt.Sprintf("📥%d", m.queuedPending))
	}
	if h := m.compactHealth(); h != "" {
		parts = append(parts, h)
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
	if h := m.compactHealth(); h != "" {
		parts = append(parts, h)
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

	// Even on the tightest layout, keep a cue that Esc cancels a busy turn.
	if m.state == StateProcessing || m.state == StateStreaming {
		parts = append(parts, lipgloss.NewStyle().Foreground(ColorDim).Render("esc"))
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
	if h := m.interruptHint(); h != "" {
		requiredRight = append(requiredRight, h)
	}

	return renderFittedStatusLine(m.width, left, m.statusBarHintSegments(false), requiredRight)
}

// renderStatusBarFull renders the full status bar for wide terminals (>= 120 chars).
// Shows full Status Bar 2.0 with all reliability and runtime details.
func (m Model) renderStatusBarFull() string {
	// Identity (model) now lives in baseStatusSegments via identitySegment —
	// no separate dim model cell appended here (it duplicated the provider).
	leftParts := m.baseStatusSegments(true)

	var requiredRight []string

	// Retry / rate-limit are shown by the consolidated engine badge in
	// baseStatusSegments — NOT duplicated here in requiredRight.

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

	if h := m.interruptHint(); h != "" {
		requiredRight = append(requiredRight, h)
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
	if safety := m.safetyBadge(); safety != "" {
		parts = append(parts, safety)
	}
	if m.queuedPending > 0 {
		parts = append(parts, providerStyle.Render(fmt.Sprintf("📥 %d queued", m.queuedPending)))
	}

	// Degraded-mode label removed: the retry/cooldown it carried is now folded
	// into the single engine badge (renderEngineStatus) below.

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
	// Breaker circuit-open / recovering cells removed — folded into the single
	// engine badge below so the same recovery state isn't shown twice.

	parts = append(parts, m.identitySegment())

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

	// Heartbeat is suppressed during recovery: the engine badge already proves
	// the session is alive, so a "heartbeat:0s" cell next to "↻ retry 3/3" is
	// pure noise. It still shows for a healthy-but-slow long operation.
	if m.shouldShowHeartbeat() && !m.isRecovering() {
		hb := "heartbeat:waiting"
		if m.runtimeStatus.HasHeartbeat {
			hb = "heartbeat:" + m.runtimeStatus.HeartbeatAge.Round(time.Second).String()
		}
		parts = append(parts, heartbeatStyle.Render(hb))
	}

	// Live Engine Status — the SINGLE provider-health + activity badge.
	engineStatus := m.renderEngineStatus()
	if engineStatus != "" {
		parts = append(parts, engineStatus)
	}

	return parts
}

// safetyBadge groups the "safety off" flags into ONE status cell so YOLO and
// !SANDBOX don't each claim a │-separated segment — they're the same concern
// (this session bypasses confirmations). Empty when both protections are on.
func (m Model) safetyBadge() string {
	var b []string
	if !m.permissionsEnabled {
		b = append(b, lipgloss.NewStyle().Foreground(ColorWarning).Bold(true).Render("YOLO"))
	}
	if !m.sandboxEnabled {
		b = append(b, lipgloss.NewStyle().Foreground(ColorError).Bold(true).Render("!SANDBOX"))
	}
	return strings.Join(b, " ")
}

// identitySegment shows WHICH model is answering. The model name already
// implies the provider (glm-5.2 → glm), so a separate provider cell is pure
// duplication; the provider is shown ONLY when the active backend diverges
// from the model's family (a failover), rendered "provider→model".
func (m Model) identitySegment() string {
	style := lipgloss.NewStyle().Foreground(ColorAccent)
	model := shortenModelName(strings.TrimSpace(m.currentModel))
	provider := strings.TrimSpace(m.runtimeStatus.Provider)
	if model == "" {
		if provider == "" {
			provider = "unknown"
		}
		return style.Render(provider)
	}
	if provider != "" && !strings.HasPrefix(strings.ToLower(model), strings.ToLower(provider)) {
		return style.Render(provider + "→" + model)
	}
	return style.Render(model)
}

// compactHealth is the narrow-bar (minimal/compact) equivalent of the engine
// badge: one short token for the current provider-recovery state, "" when
// healthy. Replaces the old separate "mode:degraded" + "breaker:x/y" cells.
func (m Model) compactHealth() string {
	switch {
	case !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil):
		return "RL " + time.Until(m.rateLimitWaitUntil).Round(time.Second).String()
	case m.retryAttempt > 0 && m.retryMax > 0:
		return fmt.Sprintf("↻%d/%d", m.retryAttempt, m.retryMax)
	case m.breakerOpen():
		return "circuit"
	case m.breakerHalfOpen():
		return "recovering"
	default:
		return ""
	}
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
		// One consolidated retry badge — the degraded-mode cooldown is folded
		// in here (it used to live in a separate mode:recovering(retry Xs)
		// cell), so attempt count AND remaining cooldown show in ONE place
		// instead of the old quartet of retry cells.
		status = fmt.Sprintf("retry %d/%d", m.retryAttempt, m.retryMax)
		if rem := m.runtimeStatus.DegradedRemaining; rem > 0 {
			status += " · " + rem.Round(time.Second).String()
		}
		color = ColorWarning
		icon = "↻"
	case m.breakerOpen():
		status = "circuit open"
		color = ColorError
		icon = MessageIcons["error"]
	case m.state == StateQuestionPrompt || m.state == StatePermissionPrompt || m.state == StatePlanApproval:
		status = "WAITING"
		color = ColorWarning
	case m.currentTool != "":
		status = "RUN " + statusToolLabel(m.currentTool)
		if active := len(m.activeToolCalls); active > 1 {
			status += fmt.Sprintf(" ×%d", active)
		}
		color = GetToolIconColor(m.currentTool)
		// No leading dot for active work — the live card already shows an animated
		// spinner for the same state; a second static ●/○ on the next row is pure
		// duplication.
		icon = ""
	case m.state == StateStreaming:
		status = "WRITING"
		if m.responseToolCount > 0 {
			status += statusSeparator() + formatToolRunSummary(m.responseToolCount, m.responseToolFailures, false)
		}
		if m.responseToolFailures > 0 {
			color = ColorWarning
			icon = MessageIcons["warning"] // keep the warning marker — a real signal
		} else {
			color = ColorSuccess
			icon = "" // card spinner already marks "busy"
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
		icon = "" // card spinner already marks "busy"
	case m.breakerHalfOpen():
		// Half-open breaker with no active retry/work — surface it so an idle
		// "recovering" provider isn't invisible. Lowest priority: any active
		// work above takes precedence.
		status = "recovering"
		color = ColorWarning
		icon = MessageIcons["warning"]
	default:
		// Idle + healthy: no engine cell at all (keeps the resting bar clean).
		return ""
	}

	if icon == "" {
		return engineStyle.Foreground(color).Render(status)
	}
	return engineStyle.Foreground(color).Render(icon + " " + status)
}

// breakerOpen / breakerHalfOpen surface the request/step circuit-breaker state
// for the consolidated engine badge. shortBreakerState normalizes the raw
// runtime strings ("open"/"half_open"/"closed"/"").
func (m Model) breakerOpen() bool {
	return shortBreakerState(m.runtimeStatus.RequestBreaker) == "open" ||
		shortBreakerState(m.runtimeStatus.StepBreaker) == "open"
}

func (m Model) breakerHalfOpen() bool {
	return shortBreakerState(m.runtimeStatus.RequestBreaker) == "half" ||
		shortBreakerState(m.runtimeStatus.StepBreaker) == "half"
}

// isRecovering is true whenever the engine badge is showing a provider-health
// state (rate-limit / retry / breaker). Used to suppress the heartbeat cell —
// during recovery the badge already proves the session is alive, so heartbeat
// is pure noise.
func (m Model) isRecovering() bool {
	if m.retryAttempt > 0 && m.retryMax > 0 {
		return true
	}
	if !m.rateLimitWaitUntil.IsZero() && time.Now().Before(m.rateLimitWaitUntil) {
		return true
	}
	return m.breakerOpen() || m.breakerHalfOpen()
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

func (m Model) contextualShortcutHintPairs() []shortcutHint {
	switch m.state {
	case StatePermissionPrompt:
		return []shortcutHint{{"y", "Allow"}, {"a", "Always"}, {"n", "Deny"}, {"esc", "Cancel"}}
	case StatePlanApproval:
		return []shortcutHint{{"y", "Approve"}, {"n", "Reject"}, {"m", "Modify"}, {"↑↓", "Navigate"}}
	case StateDiffPreview:
		return []shortcutHint{{"y", "Apply"}, {"n", "Reject"}, {"A", "Apply all"}, {"R", "Reject all"}}
	case StateMultiDiffPreview:
		return []shortcutHint{{"y", "Apply all"}, {"n", "Reject all"}, {"Tab", "Switch pane"}, {"↑↓", "Browse"}}
	case StateQuestionPrompt:
		return []shortcutHint{{"↑↓", "Navigate"}, {"Enter", "Confirm"}, {"esc", "Cancel"}}
	case StateModelSelector:
		return []shortcutHint{{"↑↓", "Navigate"}, {"Enter", "Select"}, {"esc", "Cancel"}}
	default:
		// Esc-interrupt during Processing/Streaming is NOT a droppable hint here —
		// it's a required, always-visible segment (see interruptHint), because
		// cancellation is the one action the user must never lose under width
		// pressure.
		return nil
	}
}

// interruptHint returns the always-visible "Esc interrupt" status segment during
// a busy turn. Interrupt is the single always-available action while the agent
// works, so it is appended to the REQUIRED (non-droppable) right side rather than
// riding the optional hints that vanish when the bar is crowded. Empty otherwise.
func (m Model) interruptHint() string {
	if m.state == StateProcessing || m.state == StateStreaming {
		return lipgloss.NewStyle().Foreground(ColorDim).Render("Esc interrupt")
	}
	return ""
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
