package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// titlebarMinWidth is the lower bound below which the titlebar is omitted.
// On narrower terminals the status bar at the bottom already exposes the
// same identity info, and a one-liner stacked on top fights the chat area
// for vertical real estate.
const titlebarMinWidth = 80

// renderTitlebar draws a single-line top frame echoing the Gokin Classic
// mockup's terminal chrome:
//
//	● ● ●  ~/code/payments-api                    kimi · k2.6 · $0.0243
//
// Three traffic-light dots (Error/Warning/Success — deliberately keeping
// the conventional red/yellow/green ordering even though they're not
// interactive), a muted project path, and a right-aligned provider · model
// · cost segment. Returns "" when the terminal is too narrow or there's
// nothing to show (no workDir AND no provider AND no cost).
func (m Model) renderTitlebar() string {
	if m.width < titlebarMinWidth {
		return ""
	}

	left := m.titlebarLeftSegment()
	right := m.titlebarRightSegment()
	if left == "" && right == "" {
		return ""
	}

	padding := safePadding(m.width, lipgloss.Width(left), lipgloss.Width(right))
	return left + strings.Repeat(" ", padding) + right
}

// titlebarLeftSegment renders the three traffic-light dots followed by the
// project path. Path is collapsed to ~/ and middle-truncated to a budget so
// the right segment always has room.
func (m Model) titlebarLeftSegment() string {
	var b strings.Builder

	// Three muted dots — pure chrome decoration that signals "this is a
	// terminal application frame", nothing more. They don't act and they
	// don't indicate status, so the bright red/yellow/green of an earlier
	// pass was unjustified eye-pull. ColorMuted reads as present-but-quiet
	// against the graphite background.
	dot := lipgloss.NewStyle().Foreground(ColorMuted).Render("●")
	b.WriteString(dot)
	b.WriteString(" ")
	b.WriteString(dot)
	b.WriteString(" ")
	b.WriteString(dot)

	if path := statusBarProjectPath(m.workDir); path != "" {
		// Two-space gutter between the dots and the path so the cluster
		// reads as one visual unit. Path budget caps at width/3 so the
		// right segment has room.
		budget := max(m.width/3, 12)
		shortened := shortenPath(path, budget)
		pathStyle := lipgloss.NewStyle().Foreground(ColorMuted)
		b.WriteString("  ")
		b.WriteString(pathStyle.Render(shortened))
	}

	return b.String()
}

// titlebarRightSegment renders "provider · model · $cost" using dot
// separators (not │ — the titlebar is a different visual context from the
// status bar, where dot reads as "and" rather than "segment break").
//
// Segments fall off right-to-left when the budget is tight: cost first,
// then model, then provider. Below ~24 chars of right budget we surrender
// and let renderTitlebar pad with empty space — the status bar at the
// bottom will still surface this info.
func (m Model) titlebarRightSegment() string {
	provider := strings.TrimSpace(m.runtimeStatus.Provider)
	model := strings.TrimSpace(m.currentModel)
	cost := m.sessionCost

	rightBudget := m.width - m.width/3 - 12 // dots + gutter + minimum left
	if rightBudget < 24 {
		return ""
	}

	providerStyle := lipgloss.NewStyle().Foreground(ColorPrimary).Bold(true)
	modelStyle := lipgloss.NewStyle().Foreground(ColorText)
	costStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	sepStyle := lipgloss.NewStyle().Foreground(ColorDim)

	var parts []string
	if provider != "" {
		if runes := []rune(provider); len(runes) > 12 {
			provider = string(runes[:12])
		}
		parts = append(parts, providerStyle.Render(provider))
	}
	if model != "" {
		parts = append(parts, modelStyle.Render(shortenModelName(model)))
	}
	if cost > 0 {
		var costStr string
		switch {
		case cost < 0.01:
			costStr = fmt.Sprintf("$%.4f", cost)
		case cost < 1.0:
			costStr = fmt.Sprintf("$%.3f", cost)
		default:
			costStr = fmt.Sprintf("$%.2f", cost)
		}
		parts = append(parts, costStyle.Render(costStr))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, sepStyle.Render(" · "))
}
