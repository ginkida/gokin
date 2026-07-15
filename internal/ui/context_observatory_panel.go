package ui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ContextObservatoryPanel displays a technical dashboard of agent context health.
type ContextObservatoryPanel struct {
	visible bool
	health  ContextHealthMsg
	styles  *Styles
	frame   int // For animations
}

// NewContextObservatoryPanel creates a new context observatory panel.
func NewContextObservatoryPanel(styles *Styles) *ContextObservatoryPanel {
	return &ContextObservatoryPanel{
		visible: false,
		styles:  styles,
	}
}

// UpdateHealth updates the health snapshot displayed in the panel.
func (p *ContextObservatoryPanel) UpdateHealth(health ContextHealthMsg) {
	health.ActiveFiles = append([]string(nil), health.ActiveFiles...)
	for i := range health.ActiveFiles {
		health.ActiveFiles[i] = safeKeyEntryText(health.ActiveFiles[i])
	}
	health.PruningAlert = safeKeyEntryText(health.PruningAlert)
	p.health = health
}

// Show shows the panel.
func (p *ContextObservatoryPanel) Show() {
	p.visible = true
}

// Hide hides the panel.
func (p *ContextObservatoryPanel) Hide() {
	p.visible = false
}

// Toggle toggles the panel visibility.
func (p *ContextObservatoryPanel) Toggle() {
	p.visible = !p.visible
}

// IsVisible returns whether the panel is visible.
func (p *ContextObservatoryPanel) IsVisible() bool {
	return p.visible
}

// Tick updates the animation frame.
func (p *ContextObservatoryPanel) Tick() {
	p.frame++
}

