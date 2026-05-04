package app

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// HandleInsightsCommand renders learning insights dashboard into the chat output.
func (a *App) HandleInsightsCommand() string {
	var builder strings.Builder

	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Bold(true)
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#34D399"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171"))

	builder.WriteString(titleStyle.Render("🧠 Learning Insights Dashboard"))
	builder.WriteString("\n\n")

	// 1. Strategy Metrics
	builder.WriteString(headerStyle.Render("Strategy Performance"))
	builder.WriteString("\n")

	if a.strategyOptimizer != nil {
		topStrategies := a.strategyOptimizer.GetTopStrategies(8)
		if len(topStrategies) == 0 {
			builder.WriteString(dimStyle.Render("  No strategy data yet. Run some tasks to start learning."))
			builder.WriteString("\n")
		} else {
			// Table header
			builder.WriteString(dimStyle.Render(fmt.Sprintf("  %-20s  %8s  %10s  %12s  %s", "Strategy", "Success%", "Runs", "Avg Time", "Last Used")))
			builder.WriteString("\n")
			builder.WriteString(dimStyle.Render("  " + strings.Repeat("─", 75)))
			builder.WriteString("\n")

			for _, m := range topStrategies {
				total := m.SuccessCount + m.FailureCount
				rate := m.SuccessRate() * 100

				// Color-code success rate
				var rateStr string
				if rate >= 70 {
					rateStr = valueStyle.Render(fmt.Sprintf("%5.1f%%", rate))
				} else if rate >= 40 {
					rateStr = warnStyle.Render(fmt.Sprintf("%5.1f%%", rate))
				} else {
					rateStr = errorStyle.Render(fmt.Sprintf("%5.1f%%", rate))
				}

				name := m.StrategyName
				if runes := []rune(name); len(runes) > 20 {
					name = string(runes[:17]) + "..."
				}

				lastUsed := "never"
				if !m.LastUsed.IsZero() {
					ago := time.Since(m.LastUsed)
					if ago < time.Hour {
						lastUsed = fmt.Sprintf("%dm ago", int(ago.Minutes()))
					} else if ago < 24*time.Hour {
						lastUsed = fmt.Sprintf("%dh ago", int(ago.Hours()))
					} else {
						lastUsed = fmt.Sprintf("%dd ago", int(ago.Hours()/24))
					}
				}

				fmt.Fprintf(&builder, "  %-20s  %8s  %10d  %12s  %s",
					name, rateStr, total,
					dimStyle.Render(formatInsightDuration(m.AvgDuration)),
					dimStyle.Render(lastUsed))
				builder.WriteString("\n")

				// Show top task types on second line if any
				if len(m.TaskTypes) > 0 {
					var types []string
					for typ, count := range m.TaskTypes {
						types = append(types, fmt.Sprintf("%s(%d)", typ, count))
					}
					if len(types) > 3 {
						types = types[:3]
						types = append(types, "...")
					}
					builder.WriteString(dimStyle.Render(fmt.Sprintf("  %22s %s", "", strings.Join(types, ", "))))
					builder.WriteString("\n")
				}
			}
		}
	} else {
		builder.WriteString(dimStyle.Render("  Strategy optimizer not initialized."))
		builder.WriteString("\n")
	}

	builder.WriteString("\n")

	// 2. Tool Usage Patterns
	builder.WriteString(headerStyle.Render("Tool Usage Patterns"))
	builder.WriteString("\n")

	patterns := a.detectPatterns()
	if len(patterns) == 0 {
		builder.WriteString(dimStyle.Render("  No repeating patterns detected yet."))
		builder.WriteString("\n")
	} else {
		for _, desc := range patterns {
			builder.WriteString(valueStyle.Render("  • ") + dimStyle.Render(desc))
			builder.WriteString("\n")
		}
	}

	builder.WriteString("\n")

	// 3. Meta-Agent Stats
	builder.WriteString(headerStyle.Render("Meta-Agent Health"))
	builder.WriteString("\n")

	if a.metaAgent != nil {
		stats := a.metaAgent.GetStats()
		fmt.Fprintf(&builder, "  Active agents:        %s\n",
			valueStyle.Render(fmt.Sprintf("%d", stats["active_agents"])))
		fmt.Fprintf(&builder, "  Total interventions:  %s\n",
			warnStyle.Render(fmt.Sprintf("%d", stats["total_interventions"])))
		if interval, ok := stats["check_interval"].(string); ok {
			fmt.Fprintf(&builder, "  Check interval:       %s\n",
				dimStyle.Render(interval))
		}
		if threshold, ok := stats["stuck_threshold"].(string); ok {
			fmt.Fprintf(&builder, "  Stuck threshold:      %s\n",
				dimStyle.Render(threshold))
		}
	} else {
		builder.WriteString(dimStyle.Render("  Meta-agent not initialized."))
		builder.WriteString("\n")
	}

	builder.WriteString("\n")

	// 4. Coordinator Status
	builder.WriteString(headerStyle.Render("Task Orchestrator"))
	builder.WriteString("\n")

	if a.coordinator != nil {
		status := a.coordinator.GetStatus()
		fmt.Fprintf(&builder, "  Total tasks:   %s   Running: %s   Completed: %s   Failed: %s\n",
			valueStyle.Render(fmt.Sprintf("%d", status.TotalTasks)),
			warnStyle.Render(fmt.Sprintf("%d", status.RunningTasks)),
			valueStyle.Render(fmt.Sprintf("%d", status.CompletedTasks)),
			errorStyle.Render(fmt.Sprintf("%d", status.FailedTasks)))
		if status.BlockedTasks > 0 {
			fmt.Fprintf(&builder, "  Blocked: %s   Pending: %s\n",
				dimStyle.Render(fmt.Sprintf("%d", status.BlockedTasks)),
				dimStyle.Render(fmt.Sprintf("%d", status.PendingTasks)))
		}
	} else {
		builder.WriteString(dimStyle.Render("  Coordinator not initialized."))
		builder.WriteString("\n")
	}

	return builder.String()
}

// formatInsightDuration formats a duration for the insights table.
func formatInsightDuration(d time.Duration) string {
	if d == 0 {
		return "-"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}
