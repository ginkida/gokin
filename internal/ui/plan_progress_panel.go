package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// PlanStepStatus represents the status of a plan step.
type PlanStepStatus int

const (
	PlanStepPending PlanStepStatus = iota
	PlanStepInProgress
	PlanStepCompleted
	PlanStepFailed
	PlanStepSkipped
	PlanStepPaused
)

// PlanStepState holds the state of a single plan step.
type PlanStepState struct {
	ID            int
	Title         string
	Description   string
	Status        PlanStepStatus
	StartedAt     time.Time
	CompletedAt   time.Time
	Output        string
	Error         string
	FilesModified int
}

// StepTransition tracks a single state transition in the plan timeline.
type StepTransition struct {
	StepID  int
	From    PlanStepStatus
	To      PlanStepStatus
	Reason  string
	At      time.Time
	StepRef string
}

// ActivityEntry represents a single activity log entry.
type ActivityEntry struct {
	Timestamp time.Time
	Type      string // "tool", "file", "info"
	Message   string
}

// PlanProgressPanel displays detailed plan execution progress.
type PlanProgressPanel struct {
	visible       bool
	planID        string
	planTitle     string
	planDesc      string
	steps         []PlanStepState
	currentStepID int
	startedAt     time.Time
	// finishedAt is stamped by EndPlan when the WHOLE plan reaches a terminal
	// state. The panel lingers planPanelLingerAfterDone so the user sees the
	// final state, then View/ViewCompact self-empty — same auto-hide idiom as
	// the agent tree's allDoneAt (v0.91.0). Before this the panel had NO live
	// hide path at all (Hide/EndPlanExecution/PlanCompleteMsg were dead code)
	// and stayed pinned, elapsed ticking, for the rest of the session.
	finishedAt    time.Time
	styles        *Styles
	collapsed     bool // Compact mode
	frame         int  // For animations
	reducedMotion bool

	// Live activity feed
	activities    []ActivityEntry
	maxActivities int    // Max entries to keep (default: 5)
	currentTool   string // Currently executing tool
	currentInfo   string // Tool info (file path, command, etc.)
	timeline      []StepTransition
}

// NewPlanProgressPanel creates a new plan progress panel.
func NewPlanProgressPanel(styles *Styles) *PlanProgressPanel {
	return &PlanProgressPanel{
		visible:       false,
		steps:         make([]PlanStepState, 0),
		styles:        styles,
		collapsed:     false,
		frame:         0,
		activities:    make([]ActivityEntry, 0),
		maxActivities: 5,
		timeline:      make([]StepTransition, 0, 32),
	}
}

// planAutoCollapseSteps is the largest plan that still renders fully
// expanded by default. Above this the panel starts collapsed (current step +
// one summary row) — a 10-step plan otherwise pins a ~17-line box to the
// screen for the whole execution, which was the #1 "panel eats my screen"
// complaint. Ctrl+X expands on demand.
const planAutoCollapseSteps = 4

// planPanelLingerAfterDone is how long the panel stays visible after the
// whole plan reaches a terminal state — long enough to read the final ✓/✗
// summary, short enough that a finished plan doesn't shadow the next
// conversation (mirrors agentTreeLingerAfterDone, just longer because a plan
// summary carries more to read than a task tree).
const planPanelLingerAfterDone = 10 * time.Second

// StartPlan initializes a new plan execution.
func (p *PlanProgressPanel) StartPlan(planID, title, description string, steps []PlanStepInfo) {
	p.visible = true
	p.planID = planID
	p.planTitle = title
	p.planDesc = description
	p.startedAt = time.Now()
	p.finishedAt = time.Time{}
	p.currentStepID = 0
	p.collapsed = len(steps) > planAutoCollapseSteps
	p.timeline = p.timeline[:0]
	p.currentTool = ""
	p.currentInfo = ""
	p.activities = p.activities[:0]

	p.steps = make([]PlanStepState, len(steps))
	for i, step := range steps {
		p.steps[i] = PlanStepState{
			ID:          step.ID,
			Title:       step.Title,
			Description: step.Description,
			Status:      PlanStepPending,
		}
		p.appendTransition(step.ID, PlanStepPending, PlanStepPending, "queued")
	}
	if len(steps) == 0 {
		p.finishedAt = time.Now()
	}
}

