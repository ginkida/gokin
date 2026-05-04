package ui

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"

	"gokin/internal/format"
)

const (
	// Maximum entries to display in the activity feed
	maxActivityEntries = 10
	// Maximum recent log messages
	maxRecentLog = 5
)

// SubAgentState tracks the state of a sub-agent.
type SubAgentState struct {
	AgentID     string
	AgentType   string
	Description string
	CurrentTool string
	ToolArgs    map[string]any
	StartTime   time.Time
	// LastToolTime is the wall-clock of the most recent tool_start/tool_end
	// event. Used to surface an "idle Xm" indicator when a sub-agent stops
	// making tool calls — lets users distinguish "legitimately slow" from
	// "stuck", and justifies the wait vs. an unknown hang.
	LastToolTime time.Time
	CurrentStep  int     // Current step number (e.g. tool call count)
	TotalSteps   int     // Total expected steps (0 = unknown)
	Progress     float64 // 0.0-1.0, -1 for indeterminate
}

// SubAgentIdleThreshold is the quiet window after which a running sub-agent
// is considered "idle" — no tool calls observed in that window. Tuned long
// enough to ignore normal thinking pauses (Kimi thinking + generate can be
// 30-60s) and short enough to flag genuine stalls before the full timeout.
const SubAgentIdleThreshold = 2 * time.Minute

// ActivityFeedSnapshot is a lightweight copy of the most relevant activity
// data for rendering summary UI outside the panel itself.
type ActivityFeedSnapshot struct {
	Entries       []ActivityFeedEntry
	RecentLog     []string
	RunningTools  int
	RunningAgents int
}

// ActivityFeedPanel displays real-time activity from tools and sub-agents.
type ActivityFeedPanel struct {
	visible       bool
	entries       []ActivityFeedEntry
	recentLog     []string
	activeEntries map[string]int // ID -> index in entries
	frame         int            // For spinner animation
	styles        *Styles
	mu            sync.RWMutex

	// Sub-agent tracking
	subAgentActivities map[string]*SubAgentState
}

// NewActivityFeedPanel creates a new activity feed panel.
func NewActivityFeedPanel(styles *Styles) *ActivityFeedPanel {
	return &ActivityFeedPanel{
		visible:            false, // Hidden by default - surfaced automatically for complex activity or toggled with Ctrl+O
		entries:            make([]ActivityFeedEntry, 0, maxActivityEntries),
		recentLog:          make([]string, 0, maxRecentLog),
		activeEntries:      make(map[string]int),
		styles:             styles,
		subAgentActivities: make(map[string]*SubAgentState),
	}
}

// AddEntry adds a new activity entry.
func (p *ActivityFeedPanel) AddEntry(entry ActivityFeedEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Update existing entry if it exists
	if idx, ok := p.activeEntries[entry.ID]; ok && idx < len(p.entries) {
		p.entries[idx] = entry
		return
	}

	// Add new entry
	if len(p.entries) >= maxActivityEntries {
		// Remove oldest entry
		oldID := p.entries[0].ID
		delete(p.activeEntries, oldID)
		p.entries = p.entries[1:]
		// Update indices
		for id, idx := range p.activeEntries {
			p.activeEntries[id] = idx - 1
		}
	}

	p.activeEntries[entry.ID] = len(p.entries)
	p.entries = append(p.entries, entry)

	// Auto-surface the panel only when activity becomes genuinely interesting:
	// parallel tools or agent orchestration. Single short tool calls stay quiet.
	if p.countRunningEntriesLocked() >= 2 || len(p.subAgentActivities) > 0 {
		p.visible = true
	}
}

// CompleteEntry marks an entry as completed.
func (p *ActivityFeedPanel) CompleteEntry(id string, success bool, summary string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	idx, ok := p.activeEntries[id]
	if !ok || idx >= len(p.entries) {
		return
	}

	entry := &p.entries[idx]
	entry.Duration = time.Since(entry.StartTime)
	entry.ResultSummary = summary
	if success {
		entry.Status = ActivityCompleted
	} else {
		entry.Status = ActivityFailed
	}

	// Append result summary to description if available
	if summary != "" {
		entry.Description = fmt.Sprintf("%s -> %s", entry.Description, summary)
	}

	// Add to recent log
	logMsg := p.formatLogMessage(entry, summary)
	p.addRecentLog(logMsg)
}

