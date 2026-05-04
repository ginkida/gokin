package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"

	"gokin/internal/format"
)

// AgentTreeNode represents a single node in the agent execution tree.
type AgentTreeNode struct {
	ID           string
	AgentType    string
	Description  string
	Thought      string // reasoning/thought process
	Status       string // "pending", "blocked", "ready", "running", "completed", "failed", "skipped"
	Dependencies []string
	Depth        int // indentation level based on dependency chain
	StartTime    time.Time
	Duration     time.Duration
	Progress     float64 // 0.0-1.0, -1 for indeterminate
	ToolsUsed    int
	CurrentTool  string // currently executing tool name
	Reflection   string // self-correction/reflection info
}

// AgentTreePanel displays a live tree of coordinated agent tasks.
type AgentTreePanel struct {
	visible bool
	nodes   []AgentTreeNode
	frame   int // spinner animation frame
	styles  *Styles
	mu      sync.RWMutex
}

// NewAgentTreePanel creates a new agent tree panel.
func NewAgentTreePanel(styles *Styles) *AgentTreePanel {
	return &AgentTreePanel{
		visible: false,
		nodes:   make([]AgentTreeNode, 0),
		styles:  styles,
	}
}

// UpdateTree replaces the entire tree snapshot.
func (p *AgentTreePanel) UpdateTree(nodes []AgentTreeNode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes = nodes

	// Auto-show when there are ≥2 tasks
	if len(nodes) >= 2 {
		p.visible = true
	}

	// Auto-hide when all tasks are terminal
	allDone := len(nodes) > 0
	for _, n := range nodes {
		if n.Status != "completed" && n.Status != "failed" && n.Status != "skipped" {
			allDone = false
			break
		}
	}
	if allDone && len(nodes) > 0 {
		// Keep visible briefly so user can see final state — caller can hide later
	}
}

// Toggle toggles the panel visibility.
func (p *AgentTreePanel) Toggle() {
	p.mu.Lock()
	p.visible = !p.visible
	p.mu.Unlock()
}

// IsVisible returns whether the panel is visible.
func (p *AgentTreePanel) IsVisible() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.visible
}

// Hide hides the panel.
func (p *AgentTreePanel) Hide() {
	p.mu.Lock()
	p.visible = false
	p.mu.Unlock()
}

// Tick advances the spinner animation.
func (p *AgentTreePanel) Tick() {
	p.mu.Lock()
	p.frame++
	p.mu.Unlock()
}

// NodeCount returns the number of nodes.
func (p *AgentTreePanel) NodeCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.nodes)
}