// StartStep marks a step as in progress.
func (p *PlanProgressPanel) StartStep(stepID int) {
	for i := range p.steps {
		if p.steps[i].ID == stepID {
			prev := p.steps[i].Status
			p.steps[i].Status = PlanStepInProgress
			p.steps[i].StartedAt = time.Now()
			p.steps[i].CompletedAt = time.Time{}
			p.steps[i].Output = ""
			p.steps[i].Error = ""
			p.currentStepID = stepID
			p.appendTransition(stepID, prev, PlanStepInProgress, "")
			break
		}
	}
	// A step starting means the plan is alive again (resume/replan after a
	// premature terminal signal) — cancel any pending auto-hide.
	p.finishedAt = time.Time{}
}

// CompleteStep marks a step as completed.
func (p *PlanProgressPanel) CompleteStep(stepID int, output string, reason string) {
	for i := range p.steps {
		if p.steps[i].ID == stepID {
			prev := p.steps[i].Status
			p.steps[i].Status = PlanStepCompleted
			p.steps[i].CompletedAt = time.Now()
			p.steps[i].Output = output
			p.appendTransition(stepID, prev, PlanStepCompleted, reason)
			p.clearToolForStep(stepID)
			break
		}
	}
}

// FailStep marks a step as failed.
func (p *PlanProgressPanel) FailStep(stepID int, errorMsg string, reason string) {
	for i := range p.steps {
		if p.steps[i].ID == stepID {
			prev := p.steps[i].Status
			p.steps[i].Status = PlanStepFailed
			p.steps[i].CompletedAt = time.Now()
			p.steps[i].Error = errorMsg
			p.appendTransition(stepID, prev, PlanStepFailed, reason)
			p.clearToolForStep(stepID)
			break
		}
	}
}

// SkipStep marks a step as skipped.
func (p *PlanProgressPanel) SkipStep(stepID int, reason string) {
	for i := range p.steps {
		if p.steps[i].ID == stepID {
			prev := p.steps[i].Status
			p.steps[i].Status = PlanStepSkipped
			p.steps[i].CompletedAt = time.Now()
			p.appendTransition(stepID, prev, PlanStepSkipped, reason)
			p.clearToolForStep(stepID)
			break
		}
	}
}

// PauseStep marks a step as paused.
func (p *PlanProgressPanel) PauseStep(stepID int, reason string) {
	for i := range p.steps {
		if p.steps[i].ID == stepID {
			prev := p.steps[i].Status
			p.steps[i].Status = PlanStepPaused
			p.steps[i].CompletedAt = time.Now()
			p.steps[i].Error = reason
			p.appendTransition(stepID, prev, PlanStepPaused, reason)
			p.clearToolForStep(stepID)
			break
		}
	}
}

func (p *PlanProgressPanel) clearToolForStep(stepID int) {
	if p.currentStepID == stepID {
		p.currentTool = ""
		p.currentInfo = ""
	}
}

func (p *PlanProgressPanel) appendTransition(stepID int, from, to PlanStepStatus, reason string) {
	if from == to && reason == "" {
		return
	}
	stepRef := fmt.Sprintf("step %d", stepID)
	for _, s := range p.steps {
		if s.ID == stepID && s.Title != "" {
			stepRef = fmt.Sprintf("step %d (%s)", stepID, s.Title)
			break
		}
	}
	p.timeline = append(p.timeline, StepTransition{
		StepID:  stepID,
		From:    from,
		To:      to,
		Reason:  reason,
		At:      time.Now(),
		StepRef: stepRef,
	})
	if len(p.timeline) > 40 {
		p.timeline = p.timeline[len(p.timeline)-40:]
	}
}

