package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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
	pretty := safeKeyEntryText(prettyPath(workDir))
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
	// Quit confirmation is a destructive, time-limited surface even though it
	// does not change Model.state. Put it in the guaranteed final row so short
	// panes cannot crop the toast and leave a hidden second-Ctrl+C action.
	if m.state == StateInput && m.quitConfirmationActiveAt(time.Now()) {
		return m.renderQuitConfirmationStatus()
	}
	// With only three terminal rows, the compositor can preserve one prompt row
	// plus the status bar. Keep permission recovery in that final row. Whenever
	// either dimension is unreadable, explain the fail-closed resize requirement.
	if m.state == StatePermissionPrompt && ((m.height > 0 && m.height <= permissionReadableDecisionHeight) || !m.permissionAllowChoicesReadable()) {
		return m.renderTinyPermissionSafetyStatus()
	}
	if recovery, hidden := m.hiddenPrimaryActionRecovery(); hidden {
		label := resizeRecoveryLabel(max(m.width, 1), recovery)
		return lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render(fitStatusText(label, m.width))
	}
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

func (m Model) renderQuitConfirmationStatus() string {
	width := max(m.width, 0)
	if width == 0 {
		return ""
	}
	label := "Ctrl+C again Quit · any other key Cancel"
	switch {
	case !m.quitConfirmationReadable():
		label = "Resize"
	case width < lipgloss.Width(label):
		label = "Ctrl+C Quit"
	}
	return lipgloss.NewStyle().Bold(true).Foreground(ColorWarning).Render(fitStatusText(label, width))
}

func (m Model) hiddenPrimaryActionRecovery() (string, bool) {
	switch m.state {
	case StateSettings:
		return "Esc Close", m.selectedSettingToggleAvailable() && !m.settingsPrimaryActionReadable()
	case StateModelSelector:
		recovery := "Esc Close"
		if m.modelSelectorReturnState == StateSettings {
			recovery = "Esc Back"
		}
		return recovery, len(m.availableModels) > 0 && !m.modelSelectorPrimaryActionReadable()
	case StateAPIKeyEntry:
		return "Esc Cancel", m.keyEntryAvailable() && !m.keyEntryPrimaryActionReadable()
	case StateQuestionPrompt:
		recovery := "Esc Cancel"
		if m.questionCustomInput {
			recovery = "Esc Back"
		}
		return recovery, m.questionRequest != nil && !m.questionPrimaryActionReadable()
	case StatePlanApproval:
		recovery := "Esc Cancel"
		if m.planFeedbackMode {
			recovery = "Esc Back"
		}
		return recovery, m.planRequest != nil && !m.planPrimaryActionReadable()
	case StateNotificationCenter:
		return "Esc Back", !m.notificationDetail && len(m.notificationRows()) > 0 && !m.notificationPrimaryActionReadable()
	case StateCommandPalette:
		if m.commandPalette == nil {
			return "", false
		}
		if m.commandPalette.InArgEntry() {
			return "Esc Back", !m.commandPalette.argTargetVisibleForEnter()
		}
		_, directReady := m.commandPalette.directSlashCommandWithArgs(strings.TrimSpace(m.commandPalette.GetQuery()))
		hasTarget := directReady || m.commandPalette.GetSelected() != nil
		return "Esc Close", hasTarget && !m.commandPalette.targetVisibleForEnter()
	default:
		return "", false
	}
}

func (m Model) renderTinyPermissionSafetyStatus() string {
	width := max(m.width, 0)
	if width == 0 {
		return ""
	}

	unavailable := isUnavailablePromptNotice(m.permNotice)
	recovery := "Esc Deny"
	if unavailable {
		recovery = "Esc Cancel"
	}
	label := recovery
	if !m.permissionAllowChoicesReadable() {
		label = "Resize · Esc/n Deny"
		if unavailable {
			label = "Resize · Esc Cancel"
		}
		if lipgloss.Width(label) > width {
			switch {
			case width >= lipgloss.Width("Resize"):
				label = "Resize"
			case unavailable:
				label = "Esc"
			default:
				label = "n"
			}
		}
	}

	style := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)
	return style.Render(fitStatusText(label, width))
}

