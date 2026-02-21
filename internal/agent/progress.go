package agent

import (
	"fmt"
	"time"
)

// AgentProgress represents the current progress of an agent.
type AgentProgress struct {
	AgentID            string
	AgentType          AgentType
	CurrentStep        int
	TotalSteps         int
	CurrentAction      string
	StartTime          time.Time
	Elapsed            time.Duration
	EstimatedRemaining time.Duration
	ToolsUsed          []string
	Status             AgentStatus
}

// ProgressCallback is called when agent progress is updated.
type ProgressCallback func(progress *AgentProgress)

// SetProgressCallback sets the callback for progress updates.
func (a *Agent) SetProgressCallback(callback ProgressCallback) {
	a.progressMu.Lock()
	defer a.progressMu.Unlock()
	a.progressCallback = callback
}

// updateProgress sends a progress update if callback is set.
func (a *Agent) updateProgress() {
	// Read progress fields under progressMu
	a.progressMu.Lock()
	callback := a.progressCallback
	if callback == nil {
		a.progressMu.Unlock()
		return
	}
	currentStep := a.currentStep
	totalSteps := a.totalSteps
	stepDescription := a.stepDescription
	a.progressMu.Unlock()

	// Read status and startTime under stateMu (separate lock)
	a.stateMu.RLock()
	status := a.status
	startTime := a.startTime
	a.stateMu.RUnlock()

	progress := &AgentProgress{
		AgentID:       a.ID,
		AgentType:     a.Type,
		CurrentStep:   currentStep,
		TotalSteps:    totalSteps,
		CurrentAction: stepDescription,
		StartTime:     startTime,
		Elapsed:       time.Since(startTime),
		Status:        status,
	}
	progress.EstimatedRemaining = progress.EstimateRemaining()

	callback(progress)
}

// SetProgress sets the current progress state.
func (a *Agent) SetProgress(step int, total int, description string) {
	a.progressMu.Lock()
	a.currentStep = step
	a.totalSteps = total
	a.stepDescription = description
	a.progressMu.Unlock()

	a.updateProgress()
}

// IncrementStep increments the current step and updates description.
func (a *Agent) IncrementStep(description string) {
	a.progressMu.Lock()
	a.currentStep++
	a.stepDescription = description
	a.progressMu.Unlock()

	a.updateProgress()
}

// GetProgress returns the current progress state.
func (a *Agent) GetProgress() AgentProgress {
	// Snapshot stateMu-protected fields first (separate lock domain)
	a.stateMu.RLock()
	startTime := a.startTime
	status := a.status
	a.stateMu.RUnlock()

	a.progressMu.Lock()
	currentStep := a.currentStep
	totalSteps := a.totalSteps
	stepDesc := a.stepDescription
	a.progressMu.Unlock()

	// Get tools used
	a.toolsMu.Lock()
	toolsUsed := make([]string, len(a.toolsUsed))
	copy(toolsUsed, a.toolsUsed)
	a.toolsMu.Unlock()

	return AgentProgress{
		AgentID:       a.ID,
		AgentType:     a.Type,
		CurrentStep:   currentStep,
		TotalSteps:    totalSteps,
		CurrentAction: stepDesc,
		StartTime:     startTime,
		Elapsed:       time.Since(startTime),
		Status:        status,
		ToolsUsed:     toolsUsed,
	}
}

// FormatProgress returns a formatted progress string.
func (p *AgentProgress) FormatProgress() string {
	if p.TotalSteps <= 0 {
		return fmt.Sprintf("[%s] Step %d: %s", p.AgentType, p.CurrentStep, p.CurrentAction)
	}

	percent := float64(p.CurrentStep) / float64(p.TotalSteps) * 100
	return fmt.Sprintf("[%s] %d/%d (%.0f%%): %s",
		p.AgentType, p.CurrentStep, p.TotalSteps, percent, p.CurrentAction)
}

// EstimateRemaining estimates the remaining time based on elapsed time and progress.
func (p *AgentProgress) EstimateRemaining() time.Duration {
	if p.CurrentStep <= 0 || p.TotalSteps <= 0 {
		return 0
	}

	avgTimePerStep := p.Elapsed / time.Duration(p.CurrentStep)
	stepsRemaining := p.TotalSteps - p.CurrentStep
	return avgTimePerStep * time.Duration(stepsRemaining)
}