// EndPlan marks the WHOLE plan as finished: the panel lingers
// planPanelLingerAfterDone (elapsed frozen), then self-hides via
// lingerExpired. Idempotent — a repeated terminal message must not extend
// the linger window.
func (p *PlanProgressPanel) EndPlan() {
	if p.finishedAt.IsZero() {
		p.finishedAt = time.Now()
	}
	p.currentTool = ""
	p.currentInfo = ""
}

// Hide hides the panel.
func (p *PlanProgressPanel) Hide() {
	p.visible = false
}

// IsVisible returns whether the panel is visible.
func (p *PlanProgressPanel) IsVisible() bool {
	return p.visible
}

// lingerExpired reports whether the post-completion display window is over.
func (p *PlanProgressPanel) lingerExpired() bool {
	return !p.finishedAt.IsZero() && time.Since(p.finishedAt) > planPanelLingerAfterDone
}

// Toggle toggles collapsed mode. An explicit Ctrl+X also re-pins a finished
// panel (clears the linger stamp) — user intent overrides auto-hide, the same
// idiom as the agent tree's Toggle-on clearing allDoneAt (v0.91.0).
func (p *PlanProgressPanel) Toggle() {
	p.collapsed = !p.collapsed
	p.finishedAt = time.Time{}
}

// Tick updates the animation frame.
func (p *PlanProgressPanel) Tick() {
	if !p.reducedMotion {
		p.frame++
	}
}

func (p *PlanProgressPanel) SetReducedMotion(enabled bool) { p.reducedMotion = enabled }

// SetCurrentTool updates the currently executing tool.
func (p *PlanProgressPanel) SetCurrentTool(toolName, toolInfo string) {
	toolName = strings.Join(strings.Fields(toolName), " ")
	toolInfo = strings.Join(strings.Fields(toolInfo), " ")
	if toolName == "" {
		p.currentTool = ""
		p.currentInfo = ""
		return
	}

	p.currentTool = toolName
	p.currentInfo = toolInfo

	// Track file modifications for current step
	switch toolName {
	case "write", "edit", "delete", "copy", "move":
		for i := range p.steps {
			if p.steps[i].ID == p.currentStepID {
				p.steps[i].FilesModified++
				break
			}
		}
	}

	// Add to activity log
	msg := toolName
	if toolInfo != "" {
		if runes := []rune(toolInfo); len(runes) > 40 {
			toolInfo = string(runes[:37]) + "..."
		}
		msg += ": " + toolInfo
	}
	p.AddActivity("tool", msg)
}

// ClearCurrentTool clears the current tool.
func (p *PlanProgressPanel) ClearCurrentTool() {
	p.currentTool = ""
	p.currentInfo = ""
}

// AddActivity adds an entry to the activity log.
func (p *PlanProgressPanel) AddActivity(actType, message string) {
	actType = strings.Join(strings.Fields(actType), " ")
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		return
	}
	entry := ActivityEntry{
		Timestamp: time.Now(),
		Type:      actType,
		Message:   message,
	}
	p.activities = append(p.activities, entry)

	// Trim to max size
	if len(p.activities) > p.maxActivities {
		p.activities = p.activities[len(p.activities)-p.maxActivities:]
	}
}

// ClearActivities clears the activity log.
func (p *PlanProgressPanel) ClearActivities() {
	p.activities = make([]ActivityEntry, 0)
}

// Progress returns the completion progress (0.0 to 1.0).
func (p *PlanProgressPanel) Progress() float64 {
	if len(p.steps) == 0 {
		return 0
	}

	completed := 0
	for _, step := range p.steps {
		if step.Status == PlanStepCompleted || step.Status == PlanStepSkipped {
			completed++
		}
	}
	return float64(completed) / float64(len(p.steps))
}

// CompletedCount returns the number of completed steps.
func (p *PlanProgressPanel) CompletedCount() int {
	count := 0
	for _, step := range p.steps {
		if step.Status == PlanStepCompleted || step.Status == PlanStepSkipped {
			count++
		}
	}
	return count
}