// StartSubAgent starts tracking a sub-agent.
func (p *ActivityFeedPanel) StartSubAgent(agentID, agentType, description string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.visible = true
	now := time.Now()
	p.subAgentActivities[agentID] = &SubAgentState{
		AgentID:      agentID,
		AgentType:    agentType,
		Description:  description,
		StartTime:    now,
		LastToolTime: now, // Seed so idle detection starts from birth.
		Progress:     -1,  // indeterminate until progress data arrives
	}

	// Add as activity entry
	entry := ActivityFeedEntry{
		ID:          "agent-" + agentID,
		Type:        ActivityTypeAgent,
		Name:        agentType,
		Description: description,
		Status:      ActivityRunning,
		StartTime:   time.Now(),
		AgentID:     agentID,
	}

	if len(p.entries) >= maxActivityEntries {
		oldID := p.entries[0].ID
		delete(p.activeEntries, oldID)
		p.entries = p.entries[1:]
		for id, idx := range p.activeEntries {
			p.activeEntries[id] = idx - 1
		}
	}

	p.activeEntries[entry.ID] = len(p.entries)
	p.entries = append(p.entries, entry)
}

// UpdateSubAgentTool updates the current tool for a sub-agent.
func (p *ActivityFeedPanel) UpdateSubAgentTool(agentID, toolName string, args map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, ok := p.subAgentActivities[agentID]
	if !ok {
		return
	}

	state.CurrentTool = toolName
	state.ToolArgs = args
	// Any tool activity (start OR end) resets the idle clock — the agent is
	// clearly alive if it's reporting events.
	state.LastToolTime = time.Now()

	// Increment step counter on each new tool start
	if toolName != "" {
		state.CurrentStep++
	}

	// Update the entry description
	entryID := "agent-" + agentID
	if idx, ok := p.activeEntries[entryID]; ok && idx < len(p.entries) {
		desc := state.Description
		if toolName != "" {
			toolInfo := formatToolActivity(toolName, args)
			desc = fmt.Sprintf("%s -> %s", state.AgentType, toolInfo)
		}
		p.entries[idx].Description = desc
	}
}

// UpdateSubAgentProgress updates progress data for a sub-agent.
func (p *ActivityFeedPanel) UpdateSubAgentProgress(agentID string, progress float64, currentStep, totalSteps int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, ok := p.subAgentActivities[agentID]
	if !ok {
		return
	}

	state.Progress = progress
	if currentStep > 0 {
		state.CurrentStep = currentStep
	}
	if totalSteps > 0 {
		state.TotalSteps = totalSteps
	}
}

// GetSubAgentState returns the state for a sub-agent (read-only snapshot).
func (p *ActivityFeedPanel) GetSubAgentState(agentID string) *SubAgentState {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if state, ok := p.subAgentActivities[agentID]; ok {
		// Return a copy to avoid data races
		cp := *state
		return &cp
	}
	return nil
}

// CompleteSubAgent marks a sub-agent as completed.
func (p *ActivityFeedPanel) CompleteSubAgent(agentID string, success bool, summary string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entryID := "agent-" + agentID
	idx, ok := p.activeEntries[entryID]
	if !ok || idx >= len(p.entries) {
		delete(p.subAgentActivities, agentID)
		return
	}

	state := p.subAgentActivities[agentID]
	entry := &p.entries[idx]
	if state != nil {
		entry.Duration = time.Since(state.StartTime)
	} else {
		entry.Duration = time.Since(entry.StartTime)
	}

	if success {
		entry.Status = ActivityCompleted
	} else {
		entry.Status = ActivityFailed
	}

	// Add to recent log
	logMsg := p.formatLogMessage(entry, summary)
	p.addRecentLog(logMsg)

	delete(p.subAgentActivities, agentID)
}

// Tick advances the spinner animation.
func (p *ActivityFeedPanel) Tick() {
	p.mu.Lock()
	p.frame++
	p.mu.Unlock()
}

// Toggle toggles the panel visibility.
func (p *ActivityFeedPanel) Toggle() {
	p.mu.Lock()
	p.visible = !p.visible
	p.mu.Unlock()
}

// IsVisible returns whether the panel is visible.
func (p *ActivityFeedPanel) IsVisible() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.visible
}

// HasActiveEntries returns whether there are any running entries.
func (p *ActivityFeedPanel) HasActiveEntries() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, entry := range p.entries {
		if entry.Status == ActivityRunning || entry.Status == ActivityPending {
			return true
		}
	}
	return len(p.subAgentActivities) > 0
}

