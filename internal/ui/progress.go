package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ProgressAction represents user actions on progress.
type ProgressAction int

const (
	ProgressActionNone ProgressAction = iota
	ProgressActionCancel
	ProgressActionPause
	ProgressActionResume
)

// ProgressItem represents a single item in a batch operation.
type ProgressItem struct {
	Name    string
	Status  ProgressStatus
	Error   string
	Message string
}

// ProgressStatus represents the status of a progress item.
type ProgressStatus int

const (
	ProgressStatusPending ProgressStatus = iota
	ProgressStatusInProgress
	ProgressStatusCompleted
	ProgressStatusFailed
	ProgressStatusSkipped
)

// ProgressUpdateMsg is sent to update progress.
type ProgressUpdateMsg struct {
	Current     int
	Total       int
	CurrentItem string
	Message     string
	Items       []ProgressItem
}

// ProgressCompleteMsg is sent when progress completes.
type ProgressCompleteMsg struct {
	TotalItems   int
	SuccessCount int
	FailureCount int
	SkippedCount int
	Duration     time.Duration
}

// ProgressActionMsg is sent when user performs an action.
type ProgressActionMsg struct {
	Action ProgressAction
}

// ProgressModel is the UI for displaying batch operation progress.
type ProgressModel struct {
	title           string
	current         int
	total           int
	currentItem     string
	message         string
	items           []ProgressItem
	startTime       time.Time
	isPaused        bool
	isCancelling    bool
	isComplete      bool
	wasCancelled    bool
	closePending    bool
	ownerGeneration uint64
	styles          *Styles
	width           int
	height          int
	reducedMotion   bool

	// Completion stats
	successCount int
	failureCount int
	skippedCount int
	duration     time.Duration

	// Callback for actions
	onAction func(action ProgressAction)
}

// NewProgressModel creates a new progress model.
func NewProgressModel(styles *Styles) ProgressModel {
	return ProgressModel{
		styles:    styles,
		startTime: time.Now(),
	}
}

// SetReducedMotion replaces the time-based batch spinner with a stable marker.
func (m *ProgressModel) SetReducedMotion(enabled bool) { m.reducedMotion = enabled }

// SetSize sets the size of the progress view.
func (m *ProgressModel) SetSize(width, height int) {
	m.width = max(width, 0)
	m.height = max(height, 0)
}

// setOwnerGeneration correlates asynchronous close commands with the parent
// batch surface that produced them.
func (m *ProgressModel) setOwnerGeneration(owner uint64) {
	m.ownerGeneration = owner
}

// Start starts a new progress operation.
func (m *ProgressModel) Start(title string, total int) {
	m.title = normalizeTimelineText(title)
	if m.title == "" {
		m.title = "Batch operation"
	}
	m.current = 0
	m.total = max(total, 0)
	m.currentItem = ""
	m.message = ""
	m.items = nil
	m.startTime = time.Now()
	m.isPaused = false
	m.isCancelling = false
	m.isComplete = false
	m.wasCancelled = false
	m.closePending = false
	m.successCount = 0
	m.failureCount = 0
	m.skippedCount = 0
	m.duration = 0
}

// Update updates the progress state.
func (m *ProgressModel) UpdateProgress(current int, currentItem, message string) {
	m.current = max(current, 0)
	if m.total > 0 {
		m.current = min(m.current, m.total)
	}
	m.currentItem = normalizeTimelineText(currentItem)
	m.message = normalizeTimelineText(message)
}

// AddItem adds an item to the progress list.
func (m *ProgressModel) AddItem(item ProgressItem) {
	item.Name = normalizeTimelineText(item.Name)
	if item.Name == "" {
		item.Name = "Untitled item"
	}
	item.Error = normalizeTimelineText(item.Error)
	item.Message = normalizeTimelineText(item.Message)
	m.items = append(m.items, item)

	// Update counts
	switch item.Status {
	case ProgressStatusCompleted:
		m.successCount++
	case ProgressStatusFailed:
		m.failureCount++
	case ProgressStatusSkipped:
		m.skippedCount++
	}
}

// Complete marks the progress as complete.
func (m *ProgressModel) Complete() {
	if m.isComplete {
		return
	}
	m.wasCancelled = m.wasCancelled || m.isCancelling
	m.isComplete = true
	m.isPaused = false
	m.isCancelling = false
	if m.total > 0 && !m.wasCancelled {
		m.current = m.total
	} else if m.total > 0 {
		m.current = min(max(m.current, 0), m.total)
	}
	if !m.startTime.IsZero() {
		m.duration = max(time.Since(m.startTime), 0)
	}
}

// SetActionCallback sets the callback for user actions.
func (m *ProgressModel) SetActionCallback(callback func(ProgressAction)) {
	m.onAction = callback
}