// View renders the agent tree panel.
func (p *AgentTreePanel) View(width int) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.visible || len(p.nodes) == 0 {
		return ""
	}

	var builder strings.Builder

	// Styles
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorGradient2).
		Padding(0, 1)

	headerStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Bold(true)

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	successStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	errorStyle := lipgloss.NewStyle().Foreground(ColorError)
	runningStyle := lipgloss.NewStyle().Foreground(ColorGradient1).Bold(true)
	pendingStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	agentTypeStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	timeStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	toolStyle := lipgloss.NewStyle().Foreground(ColorInfo).Italic(true)
	thoughtStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	// Header
	builder.WriteString(headerStyle.Render("⬡ Agent Orchestrator"))
	builder.WriteString("\n")

	// Render tree nodes
	for _, node := range p.nodes {
		var line strings.Builder

		// Indentation with tree connectors
		if node.Depth > 0 {
			indent := strings.Repeat("  ", node.Depth-1)
			line.WriteString(dimStyle.Render(indent + "├─ "))
		}

		// Status icon
		switch node.Status {
		case "running":
			spinner := spinnerFrames[p.frame%len(spinnerFrames)]
			line.WriteString(runningStyle.Render(spinner))
		case "completed":
			line.WriteString(successStyle.Render("✓"))
		case "failed":
			line.WriteString(errorStyle.Render("✗"))
		case "skipped":
			line.WriteString(dimStyle.Render("⊘"))
		case "blocked":
			line.WriteString(pendingStyle.Render("◌"))
		default: // pending, ready
			line.WriteString(pendingStyle.Render("○"))
		}
		line.WriteString(" ")

		// Agent type badge
		line.WriteString(agentTypeStyle.Render(fmt.Sprintf("[%s]", node.AgentType)))
		line.WriteString(" ")

		// Progress bar for running agents
		if node.Status == "running" && node.ToolsUsed > 0 {
			barWidth := 8
			var filled int
			if node.Progress >= 0 && node.Progress <= 1 {
				filled = int(node.Progress * float64(barWidth))
			} else {
				// Indeterminate: bounce
				pos := (p.frame / 2) % (barWidth * 2)
				if pos >= barWidth {
					pos = barWidth*2 - pos - 1
				}
				filled = pos + 1
			}
			if filled > barWidth {
				filled = barWidth
			}
			bar := successStyle.Render(strings.Repeat("━", filled)) +
				dimStyle.Render(strings.Repeat("─", barWidth-filled))
			line.WriteString(bar)
			line.WriteString(" ")
		}

		// Description (truncated)
		maxDescLen := width - lipgloss.Width(line.String()) - 20 // reserve space for time
		if maxDescLen < 15 {
			maxDescLen = 15
		}
		desc := node.Description
		descRunes := []rune(desc)
		if len(descRunes) > maxDescLen {
			desc = string(descRunes[:maxDescLen-3]) + "..."
		}
		line.WriteString(dimStyle.Render(desc))

		// Current tool for running agents
		if node.Status == "running" && node.CurrentTool != "" {
			line.WriteString(" ")
			line.WriteString(toolStyle.Render("→ " + node.CurrentTool))
		}

		// Duration (right side)
		var durStr string
		if node.Status == "running" {
			durStr = format.Duration(time.Since(node.StartTime))
		} else if node.Duration > 0 {
			durStr = format.Duration(node.Duration)
		}
		if durStr != "" {
			padding := width - lipgloss.Width(line.String()) - len(durStr) - 6
			if padding > 0 {
				line.WriteString(strings.Repeat(" ", padding))
			}
			line.WriteString(" ")
			line.WriteString(timeStyle.Render(durStr))
		}

		builder.WriteString(line.String())
		builder.WriteString("\n")

		// Thought rendering (if present and agent is active)
		if node.Thought != "" && (node.Status == "running" || node.Status == "completed") {
			var thoughtLine strings.Builder
			indentStr := strings.Repeat("  ", node.Depth)
			connector := "   │ "
			if node.Depth > 0 {
				connector = "  │ "
			}
			thoughtLine.WriteString(dimStyle.Render(indentStr + connector))

			// Truncate thought to fit width
			maxThoughtLen := width - lipgloss.Width(thoughtLine.String()) - 10
			if maxThoughtLen < 20 {
				maxThoughtLen = 20
			}
			thought := node.Thought
			if runes := []rune(thought); len(runes) > maxThoughtLen {
				thought = string(runes[:maxThoughtLen-3]) + "..."
			}

			thoughtLine.WriteString(thoughtStyle.Render("💭 " + thought))
			builder.WriteString(thoughtLine.String())
			builder.WriteString("\n")
		}

		// Reflection rendering (Self-correction)
		if node.Reflection != "" {
			var reflLine strings.Builder
			indentStr := strings.Repeat("  ", node.Depth)
			connector := "   │ "
			if node.Depth > 0 {
				connector = "  │ "
			}
			reflLine.WriteString(dimStyle.Render(indentStr + connector))

			reflBadgeStyle := lipgloss.NewStyle().
				Foreground(ColorWarning).
				Padding(0, 1).
				Bold(true)

			reflection := node.Reflection
			maxReflLen := width - lipgloss.Width(reflLine.String()) - 20
			if maxReflLen > 20 {
				if runes := []rune(reflection); len(runes) > maxReflLen {
					reflection = string(runes[:maxReflLen-3]) + "..."
				}
			}

			reflLine.WriteString(reflBadgeStyle.Render("RECOVERY"))
			reflLine.WriteString(" ")
			reflLine.WriteString(lipgloss.NewStyle().Foreground(ColorWarning).Render(reflection))
			builder.WriteString(reflLine.String())
			builder.WriteString("\n")
		}
	}

	// Summary footer
	summary := p.buildSummary()
	if summary != "" {
		builder.WriteString(dimStyle.Render(strings.Repeat("─", width-4)))
		builder.WriteString("\n")
		builder.WriteString(dimStyle.Render("  " + summary))
		builder.WriteString("\n")
	}

	content := strings.TrimSuffix(builder.String(), "\n")
	return borderStyle.Width(width - 2).Render(content)
}

// buildSummary returns a compact summary line.
func (p *AgentTreePanel) buildSummary() string {
	var running, completed, pending, failed int
	var totalDuration time.Duration

	for _, node := range p.nodes {
		switch node.Status {
		case "running":
			running++
			totalDuration += time.Since(node.StartTime)
		case "completed":
			completed++
			totalDuration += node.Duration
		case "failed":
			failed++
			totalDuration += node.Duration
		default:
			pending++
		}
	}

	var parts []string
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d running", running))
	}
	if completed > 0 {
		parts = append(parts, fmt.Sprintf("%d completed", completed))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if totalDuration > 0 {
		parts = append(parts, format.Duration(totalDuration))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// Clear removes all nodes from the tree.
func (p *AgentTreePanel) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes = make([]AgentTreeNode, 0)
}
