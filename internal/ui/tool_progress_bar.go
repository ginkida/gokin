package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"gokin/internal/format"
)

const (
	// progressBarStaleTimeout is the time after which progress bar auto-hides if no updates
	progressBarStaleTimeout = 30 * time.Second
)

// ToolProgressBarModel displays a progress bar for long-running tool execution.
type ToolProgressBarModel struct {
	toolName       string
	progress       float64 // 0.0-1.0, -1 for indeterminate
	currentStep    string
	elapsed        time.Duration
	totalBytes     int64
	processedBytes int64
	cancellable    bool
	visible        bool
	frame          int // for indeterminate animation
	reducedMotion  bool
	styles         *Styles
	lastUpdateTime time.Time // for auto-hide on stale progress
}

// NewToolProgressBarModel creates a new progress bar model.
func NewToolProgressBarModel(styles *Styles) *ToolProgressBarModel {
	return &ToolProgressBarModel{
		styles:   styles,
		progress: -1, // indeterminate by default
	}
}

// Update updates the progress bar from a ToolProgressMsg.
func (m *ToolProgressBarModel) Update(msg ToolProgressMsg) {
	name := safeKeyEntryText(msg.Name)
	nameChanged := name != "" && m.toolName != "" && name != m.toolName
	if name != "" {
		m.toolName = name
	}
	incomingElapsed := max(msg.Elapsed, 0)
	// Detailed progress messages do not carry elapsed time. Preserve the last
	// heartbeat for the same operation instead of making the timer jump to 0s;
	// a different named operation starts a fresh clock.
	if nameChanged {
		m.elapsed = 0
	}
	if incomingElapsed > 0 || m.elapsed == 0 {
		m.elapsed = incomingElapsed
	}
	if math.IsNaN(msg.Progress) || msg.Progress < 0 {
		m.progress = -1
	} else {
		m.progress = min(max(msg.Progress, 0), 1)
	}
	m.currentStep = msg.CurrentStep
	m.totalBytes = max(msg.TotalBytes, 0)
	m.processedBytes = max(msg.ProcessedBytes, 0)
	if m.totalBytes > 0 {
		m.processedBytes = min(m.processedBytes, m.totalBytes)
	}
	m.cancellable = msg.Cancellable
	m.visible = true
	m.lastUpdateTime = time.Now()
}

// Show makes the progress bar visible.
func (m *ToolProgressBarModel) Show(toolName string) {
	m.toolName = safeKeyEntryText(toolName)
	m.visible = true
	m.progress = -1 // start indeterminate
	m.currentStep = ""
	m.elapsed = 0
	m.totalBytes = 0
	m.processedBytes = 0
	m.cancellable = false
	m.frame = 0
	m.lastUpdateTime = time.Now()
}

// Hide hides the progress bar.
func (m *ToolProgressBarModel) Hide() {
	m.visible = false
	m.progress = -1
	m.currentStep = ""
	m.toolName = ""
	m.elapsed = 0
	m.totalBytes = 0
	m.processedBytes = 0
	m.cancellable = false
	m.frame = 0
	m.lastUpdateTime = time.Time{}
}

// IsVisible returns whether the progress bar is visible.
func (m *ToolProgressBarModel) IsVisible() bool {
	return m.visible
}

// HasDistinctSignal reports whether the bar carries information the live
// activity card's tool line does not already show: a determinate progress
// fraction, byte counts, or a named step. An INDETERMINATE bar with none of
// those renders the same spinner + tool name + elapsed the card already
// shows one row above (field report: «⠋ Bash — $ go test … · 23.5s» over
// «⠏ Bash 23.5s go test…») — the View suppresses it in that case, same
// one-activity-surface-per-frame rule as feed-vs-tree and file-peek.
func (m *ToolProgressBarModel) HasDistinctSignal() bool {
	return m.progress >= 0 || m.totalBytes > 0 || strings.TrimSpace(m.currentStep) != ""
}

// IsCancellable returns whether the current operation can be cancelled.
func (m *ToolProgressBarModel) IsCancellable() bool {
	return m.cancellable
}