// Init initializes the progress model.
func (m ProgressModel) Init() tea.Cmd {
	return nil
}

// Update handles input events for the progress model.
func (m ProgressModel) Update(msg tea.Msg) (ProgressModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			if m.isComplete {
				return m.requestClose(msg.String())
			}
			if !m.isCancelling && m.onAction != nil {
				m.isCancelling = true
				m.isPaused = false
				m.onAction(ProgressActionCancel)
				return m, func() tea.Msg {
					return ProgressActionMsg{Action: ProgressActionCancel}
				}
			}

		case "c", "ctrl+c":
			if !m.isComplete && !m.isCancelling && m.onAction != nil {
				m.isCancelling = true
				m.isPaused = false
				m.onAction(ProgressActionCancel)
				return m, func() tea.Msg {
					return ProgressActionMsg{Action: ProgressActionCancel}
				}
			}

		case "p":
			if !m.isComplete && !m.isCancelling && m.onAction != nil && m.pauseActionReadable() {
				if m.isPaused {
					m.isPaused = false
					if m.onAction != nil {
						m.onAction(ProgressActionResume)
					}
				} else {
					m.isPaused = true
					if m.onAction != nil {
						m.onAction(ProgressActionPause)
					}
				}
			}

		case "q", "enter":
			if m.isComplete {
				return m.requestClose(msg.String())
			}
		}

	case ProgressUpdateMsg:
		// Completion is terminal. A queued worker update must not replace the
		// result the user is already reading or resurrect stale current-item text.
		if m.isComplete {
			return m, nil
		}
		m.total = max(msg.Total, 0)
		m.UpdateProgress(msg.Current, msg.CurrentItem, msg.Message)
		if msg.Items != nil {
			m.items = nil
			m.successCount = 0
			m.failureCount = 0
			m.skippedCount = 0
			for _, item := range msg.Items {
				m.AddItem(item)
			}
		}

	case ProgressCompleteMsg:
		// Completion is terminal. Workers may race while shutting down and send
		// more than one completion event; keep the first result the user saw.
		if m.isComplete {
			return m, nil
		}
		m.wasCancelled = m.wasCancelled || m.isCancelling
		m.isComplete = true
		m.isPaused = false
		m.isCancelling = false
		m.successCount = max(msg.SuccessCount, 0)
		m.failureCount = max(msg.FailureCount, 0)
		m.skippedCount = max(msg.SkippedCount, 0)
		settled := m.successCount + m.failureCount + m.skippedCount
		m.total = max(max(msg.TotalItems, 0), max(m.total, settled))
		if m.wasCancelled {
			m.current = min(max(m.current, settled), m.total)
		} else {
			m.current = m.total
		}
		m.duration = max(msg.Duration, 0)

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
	}

	return m, nil
}

// requestClose is idempotent while its asynchronous command is in flight.
// This keeps key auto-repeat from enqueueing multiple closes for one summary.
func (m ProgressModel) requestClose(triggerKey string) (ProgressModel, tea.Cmd) {
	if m.closePending {
		return m, nil
	}
	m.closePending = true
	owner := m.ownerGeneration
	return m, func() tea.Msg {
		return CloseOverlayMsg{triggerKey: triggerKey, ownerGeneration: owner}
	}
}