// renderStatusBarMinimal renders a minimal status bar for very narrow terminals (< 60 chars).
// Shows compact core reliability fields.
func (m Model) renderStatusBarMinimal() string {
	left := strings.Join(m.minimalStatusSegments(), " ")
	var requiredRight []string
	// Queue ownership and cancellation are recovery-critical. Keep them on the
	// non-droppable side of the layout: truncating the old left-to-right string
	// at 10–59 columns could remove both while leaving lower-value identity or
	// context cells visible.
	recovery := ""
	if m.state == StateProcessing || m.state == StateStreaming {
		recovery = "esc"
	}
	// Tiny terminals used to hide the only signal that auto-follow had stopped.
	// Combine it with Esc in one compact, non-droppable cell so even a 10-column
	// pane can show both cancellation and transcript ownership.
	switch {
	case !m.output.IsAtBottom():
		recovery = strings.TrimSpace(recovery + fmt.Sprintf(" ↑%d%%", m.output.ScrollPercent()))
	case m.output.IsFrozen():
		recovery = strings.TrimSpace(recovery + " PAUSE")
	}
	if recovery != "" {
		requiredRight = append(requiredRight, lipgloss.NewStyle().Foreground(ColorDim).Render(recovery))
	}
	if m.queuedPending > 0 {
		requiredRight = append(requiredRight, lipgloss.NewStyle().Foreground(ColorAccent).Render(fmt.Sprintf("📥%d", m.queuedPending)))
	}
	if pending := m.modelSwitchStatus(true); pending != "" {
		requiredRight = append(requiredRight, pending)
	}
	return renderFittedStatusLine(m.width, left, nil, requiredRight)
}

