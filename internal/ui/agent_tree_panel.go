package ui

import (
	"fmt"
	"math"
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
	// allDoneAt is when every node reached a terminal state; zero while any
	// task is still live. Drives the linger-then-hide in View.
	allDoneAt time.Time
	// userHidden latches an EXPLICIT hide (toggle while visible): auto-show
	// in UpdateTree stands down until the user shows the panel again.
	userHidden    bool
	nodes         []AgentTreeNode
	frame         int // spinner animation frame
	reducedMotion bool
	styles        *Styles
	mu            sync.RWMutex
}

// NewAgentTreePanel creates a new agent tree panel.
func NewAgentTreePanel(styles *Styles) *AgentTreePanel {
	return &AgentTreePanel{
		visible: false,
		nodes:   make([]AgentTreeNode, 0),
		styles:  styles,
	}
}

// agentTreeLingerAfterDone is how long the panel stays on screen after every
// task reaches a terminal state — long enough to read the final ✓/✗ column,
// short enough that a finished task tree doesn't pin itself to the layout
// for the rest of the session (the pre-fix behavior: auto-show on ≥2 tasks
// with an auto-hide that was never implemented).
const agentTreeLingerAfterDone = 5 * time.Second

// agentTreeFullRender is the largest tree that still renders every node.
// Above it the panel goes compact: terminal nodes collapse into the footer
// summary, queued nodes render up to this budget.
const agentTreeFullRender = 8

// UpdateTree replaces the entire tree snapshot.
func (p *AgentTreePanel) UpdateTree(nodes []AgentTreeNode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nodes = make([]AgentTreeNode, len(nodes))
	for i, node := range nodes {
		node.ID = safeKeyEntryText(node.ID)
		node.AgentType = safeKeyEntryText(node.AgentType)
		node.Description = safeKeyEntryText(node.Description)
		node.Thought = safeKeyEntryText(node.Thought)
		node.CurrentTool = safeKeyEntryText(node.CurrentTool)
		node.Reflection = safeKeyEntryText(node.Reflection)
		node.Status = normalizeAgentTreeStatus(node.Status)
		node.Dependencies = append([]string(nil), node.Dependencies...)
		for j := range node.Dependencies {
			node.Dependencies[j] = safeKeyEntryText(node.Dependencies[j])
		}
		p.nodes[i] = node
	}
	nodes = p.nodes

	// Auto-show when there are ≥2 tasks — but never against an explicit
	// user hide (userHidden): without the latch, a user closing the tree
	// mid-multi-agent-work had it snap back open on the next tree update.
	if len(nodes) >= 2 && !p.userHidden {
		p.visible = true
	}

	// Auto-hide when all tasks are terminal: stamp the moment; View keeps
	// rendering for the linger window, then collapses to nothing.
	allDone := len(nodes) > 0
	for _, n := range nodes {
		if n.Status != "completed" && n.Status != "failed" && n.Status != "skipped" {
			allDone = false
			break
		}
	}
	if allDone {
		if p.allDoneAt.IsZero() {
			p.allDoneAt = time.Now()
		}
	} else {
		p.allDoneAt = time.Time{}
	}
}

func normalizeAgentTreeStatus(status string) string {
	status = strings.ToLower(safeKeyEntryText(status))
	switch status {
	case "pending", "ready", "running", "completed", "failed", "skipped", "blocked":
		return status
	default:
		return "unknown"
	}
}

// Toggle toggles the panel visibility.
func (p *AgentTreePanel) Toggle() {
	p.mu.Lock()
	p.visible = !p.visible
	// Latch explicit intent symmetrically: hidden by the user → auto-show
	// stands down until the user shows it again (mirrors the activity feed).
	p.userHidden = !p.visible
	if p.visible {
		// Explicit user intent overrides the auto-hide: keep a finished tree
		// on screen until the user toggles it off or a new tree arrives.
		p.allDoneAt = time.Time{}
	}
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
	if !p.reducedMotion {
		p.frame++
	}
	p.mu.Unlock()
}

func (p *AgentTreePanel) SetReducedMotion(enabled bool) {
	p.mu.Lock()
	p.reducedMotion = enabled
	p.mu.Unlock()
}

// NodeCount returns the number of nodes.
func (p *AgentTreePanel) NodeCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.nodes)
}