// View renders the progress view.
func (m ProgressModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	if width < 4 {
		return truncateForWidth("Progress", width)
	}

	spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	spinnerIdx := int(time.Now().UnixMilli()/100) % len(spinners)
	spinnerGlyph := spinners[spinnerIdx]
	if m.reducedMotion {
		spinnerGlyph = "●"
	}
	spinnerStyle := lipgloss.NewStyle().Foreground(ColorGradient1).Bold(true)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorHighlight)
	successStyle := lipgloss.NewStyle().Foreground(ColorSuccess).Bold(true)
	failStyle := lipgloss.NewStyle().Foreground(ColorError).Bold(true)
	warningStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	title := normalizeTimelineText(m.title)
	if title == "" {
		title = "Batch operation"
	}
	var lines []string
	appendLine := func(line string) {
		lines = append(lines, fitPanelContent(line, width))
	}

	if m.isComplete && m.wasCancelled {
		appendLine(warningStyle.Render("◌ " + title + " cancelled"))
	} else if m.isComplete && m.failureCount > 0 && m.successCount == 0 {
		appendLine(failStyle.Render("✗ " + title + " failed"))
	} else if m.isComplete && m.failureCount > 0 {
		appendLine(warningStyle.Render("⚠ " + title + " completed with issues"))
	} else if m.isComplete {
		appendLine(successStyle.Render("✓ " + title + " complete"))
	} else if m.isCancelling {
		appendLine(warningStyle.Render("◌ " + title + " · cancelling"))
	} else if m.isPaused {
		appendLine(warningStyle.Render("⏸ " + title + " paused"))
	} else {
		appendLine(spinnerStyle.Render(spinnerGlyph) + " " + headerStyle.Render(title))
	}

	total := max(m.total, 0)
	current := max(m.current, 0)
	if total > 0 {
		current = min(current, total)
	}
	elapsed := time.Duration(0)
	if m.isComplete {
		elapsed = max(m.duration, 0)
	} else if !m.startTime.IsZero() {
		elapsed = max(time.Since(m.startTime), 0)
	}

	barColor := ColorPrimary
	if m.isComplete {
		if m.wasCancelled || m.failureCount > 0 {
			barColor = ColorWarning
		} else {
			barColor = ColorSuccess
		}
	}
	filledStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorDim)
	if total > 0 {
		progress := min(max(float64(current)/float64(total), 0), 1)
		meta := fmt.Sprintf("%d/%d · %.0f%% · %s", current, total, progress*100, formatProgressDuration(elapsed))
		barWidth := min(40, max(width-lipgloss.Width(meta)-1, 0))
		if barWidth >= 4 {
			filled := min(max(int(progress*float64(barWidth)), 0), barWidth)
			bar := filledStyle.Render(strings.Repeat("▓", filled)) + emptyStyle.Render(strings.Repeat("░", barWidth-filled))
			appendLine(bar + " " + mutedStyle.Render(meta))
		} else {
			appendLine(mutedStyle.Render(meta))
		}
	} else {
		appendLine(mutedStyle.Render("Indeterminate · " + formatProgressDuration(elapsed)))
	}

	if !m.isComplete && !m.isCancelling && normalizeTimelineText(m.currentItem) != "" {
		itemStyle := lipgloss.NewStyle().Foreground(ColorAccent)
		appendLine(dimStyle.Render("Now · ") + itemStyle.Render(normalizeTimelineText(m.currentItem)))
	}
	if message := normalizeTimelineText(m.message); !m.isComplete && !m.isCancelling && message != "" {
		appendLine(lipgloss.NewStyle().Foreground(ColorText).Render(message))
	}

	settled := m.successCount + m.failureCount + m.skippedCount
	if settled > 0 || (m.isComplete && !m.wasCancelled) {
		skipStyle := lipgloss.NewStyle().Foreground(ColorWarning)
		stats := []string{successStyle.Render(fmt.Sprintf("✓ %d", max(m.successCount, 0)))}
		if m.failureCount > 0 {
			stats = append(stats, failStyle.Render(fmt.Sprintf("✗ %d", m.failureCount)))
		}
		if m.skippedCount > 0 {
			stats = append(stats, skipStyle.Render(fmt.Sprintf("⊘ %d", m.skippedCount)))
		}
		appendLine(strings.Join(stats, dimStyle.Render(" · ")))
	}

	footer := m.renderActions()
	itemBudget := min(5, len(m.items))
	showDivider := len(m.items) > 0
	showHiddenSummary := len(m.items) > itemBudget
	if m.height > 0 && len(m.items) > 0 {
		available := max(m.height-len(lines)-1, 0) // footer is mandatory
		maxVisible := min(5, len(m.items))
		switch {
		case len(m.items) <= maxVisible && 1+len(m.items) <= available:
			itemBudget = len(m.items)
			showDivider = true
			showHiddenSummary = false
		case available >= 2:
			// Divider + hidden summary are real rows. Reserve both before
			// selecting high-priority items so the summary is never the row
			// discarded by the final height guard.
			itemBudget = min(maxVisible, max(available-2, 0))
			showDivider = true
			showHiddenSummary = len(m.items) > itemBudget
		case available == 1:
			itemBudget = 0
			showDivider = false
			showHiddenSummary = true
		default:
			itemBudget = 0
			showDivider = false
			showHiddenSummary = false
		}
	}
	selected := selectProgressItems(m.items, itemBudget)
	if showDivider {
		appendLine(dimStyle.Render(strings.Repeat("─", width)))
	}
	for i, item := range m.items {
		if selected[i] {
			appendLine(m.formatItemLine(item, width))
		}
	}
	if hidden := len(m.items) - len(selected); showHiddenSummary && hidden > 0 {
		appendLine(dimStyle.Render(fmt.Sprintf("… %d earlier or lower-priority item(s)", hidden)))
	}
	appendLine(footer)
	if m.height > 0 && len(lines) > m.height {
		lines = append(lines[:max(m.height-1, 0)], lines[len(lines)-1])
	}
	return strings.Join(lines, "\n")
}