// Tick advances the animation frame and returns a command for the next tick.
// Also checks for stale progress and auto-hides if no updates for too long.
func (m *ToolProgressBarModel) Tick() tea.Cmd {
	if m.visible {
		if !m.reducedMotion {
			m.frame++
		}

		// Auto-hide if no updates for too long (tool might have crashed)
		if !m.lastUpdateTime.IsZero() && time.Since(m.lastUpdateTime) > progressBarStaleTimeout {
			m.Hide()
		}
	}
	return nil
}

// SetReducedMotion keeps progress semantics while replacing the animated
// spinner with a stable activity marker.
func (m *ToolProgressBarModel) SetReducedMotion(enabled bool) {
	m.reducedMotion = enabled
}

// spinnerFrames contains braille spinner animation frames.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// View renders the progress bar as a compact line with optional recent output.
// A supplied terminal height bounds that history before the parent compositor
// has to crop it.
func (m *ToolProgressBarModel) View(width int, heights ...int) string {
	if !m.visible {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	height := 0
	if len(heights) > 0 {
		height = heights[0]
	}
	maxRows := toolProgressHeightBudget(height)

	// Styles
	dimStyle := lipgloss.NewStyle().
		Foreground(ColorDim)

	accentStyle := lipgloss.NewStyle().
		Foreground(ColorHighlight)

	progressStyle := lipgloss.NewStyle().
		Foreground(ColorSuccess)

	// Animated spinner
	spinnerGlyph := spinnerFrames[m.frame%len(spinnerFrames)]
	if m.reducedMotion {
		spinnerGlyph = "●"
	}
	spinner := accentStyle.Render(spinnerGlyph)

	// Tool name
	toolName := capitalizeToolName(strings.TrimSpace(m.toolName))
	if toolName == "" {
		toolName = "Tool"
	}

	// Time elapsed
	elapsedStr := formatElapsed(m.elapsed)

	// Build the primary line by priority. Cancellation is placed before
	// secondary details so it cannot disappear behind a long step message.
	var builder strings.Builder
	appendPart := func(raw string, style lipgloss.Style) {
		remaining := width - lipgloss.Width(builder.String())
		if remaining <= 0 || raw == "" {
			return
		}
		fitted := raw
		if lipgloss.Width(fitted) > remaining {
			if strings.Contains(fitted, "\x1b") {
				fitted = fitStatusText(fitted, remaining)
			} else {
				// Runtime step text often contains emoji and combining marks. Keep
				// whole grapheme clusters when the volatile tail is elided.
				fitted = truncateMiddleForWidth(fitted, remaining)
			}
		}
		builder.WriteString(style.Render(fitted))
	}
	if m.cancellable {
		cancelLabel := " · Esc cancel"
		if width < lipgloss.Width(spinner)+lipgloss.Width(cancelLabel) {
			return dimStyle.Render(compactToolCancelLabel(width))
		}
		appendPart(spinner, lipgloss.NewStyle())
		nameBudget := max(width-lipgloss.Width(spinner)-lipgloss.Width(cancelLabel), 0)
		appendPart(truncateForWidth(" "+toolName, nameBudget), dimStyle)
		appendPart(cancelLabel, dimStyle)
	} else {
		appendPart(spinner, lipgloss.NewStyle())
		appendPart(" "+toolName, dimStyle)
	}

	// Progress indicator
	if m.progress >= 0 {
		// Determinate: show a width-aware mini bar and clamped percentage.
		percent := int(m.progress * 100)
		remaining := width - lipgloss.Width(builder.String())
		percentLabel := fmt.Sprintf(" %d%%", percent)
		barWidth := min(20, max(remaining-lipgloss.Width(percentLabel)-2, 0))
		filled := int(m.progress * float64(barWidth))
		if barWidth >= 4 {
			appendPart(" "+strings.Repeat("━", filled)+strings.Repeat("─", barWidth-filled), progressStyle)
		}
		appendPart(percentLabel, dimStyle)
	} else {
		// Indeterminate: just show running dots
		dots := strings.Repeat(".", (m.frame/3)%4)
		appendPart(dots+strings.Repeat(" ", 3-len(dots)), dimStyle)
	}

	// Stable facts precede volatile step text, which is truncated last.
	if m.totalBytes > 0 {
		appendPart(fmt.Sprintf(" %s/%s", format.Bytes(m.processedBytes), format.Bytes(m.totalBytes)), dimStyle)
	} else if m.processedBytes > 0 {
		appendPart(fmt.Sprintf(" %s", format.Bytes(m.processedBytes)), dimStyle)
	}
	appendPart(" "+elapsedStr, dimStyle)

	// Current step — single line of output renders inline to keep compact.
	// Multi-line output (e.g. `npm install` progress) falls through to the
	// history block below so the user sees recent activity, not just the
	// last line, and can tell whether the tool is actually making progress.
	historyRowsBudget := maxProgressHistoryLines + 1 // heading + body rows
	if maxRows > 0 {
		historyRowsBudget = min(historyRowsBudget, max(maxRows-1, 0))
	}
	recent := lastNNonEmptyLines(m.currentStep, maxProgressHistoryLines)
	if len(recent) == 1 {
		step := recent[0]
		appendPart(" "+step, dimStyle)
	}

	// Multi-line progress history — only when there's >1 line of output.
	// Renders as a tiny indented block below the main progress line so the
	// user sees recent activity without losing the compact single-line
	// style for simple tools.
	if len(recent) > 1 {
		indent := strings.Repeat(" ", min(4, max(width-1, 0)))
		if historyRowsBudget == 1 {
			// At two total rows the newest output is more useful than a heading
			// with no content beneath it.
			builder.WriteString("\n")
			builder.WriteString(dimStyle.Render(truncateMiddleForWidth(recent[len(recent)-1], width)))
		} else if historyRowsBudget >= 2 {
			builder.WriteString("\n")
			builder.WriteString(dimStyle.Render(truncateMiddleForWidth(indent+"Recent output", width)))
			visible := recent[max(len(recent)-(historyRowsBudget-1), 0):]
			for _, line := range visible {
				builder.WriteString("\n")
				builder.WriteString(dimStyle.Render(indent + truncateMiddleForWidth(line, max(width-lipgloss.Width(indent), 0))))
			}
		}
	}

	return builder.String()
}

// compactToolCancelLabel preserves a recognizable recovery action even when
// there is not enough room for the full progress row. A lone ellipsis—the old
// one/two-column rendering—looked like truncated status rather than a key.
func compactToolCancelLabel(width int) string {
	for _, label := range []string{"Esc cancel", "Esc", "⎋"} {
		if lipgloss.Width(label) <= width {
			return label
		}
	}
	return ""
}

// maxProgressHistoryLines is how many recent non-empty lines of tool output
// the progress bar shows as a history block. Small enough to avoid pushing
// the input off-screen on short terminals; big enough to give context.
const maxProgressHistoryLines = 3

// toolProgressHeightBudget reserves five terminal rows for the composer and
// status chrome. The progress surface itself needs one row and may grow to the
// existing five-row maximum when the terminal has room.
func toolProgressHeightBudget(terminalHeight int) int {
	if terminalHeight <= 0 {
		return 0
	}
	return min(terminalHeight, min(max(terminalHeight-5, 1), maxProgressHistoryLines+2))
}

// lastNNonEmptyLines returns up to n trailing non-empty lines from s,
// oldest→newest. Empty input yields an empty slice. Used by the tool
// progress bar to render recent output as a short history block.
func lastNNonEmptyLines(s string, n int) []string {
	if s == "" || n <= 0 {
		return nil
	}
	// Split on any line terminator.
	rawLines := strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r'
	})
	out := make([]string, 0, n)
	for i := len(rawLines) - 1; i >= 0 && len(out) < n; i-- {
		trimmed := safeKeyEntryText(rawLines[i])
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	// Reverse so output is oldest→newest (reading order).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
