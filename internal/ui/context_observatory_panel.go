package ui

import (
	"fmt"
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
func (p *ContextObservatoryPanel) View(width int) string {
	if !p.visible {
		return ""
	}

	// Styles
	borderStyle := lipgloss.NewStyle().Foreground(ColorPlan)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPlan)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	errorStyle := lipgloss.NewStyle().Foreground(ColorError)

	var content strings.Builder

	// Panel width
	panelWidth := width - 10
	if panelWidth < 60 {
		panelWidth = 60
	}
	if panelWidth > 120 {
		panelWidth = 120
	}

	// Header
	header := " " + MessageIcons["info"] + " Context Observatory "
	headerLine := titleStyle.Render(header)

	content.WriteString(borderStyle.Render("╭") + borderStyle.Render(strings.Repeat("─", panelWidth-1)) + borderStyle.Render("╮"))
	content.WriteString("\n")

	// Title line
	titleBar := "  " + headerLine
	content.WriteString(borderStyle.Render("│"))
	content.WriteString(titleBar)
	padding := panelWidth - 1 - lipgloss.Width(titleBar)
	if padding > 0 {
		content.WriteString(strings.Repeat(" ", padding))
	}
	content.WriteString(borderStyle.Render("│"))
	content.WriteString("\n")

	content.WriteString(borderStyle.Render("├") + borderStyle.Render(strings.Repeat("─", panelWidth-1)) + borderStyle.Render("┤"))
	content.WriteString("\n")

	// 1. Token Budget Section
	p.writeBoxLine(&content, borderStyle, "Budget & Usage", lipgloss.NewStyle().Bold(true).Foreground(ColorAccent), panelWidth)

	usagePercent := p.health.PercentUsed
	barWidth := panelWidth - 30
	filled := int(usagePercent * float64(barWidth))
	if filled > barWidth {
		filled = barWidth
	}

	var barColor lipgloss.Color = ColorSuccess
	if usagePercent > 0.90 {
		barColor = ColorError
	} else if usagePercent > 0.70 {
		barColor = ColorWarning
	}

	progressBar := lipgloss.NewStyle().Foreground(barColor).Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", barWidth-filled))

	usageText := fmt.Sprintf("  %d / %d tokens (%.1f%%)", p.health.TotalTokens, p.health.MaxTokens, usagePercent*100)
	p.writeBoxLine(&content, borderStyle, usageText+"  "+progressBar, titleStyle, panelWidth)

	content.WriteString(borderStyle.Render("│") + strings.Repeat(" ", panelWidth-1) + borderStyle.Render("│") + "\n")

	// 2. Token Breakdown
	p.writeBoxLine(&content, borderStyle, " Breakdown", lipgloss.NewStyle().Bold(true).Foreground(ColorAccent), panelWidth)

	breakdownWidth := (panelWidth - 8) / 3
	sysText := fmt.Sprintf("System: %d", p.health.SystemTokens)
	histText := fmt.Sprintf("History: %d", p.health.HistoryTokens)
	toolText := fmt.Sprintf("Tools: %d", p.health.ToolTokens)

	breakdownLine := fmt.Sprintf("    %-*s %-*s %-s", breakdownWidth, sysText, breakdownWidth, histText, toolText)
	p.writeBoxLine(&content, borderStyle, breakdownLine, dimStyle, panelWidth)

	content.WriteString(borderStyle.Render("├") + borderStyle.Render(strings.Repeat("─", panelWidth-1)) + borderStyle.Render("┤") + "\n")

	// 3. Active Inventory (Files)
	p.writeBoxLine(&content, borderStyle, " Active File Inventory", lipgloss.NewStyle().Bold(true).Foreground(ColorAccent), panelWidth)

	if len(p.health.ActiveFiles) == 0 {
		p.writeBoxLine(&content, borderStyle, "    (No files in active context)", mutedStyle, panelWidth)
	} else {
		for _, file := range p.health.ActiveFiles {
			p.writeBoxLine(&content, borderStyle, "    "+MessageIcons["hint"]+" "+file, lipgloss.NewStyle().Foreground(ColorText), panelWidth)
		}
	}

	content.WriteString(borderStyle.Render("├") + borderStyle.Render(strings.Repeat("─", panelWidth-1)) + borderStyle.Render("┤") + "\n")

	// 4. Adaptive Rate Limiting Section
	p.writeBoxLine(&content, borderStyle, " Adaptive Rate Limiting (Live)", lipgloss.NewStyle().Bold(true).Foreground(ColorAccent), panelWidth)

	// Requests gauge
	reqPerc := 100.0
	if p.health.RequestsLimit > 0 {
		reqPerc = (float64(p.health.RequestsRemaining) / float64(p.health.RequestsLimit)) * 100.0
	}
	reqBar := p.renderGauge(reqPerc, panelWidth-25)
	p.writeBoxLine(&content, borderStyle, fmt.Sprintf("    Requests: %-4d  %s", p.health.RequestsRemaining, reqBar), dimStyle, panelWidth)

	// Tokens gauge
	tokPerc := 100.0
	if p.health.TokensLimit > 0 {
		tokPerc = (float64(p.health.TokensRemaining) / float64(p.health.TokensLimit)) * 100.0
	}
	tokBar := p.renderGauge(tokPerc, panelWidth-25)
	p.writeBoxLine(&content, borderStyle, fmt.Sprintf("    Tokens:   %-6d %s", p.health.TokensRemaining, tokBar), dimStyle, panelWidth)

	content.WriteString(borderStyle.Render("├") + borderStyle.Render(strings.Repeat("─", panelWidth-1)) + borderStyle.Render("┤") + "\n")

	// 4. Maintenance & Health
	p.writeBoxLine(&content, borderStyle, " Health Events", lipgloss.NewStyle().Bold(true).Foreground(ColorAccent), panelWidth)

	lastPruning := "None"
	if !p.health.LastPruningTime.IsZero() {
		lastPruning = fmt.Sprintf("%s ago", time.Since(p.health.LastPruningTime).Round(time.Second))
	}
	p.writeBoxLine(&content, borderStyle, "    Last Pruning: "+lastPruning, dimStyle, panelWidth)

	if p.health.PruningAlert != "" {
		p.writeBoxLine(&content, borderStyle, "    Status: "+p.health.PruningAlert, errorStyle.Bold(true), panelWidth)
	} else {
		p.writeBoxLine(&content, borderStyle, "    Status: Context Healthy", lipgloss.NewStyle().Foreground(ColorSuccess), panelWidth)
	}

	// Footer with instructions
	content.WriteString(borderStyle.Render("├") + borderStyle.Render(strings.Repeat("─", panelWidth-1)) + borderStyle.Render("┤") + "\n")
	p.writeBoxLine(&content, borderStyle, " [Esc/Ctrl+O] Close Observatory", mutedStyle, panelWidth)

	content.WriteString(borderStyle.Render("╰" + strings.Repeat("─", panelWidth-1) + "╯"))

	return content.String()
}

func (p *ContextObservatoryPanel) writeBoxLine(content *strings.Builder, borderStyle lipgloss.Style, text string, textStyle lipgloss.Style, panelWidth int) {
	content.WriteString(borderStyle.Render("│"))
	rendered := textStyle.Render(text)
	content.WriteString(rendered)
	padding := panelWidth - 1 - lipgloss.Width(rendered)
	if padding > 0 {
		content.WriteString(strings.Repeat(" ", padding))
	}
	content.WriteString(borderStyle.Render("│"))
	content.WriteString("\n")
}

func (p *ContextObservatoryPanel) renderGauge(percent float64, width int) string {
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