// View renders the agent tree panel. When a terminal height is supplied, the
// panel budgets its own rows instead of relying on the final screen compositor
// to crop the top of an oversized tree.
func (p *AgentTreePanel) View(width int, heights ...int) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.visible || len(p.nodes) == 0 {
		return ""
	}
	// Linger expired after all tasks finished — render nothing. The next
	// UpdateTree with live tasks clears the stamp and the panel returns.
	if !p.allDoneAt.IsZero() && time.Since(p.allDoneAt) > agentTreeLingerAfterDone {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	if width < 4 {
		return truncateForWidth("Agents", width)
	}
	height := 0
	if len(heights) > 0 {
		height = heights[0]
	}
	maxPanelHeight := agentTreePanelHeightBudget(height)
	if maxPanelHeight > 0 && maxPanelHeight < 4 {
		return truncateForWidth(fmt.Sprintf("Agents · Ctrl+A hide · %d tasks", len(p.nodes)), width)
	}
	horizontalPadding := 1
	if width < 6 {
		horizontalPadding = 0
	}
	contentWidth := max(width-2-horizontalPadding*2, 1)

	var builder strings.Builder

	// Styles
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorGradient2).
		Padding(0, horizontalPadding)

	headerStyle := lipgloss.NewStyle().
		Foreground(ColorSecondary).
		Bold(true)

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	successStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	errorStyle := lipgloss.NewStyle().Foreground(ColorError)
	warningStyle := lipgloss.NewStyle().Foreground(ColorWarning)
	runningStyle := lipgloss.NewStyle().Foreground(ColorGradient1).Bold(true)
	pendingStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	agentTypeStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	timeStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	toolStyle := lipgloss.NewStyle().Foreground(ColorInfo).Italic(true)
	thoughtStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)

	spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	// Long or height-constrained trees render in compact mode: terminal nodes
	// (✓/⊘) collapse into
	// the footer summary and queued nodes render up to a budget — 10
	// parallel tasks otherwise pin a 12+ line box while they run. Running
	// and failed nodes ALWAYS render; thought/reflection sublines are
	// suppressed for non-running nodes in compact mode.
	baseNodeBudget := agentTreeFullRender
	showSummary := true
	if maxPanelHeight > 0 {
		// Border + title + divider + summary consume five rows. At the
		// irreducible four/five-row size, keep the title/action and current task
		// rather than dropping the task itself for aggregate counts.
		if maxPanelHeight >= 6 {
			baseNodeBudget = min(baseNodeBudget, max(maxPanelHeight-5, 1))
		} else {
			showSummary = false
			baseNodeBudget = min(baseNodeBudget, max(maxPanelHeight-3, 1))
		}
	}
	compact := len(p.nodes) > agentTreeFullRender || baseNodeBudget < len(p.nodes)
	doneHidden := 0
	queuedHidden := 0
	selectedCompact := make(map[int]bool, baseNodeBudget)
	if compact {
		// Choose a bounded set by operational priority, then render those nodes
		// in original tree order. This prevents dozens of simultaneous running
		// rows from pinning the entire terminal while still preferring live and
		// failed work over queued tasks.
		allowTerminalRows := len(p.nodes) <= agentTreeFullRender
		for priority := 0; priority < 3 && len(selectedCompact) < baseNodeBudget; priority++ {
			for i, node := range p.nodes {
				if len(selectedCompact) >= baseNodeBudget {
					break
				}
				isLive := node.Status == "running" || node.Status == "failed"
				isWaiting := node.Status == "blocked" || node.Status == "ready" || node.Status == "pending" || node.Status == "unknown"
				if (priority == 0 && isLive) || (priority == 1 && isWaiting) || (priority == 2 && allowTerminalRows && !isLive && !isWaiting) {
					selectedCompact[i] = true
				}
			}
		}
	}

	// Header. At the irreducible height the footer yields, so keep the inverse
	// action and any node compaction discoverable in the title itself.
	title := "⬡ Agent Orchestrator"
	if maxPanelHeight > 0 && maxPanelHeight < 6 {
		title = "⬡ Agents · Ctrl+A hide"
		visibleNodes := len(p.nodes)
		if compact {
			visibleNodes = 0
			for index, node := range p.nodes {
				if selectedCompact[index] && node.Status != "completed" && node.Status != "skipped" {
					visibleNodes++
				}
			}
		}
		if hidden := len(p.nodes) - visibleNodes; hidden > 0 {
			title += fmt.Sprintf(" · %d folded", hidden)
		}
	}
	builder.WriteString(headerStyle.Render(title))
	builder.WriteString("\n")

	// Detail rows use only space left after the selected task rows. Recovery
	// information outranks thought prose; active thoughts outrank completed
	// thoughts. This keeps compact output useful without exceeding its budget.
	detailBudget := 0
	if maxPanelHeight > 0 {
		selectedCount := len(p.nodes)
		if compact {
			selectedCount = len(selectedCompact)
		}
		detailBudget = max(baseNodeBudget-selectedCount, 0)
	}
	showReflection := make(map[int]bool)
	showThought := make(map[int]bool)
	selected := func(index int) bool { return !compact || selectedCompact[index] }
	if maxPanelHeight <= 0 {
		for index, node := range p.nodes {
			showReflection[index] = node.Reflection != ""
			showThought[index] = node.Thought != "" && (node.Status == "running" || node.Status == "completed")
		}
	} else {
		for index, node := range p.nodes {
			if detailBudget == 0 {
				break
			}
			if selected(index) && node.Reflection != "" {
				showReflection[index] = true
				detailBudget--
			}
		}
		for _, status := range []string{"running", "completed"} {
			for index, node := range p.nodes {
				if detailBudget == 0 {
					break
				}
				if selected(index) && node.Status == status && node.Thought != "" {
					showThought[index] = true
					detailBudget--
				}
			}
		}
	}

	// Render tree nodes
	for index, node := range p.nodes {
		if compact {
			switch node.Status {
			case "completed", "skipped":
				doneHidden++
				continue
			}
			if !selectedCompact[index] {
				queuedHidden++
				continue
			}
		}
		var line strings.Builder

		// Indentation with tree connectors
		depth := max(node.Depth, 0)
		if depth > 0 {
			// Preserve status/type/tool even for pathological dependency depths.
			// Four visible levels plus an ellipsis communicate nesting without
			// letting indentation consume the whole row.
			maxVisibleDepth := min(4, max((contentWidth-16)/2, 0))
			visibleDepth := min(depth, maxVisibleDepth)
			prefix := strings.Repeat("  ", max(visibleDepth-1, 0)) + "├─ "
			if visibleDepth < depth {
				prefix = "… " + prefix
			}
			line.WriteString(dimStyle.Render(prefix))
		}

		// Status icon
		switch node.Status {
		case "running":
			spinner := spinnerFrames[p.frame%len(spinnerFrames)]
			if p.reducedMotion {
				spinner = "●"
			}
			line.WriteString(runningStyle.Render(spinner))
		case "completed":
			line.WriteString(successStyle.Render("✓"))
		case "failed":
			line.WriteString(errorStyle.Render("✗"))
		case "skipped":
			line.WriteString(dimStyle.Render("⊘"))
		case "blocked":
			line.WriteString(pendingStyle.Render("◌"))
		case "pending", "ready":
			line.WriteString(pendingStyle.Render("○"))
		default:
			line.WriteString(warningStyle.Render("?"))
		}
		line.WriteString(" ")

		// Agent type badge
		agentType := strings.Join(strings.Fields(node.AgentType), " ")
		if agentType == "" {
			agentType = "agent"
		}
		agentType = truncateForWidth(agentType, min(20, max(contentWidth/3, 1)))
		line.WriteString(agentTypeStyle.Render(fmt.Sprintf("[%s]", agentType)))
		line.WriteString(" ")

		// Progress bar for running agents
		if node.Status == "running" && node.ToolsUsed > 0 {
			barWidth := min(8, max(contentWidth-lipgloss.Width(line.String())-5, 0))
			if barWidth >= 3 {
				var filled int
				if !math.IsNaN(node.Progress) && node.Progress >= 0 {
					progress := min(node.Progress, 1)
					filled = int(progress * float64(barWidth))
				} else {
					// Indeterminate: bounce. Keep the modulo inside the visible
					// bar guard so constrained rows can never divide by zero.
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
		}

		// Current tool is operationally more important than the prose
		// description, so render it first on constrained widths.
		if node.Status == "running" {
			if currentTool := strings.Join(strings.Fields(node.CurrentTool), " "); currentTool != "" {
				toolBudget := max(contentWidth-lipgloss.Width(line.String())-1, 0)
				line.WriteString(toolStyle.Render(truncateForWidth("→ "+currentTool+" ", toolBudget)))
			}
		}

		desc := strings.Join(strings.Fields(node.Description), " ")
		if desc == "" {
			desc = "Task"
		}

		// Duration (right side)
		var durStr string
		if node.Status == "running" && !node.StartTime.IsZero() {
			durStr = format.Duration(max(time.Since(node.StartTime), 0))
		} else if node.Duration > 0 {
			durStr = format.Duration(node.Duration)
		}
		reserve := 0
		if durStr != "" {
			reserve = lipgloss.Width(durStr) + 1
		}
		descBudget := max(contentWidth-lipgloss.Width(line.String())-reserve, 0)
		line.WriteString(dimStyle.Render(truncateForWidth(desc, descBudget)))
		if durStr != "" {
			padding := contentWidth - lipgloss.Width(line.String()) - lipgloss.Width(durStr)
			if padding > 0 {
				line.WriteString(strings.Repeat(" ", padding))
			}
			line.WriteString(" ")
			line.WriteString(timeStyle.Render(durStr))
		}

		builder.WriteString(line.String())
		builder.WriteString("\n")

		// Thought rendering (if present and agent is active)
		if showThought[index] {
			var thoughtLine strings.Builder
			indentStr := strings.Repeat("  ", min(max(node.Depth, 0), max(contentWidth/4, 0)))
			connector := "   │ "
			if node.Depth > 0 {
				connector = "  │ "
			}
			thoughtLine.WriteString(dimStyle.Render(indentStr + connector))

			// Truncate thought to fit width
			maxThoughtLen := max(contentWidth-lipgloss.Width(thoughtLine.String())-3, 0)
			thought := strings.Join(strings.Fields(node.Thought), " ")
			thought = truncateForWidth(thought, maxThoughtLen)

			thoughtLine.WriteString(thoughtStyle.Render("💭 " + thought))
			builder.WriteString(thoughtLine.String())
			builder.WriteString("\n")
		}

		// Reflection rendering (Self-correction)
		if showReflection[index] {
			var reflLine strings.Builder
			indentStr := strings.Repeat("  ", min(max(node.Depth, 0), max(contentWidth/4, 0)))
			connector := "   │ "
			if node.Depth > 0 {
				connector = "  │ "
			}
			reflLine.WriteString(dimStyle.Render(indentStr + connector))

			reflBadgeStyle := lipgloss.NewStyle().
				Foreground(ColorWarning).
				Padding(0, 1).
				Bold(true)

			reflection := strings.Join(strings.Fields(node.Reflection), " ")

			reflLine.WriteString(reflBadgeStyle.Render("RECOVERY"))
			reflLine.WriteString(" ")
			reflectionBudget := max(contentWidth-lipgloss.Width(reflLine.String()), 0)
			reflLine.WriteString(lipgloss.NewStyle().Foreground(ColorWarning).Render(truncateForWidth(reflection, reflectionBudget)))
			builder.WriteString(reflLine.String())
			builder.WriteString("\n")
		}
	}

	// Summary footer. Put the hide action and fold disclosure first so both
	// survive truncation on narrow terminals; aggregate counts follow.
	summary := "Ctrl+A hide"
	if hidden := doneHidden + queuedHidden; hidden > 0 {
		// Make the compaction visible — without this a user counting rows
		// would think tasks vanished. Totals live in the summary below.
		summary += fmt.Sprintf(" · %d row(s) folded", hidden)
	}
	if counts := p.buildSummary(); counts != "" {
		summary += " · " + counts
	}
	if showSummary {
		builder.WriteString(dimStyle.Render(strings.Repeat("─", contentWidth)))
		builder.WriteString("\n")
		builder.WriteString(dimStyle.Render("  " + summary))
		builder.WriteString("\n")
	}

	content := fitPanelContent(strings.TrimSuffix(builder.String(), "\n"), contentWidth)
	return borderStyle.Width(width - 2).Render(content)
}

// agentTreePanelHeightBudget leaves two terminal rows for the surrounding
// activity/composer chrome and caps this background panel at fourteen rows so
// it never takes over a tall screen. A zero height preserves the legacy
// standalone rendering contract used by callers that have no terminal size.
func agentTreePanelHeightBudget(terminalHeight int) int {
	if terminalHeight <= 0 {
		return 0
	}
	return min(terminalHeight, min(max(terminalHeight-2, 4), 14))
}

// buildSummary returns a compact summary line.
func (p *AgentTreePanel) buildSummary() string {
	var running, completed, pending, blocked, failed, skipped, unknown int
	var totalDuration time.Duration

	for _, node := range p.nodes {
		switch node.Status {
		case "running":
			running++
			if !node.StartTime.IsZero() {
				totalDuration += max(time.Since(node.StartTime), 0)
			}
		case "completed":
			completed++
			totalDuration += node.Duration
		case "failed":
			failed++
			totalDuration += node.Duration
		case "skipped":
			skipped++
			totalDuration += node.Duration
		case "blocked":
			blocked++
		case "pending", "ready":
			pending++
		default:
			unknown++
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
	if blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d blocked", blocked))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("%d unknown", unknown))
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