func (p *PlanProgressPanel) FailedCount() int {
	count := 0
	for _, step := range p.steps {
		if step.Status == PlanStepFailed {
			count++
		}
	}
	return count
}

func (p *PlanProgressPanel) PausedCount() int {
	count := 0
	for _, step := range p.steps {
		if step.Status == PlanStepPaused {
			count++
		}
	}
	return count
}

func (p *PlanProgressPanel) elapsed() time.Duration {
	if p.startedAt.IsZero() {
		return 0
	}
	end := time.Now()
	if !p.finishedAt.IsZero() {
		end = p.finishedAt
	}
	return max(end.Sub(p.startedAt), 0)
}

func (p *PlanProgressPanel) focusStepIndex() int {
	for _, status := range []PlanStepStatus{PlanStepInProgress, PlanStepFailed, PlanStepPaused, PlanStepPending, PlanStepCompleted, PlanStepSkipped} {
		for i := range p.steps {
			if p.steps[i].Status == status {
				return i
			}
		}
	}
	return -1
}

// View renders the plan progress panel with exact outer-width geometry.
func (p *PlanProgressPanel) View(width int, heights ...int) string {
	if !p.visible || p.lingerExpired() {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	if width < 4 {
		return truncateForWidth("Plan", width)
	}
	height := 0
	if len(heights) > 0 {
		height = heights[0]
	}

	borderStyle := lipgloss.NewStyle().Foreground(ColorPlan)
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorPlan)
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted)
	errorStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorError)
	warningStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorWarning)

	var content strings.Builder
	outerWidth := min(width, 100)
	innerWidth := outerWidth - 2
	separator := func(left, fill, right string) {
		content.WriteString(borderStyle.Render(left + strings.Repeat(fill, innerWidth) + right))
		content.WriteString("\n")
	}
	separator("╭", "─", "╮")

	title := strings.Join(strings.Fields(p.planTitle), " ")
	if title == "" {
		title = "Untitled plan"
	}
	failed := p.FailedCount()
	paused := p.PausedCount()
	titlePrefix := "Plan: "
	titleLineStyle := titleStyle
	if !p.finishedAt.IsZero() && failed > 0 {
		titlePrefix = "Plan failed: "
		titleLineStyle = errorStyle
	} else if !p.finishedAt.IsZero() {
		titlePrefix = "Plan complete: "
	} else if paused > 0 {
		titlePrefix = "Plan paused: "
		titleLineStyle = warningStyle
	}
	p.writePlanLine(&content, borderStyle, titleLineStyle.Render(titlePrefix+title), innerWidth)

	if len(p.steps) == 0 {
		p.writePlanLine(&content, borderStyle, mutedStyle.Italic(true).Render("No executable steps in this plan."), innerWidth)
		content.WriteString(borderStyle.Render("╰" + strings.Repeat("─", innerWidth) + "╯"))
		return content.String()
	}

	progress := p.Progress()
	progressInfo := fmt.Sprintf("%d/%d · %s", p.CompletedCount(), len(p.steps), formatElapsed(p.elapsed()))
	if failed > 0 {
		progressInfo += fmt.Sprintf(" · %d failed", failed)
	}
	barWidth := min(20, max(innerWidth-lipgloss.Width(progressInfo)-1, 0))
	progressLine := ""
	if barWidth >= 4 {
		filled := min(max(int(progress*float64(barWidth)), 0), barWidth)
		progressLine = p.renderProgressBar(filled, barWidth, progress) + " "
	}
	progressLine += mutedStyle.Render(progressInfo)
	p.writePlanLine(&content, borderStyle, progressLine, innerWidth)
	separator("├", "─", "┤")

	autoCompact := height > 0 && height < 16
	effectiveCollapsed := p.collapsed || autoCompact
	if !effectiveCollapsed {
		for _, step := range p.steps {
			p.writeAdaptiveStepLines(&content, borderStyle, p.renderStep(step, max(innerWidth-2, 1)), innerWidth)
		}
	} else {
		shown := 0
		if index := p.focusStepIndex(); index >= 0 {
			p.writeAdaptiveStepLines(&content, borderStyle, p.renderStep(p.steps[index], max(innerWidth-2, 1)), innerWidth)
			shown = 1
		}
		hidden := len(p.steps) - shown
		action := "Ctrl+X to expand"
		if autoCompact && !p.collapsed {
			action = "resize to expand"
		}
		summary := fmt.Sprintf("… %d hidden · ✓ %d done", hidden, p.CompletedCount())
		if failed > 0 {
			summary += fmt.Sprintf(" · %d failed", failed)
		}
		p.writePlanLine(&content, borderStyle, dimStyle.Render(summary+" · "+action), innerWidth)
	}

	if p.currentTool != "" {
		separator("├", "─", "┤")
		spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		toolStyle := lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
		infoStyle := lipgloss.NewStyle().Foreground(ColorAccent)
		indicator := spinners[p.frame%len(spinners)]
		if p.reducedMotion {
			indicator = "●"
		}
		toolLine := toolStyle.Render(indicator + " " + strings.Join(strings.Fields(p.currentTool), " "))
		if p.currentInfo != "" {
			if budget := innerWidth - lipgloss.Width(toolLine) - 1; budget > 0 {
				toolLine += " " + infoStyle.Render(truncateForWidth(strings.Join(strings.Fields(p.currentInfo), " "), budget))
			}
		}
		p.writePlanLine(&content, borderStyle, toolLine, innerWidth)
	}

	if paused > 0 {
		resumeStyle := lipgloss.NewStyle().Foreground(ColorInfo).Bold(true)
		p.writePlanLine(&content, borderStyle, resumeStyle.Render("/resume-plan")+" "+mutedStyle.Render("Continue execution"), innerWidth)
	}
	if !effectiveCollapsed {
		p.writePlanLine(&content, borderStyle, dimStyle.Render("Ctrl+X  Collapse"), innerWidth)
	}
	content.WriteString(borderStyle.Render("╰" + strings.Repeat("─", innerWidth) + "╯"))
	return content.String()
}