// formatItemLine formats a progress item for display.
func (m ProgressModel) formatItemLine(item ProgressItem, maxWidth int) string {
	var icon string
	var style lipgloss.Style

	switch item.Status {
	case ProgressStatusPending:
		icon = "○"
		style = lipgloss.NewStyle().Foreground(ColorMuted)
	case ProgressStatusInProgress:
		icon = "◐"
		style = lipgloss.NewStyle().Foreground(ColorAccent)
	case ProgressStatusCompleted:
		icon = "●"
		style = lipgloss.NewStyle().Foreground(ColorSuccess)
	case ProgressStatusFailed:
		icon = "✗"
		style = lipgloss.NewStyle().Foreground(ColorError)
	case ProgressStatusSkipped:
		icon = "⊘"
		style = lipgloss.NewStyle().Foreground(ColorWarning)
	default:
		icon = "•"
		style = lipgloss.NewStyle().Foreground(ColorMuted)
	}

	name := normalizeTimelineText(item.Name)
	if name == "" {
		name = "Untitled item"
	}
	line := fmt.Sprintf("%s %s", icon, style.Render(name))
	if errText := normalizeTimelineText(item.Error); errText != "" {
		errStyle := lipgloss.NewStyle().Foreground(ColorError)
		line += " · " + errStyle.Render(errText)
	} else if message := normalizeTimelineText(item.Message); message != "" {
		msgStyle := lipgloss.NewStyle().Foreground(ColorMuted)
		line += " · " + msgStyle.Render(message)
	}
	return fitPanelContent(line, maxWidth)
}

// renderActions renders the available actions.
func (m ProgressModel) renderActions() string {
	hintStyle := lipgloss.NewStyle().Foreground(ColorDim)
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Bold(true)

	if m.isComplete && m.closePending {
		return hintStyle.Render("Closing…")
	}
	if m.isComplete {
		return keyStyle.Render("Enter/Esc") + hintStyle.Render(" Close")
	}
	if m.isCancelling {
		return hintStyle.Render("Cancellation requested · waiting for the operation to stop…")
	}
	if m.onAction != nil {
		if m.isPaused && !m.pauseActionReadable() {
			// A resize can make Resume unreadable after Pause was chosen. Keep p
			// fail-closed and replace it with an explicit recovery instruction.
			switch {
			case m.width <= 0 || m.width >= lipgloss.Width("↔ Resize  ·  Esc Cancel"):
				return keyStyle.Render("↔") + hintStyle.Render(" Resize  ·  ") + keyStyle.Render("Esc") + hintStyle.Render(" Cancel")
			case m.width >= lipgloss.Width("↔ Resize · Esc"):
				return keyStyle.Render("↔") + hintStyle.Render(" Resize · ") + keyStyle.Render("Esc")
			case m.width >= lipgloss.Width("↔ Resize"):
				return keyStyle.Render("↔") + hintStyle.Render(" Resize")
			default:
				return keyStyle.Render("↔")
			}
		}
		hints := []string{
			keyStyle.Render("Esc") + " Cancel",
		}
		if m.pauseActionReadable() {
			if m.isPaused {
				hints = append(hints, keyStyle.Render("p")+" Resume")
			} else {
				hints = append(hints, keyStyle.Render("p")+" Pause")
			}
		}
		return hintStyle.Render(strings.Join(hints, "  │  "))
	}
	return hintStyle.Render("Please wait…")
}

// pauseActionReadable mirrors the actual footer after width fitting. Unknown
// geometry remains the standalone/headless sentinel; once a positive width is
// known, p cannot mutate state unless both sides of the reversible action fit.
// Using the longer Resume label while still running prevents a narrow pane
// from offering Pause and then hiding the only way to continue.
func (m ProgressModel) pauseActionReadable() bool {
	if m.width <= 0 || m.height <= 0 {
		return true
	}
	if m.height < 2 {
		return false
	}
	return m.width >= lipgloss.Width("Esc Cancel  │  p Resume")
}

func selectProgressItems(items []ProgressItem, budget int) map[int]bool {
	selected := make(map[int]bool, min(max(budget, 0), len(items)))
	if budget <= 0 {
		return selected
	}
	for _, status := range []ProgressStatus{ProgressStatusInProgress, ProgressStatusFailed} {
		for i, item := range items {
			if len(selected) >= budget {
				return selected
			}
			if item.Status == status {
				selected[i] = true
			}
		}
	}
	for i := len(items) - 1; i >= 0 && len(selected) < budget; i-- {
		selected[i] = true
	}
	return selected
}

// IsComplete returns whether the progress is complete.
func (m ProgressModel) IsComplete() bool {
	return m.isComplete
}

// GetStats returns the completion statistics.
func (m ProgressModel) GetStats() (success, failure, skipped int) {
	return m.successCount, m.failureCount, m.skippedCount
}

// formatProgressDuration formats a duration for progress display.
func formatProgressDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}