// View renders the context observatory panel.
func (p *ContextObservatoryPanel) View(width int, heights ...int) string {
	if !p.visible {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	if width < 4 {
		return truncateForWidth("Context Observatory", width)
	}
	requestedHeight := 0
	if len(heights) > 0 {
		requestedHeight = heights[0]
	}

	// Styles
	borderStyle := lipgloss.NewStyle().Foreground(ColorPlan)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPlan)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	accentStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)

	var content strings.Builder
	outerWidth := min(width, 120)
	innerWidth := outerWidth - 2
	separator := func(left, fill, right string) {
		content.WriteString(borderStyle.Render(left + strings.Repeat(fill, innerWidth) + right))
		content.WriteString("\n")
	}
	separator("╭", "─", "╮")
	p.writeBoxLine(&content, borderStyle, " "+MessageIcons["info"]+" Context Observatory", titleStyle, innerWidth)
	separator("├", "─", "┤")

	total := max(p.health.TotalTokens, 0)
	maximum := max(p.health.MaxTokens, 0)
	system := max(p.health.SystemTokens, 0)
	instructions := max(p.health.InstructionTokens, 0)
	history := max(p.health.HistoryTokens, 0)
	tools := max(p.health.ToolTokens, 0)
	usage := p.normalizedUsage(total, maximum)
	healthSummary, healthColor := contextHealthSummary(usage, maximum > 0, p.health.PruningAlert)
	hasContext := total > 0 || maximum > 0 || system > 0 || instructions > 0 || history > 0 || tools > 0 || len(p.health.ActiveFiles) > 0 || p.health.PruningAlert != "" || !p.health.LastPruningTime.IsZero()
	hasRate := p.health.RequestsLimit > 0 || p.health.RequestsRemaining > 0 || p.health.TokensLimit > 0 || p.health.TokensRemaining > 0
	if requestedHeight > 0 && requestedHeight < 7 {
		// A bordered dashboard cannot fit in fewer than seven rows. Return a
		// truthful one-line recovery summary instead of relying on the parent to
		// tail-crop seven rows (which hid the title and health state).
		summary := "Context · Esc/Ctrl+H close"
		if hasContext {
			summary += " · " + healthSummary
		} else if hasRate {
			summary += " · provider limits available"
		} else {
			summary += " · no data yet"
		}
		return truncateForWidth(summary, outerWidth)
	}
	if !hasContext && !hasRate {
		p.writeBoxLine(&content, borderStyle, "No context health data yet", mutedStyle.Italic(true), innerWidth)
		// Seven rows are the irreducible bordered empty state. The explanatory
		// line is useful breathing room, but must yield before recovery/footer.
		if requestedHeight <= 0 || requestedHeight >= 9 {
			p.writeBoxLine(&content, borderStyle, "Metrics appear after the first request.", dimStyle, innerWidth)
		}
		separator("├", "─", "┤")
		p.writeBoxLine(&content, borderStyle, "Esc / Ctrl+H  Close", mutedStyle, innerWidth)
		content.WriteString(borderStyle.Render("╰" + strings.Repeat("─", innerWidth) + "╯"))
		return content.String()
	}
	if requestedHeight > 0 && requestedHeight < 24 {
		// Short-terminal mode keeps the dashboard actionable instead of letting
		// the parent compositor crop its footer. Structural rows consume six:
		// top/title/separator + separator/footer/bottom.
		targetHeight := max(requestedHeight-1, 7)
		rowBudget := max(targetHeight-6, 1)
		var rows []string
		if hasContext {
			// Health is the decision-driving signal. Keep it first so an alert or
			// unknown limit survives even when only one compact data row fits.
			rows = append(rows, "Status: "+healthSummary)
			if maximum > 0 {
				rows = append(rows, fmt.Sprintf("Context: %d/%d tokens · %.0f%%", total, maximum, usage*100))
			} else {
				rows = append(rows, fmt.Sprintf("Context: %d tokens · limit unavailable", total))
			}
		}
		if hasRate {
			rows = append(rows, p.compactRateSummary())
		}
		if len(p.health.ActiveFiles) > 0 {
			rows = append(rows, fmt.Sprintf("Active files: %d", len(p.health.ActiveFiles)))
		}
		for _, row := range rows[:min(len(rows), rowBudget)] {
			style := dimStyle
			if strings.HasPrefix(row, "Status:") {
				style = lipgloss.NewStyle().Foreground(healthColor).Bold(healthColor == ColorError)
			}
			p.writeBoxLine(&content, borderStyle, row, style, innerWidth)
		}
		separator("├", "─", "┤")
		p.writeBoxLine(&content, borderStyle, "Esc / Ctrl+H  Close", mutedStyle, innerWidth)
		content.WriteString(borderStyle.Render("╰" + strings.Repeat("─", innerWidth) + "╯"))
		return content.String()
	}

	if hasContext {
		p.writeBoxLine(&content, borderStyle, "Budget & Usage", accentStyle, innerWidth)
		if maximum > 0 {
			usageLine := fmt.Sprintf("%d / %d tokens (%.1f%%)", total, maximum, usage*100)
			p.writeBoxLine(&content, borderStyle, usageLine, titleStyle, innerWidth)
			p.writeBoxLine(&content, borderStyle, p.renderUsageGauge(usage, innerWidth), titleStyle, innerWidth)
		} else {
			p.writeBoxLine(&content, borderStyle, fmt.Sprintf("%d tokens · limit unavailable", total), dimStyle, innerWidth)
		}
		breakdown := fmt.Sprintf("System %d · Instructions %d · History %d · Tools %d", system, instructions, history, tools)
		p.writeBoxLine(&content, borderStyle, breakdown, dimStyle, innerWidth)

		separator("├", "─", "┤")
		p.writeBoxLine(&content, borderStyle, "Active Files", accentStyle, innerWidth)
		if len(p.health.ActiveFiles) == 0 {
			p.writeBoxLine(&content, borderStyle, "No files in active context", mutedStyle, innerWidth)
		} else {
			const maxVisibleFiles = 5
			end := min(len(p.health.ActiveFiles), maxVisibleFiles)
			for _, file := range p.health.ActiveFiles[:end] {
				file = safeKeyEntryText(file)
				if file == "" {
					file = "(unnamed file)"
				}
				prefix := MessageIcons["hint"] + " "
				file = truncateLeftForWidth(file, max(innerWidth-lipgloss.Width(prefix), 1))
				p.writeBoxLine(&content, borderStyle, prefix+file, lipgloss.NewStyle().Foreground(ColorText), innerWidth)
			}
			if hidden := len(p.health.ActiveFiles) - end; hidden > 0 {
				p.writeBoxLine(&content, borderStyle, fmt.Sprintf("… %d more files", hidden), mutedStyle.Italic(true), innerWidth)
			}
		}
	}

	separator("├", "─", "┤")
	p.writeBoxLine(&content, borderStyle, "Provider Limits", accentStyle, innerWidth)
	p.writeRateLimit(&content, borderStyle, "Requests", p.health.RequestsRemaining, p.health.RequestsLimit, innerWidth, dimStyle)
	p.writeRateLimit(&content, borderStyle, "Tokens", p.health.TokensRemaining, p.health.TokensLimit, innerWidth, dimStyle)

	if hasContext {
		separator("├", "─", "┤")
		p.writeBoxLine(&content, borderStyle, "Context Health", accentStyle, innerWidth)
		lastPruning := "Not yet"
		if !p.health.LastPruningTime.IsZero() {
			elapsed := max(time.Since(p.health.LastPruningTime), 0)
			lastPruning = fmt.Sprintf("%s ago", elapsed.Round(time.Second))
		}
		p.writeBoxLine(&content, borderStyle, "Last pruning: "+lastPruning, dimStyle, innerWidth)
		statusStyle := lipgloss.NewStyle().Foreground(healthColor)
		if healthColor == ColorError {
			statusStyle = statusStyle.Bold(true)
		}
		p.writeBoxLine(&content, borderStyle, "Status: "+healthSummary, statusStyle, innerWidth)
	}

	separator("├", "─", "┤")
	p.writeBoxLine(&content, borderStyle, "Esc / Ctrl+H  Close", mutedStyle, innerWidth)
	content.WriteString(borderStyle.Render("╰" + strings.Repeat("─", innerWidth) + "╯"))

	return content.String()
}