func (p *PlanProgressPanel) writePlanLine(content *strings.Builder, borderStyle lipgloss.Style, rendered string, innerWidth int) {
	rendered = fitPanelContent(rendered, innerWidth)
	content.WriteString(borderStyle.Render("│"))
	content.WriteString(rendered)
	if padding := innerWidth - lipgloss.Width(rendered); padding > 0 {
		content.WriteString(strings.Repeat(" ", padding))
	}
	content.WriteString(borderStyle.Render("│"))
	content.WriteString("\n")
}

func (p *PlanProgressPanel) writeAdaptiveStepLines(content *strings.Builder, borderStyle lipgloss.Style, rendered string, innerWidth int) {
	horizontalPadding := 1
	if innerWidth < 3 {
		horizontalPadding = 0
	}
	lineWidth := max(innerWidth-horizontalPadding*2, 1)
	for _, line := range strings.Split(rendered, "\n") {
		line = fitPanelContent(line, lineWidth)
		content.WriteString(borderStyle.Render("│" + strings.Repeat(" ", horizontalPadding)))
		content.WriteString(line)
		if padding := lineWidth - lipgloss.Width(line); padding > 0 {
			content.WriteString(strings.Repeat(" ", padding))
		}
		content.WriteString(borderStyle.Render(strings.Repeat(" ", horizontalPadding) + "│"))
		content.WriteString("\n")
	}
}