// Snapshot returns a small copy of the most useful activity state for compact
// summary components such as the live "Now" card.
func (p *ActivityFeedPanel) Snapshot(maxEntries, maxLog int) ActivityFeedSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if maxEntries < 0 {
		maxEntries = 0
	}
	if maxLog < 0 {
		maxLog = 0
	}

	var snap ActivityFeedSnapshot
	for _, entry := range p.entries {
		if entry.Status != ActivityRunning && entry.Status != ActivityPending {
			continue
		}
		switch entry.Type {
		case ActivityTypeTool:
			snap.RunningTools++
		case ActivityTypeAgent:
			snap.RunningAgents++
		}
	}
	if snap.RunningAgents == 0 && len(p.subAgentActivities) > 0 {
		snap.RunningAgents = len(p.subAgentActivities)
	}

	if maxEntries > len(p.entries) {
		maxEntries = len(p.entries)
	}
	if maxEntries > 0 {
		start := len(p.entries) - maxEntries
		snap.Entries = append([]ActivityFeedEntry(nil), p.entries[start:]...)
	}

	if maxLog > len(p.recentLog) {
		maxLog = len(p.recentLog)
	}
	if maxLog > 0 {
		start := len(p.recentLog) - maxLog
		snap.RecentLog = append([]string(nil), p.recentLog[start:]...)
	}

	return snap
}

// View renders the activity feed panel.
func (p *ActivityFeedPanel) View(width int) string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.visible {
		return ""
	}

	// Show empty state placeholder when visible but no data
	if len(p.entries) == 0 && len(p.recentLog) == 0 {
		dimStyle := lipgloss.NewStyle().Foreground(ColorDim).Italic(true)
		borderStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(0, 1)
		return borderStyle.Width(width - 2).Render(dimStyle.Render("  No activity yet"))
	}

	var builder strings.Builder

	// Styles
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorBorder).
		Padding(0, 1)

	headerStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)

	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	successStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	errorStyle := lipgloss.NewStyle().Foreground(ColorError)
	toolStyle := lipgloss.NewStyle().Foreground(ColorGradient1).Bold(true)
	agentStyle := lipgloss.NewStyle().Foreground(ColorGradient2).Bold(true)
	timeStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	// Header
	title := "Live Activity"
	if summary := p.buildMetricsSummary(); summary != "" {
		title += " · " + summary
	}
	builder.WriteString(headerStyle.Render(title))
	builder.WriteString("\n")

	// Spinner frames
	spinnerFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	progressStyle := lipgloss.NewStyle().Foreground(ColorSuccess)
	stepStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	timestampStyle := lipgloss.NewStyle().Foreground(ColorDim)

	// Render active entries (most recent first)
	for i := len(p.entries) - 1; i >= 0 && i >= len(p.entries)-5; i-- {
		entry := p.entries[i]
		var line strings.Builder

		// Timestamp
		line.WriteString(timestampStyle.Render(entry.StartTime.Format("15:04:05")))
		line.WriteString(" ")

		// Status icon
		switch entry.Status {
		case ActivityRunning, ActivityPending:
			spinner := spinnerFrames[p.frame%len(spinnerFrames)]
			line.WriteString(toolStyle.Render(spinner))
		case ActivityCompleted:
			line.WriteString(successStyle.Render(MessageIcons["success"]))
		case ActivityFailed:
			line.WriteString(errorStyle.Render(MessageIcons["error"]))
		}
		line.WriteString(" ")

		// Name (tool or agent)
		if entry.Type == ActivityTypeAgent {
			line.WriteString(agentStyle.Render(fmt.Sprintf("[%s]", entry.Name)))
		} else {
			line.WriteString(toolStyle.Render(entry.Name))
		}
		line.WriteString(" ")

		// For running agents, show inline progress bar
		if entry.Type == ActivityTypeAgent && (entry.Status == ActivityRunning || entry.Status == ActivityPending) {
			if state, ok := p.subAgentActivities[entry.AgentID]; ok && state.CurrentStep > 0 {
				barWidth := 10
				var filled int
				if state.TotalSteps > 0 {
					filled = state.CurrentStep * barWidth / state.TotalSteps
					if filled > barWidth {
						filled = barWidth
					}
				} else {
					// Indeterminate: animate bouncing position
					pos := (p.frame / 2) % (barWidth * 2)
					if pos >= barWidth {
						pos = barWidth*2 - pos - 1
					}
					filled = pos + 1
				}
				bar := progressStyle.Render(strings.Repeat("━", filled)) +
					dimStyle.Render(strings.Repeat("─", barWidth-filled))
				line.WriteString(bar)
				line.WriteString(" ")

				// Step counter
				if state.TotalSteps > 0 {
					line.WriteString(stepStyle.Render(fmt.Sprintf("%d/%d", state.CurrentStep, state.TotalSteps)))
				} else {
					line.WriteString(stepStyle.Render(fmt.Sprintf("%d steps", state.CurrentStep)))
				}
				line.WriteString(dimStyle.Render(" — "))
			}
		}

		// Description (truncated to fit)
		maxDescLen := width - lipgloss.Width(line.String()) - 14
		if maxDescLen < 20 {
			maxDescLen = 20
		}
		desc := entry.Description
		if utf8.RuneCountInString(desc) > maxDescLen {
			runes := []rune(desc)
			desc = string(runes[:maxDescLen-3]) + "..."
		}
		line.WriteString(dimStyle.Render(desc))

		// Idle marker: if this is a running sub-agent that hasn't made a
		// tool call in SubAgentIdleThreshold, append "· idle Xm". Users
		// see a clear "may be stuck" signal before the agent hits its
		// hard timeout — and can decide to cancel.
		if entry.Type == ActivityTypeAgent &&
			(entry.Status == ActivityRunning || entry.Status == ActivityPending) {
			if state, ok := p.subAgentActivities[entry.AgentID]; ok &&
				!state.LastToolTime.IsZero() {
				idle := time.Since(state.LastToolTime)
				if idle >= SubAgentIdleThreshold {
					warnStyle := lipgloss.NewStyle().Foreground(ColorWarning).Italic(true)
					line.WriteString(warnStyle.Render(
						fmt.Sprintf("  · idle %s", format.Duration(idle))))
				}
			}
		}

		// Duration (right-aligned)
		var duration string
		if entry.Status == ActivityRunning || entry.Status == ActivityPending {
			duration = format.Duration(time.Since(entry.StartTime))
		} else {
			duration = format.Duration(entry.Duration)
		}
		// Pad to right
		padding := width - lipgloss.Width(line.String()) - len(duration) - 6
		if padding > 0 {
			line.WriteString(strings.Repeat(" ", padding))
		}
		line.WriteString(" ")
		line.WriteString(timeStyle.Render(duration))

		builder.WriteString(line.String())
		builder.WriteString("\n")
	}

	// Metrics summary line
	if metricsLine := p.buildMetricsSummary(); metricsLine != "" {
		builder.WriteString(dimStyle.Render(strings.Repeat("─", width-4)))
		builder.WriteString("\n")
		builder.WriteString(dimStyle.Render("  " + metricsLine))
		builder.WriteString("\n")
	}

	// Separator if we have recent log
	if len(p.recentLog) > 0 && len(p.entries) > 0 {
		builder.WriteString(dimStyle.Render(strings.Repeat("─", width-4)))
		builder.WriteString("\n")
	}

	// Recent log (last few messages)
	for _, msg := range p.recentLog {
		builder.WriteString(dimStyle.Render("  › " + msg))
		builder.WriteString("\n")
	}

	// Apply border
	content := strings.TrimSuffix(builder.String(), "\n")
	return borderStyle.Width(width - 2).Render(content)
}

