package ui

import (
	"fmt"
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
	m.toolName = msg.Name
	m.elapsed = msg.Elapsed
	m.progress = msg.Progress
	m.currentStep = msg.CurrentStep
	m.totalBytes = msg.TotalBytes
	m.processedBytes = msg.ProcessedBytes
	m.cancellable = msg.Cancellable
	m.visible = true
	m.lastUpdateTime = time.Now()
}

// Show makes the progress bar visible.
func (m *ToolProgressBarModel) Show(toolName string) {
	m.toolName = toolName
	m.visible = true
	m.progress = -1 // start indeterminate
	m.currentStep = ""
	m.elapsed = 0
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
}

// IsVisible returns whether the progress bar is visible.
func (m *ToolProgressBarModel) IsVisible() bool {
	return m.visible
}

// IsCancellable returns whether the current operation can be cancelled.
func (m *ToolProgressBarModel) IsCancellable() bool {
	return m.cancellable
}

// Tick advances the animation frame and returns a command for the next tick.
// Also checks for stale progress and auto-hides if no updates for too long.
func (m *ToolProgressBarModel) Tick() tea.Cmd {
	if m.visible {
		m.frame++

		// Auto-hide if no updates for too long (tool might have crashed)
		if !m.lastUpdateTime.IsZero() && time.Since(m.lastUpdateTime) > progressBarStaleTimeout {
			m.Hide()
		}
	}
	return nil
}

// spinnerFrames contains braille spinner animation frames.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// View renders the progress bar as a compact single line.
func (m *ToolProgressBarModel) View(width int) string {
	if !m.visible || m.toolName == "" {
		return ""
	}

	// Styles
	dimStyle := lipgloss.NewStyle().
		Foreground(ColorDim)

	accentStyle := lipgloss.NewStyle().
		Foreground(ColorHighlight)

	progressStyle := lipgloss.NewStyle().
		Foreground(ColorSuccess)

	// Animated spinner
	spinnerIdx := m.frame % len(spinnerFrames)
	spinner := accentStyle.Render(spinnerFrames[spinnerIdx])

	// Tool name
	toolName := capitalizeToolName(m.toolName)

	// Time elapsed
	elapsedStr := formatElapsed(m.elapsed)

	// Build compact single line
	var builder strings.Builder
	builder.WriteString(spinner)
	builder.WriteString(" ")
	builder.WriteString(dimStyle.Render(toolName))

	// Progress indicator
	if m.progress >= 0 {
		// Determinate: show percentage with mini bar
		percent := int(m.progress * 100)
		barWidth := 20
		filled := int(m.progress * float64(barWidth))
		if filled > barWidth {
			filled = barWidth
		}
		miniBar := strings.Repeat("━", filled) + strings.Repeat("─", barWidth-filled)
		builder.WriteString(" ")
		builder.WriteString(progressStyle.Render(miniBar))
		builder.WriteString(dimStyle.Render(fmt.Sprintf(" %d%%", percent)))
	} else {
		// Indeterminate: just show running dots
		dots := strings.Repeat(".", (m.frame/3)%4)
		builder.WriteString(dimStyle.Render(dots))
		// Pad to keep width stable
		builder.WriteString(strings.Repeat(" ", 3-len(dots)))
	}

	// Current step — single line of output renders inline to keep compact.
	// Multi-line output (e.g. `npm install` progress) falls through to the
	// history block below so the user sees recent activity, not just the
	// last line, and can tell whether the tool is actually making progress.
	recent := lastNNonEmptyLines(m.currentStep, maxProgressHistoryLines)
	if len(recent) == 1 {
		step := recent[0]
		// Rune-aware truncation so multibyte chars (emoji, Cyrillic) don't
		// get sliced mid-byte and render as replacement chars.
		const maxRunes = 60
		runes := []rune(step)
		if len(runes) > maxRunes {
			step = string(runes[:maxRunes-1]) + "…"
		}
		builder.WriteString(" ")
		builder.WriteString(dimStyle.Render(step))
	}

	// Bytes if available
	if m.totalBytes > 0 {
		builder.WriteString(dimStyle.Render(fmt.Sprintf(" %s/%s",
			format.Bytes(m.processedBytes), format.Bytes(m.totalBytes))))
	} else if m.processedBytes > 0 {
		builder.WriteString(dimStyle.Render(fmt.Sprintf(" %s", format.Bytes(m.processedBytes))))
	}

	// Elapsed time (right side)
	builder.WriteString(" ")
	builder.WriteString(dimStyle.Render(elapsedStr))

	// Multi-line progress history — only when there's >1 line of output.
	// Renders as a tiny indented block below the main progress line so the
	// user sees recent activity without losing the compact single-line
	// style for simple tools.
	if len(recent) > 1 {
		const maxRunesPerLine = 70
		for _, line := range recent {
			runes := []rune(line)
			if len(runes) > maxRunesPerLine {
				line = string(runes[:maxRunesPerLine-1]) + "…"
			}
			builder.WriteString("\n")
			builder.WriteString(dimStyle.Render("    " + line))
		}
	}

	return builder.String()
}

// maxProgressHistoryLines is how many recent non-empty lines of tool output
// the progress bar shows as a history block. Small enough to avoid pushing
// the input off-screen on short terminals; big enough to give context.
const maxProgressHistoryLines = 3

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
		trimmed := strings.TrimSpace(rawLines[i])
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

// lastNonEmptyLine returns the last non-empty line from s, trimmed.
// Used to surface the most recent progress output from multiline tool stdout.
func lastNonEmptyLine(s string) string {
	s = strings.TrimRight(s, "\n\r \t")
	if s == "" {
		return ""
	}
	if idx := strings.LastIndexAny(s, "\n\r"); idx >= 0 {
		return strings.TrimSpace(s[idx+1:])
	}
	return strings.TrimSpace(s)
}