// renderStep renders a single step with tree-style status icon.
// Uses animated spinner for in-progress, "✓" completed, "○" pending, "✗" failed, "⊘" skipped.
func (p *PlanProgressPanel) renderStep(step PlanStepState, maxWidth int) string {
	var icon string
	var iconStyle lipgloss.Style
	dimStyle := lipgloss.NewStyle().Foreground(ColorDim)

	switch step.Status {
	case PlanStepPending:
		icon = "○"
		iconStyle = lipgloss.NewStyle().Foreground(ColorDim)
	case PlanStepInProgress:
		// Animated braille spinner so it's obvious the step is actively running
		spinners := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		icon = spinners[p.frame%len(spinners)]
		if p.reducedMotion {
			icon = "●"
		}
		iconStyle = lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	case PlanStepPaused:
		icon = "⏸"
		iconStyle = lipgloss.NewStyle().Foreground(ColorWarning).Bold(true)
	case PlanStepCompleted:
		icon = "✓"
		iconStyle = lipgloss.NewStyle().Foreground(ColorSuccess)
	case PlanStepFailed:
		icon = "✗"
		iconStyle = lipgloss.NewStyle().Foreground(ColorError)
	case PlanStepSkipped:
		icon = "⊘"
		iconStyle = lipgloss.NewStyle().Foreground(ColorMuted)
	}

	titleStyle := lipgloss.NewStyle().Foreground(ColorText)
	if step.Status == PlanStepInProgress {
		titleStyle = titleStyle.Bold(true)
	} else if step.Status == PlanStepPending {
		titleStyle = lipgloss.NewStyle().Foreground(ColorDim)
	}

	stepTitle := strings.Join(strings.Fields(step.Title), " ")
	if stepTitle == "" {
		stepTitle = "Untitled step"
	}
	title := fmt.Sprintf("Step %d: %s", step.ID, stepTitle)

	durationStr := ""
	switch step.Status {
	case PlanStepCompleted, PlanStepFailed:
		if !step.CompletedAt.IsZero() && !step.StartedAt.IsZero() {
			duration := max(step.CompletedAt.Sub(step.StartedAt), 0)
			if step.FilesModified > 0 {
				durationStr = " " + dimStyle.Render(fmt.Sprintf("(%s · %d files)", formatElapsed(duration), step.FilesModified))
			} else {
				durationStr = " " + dimStyle.Render("("+formatElapsed(duration)+")")
			}
		}
	case PlanStepInProgress:
		// Show live elapsed time so users know how long the current step has been running
		if !step.StartedAt.IsZero() {
			durationStr = " " + dimStyle.Render("["+formatElapsed(max(time.Since(step.StartedAt), 0))+"]")
		}
	}
	maxTitleWidth := max(maxWidth-2-lipgloss.Width(durationStr), 0)
	title = truncateForWidth(title, maxTitleWidth)

	result := iconStyle.Render(icon) + " " + titleStyle.Render(title) + durationStr

	// For the active step: prioritise the currently-executing tool over the
	// static description — it's more actionable ("what is the agent doing RIGHT NOW").
	if step.Status == PlanStepInProgress {
		if p.currentStepID == step.ID && p.currentTool != "" {
			toolLine := strings.Join(strings.Fields(p.currentTool), " ")
			if p.currentInfo != "" {
				toolLine += ": " + strings.Join(strings.Fields(p.currentInfo), " ")
			}
			result += "\n    " + dimStyle.Render(truncateForWidth("└─ "+toolLine, max(maxWidth-4, 0)))
		} else if step.Description != "" {
			desc := truncateForWidth(strings.Join(strings.Fields(step.Description), " "), max(maxWidth-4, 0))
			result += "\n    " + dimStyle.Italic(true).Render(desc)
		}
	}

	// Show first line of output for completed steps
	if step.Status == PlanStepCompleted && step.Output != "" {
		summary := truncateForWidth(normalizeTimelineText(firstLine(step.Output)), max(maxWidth-4, 0))
		if summary != "" {
			result += "\n    " + dimStyle.Italic(true).Render(summary)
		}
	}

	if step.Status == PlanStepPaused && step.Error != "" {
		summary := truncateForWidth(strings.Join(strings.Fields(step.Error), " "), max(maxWidth-4, 0))
		result += "\n    " + lipgloss.NewStyle().Foreground(ColorWarning).Italic(true).Render(summary)
	}

	if step.Status == PlanStepFailed && step.Error != "" {
		errLine := truncateForWidth("✗ "+strings.Join(strings.Fields(step.Error), " "), max(maxWidth-4, 0))
		result += "\n    " + lipgloss.NewStyle().Foreground(ColorError).Render(errLine)
	}

	return result
}