// formatLogMessage formats an entry for the recent log.
func (p *ActivityFeedPanel) formatLogMessage(entry *ActivityFeedEntry, summary string) string {
	var msg string
	switch entry.Type {
	case ActivityTypeAgent:
		if summary != "" {
			return summary
		}
		if entry.Status == ActivityCompleted {
			msg = fmt.Sprintf("Sub-agent [%s] completed", entry.Name)
		} else {
			msg = fmt.Sprintf("Sub-agent [%s] failed", entry.Name)
		}
	default:
		// Use enriched description (includes "-> summary" if available)
		msg = entry.Description
	}

	if entry.Duration > 0 {
		msg += fmt.Sprintf(" (%s)", format.Duration(entry.Duration))
	}

	return msg
}

// addRecentLog adds a message to the recent log.
func (p *ActivityFeedPanel) addRecentLog(msg string) {
	if len(p.recentLog) >= maxRecentLog {
		p.recentLog = p.recentLog[1:]
	}
	p.recentLog = append(p.recentLog, msg)
}

// formatToolActivity generates a description for a tool call.
func formatToolActivity(toolName string, args map[string]any) string {
	switch toolName {
	case "read":
		if path, ok := args["file_path"].(string); ok {
			return fmt.Sprintf("Reading %s", shortenPath(path, 40))
		}
	case "write":
		if path, ok := args["file_path"].(string); ok {
			size := 0
			if content, ok := args["content"].(string); ok {
				size = len(content)
			}
			return fmt.Sprintf("Writing %d bytes to %s", size, shortenPath(path, 30))
		}
	case "edit":
		if path, ok := args["file_path"].(string); ok {
			return fmt.Sprintf("Editing %s", shortenPath(path, 40))
		}
	case "grep":
		if pattern, ok := args["pattern"].(string); ok {
			p := pattern
			if runes := []rune(p); len(runes) > 30 {
				p = string(runes[:27]) + "..."
			}
			return fmt.Sprintf("Searching '%s'", p)
		}
	case "glob":
		if pattern, ok := args["pattern"].(string); ok {
			return fmt.Sprintf("Finding files: %s", pattern)
		}
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			c := cmd
			if runes := []rune(c); len(runes) > 40 {
				c = string(runes[:37]) + "..."
			}
			c = strings.ReplaceAll(c, "\n", " ")
			return fmt.Sprintf("Running: %s", c)
		}
	case "web_fetch":
		if url, ok := args["url"].(string); ok {
			u := url
			if runes := []rune(u); len(runes) > 40 {
				u = string(runes[:37]) + "..."
			}
			return fmt.Sprintf("Fetching %s", u)
		}
	case "web_search":
		if query, ok := args["query"].(string); ok {
			return fmt.Sprintf("Searching: %s", query)
		}
	case "list_dir", "tree":
		if path, ok := args["directory_path"].(string); ok {
			return fmt.Sprintf("Listing %s", shortenPath(path, 40))
		}
		return "Listing directory"
	case "task":
		if desc, ok := args["description"].(string); ok {
			return desc
		}
		if prompt, ok := args["prompt"].(string); ok {
			p := prompt
			if runes := []rune(p); len(runes) > 40 {
				p = string(runes[:37]) + "..."
			}
			return p
		}
	}
	return toolName
}