// renderStatusBarCompact renders a compact status bar for narrow terminals (60-79 chars).
// Shows all mandatory fields in short form.
func (m Model) renderStatusBarCompact() string {
	left := joinStatusSegments(m.compactStatusSegments())
	var requiredRight []string
	if h := m.interruptHint(); h != "" {
		requiredRight = append(requiredRight, h)
	}
	if pending := m.modelSwitchStatus(true); pending != "" {
		requiredRight = append(requiredRight, pending)
	}
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
	if h := m.compactHealth(); h != "" {
		parts = append(parts, h)
	}

	parts = append(parts, m.compactIdentity(12))

	tokens, maxTokens := m.getTokenCounts()
	if m.showTokens && maxTokens > 0 {
		pct := m.getContextPercent()
		color := contextUrgencyColor(pct)
		hint := contextUrgencyHint(pct)
		label := formatAbsoluteTokens(tokens, maxTokens, m.getOutputTokens())
		if label == "" {
			label = fmt.Sprintf("ctx:%.0f%%", pct*100)
		}
		if m.tokenUsage != nil && m.tokenUsage.IsEstimate {
			label = "≈" + label
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

	parts = append(parts, m.compactIdentity(10))

	_, maxTokens := m.getTokenCounts()
	if m.showTokens && maxTokens > 0 {
		pct := m.getContextPercent()
		color := contextUrgencyColor(pct)
		label := fmt.Sprintf("ctx:%.0f%%", pct*100)
		if m.tokenUsage != nil && m.tokenUsage.IsEstimate {
			label = "≈" + label
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(color).Render(label))
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
	if h := m.interruptHint(); h != "" {
		requiredRight = append(requiredRight, h)
	}
	if pending := m.modelSwitchStatus(false); pending != "" {
		requiredRight = append(requiredRight, pending)
	}
	if !m.output.IsAtBottom() {
		scrollStyle := lipgloss.NewStyle().Foreground(ColorDim)
		requiredRight = append(requiredRight, scrollStyle.Render(fmt.Sprintf("↑ %d%%", m.output.ScrollPercent())))
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
	if h := m.interruptHint(); h != "" {
		requiredRight = append(requiredRight, h)
	}
	if pending := m.modelSwitchStatus(false); pending != "" {
		requiredRight = append(requiredRight, pending)
	}

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
	if width <= 0 {
		return ""
	}
	optional := append([]string(nil), optionalRight...)
	for {
		rightParts := append([]string(nil), optional...)
		rightParts = append(rightParts, requiredRight...)
		right := joinStatusSegments(rightParts)
		if right == "" {
			return fitStatusText(left, width)
		}
		if lipgloss.Width(left)+lipgloss.Width(right)+1 <= width {
			padding := safePadding(width, lipgloss.Width(left), lipgloss.Width(right))
			return fitStatusText(left+strings.Repeat(" ", padding)+right, width)
		}
		if len(optional) == 0 {
			// requiredRight is ordered highest priority first. If even the
			// required set cannot fit, prefix truncation therefore preserves the
			// recovery action before status/detail cells.
			right = fitStatusText(right, width)
			rightWidth := lipgloss.Width(right)
			if rightWidth >= width {
				return right
			}
			leftBudget := width - rightWidth - 1
			left = fitStatusText(left, leftBudget)
			padding := safePadding(width, lipgloss.Width(left), rightWidth)
			return fitStatusText(left+strings.Repeat(" ", padding)+right, width)
		}
		optional = optional[:len(optional)-1]
	}
}

func fitStatusText(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "")
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
	// Reliability and live-work signals lead whenever present. This lets the
	// fitted renderer drop project/identity detail without hiding unsafe mode,
	// queued ownership, or what the engine is doing right now.
	if safety := m.safetyBadge(); safety != "" {
		parts = append(parts, safety)
	}
	if m.queuedPending > 0 {
		parts = append(parts, providerStyle.Render(fmt.Sprintf("📥 %d queued", m.queuedPending)))
	}
	if engineStatus := m.renderEngineStatus(); engineStatus != "" {
		parts = append(parts, engineStatus)
	}
	if m.hasActivePlanStatus() {
		planStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
		activeStep := fmt.Sprintf("%s %d/%d", MessageIcons["info"], m.planProgress.CurrentStepID, m.planProgress.TotalSteps)
		if title := safeKeyEntryText(m.planProgress.CurrentTitle); title != "" {
			title = truncateForWidth(title, 20)
			activeStep += " " + title
		}
		parts = append(parts, planStyle.Render(activeStep))
	}

	// Project path is the first anchor in the ordinary healthy/idle case — you instantly
	// see which repo this session is bound to without scrolling up to the
	// welcome banner. Kept dim so it doesn't fight for attention with the
	// live status cells. Always ~/-collapsed when inside $HOME; shortened
	// only when long enough to eat the bar (>36 runes ≈ "~/a/very/long/path").
	if dir := statusBarProjectPath(m.workDir); dir != "" {
		parts = append(parts, dimStyle.Render(dir))
	}

	// Degraded-mode label removed: the retry/cooldown it carried is now folded
	// into the single engine badge (renderEngineStatus) below.

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

	// Active background loops — persistent awareness that a (possibly
	// restart-restored) loop keeps firing. Highlighted while an iteration is
	// actually running; dim otherwise. Calm UI: absent when no active loops.
	if n := m.runtimeStatus.ActiveLoops; n > 0 {
		label := fmt.Sprintf("⟳ %d loop", n)
		if n > 1 {
			label += "s"
		}
		if m.runtimeStatus.LoopFiring {
			parts = append(parts, lipgloss.NewStyle().Foreground(ColorInfo).Render(label+" · running"))
		} else {
			parts = append(parts, dimStyle.Render(label))
		}
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
	model := shortenModelName(safeKeyEntryText(m.currentModel))
	provider := safeKeyEntryText(m.runtimeStatus.Provider)
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

// compactIdentity keeps the active model visible on narrow layouts too. The
// previous compact bar showed only the provider (or "ready"), so resizing from
// 80 to 79 columns silently removed the most useful session identity.
func (m Model) compactIdentity(width int) string {
	model := shortenModelName(safeKeyEntryText(m.currentModel))
	provider := safeKeyEntryText(m.runtimeStatus.Provider)
	identity := model
	if identity == "" {
		identity = provider
	}
	if identity == "" {
		identity = "ready"
	}
	if model != "" && provider != "" && !strings.HasPrefix(strings.ToLower(model), strings.ToLower(provider)) {
		identity = provider + "→" + model
	}
	return truncateForWidth(identity, width)
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

// modelSwitchStatus keeps an asynchronous selector request visible after the
// modal closes and after its toast expires. The identity cell intentionally
// stays on the authoritative old model until ModelSelectResultMsg arrives.
func (m Model) modelSwitchStatus(compact bool) string {
	target := shortenModelName(safeKeyEntryText(m.modelSwitchPending))
	if target == "" {
		return ""
	}
	style := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
	if compact {
		return style.Render("↻" + truncateForWidth(target, 12))
	}
	return style.Render("switching→" + truncateForWidth(target, 18))
}

// renderEngineStatus returns a compact status indicator for the AI engine.
func (m Model) renderEngineStatus() string {
	engineStyle := lipgloss.NewStyle().Bold(true)
	// Read-only overlays own Esc and navigation, but they do not stop the turn
	// underneath. Keep the engine cell tied to that underlying request state so
	// opening notifications/settings never makes active writing look idle.
	activeState := m.activeRequestState()

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
	case activeState == StateStreaming:
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
	case activeState == StateProcessing:
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
		return renderContextBar(pct, barWidth, tokens, maxTokens, m.getOutputTokens(), m.tokenUsage.IsEstimate)
	}
	color := contextUrgencyColor(pct)
	if label := formatAbsoluteTokens(tokens, maxTokens, m.getOutputTokens()); label != "" {
		if m.tokenUsage.IsEstimate {
			label = "≈" + label
		}
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
func renderContextBar(pct float64, barWidth int, tokens int, maxTokens int, outputTokens int, estimated ...bool) string {
	if pct <= 0 && maxTokens <= 0 {
		return ""
	}
	// Defensive: this renders every frame and its no-panic invariant otherwise
	// lives entirely in the caller (barWidth ∈ {8,12,16}, pct ≥ 0). A negative
	// barWidth or pct would drive a strings.Repeat count negative → panic.
	if barWidth <= 0 {
		return ""
	}

	clampedPct := min(max(pct, 0), 1)
	scaled := clampedPct * float64(barWidth)
	filled := int(scaled)
	if filled > barWidth {
		filled = barWidth
	}
	partial := ""
	if filled < barWidth {
		// Eighth-cell glyphs reduce a 16-column bar's quantization error from
		// 6.25 percentage points to less than 0.8 points.
		partials := [...]string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
		eighths := int((scaled - float64(filled)) * 8)
		if eighths == 0 && pct > 0 && filled == 0 {
			eighths = 1
		}
		partial = partials[eighths]
	}
	occupied := filled
	if partial != "" {
		occupied++
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
		if projectedTotal > occupied {
			projected = projectedTotal - occupied
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

	bar := filledStyle.Render(strings.Repeat("█", filled)+partial) +
		projectedStyle.Render(strings.Repeat("▓", projected)) +
		emptyStyle.Render(strings.Repeat("░", barWidth-occupied-projected))

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
	if len(estimated) > 0 && estimated[0] {
		label = "≈" + label
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
		if m.tokenUsage.MaxTokens > 0 && m.tokenUsage.Tokens >= 0 {
			return float64(m.tokenUsage.Tokens) / float64(m.tokenUsage.MaxTokens)
		}
		return m.tokenUsage.PercentUsed
	}
	return 0
}

// formatBackgroundTaskStatus returns a compact status string for background tasks.
// Shows current action if a task has progress info, otherwise falls back to "N bg".
func (m Model) formatBackgroundTaskStatus(bgCount int) string {
	var bestAction string
	for _, t := range m.backgroundTasks {
		if action := safeKeyEntryText(t.CurrentAction); action != "" {
			bestAction = action
			break
		}
	}
	if bestAction != "" {
		return compactInline(bestAction, 25)
	}
	return fmt.Sprintf("%d bg", bgCount)
}

func (m Model) contextualShortcutHintPairs() []shortcutHint {
	switch m.state {
	case StatePermissionPrompt:
		if m.permShowDetails {
			if isUnavailablePromptNotice(m.permNotice) {
				hints := []shortcutHint{{"?", "Back"}, {"esc", "Cancel"}}
				if m.permissionDetailsCanScroll() {
					hints = append([]shortcutHint{{"↑↓", "Scroll"}, {"PgUp/PgDn", "Page"}}, hints...)
				}
				return hints
			}
			if m.permissionDetailsCanScroll() {
				return []shortcutHint{{"↑↓", "Scroll"}, {"PgUp/PgDn", "Page"}, {"?", "Back"}, {"y/a/n", "Decide"}, {"esc", "Deny"}}
			}
			return []shortcutHint{{"?", "Back"}, {"y/a/n", "Decide"}, {"esc", "Deny"}}
		}
		if isUnavailablePromptNotice(m.permNotice) {
			return []shortcutHint{{"esc", "Cancel"}, {"?", "Details"}}
		}
		return []shortcutHint{{"y", "Allow"}, {"a", "Always"}, {"n", "Deny"}, {"?", "Details"}, {"esc", "Deny"}}
	case StatePlanApproval:
		if m.planRequest != nil && !m.planPrimaryActionReadable() {
			recovery := "Cancel"
			if m.planFeedbackMode {
				recovery = "Back"
			}
			return []shortcutHint{{"↔", "Resize"}, {"esc", recovery}}
		}
		if m.planFeedbackMode {
			if isUnavailablePromptNotice(m.planFeedbackError) {
				return []shortcutHint{{"esc", "Back"}}
			}
			return []shortcutHint{{"Enter", "Submit"}, {"Alt+Enter", "New line"}, {"esc", "Back"}}
		}
		if isUnavailablePromptNotice(m.planApprovalNotice) {
			hints := []shortcutHint{{"esc", "Cancel"}}
			if m.planStepsCanPage() {
				hints = append(hints, shortcutHint{"PgUp/PgDn", "Steps"})
			}
			return hints
		}
		if m.planRequest == nil {
			return []shortcutHint{{"esc", "Cancel"}}
		}
		if len(m.planRequest.Steps) == 0 {
			return []shortcutHint{{"n", "Reject"}, {"m", "Modify"}, {"↑↓", "Navigate"}, {"esc", "Cancel"}}
		}
		hints := []shortcutHint{{"y", "Approve"}, {"n", "Reject"}, {"m", "Modify"}, {"↑↓", "Navigate"}}
		if m.planStepsCanPage() {
			hints = append(hints, shortcutHint{"PgUp/PgDn", "Steps"})
		}
		return hints
	case StateDiffPreview:
		inspection := make([]shortcutHint, 0, 2)
		if m.diffPreview.canScrollDiff() {
			inspection = append(inspection, shortcutHint{"j/k", "Scroll"})
		}
		if m.diffPreview.canJumpChanges() {
			inspection = append(inspection, shortcutHint{"[ ]", "Changes"})
		}
		if m.diffPreview.responsePending {
			return inspection
		}
		if m.diffPreview.responseUnavailable {
			return append([]shortcutHint{{"esc", "Cancel"}}, inspection...)
		}
		if m.diffPreview.terminalSizeKnown && tinyReviewNeedsResize(m.diffPreview.width, m.diffPreview.height) {
			return []shortcutHint{{"↔", "Resize"}, {"n/esc", "Reject"}}
		}
		hints := make([]shortcutHint, 0, 7)
		hints = append(hints, shortcutHint{"y", "Apply"}, shortcutHint{"n", "Reject"}, shortcutHint{"A", "Apply all"}, shortcutHint{"R", "Reject all"})
		return append(hints, inspection...)
	case StateMultiDiffPreview:
		if len(m.multiDiffPreview.files) == 0 {
			return []shortcutHint{{"esc", "Close"}}
		}
		inspection := make([]shortcutHint, 0, 2)
		if m.multiDiffPreview.focusOnList {
			if m.multiDiffPreview.canNavigateFiles() {
				inspection = append(inspection, shortcutHint{"↑↓", "Files"})
			}
			if m.multiDiffPreview.canScrollDiff() {
				inspection = append(inspection, shortcutHint{"Tab", "Focus diff"})
			}
		} else {
			if m.multiDiffPreview.canScrollDiff() {
				inspection = append(inspection, shortcutHint{"j/k", "Scroll diff"})
			}
			if m.multiDiffPreview.canNavigateFiles() {
				inspection = append(inspection, shortcutHint{"Tab", "Focus files"})
			}
		}
		if m.multiDiffPreview.responsePending {
			return inspection
		}
		if m.multiDiffPreview.responseUnavailable {
			return append([]shortcutHint{{"esc", "Cancel"}}, inspection...)
		}
		if m.multiDiffPreview.terminalSizeKnown && tinyReviewNeedsResize(m.multiDiffPreview.width, m.multiDiffPreview.height) {
			if m.multiDiffPreview.confirmFinish {
				return []shortcutHint{{"↔", "Resize"}, {"esc/q", "Back to review"}}
			}
			return []shortcutHint{{"↔", "Resize"}, {"n", "Reject file"}, {"esc", "Reject all"}}
		}
		if m.multiDiffPreview.confirmFinish {
			return []shortcutHint{{"esc/q", "Back to review"}, {"Enter", "Confirm"}}
		}
		hints := make([]shortcutHint, 0, 7)
		hints = append(hints, shortcutHint{"y/n", "Decide file"}, shortcutHint{"A/R", "Decide rest"}, shortcutHint{"Enter", "Finish…"})
		return append(hints, inspection...)
	case StateQuestionPrompt:
		if m.questionRequest != nil && !m.questionPrimaryActionReadable() {
			recovery := "Cancel"
			if m.questionCustomInput {
				recovery = "Back"
			}
			return []shortcutHint{{"↔", "Resize"}, {"esc", recovery}}
		}
		if m.questionCustomInput {
			if isUnavailablePromptNotice(m.questionInputError) {
				return []shortcutHint{{"esc", "Back"}}
			}
			return []shortcutHint{{"Enter", "Submit"}, {"Alt+Enter", "New line"}, {"esc", "Back"}}
		}
		if m.questionRequest != nil && len(m.questionRequest.Options) == 0 {
			if isUnavailablePromptNotice(m.questionInputError) {
				return []shortcutHint{{"esc", "Cancel"}}
			}
			return []shortcutHint{{"Enter", "Submit"}, {"Alt+Enter", "New line"}, {"esc", "Cancel"}}
		}
		if m.questionRequest == nil {
			return []shortcutHint{{"esc", "Cancel"}}
		}
		if isUnavailablePromptNotice(m.questionInputError) {
			hints := []shortcutHint{{"esc", "Cancel"}, {"↑↓", "Navigate"}}
			if m.questionChoicesCanPage() {
				hints = append(hints, shortcutHint{"PgUp/PgDn", "Page"})
			}
			return hints
		}
		hints := []shortcutHint{{"↑↓", "Navigate"}}
		if m.questionChoicesCanPage() {
			hints = append(hints, shortcutHint{"PgUp/PgDn", "Page"})
		}
		return append(hints, shortcutHint{"Enter", "Confirm"}, shortcutHint{"esc", "Cancel"})
	case StateModelSelector:
		escapeAction := "Close"
		if m.modelSelectorReturnState == StateSettings {
			escapeAction = "Back"
		}
		if len(m.availableModels) == 0 {
			return []shortcutHint{{"esc", escapeAction}}
		}
		if !m.modelSelectorPrimaryActionReadable() {
			return []shortcutHint{{"↔", "Resize"}, {"esc", escapeAction}}
		}
		selected, _ := m.selectedModelChoice()
		navigation := func(action string) []shortcutHint {
			if len(m.availableModels) > 1 {
				return []shortcutHint{{"↑↓", action}, {"esc", escapeAction}}
			}
			return []shortcutHint{{"esc", escapeAction}}
		}
		if m.modelSwitchPending != "" {
			return navigation("Inspect")
		}
		if selected.ID == "" {
			return navigation("Navigate")
		}
		if selected.ID == safeKeyEntryText(m.currentModel) {
			hints := make([]shortcutHint, 0, 3)
			if len(m.availableModels) > 1 {
				hints = append(hints, shortcutHint{"↑↓", "Navigate"})
			}
			return append(hints, shortcutHint{"Enter", "Keep current"}, shortcutHint{"esc", escapeAction})
		}
		if !m.modelSelectAvailable() {
			return navigation("Inspect")
		}
		hints := make([]shortcutHint, 0, 3)
		if len(m.availableModels) > 1 {
			hints = append(hints, shortcutHint{"↑↓", "Navigate"})
		}
		return append(hints, shortcutHint{"Enter", "Select"}, shortcutHint{"esc", escapeAction})
	case StateSettings:
		if len(m.settingsItems) == 0 {
			return []shortcutHint{{"m", "Model"}, {"esc", "Close"}}
		}
		if m.selectedSettingToggleAvailable() && !m.settingsPrimaryActionReadable() {
			return []shortcutHint{{"↔", "Resize"}, {"m", "Model"}, {"esc", "Close"}}
		}
		hints := make([]shortcutHint, 0, 4)
		if m.selectedSettingToggleAvailable() {
			hints = append(hints, shortcutHint{"Space", "Toggle"})
		}
		if len(m.settingsItems) > 1 {
			action := "Navigate"
			if !m.selectedSettingToggleAvailable() {
				action = "Inspect"
			}
			hints = append(hints, shortcutHint{"↑↓", action})
		}
		return append(hints, shortcutHint{"m", "Model"}, shortcutHint{"esc", "Close"})
	case StateAPIKeyEntry:
		if m.keyEntryAvailable() {
			if !m.keyEntryPrimaryActionReadable() {
				return []shortcutHint{{"↔", "Resize"}, {"esc", "Cancel"}}
			}
			return []shortcutHint{{"Enter", "Save"}, {"esc", "Cancel"}}
		}
		return []shortcutHint{{"esc", "Close"}}
	case StateShortcutsOverlay:
		escapeAction := "Close"
		hints := []shortcutHint{{"Type", "Filter"}}
		if m.shortcutsOverlay == nil {
			return append(hints, shortcutHint{"↑↓", "Browse"}, shortcutHint{"esc", escapeAction})
		}
		if m.shortcutsOverlay.GetSearch() != "" {
			escapeAction = "Clear filter"
			hints = append(hints, shortcutHint{"Backspace", "Edit"})
		}
		entries := flattenShortcuts(m.shortcutsOverlay.getFilteredCategories())
		pageSize := m.shortcutsOverlay.pageSize
		canBrowse := len(entries) > 1 && (pageSize <= 0 || m.shortcutsOverlay.scrollIndex > 0 || m.shortcutsOverlay.scrollIndex+pageSize < len(entries))
		if canBrowse {
			hints = append(hints, shortcutHint{"↑↓", "Browse"})
		}
		return append(hints, shortcutHint{"esc", escapeAction})
	case StateNotificationCenter:
		rows := m.notificationRows()
		if len(rows) == 0 {
			return []shortcutHint{{"esc", "Back"}}
		}
		if m.notificationDetail {
			selected := selectedNotificationIndex(rows, m.notificationSelected, m.notificationSelectedID)
			lines := notificationDetailLines(rows[selected], notificationContentWidth(m.width))
			if len(lines) > notificationDetailVisibleCount(m.height, len(lines)) {
				return []shortcutHint{{"↑↓", "Scroll"}, {"PgUp/PgDn", "Page"}, {"esc", "Back to list"}}
			}
			return []shortcutHint{{"esc", "Back to list"}}
		}
		if !m.notificationPrimaryActionReadable() {
			return []shortcutHint{{"↔", "Resize"}, {"esc", "Back"}}
		}
		hints := make([]shortcutHint, 0, 4)
		if len(rows) > 1 {
			hints = append(hints, shortcutHint{"↑↓", "Select"})
		}
		hints = append(hints, shortcutHint{"Enter", "Details"})
		if m.toastManager != nil && len(m.toastManager.History()) > 0 && m.notificationClearActionVisible(len(rows) > 1) {
			hints = append(hints, shortcutHint{"c", "Clear earlier"})
		}
		return append(hints, shortcutHint{"esc", "Back"})
	case StateBatchProgress:
		if m.progressModel.isComplete {
			if m.progressModel.closePending {
				return nil
			}
			return []shortcutHint{{"Enter/Esc", "Close"}}
		}
		if m.progressModel.isCancelling || m.progressModel.onAction == nil {
			return nil
		}
		if !m.progressModel.pauseActionReadable() {
			if m.progressModel.isPaused {
				return []shortcutHint{{"↔", "Resize to resume"}, {"esc", "Cancel"}}
			}
			return []shortcutHint{{"esc", "Cancel"}}
		}
		pauseAction := "Pause"
		if m.progressModel.isPaused {
			pauseAction = "Resume"
		}
		return []shortcutHint{{"p", pauseAction}, {"esc", "Cancel"}}
	case StateSearchResults:
		if len(m.searchResults.results) == 0 {
			return []shortcutHint{{"esc/q", "Close"}}
		}
		if workspaceTargetActionsUnreadable(m.searchResults.width, m.searchResults.height, minSearchTargetWidth) {
			return []shortcutHint{{"↔", "Resize"}, {"esc/q", "Close"}}
		}
		previewAction := "Preview"
		if m.searchResults.previewVisible() {
			previewAction = "Hide preview"
		}
		hints := []shortcutHint{{"Space", previewAction}, {"y", "Copy path"}}
		if m.searchResults.actionsAvailable() {
			hints = append(hints, shortcutHint{"Enter", "Open"})
		}
		if m.searchResults.canNavigateResults() {
			hints = append(hints, shortcutHint{"↑↓", "Navigate"})
		}
		if m.searchResults.canPageResults() {
			hints = append(hints, shortcutHint{"PgUp/PgDn", "Page"})
		}
		if m.searchResults.canScrollPreview() {
			hints = append(hints, shortcutHint{"Ctrl+j/k", "Scroll preview"})
		}
		return append(hints, shortcutHint{"esc/q", "Close"})
	case StateGitStatus:
		if len(m.gitStatusModel.entries) == 0 {
			return []shortcutHint{{"esc/q", "Close"}}
		}
		if workspaceTargetActionsUnreadable(m.gitStatusModel.width, m.gitStatusModel.height, minGitTargetWidth) {
			if m.gitStatusModel.confirmReset {
				return []shortcutHint{{"↔", "Resize"}, {"esc/q", "Cancel"}}
			}
			return []shortcutHint{{"↔", "Resize"}, {"esc/q", "Close"}}
		}
		if m.gitStatusModel.confirmReset {
			return []shortcutHint{{"esc/q", "Cancel"}, {"Enter", "Confirm reset"}}
		}
		if !m.gitStatusModel.actionsAvailable() {
			hints := make([]shortcutHint, 0, 3)
			if m.gitStatusModel.canNavigateEntries() {
				hints = append(hints, shortcutHint{"↑↓", "Inspect"})
			}
			if m.gitStatusModel.canPageEntries() {
				hints = append(hints, shortcutHint{"PgUp/PgDn", "Page"})
			}
			return append(hints, shortcutHint{"esc/q", "Close"})
		}
		diffAction := "Show diff"
		if m.gitStatusModel.showDiff {
			diffAction = "Hide diff"
		}
		if m.gitStatusModel.showDiff && m.gitStatusModel.diffLoadError {
			diffAction = "Retry diff"
		}
		hints := []shortcutHint{{"Space", "Stage/unstage"}, {"d", diffAction}}
		if m.gitStatusModel.canNavigateEntries() {
			hints = append(hints, shortcutHint{"↑↓", "Navigate"})
		}
		if m.gitStatusModel.canPageEntries() {
			hints = append(hints, shortcutHint{"PgUp/PgDn", "Page"})
		}
		if m.gitStatusModel.canScrollDiff() {
			hints = append(hints, shortcutHint{"Ctrl+j/k", "Scroll diff"})
		}
		return append(hints, shortcutHint{"esc/q", "Close"})
	case StateFileBrowser:
		if m.fileBrowser.filterActive {
			hints := []shortcutHint{{"Type", "Filter"}}
			if m.fileBrowser.filterInput != "" {
				hints = append(hints, shortcutHint{"Backspace", "Delete"})
			}
			return append(hints, shortcutHint{"Enter/Esc", "Done"})
		}
		if len(m.fileBrowser.entries) == 0 {
			hints := []shortcutHint{{"/", "Filter"}, {"r", "Refresh"}}
			if m.fileBrowser.filter != "" {
				hints = append(hints, shortcutHint{"c", "Clear"})
			}
			return append(hints, shortcutHint{"esc/q", "Close"})
		}
		if m.fileBrowser.targetActionsUnreadable() {
			return []shortcutHint{{"↔", "Resize"}, {"esc/q", "Close"}}
		}
		entryAction := "Add to draft"
		selected := min(max(m.fileBrowser.selectedIndex, 0), len(m.fileBrowser.entries)-1)
		if m.fileBrowser.entries[selected].IsDir {
			entryAction = "Open folder"
		}
		hints := []shortcutHint{{"Enter", entryAction}}
		if m.fileBrowser.entries[selected].Name != ".." {
			hints = append(hints, shortcutHint{"Space", "Select"})
		}
		if m.fileBrowser.canNavigateEntries() {
			hints = append(hints, shortcutHint{"↑↓", "Move"})
		}
		if m.fileBrowser.canPageEntries() {
			hints = append(hints, shortcutHint{"PgUp/PgDn", "Page"})
		}
		if len(m.fileBrowser.selectedFiles) > 0 {
			hints = append(hints, shortcutHint{"y", "Add selection"})
		}
		return append(hints, shortcutHint{"esc/q", "Close"})
	case StateCommandPalette:
		if m.commandPalette == nil {
			return []shortcutHint{{"esc", "Close"}}
		}
		if m.commandPalette.InArgEntry() {
			if !m.commandPalette.argTargetVisibleForEnter() {
				return []shortcutHint{{"↔", "Resize"}, {"esc", "Back"}}
			}
			if !m.commandPalette.submissionAvailable() || isUnavailablePromptNotice(m.commandPalette.argError) {
				return []shortcutHint{{"esc", "Back"}}
			}
			return []shortcutHint{{"Enter", "Run"}, {"esc", "Back"}}
		}
		_, directReady := m.commandPalette.directSlashCommandWithArgs(strings.TrimSpace(m.commandPalette.GetQuery()))
		hasTarget := directReady || m.commandPalette.GetSelected() != nil
		if hasTarget && !m.commandPalette.targetVisibleForEnter() {
			return []shortcutHint{{"↔", "Resize"}, {"esc", "Close"}}
		}
		if m.commandPalette.submitError != "" {
			return []shortcutHint{{"esc", "Close"}}
		}
		if len(m.commandPalette.filtered) == 0 {
			hints := make([]shortcutHint, 0, 3)
			if directReady && m.commandPalette.submissionAvailable() {
				hints = append(hints, shortcutHint{"Enter", "Run"})
			}
			if strings.TrimSpace(m.commandPalette.GetQuery()) != "" {
				hints = append(hints, shortcutHint{"Backspace", "Edit"})
			}
			return append(hints, shortcutHint{"esc", "Close"})
		}
		hints := make([]shortcutHint, 0, 4)
		if len(m.commandPalette.filtered) > 1 {
			hints = append(hints, shortcutHint{"↑↓", "Navigate"})
		}
		if m.commandPalette.selectedRunnable() {
			hints = append(hints, shortcutHint{"Enter", "Run"})
		}
		previewAction := "Details"
		if m.commandPalette.showPreview {
			previewAction = "List"
		}
		return append(hints, shortcutHint{"Tab", previewAction}, shortcutHint{"esc", "Close"})
	case StateContextObservatory:
		return []shortcutHint{{"Esc/Ctrl+H", "Close"}}
	case StateProcessing, StateStreaming:
		// Esc-interrupt is NOT here — it's a required, always-visible segment
		// (see interruptHint), because cancellation must never vanish under
		// width pressure. This droppable hint surfaces the live-activity
		// detail toggle (Ctrl+O), discoverable exactly when it's useful.
		if m.liveDetailExpanded {
			return []shortcutHint{{"ctrl+o", "minimal"}}
		}
		return []shortcutHint{{"ctrl+o", "detail"}}
	default:
		return nil
	}
}

// interruptHint returns the always-visible busy-action status segment. Besides
// cancellation it makes follow-up composition discoverable before the hidden
// busy composer has any text, then changes to the action Enter will actually
// perform. It rides the REQUIRED (non-droppable) right side so recovery never
// disappears under width pressure. Minimal layouts use the shorter `esc` cell.
func (m Model) interruptHint() string {
	if m.state != StateProcessing && m.state != StateStreaming {
		return ""
	}
	action := "Type follow-up"
	switch {
	case m.input.historySearchMode:
		action = "Enter use"
	case m.input.SuggestionsBlockSubmit():
		action = "Enter complete"
	case m.input.Value() != "" && m.onSubmit == nil:
		action = "Send unavailable"
	case m.input.Value() != "":
		action = "Enter send"
	}
	return lipgloss.NewStyle().Foreground(ColorDim).Render(action + " · Esc interrupt")
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