// renderProgressBar renders a visual progress bar.
func (p *PlanProgressPanel) renderProgressBar(filled, width int, progress float64) string {
	width = max(width, 0)
	filled = min(max(filled, 0), width)
	// Determine color based on progress
	var barColor lipgloss.Color
	if progress >= 1.0 {
		barColor = ColorSuccess
	} else if progress >= 0.5 {
		barColor = ColorWarning
	} else {
		barColor = ColorPlan
	}

	barStyle := lipgloss.NewStyle().Foreground(barColor)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorDim)

	bar := barStyle.Render(strings.Repeat("█", filled)) +
		emptyStyle.Render(strings.Repeat("░", width-filled))

	return bar
}

// firstLine returns the first non-empty line from text.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// formatElapsed formats a duration for display.
func formatElapsed(d time.Duration) string {
	d = max(d, 0)
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.1fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}

// ViewCompact renders a compact single-line version for status bar.
func (p *PlanProgressPanel) ViewCompact() string {
	if !p.visible || len(p.steps) == 0 || p.lingerExpired() {
		return ""
	}

	progress := p.Progress()
	completed := p.CompletedCount()
	total := len(p.steps)

	// Find current step
	currentTitle := ""
	for _, step := range p.steps {
		if step.Status == PlanStepInProgress {
			currentTitle = truncateForWidth(strings.Join(strings.Fields(step.Title), " "), 15)
			break
		}
	}

	// Animated icon
	var icon string
	if !p.finishedAt.IsZero() && p.FailedCount() > 0 {
		icon = "✗"
	} else if p.PausedCount() > 0 {
		icon = "⏸"
	} else if progress >= 1.0 {
		icon = "✓"
	} else {
		spinners := []string{"◐", "◓", "◑", "◒"}
		icon = spinners[p.frame%len(spinners)]
		if p.reducedMotion {
			icon = "●"
		}
	}

	planStyle := lipgloss.NewStyle().Foreground(ColorPlan).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(ColorMuted)

	result := planStyle.Render(icon+" Plan") +
		mutedStyle.Render(fmt.Sprintf(" %d/%d", completed, total))

	if currentTitle != "" {
		result += lipgloss.NewStyle().Foreground(ColorDim).Render(" • " + currentTitle)
	}

	return result
}

// RenderStepNotification renders a notification for step status change.
func (p *PlanProgressPanel) RenderStepNotification(stepID int, status PlanStepStatus) string {
	var step *PlanStepState
	for i := range p.steps {
		if p.steps[i].ID == stepID {
			step = &p.steps[i]
			break
		}
	}

	if step == nil {
		return ""
	}

	var icon, verb string
	var color lipgloss.Color

	switch status {
	case PlanStepInProgress:
		icon = "→"
		verb = "Starting"
		color = ColorWarning
	case PlanStepCompleted:
		icon = "✓"
		verb = "Completed"
		color = ColorSuccess
	case PlanStepFailed:
		icon = "✗"
		verb = "Failed"
		color = ColorError
	case PlanStepSkipped:
		icon = "⊘"
		verb = "Skipped"
		color = ColorMuted
	default:
		return ""
	}

	style := lipgloss.NewStyle().Foreground(color)
	titleStyle := lipgloss.NewStyle().Foreground(ColorText)

	stepNum := fmt.Sprintf("[%d/%d]", stepID, len(p.steps))

	return style.Render(icon+" "+verb) + " " +
		lipgloss.NewStyle().Foreground(ColorDim).Render(stepNum) + " " +
		titleStyle.Render(step.Title)
}