func (p *ContextObservatoryPanel) writeBoxLine(content *strings.Builder, borderStyle lipgloss.Style, text string, textStyle lipgloss.Style, innerWidth int) {
	content.WriteString(borderStyle.Render("│"))
	text = truncateForWidth(strings.Join(strings.Fields(text), " "), innerWidth)
	rendered := textStyle.Render(text)
	content.WriteString(rendered)
	padding := innerWidth - lipgloss.Width(rendered)
	if padding > 0 {
		content.WriteString(strings.Repeat(" ", padding))
	}
	content.WriteString(borderStyle.Render("│"))
	content.WriteString("\n")
}

func (p *ContextObservatoryPanel) renderGauge(percent float64, width int) string {
	if width <= 0 {
		return ""
	}
	if math.IsNaN(percent) || math.IsInf(percent, 0) {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}

	filled := int((percent / 100.0) * float64(width))

	var color lipgloss.Color = ColorSuccess
	if percent < 15 {
		color = ColorError
	} else if percent < 40 {
		color = ColorWarning
	}

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	return lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", width-filled))
}

func (p *ContextObservatoryPanel) normalizedUsage(total, maximum int) float64 {
	usage := p.health.PercentUsed
	if math.IsNaN(usage) || math.IsInf(usage, 0) || usage <= 0 {
		if maximum > 0 {
			usage = float64(total) / float64(maximum)
		} else {
			usage = 0
		}
	}
	return min(max(usage, 0), 1)
}

func (p *ContextObservatoryPanel) renderUsageGauge(usage float64, width int) string {
	usage = min(max(usage, 0), 1)
	label := fmt.Sprintf("%.0f%%", usage*100)
	barWidth := max(width-lipgloss.Width(label)-1, 0)
	if barWidth == 0 {
		return label
	}
	color := contextUsageColor(usage)
	filled := min(max(int(usage*float64(barWidth)), 0), barWidth)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	bar := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", barWidth-filled))
	return bar + " " + label
}

func contextUsageColor(usage float64) lipgloss.Color {
	usage = min(max(usage, 0), 1)
	if usage >= .90 {
		return ColorError
	}
	if usage >= .75 {
		return ColorWarning
	}
	return ColorSuccess
}

func contextHealthSummary(usage float64, hasLimit bool, alert string) (string, lipgloss.Color) {
	if alert = safeKeyEntryText(alert); alert != "" {
		return alert, ColorError
	}
	if !hasLimit {
		return "Context limit unavailable", ColorMuted
	}
	if usage >= .90 {
		return "Context critical · pruning likely soon", contextUsageColor(usage)
	}
	if usage >= .75 {
		return "Context pressure elevated", contextUsageColor(usage)
	}
	return "Context healthy", contextUsageColor(usage)
}

func (p *ContextObservatoryPanel) writeRateLimit(content *strings.Builder, borderStyle lipgloss.Style, label string, remaining, limit int64, width int, style lipgloss.Style) {
	remaining = max(remaining, 0)
	limit = max(limit, 0)
	if limit == 0 {
		value := "unavailable"
		if remaining > 0 {
			value = fmt.Sprintf("%d remaining · limit unavailable", remaining)
		}
		p.writeBoxLine(content, borderStyle, label+": "+value, style, width)
		return
	}
	remaining = min(remaining, limit)
	percent := float64(remaining) / float64(limit) * 100
	value := fmt.Sprintf("%s: %d / %d", label, remaining, limit)
	gaugeWidth := max(width-lipgloss.Width(value)-1, 0)
	if gaugeWidth > 0 {
		value += " " + p.renderGauge(percent, gaugeWidth)
	}
	p.writeBoxLine(content, borderStyle, value, style, width)
}

func (p *ContextObservatoryPanel) compactRateSummary() string {
	formatValue := func(remaining, limit int64) string {
		remaining = max(remaining, 0)
		limit = max(limit, 0)
		if limit == 0 {
			if remaining > 0 {
				return fmt.Sprintf("%d/?", remaining)
			}
			return "n/a"
		}
		return fmt.Sprintf("%d/%d", min(remaining, limit), limit)
	}
	return fmt.Sprintf("Limits: requests %s · tokens %s",
		formatValue(p.health.RequestsRemaining, p.health.RequestsLimit),
		formatValue(p.health.TokensRemaining, p.health.TokensLimit))
}