// GenerateResultSummary generates a brief summary from a tool result.
func GenerateResultSummary(toolName, result string) string {
	switch toolName {
	case "read":
		lines := strings.Count(result, "\n")
		if lines > 0 {
			return fmt.Sprintf("%d lines", lines)
		}
	case "glob":
		lines := strings.Count(strings.TrimSpace(result), "\n") + 1
		if strings.TrimSpace(result) == "" {
			return "0 files"
		}
		return fmt.Sprintf("%d files", lines)
	case "grep":
		lines := strings.Count(strings.TrimSpace(result), "\n") + 1
		if strings.TrimSpace(result) == "" {
			return "0 matches"
		}
		return fmt.Sprintf("%d matches", lines)
	case "bash":
		lines := strings.Count(strings.TrimSpace(result), "\n") + 1
		if strings.TrimSpace(result) == "" {
			return "done"
		}
		if lines == 1 {
			r := strings.TrimSpace(result)
			if runes := []rune(r); len(runes) > 30 {
				r = string(runes[:27]) + "..."
			}
			return r
		}
		return fmt.Sprintf("%d lines output", lines)
	case "edit":
		return "applied"
	case "write":
		lines := strings.Count(result, "\n")
		if lines > 0 {
			return fmt.Sprintf("%d lines written", lines)
		}
		return "written"
	}
	return ""
}

// buildMetricsSummary returns a compact metrics line like "2 agents · 8 tools · 3.2s".
// Must be called under read lock.
func (p *ActivityFeedPanel) buildMetricsSummary() string {
	if len(p.entries) == 0 {
		return ""
	}

	var agentCount, toolCount int
	var totalDuration time.Duration

	for _, entry := range p.entries {
		switch entry.Type {
		case ActivityTypeAgent:
			agentCount++
		case ActivityTypeTool:
			toolCount++
		}
		if entry.Duration > 0 {
			totalDuration += entry.Duration
		} else if entry.Status == ActivityRunning || entry.Status == ActivityPending {
			totalDuration += time.Since(entry.StartTime)
		}
	}

	var parts []string
	if agentCount > 0 {
		parts = append(parts, fmt.Sprintf("%d agents", agentCount))
	}
	if toolCount > 0 {
		parts = append(parts, fmt.Sprintf("%d tools", toolCount))
	}
	if totalDuration > 0 {
		parts = append(parts, format.Duration(totalDuration))
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func (p *ActivityFeedPanel) countRunningEntriesLocked() int {
	count := 0
	for _, entry := range p.entries {
		if entry.Status == ActivityRunning || entry.Status == ActivityPending {
			count++
		}
	}
	return count
}

// Clear removes all entries from the activity feed.
func (p *ActivityFeedPanel) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.entries = make([]ActivityFeedEntry, 0, maxActivityEntries)
	p.recentLog = make([]string, 0, maxRecentLog)
	p.activeEntries = make(map[string]int)
	p.subAgentActivities = make(map[string]*SubAgentState)
}
