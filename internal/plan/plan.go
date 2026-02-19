package plan

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Status represents the status of a plan or step.
type Status int

const (
	StatusPending Status = iota
	StatusInProgress
	StatusCompleted
	StatusFailed
	StatusSkipped
	StatusPaused // Temporarily paused, can be resumed
)

func (s Status) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusInProgress:
		return "in_progress"
	case StatusCompleted:
		return "completed"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped"
	case StatusPaused:
		return "paused"
	default:
		return "unknown"
	}
}

// Icon returns a display icon for the status.
func (s Status) Icon() string {
	switch s {
	case StatusPending:
		return "○"
	case StatusInProgress:
		return "◐"
	case StatusCompleted:
		return "●"
	case StatusFailed:
		return "✗"
	case StatusSkipped:
		return "⊘"
	case StatusPaused:
		return "⏸"
	default:
		return "?"
	}
}

// Step represents a single step in a plan.
type Step struct {
	ID               int               `json:"id"`
	Title            string            `json:"title"`
	Description      string            `json:"description"`
	Status           Status            `json:"status"`
	Output           string            `json:"output"`
	Error            string            `json:"error"`
	StartTime        time.Time         `json:"start_time,omitempty"`
	EndTime          time.Time         `json:"end_time,omitempty"`
	Parallel         bool              `json:"parallel"`                // Can execute in parallel with other steps
	DependsOn        []int             `json:"depends_on,omitempty"`    // Step IDs this step depends on
	Children         []*Step           `json:"children,omitempty"`      // Nested sub-steps
	MaxRetries       int               `json:"max_retries,omitempty"`   // Max retry attempts (0 = no retries)
	Timeout          time.Duration     `json:"timeout,omitempty"`       // Per-step timeout (0 = no timeout)
	RetryCount       int               `json:"retry_count,omitempty"`   // Current retry count
	TokensUsed       int               `json:"tokens_used,omitempty"`   // Tokens consumed by this step
	Condition        string            `json:"condition,omitempty"`     // Condition: "step_N_failed", "step_N_succeeded"
	AgentMetrics     *StepAgentMetrics `json:"agent_metrics,omitempty"` // Metrics from sub-agent tree planner
	CheckpointPassed bool              `json:"checkpoint_passed,omitempty"`
}

// StepAgentMetrics contains metrics from sub-agent tree planner execution.
type StepAgentMetrics struct {
	TotalNodes     int           `json:"total_nodes,omitempty"`
	MaxDepth       int           `json:"max_depth,omitempty"`
	ExpandedNodes  int           `json:"expanded_nodes,omitempty"`
	ReplanCount    int           `json:"replan_count,omitempty"`
	SucceededNodes int           `json:"succeeded_nodes,omitempty"`
	FailedNodes    int           `json:"failed_nodes,omitempty"`
	Duration       time.Duration `json:"duration,omitempty"`
}

// RunLedgerEntry tracks side effects produced by a plan step.
// It is used for safer resume and idempotency guardrails.
type RunLedgerEntry struct {
	StepID           int       `json:"step_id"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	LastHeartbeat    time.Time `json:"last_heartbeat,omitempty"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
	ToolCalls        int       `json:"tool_calls,omitempty"`
	Tools            []string  `json:"tools,omitempty"`
	FilesTouched     []string  `json:"files_touched,omitempty"`
	Commands         []string  `json:"commands,omitempty"`
	EffectSignatures []string  `json:"effect_signatures,omitempty"`
	DuplicateEffects int       `json:"duplicate_effects,omitempty"`
	PartialEffects   bool      `json:"partial_effects"`
	Completed        bool      `json:"completed"`
}

// Duration returns the step execution duration.
func (s *Step) Duration() time.Duration {
	if s.StartTime.IsZero() {
		return 0
	}
	if s.EndTime.IsZero() {
		return time.Since(s.StartTime)
	}
	return s.EndTime.Sub(s.StartTime)
}

// Plan represents an execution plan.
type Plan struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Steps       []*Step   `json:"steps"`
	Status      Status    `json:"status"`
	Lifecycle   Lifecycle `json:"lifecycle"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	Request     string    `json:"request"`            // Original user request
	Version     int       `json:"version"`            // Incremented on replan
	WorkDir     string    `json:"work_dir,omitempty"` // Project directory this plan belongs to

	// Context snapshot from planning conversation (preserved across session clear)
	ContextSnapshot string                  `json:"context_snapshot,omitempty"`
	RunLedger       map[int]*RunLedgerEntry `json:"run_ledger,omitempty"`

	mu sync.RWMutex
}

// SetContextSnapshot stores a summary of the planning conversation context.
func (p *Plan) SetContextSnapshot(snapshot string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ContextSnapshot = snapshot
	p.UpdatedAt = time.Now()
}

// GetContextSnapshot returns the saved context snapshot.
func (p *Plan) GetContextSnapshot() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ContextSnapshot
}

// NewPlan creates a new plan.
func NewPlan(title, description string) *Plan {
	return &Plan{
		ID:          fmt.Sprintf("plan_%d", time.Now().UnixNano()),
		Title:       title,
		Description: description,
		Steps:       make([]*Step, 0),
		Status:      StatusPending,
		Lifecycle:   LifecycleDraft,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		RunLedger:   make(map[int]*RunLedgerEntry),
	}
}

// AddStep adds a step to the plan.
func (p *Plan) AddStep(title, description string) *Step {
	return p.AddStepWithOptions(title, description, false, nil)
}

// AddStepWithOptions adds a step to the plan with options.
func (p *Plan) AddStepWithOptions(title, description string, parallel bool, dependsOn []int) *Step {
	p.mu.Lock()
	defer p.mu.Unlock()

	step := &Step{
		ID:          len(p.Steps) + 1,
		Title:       title,
		Description: description,
		Status:      StatusPending,
		Parallel:    parallel,
		DependsOn:   dependsOn,
	}
	p.Steps = append(p.Steps, step)
	p.UpdatedAt = time.Now()
	return step
}

// GetStep returns a step by ID.
func (p *Plan) GetStep(id int) *Step {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, step := range p.Steps {
		if step.ID == id {
			return step
		}
	}
	return nil
}

// CurrentStep returns the current in-progress step, or next pending step.
func (p *Plan) CurrentStep() *Step {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, step := range p.Steps {
		if step.Status == StatusInProgress {
			return step
		}
	}
	for _, step := range p.Steps {
		if step.Status == StatusPending {
			return step
		}
	}
	return nil
}

// NextStep returns the next pending step.
func (p *Plan) NextStep() *Step {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, step := range p.Steps {
		if step.Status == StatusPending {
			return step
		}
	}
	return nil
}

// StartStep marks a step as in progress.
func (p *Plan) StartStep(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, step := range p.Steps {
		if step.ID == id {
			step.Status = StatusInProgress
			step.StartTime = time.Now()
			p.Status = StatusInProgress
			p.markStepStartedLocked(id)
			p.UpdatedAt = time.Now()
			break
		}
	}
}

// CompleteStep marks a step as completed.
func (p *Plan) CompleteStep(id int, output string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, step := range p.Steps {
		if step.ID == id {
			step.Status = StatusCompleted
			step.Output = output
			step.EndTime = time.Now()
			p.markStepCompletedLocked(id)
			p.UpdatedAt = time.Now()
			break
		}
	}

	// Check if all steps are completed
	allCompleted := true
	for _, step := range p.Steps {
		if step.Status != StatusCompleted && step.Status != StatusSkipped {
			allCompleted = false
			break
		}
	}
	if allCompleted {
		p.Status = StatusCompleted
	}
}

// FailStep marks a step as failed.
func (p *Plan) FailStep(id int, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, step := range p.Steps {
		if step.ID == id {
			step.Status = StatusFailed
			step.Error = errMsg
			step.EndTime = time.Now()
			p.Status = StatusFailed
			p.markStepFailedLocked(id)
			p.UpdatedAt = time.Now()
			break
		}
	}
}

// SkipStep marks a step as skipped.
func (p *Plan) SkipStep(id int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, step := range p.Steps {
		if step.ID == id {
			step.Status = StatusSkipped
			p.markStepFailedLocked(id)
			p.UpdatedAt = time.Now()
			break
		}
	}
}

// Progress returns the completion progress (0.0 to 1.0).
func (p *Plan) Progress() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if len(p.Steps) == 0 {
		return 0
	}

	completed := 0
	for _, step := range p.Steps {
		if step.Status == StatusCompleted || step.Status == StatusSkipped {
			completed++
		}
	}
	return float64(completed) / float64(len(p.Steps))
}

// IsComplete returns true if the plan is complete (thread-safe).
func (p *Plan) IsComplete() bool {
	p.mu.RLock()
	status := p.Status
	p.mu.RUnlock()
	return status == StatusCompleted || status == StatusFailed
}

// Format returns a formatted string representation of the plan using tree view.
func (p *Plan) Format() string {
	return p.RenderTree()
}

// RenderTree returns a tree view of the plan with status indicators.
// Symbols: "✓" (completed), "→" (in progress), "○" (pending), "✗" (failed), "⊘" (skipped).
// Parallel steps are visually marked with "║" borders.
func (p *Plan) RenderTree() string {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var builder strings.Builder

	builder.WriteString(fmt.Sprintf("## %s\n", p.Title))
	if p.Description != "" {
		builder.WriteString(fmt.Sprintf("%s\n", p.Description))
	}
	builder.WriteString("\n")

	for _, step := range p.Steps {
		renderStepTree(&builder, step, "")
	}

	progress := p.progressLocked()
	completedCount := 0
	for _, step := range p.Steps {
		if step.Status == StatusCompleted || step.Status == StatusSkipped {
			completedCount++
		}
	}
	builder.WriteString(fmt.Sprintf("\nProgress: %.0f%% (%d/%d steps)\n",
		progress*100, completedCount, len(p.Steps)))

	return builder.String()
}

// progressLocked returns the completion progress without acquiring the lock.
// Caller must hold at least a read lock.
func (p *Plan) progressLocked() float64 {
	if len(p.Steps) == 0 {
		return 0
	}

	completed := 0
	for _, step := range p.Steps {
		if step.Status == StatusCompleted || step.Status == StatusSkipped {
			completed++
		}
	}
	return float64(completed) / float64(len(p.Steps))
}

// renderStepTree renders a single step and its children recursively.
func renderStepTree(builder *strings.Builder, step *Step, indent string) {
	icon := stepTreeIcon(step.Status)

	if step.Parallel {
		builder.WriteString(fmt.Sprintf("%s║ %s Step %d: %s  ║  (parallel)\n", indent, icon, step.ID, step.Title))
	} else {
		builder.WriteString(fmt.Sprintf("%s%s Step %d: %s\n", indent, icon, step.ID, step.Title))
	}

	childIndent := indent + "  "
	for _, child := range step.Children {
		renderStepTree(builder, child, childIndent)
	}
}

// stepTreeIcon returns the tree icon for a given status.
func stepTreeIcon(s Status) string {
	switch s {
	case StatusCompleted:
		return "✓"
	case StatusInProgress:
		return "→"
	case StatusPending:
		return "○"
	case StatusFailed:
		return "✗"
	case StatusSkipped:
		return "⊘"
	default:
		return "○"
	}
}

// StepCount returns the number of steps.
func (p *Plan) StepCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.Steps)
}

// GetStepsSnapshot returns a snapshot of all steps (thread-safe).
// The returned slice is a deep copy that can be safely modified without affecting the original.
func (p *Plan) GetStepsSnapshot() []*Step {
	p.mu.RLock()
	defer p.mu.RUnlock()

	snapshot := make([]*Step, len(p.Steps))
	for i, step := range p.Steps {
		if step != nil {
			snapshot[i] = deepCopyStep(step)
		}
	}
	return snapshot
}

// GetRunLedgerSnapshot returns a copy of run ledger entries keyed by step ID.
func (p *Plan) GetRunLedgerSnapshot() map[int]*RunLedgerEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	out := make(map[int]*RunLedgerEntry, len(p.RunLedger))
	for id, entry := range p.RunLedger {
		if entry == nil {
			continue
		}
		copyEntry := *entry
		copyEntry.Tools = append([]string(nil), entry.Tools...)
		copyEntry.FilesTouched = append([]string(nil), entry.FilesTouched...)
		copyEntry.Commands = append([]string(nil), entry.Commands...)
		copyEntry.EffectSignatures = append([]string(nil), entry.EffectSignatures...)
		out[id] = &copyEntry
	}
	return out
}

// deepCopyStep performs a deep copy of a Step, including its Children.
func deepCopyStep(step *Step) *Step {
	stepCopy := *step
	if len(step.Children) > 0 {
		stepCopy.Children = make([]*Step, len(step.Children))
		for i, child := range step.Children {
			if child != nil {
				stepCopy.Children[i] = deepCopyStep(child)
			}
		}
	}
	return &stepCopy
}

// CompletedCount returns the number of completed steps.
func (p *Plan) CompletedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, step := range p.Steps {
		if step.Status == StatusCompleted {
			count++
		}
	}
	return count
}

// PendingCount returns the number of pending or paused steps (resumable).
func (p *Plan) PendingCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	count := 0
	for _, step := range p.Steps {
		if step.Status == StatusPending || step.Status == StatusPaused || step.Status == StatusFailed {
			count++
		}
	}
	return count
}

// CanRetry returns true if the step can be retried.
func (s *Step) CanRetry() bool {
	return s.Status == StatusFailed && s.MaxRetries > 0 && s.RetryCount < s.MaxRetries
}

// RetryStep resets a failed step for retry.
func (p *Plan) RetryStep(id int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, step := range p.Steps {
		if step.ID == id {
			if step.Status != StatusFailed || step.MaxRetries == 0 || step.RetryCount >= step.MaxRetries {
				return false
			}
			step.RetryCount++
			step.Status = StatusPending
			step.Error = ""
			step.Output = ""
			step.StartTime = time.Time{}
			step.EndTime = time.Time{}
			p.Status = StatusInProgress
			p.UpdatedAt = time.Now()
			return true
		}
	}
	return false
}

// HasTimedOut returns true if the step has exceeded its timeout.
func (s *Step) HasTimedOut() bool {
	if s.Timeout <= 0 || s.StartTime.IsZero() || s.Status != StatusInProgress {
		return false
	}
	return time.Since(s.StartTime) > s.Timeout
}

// AddStepFull adds a step with all options including retry and timeout.
func (p *Plan) AddStepFull(title, description string, parallel bool, dependsOn []int, maxRetries int, timeout time.Duration) *Step {
	p.mu.Lock()
	defer p.mu.Unlock()

	step := &Step{
		ID:          len(p.Steps) + 1,
		Title:       title,
		Description: description,
		Status:      StatusPending,
		Parallel:    parallel,
		DependsOn:   dependsOn,
		MaxRetries:  maxRetries,
		Timeout:     timeout,
	}
	p.Steps = append(p.Steps, step)
	p.UpdatedAt = time.Now()
	return step
}

// CheckTimeouts checks all in-progress steps for timeouts and fails them.
func (p *Plan) CheckTimeouts() []int {
	p.mu.Lock()
	defer p.mu.Unlock()

	var timedOut []int
	for _, step := range p.Steps {
		if step.Status == StatusInProgress && step.Timeout > 0 && !step.StartTime.IsZero() {
			if time.Since(step.StartTime) > step.Timeout {
				step.Status = StatusFailed
				step.Error = fmt.Sprintf("step timed out after %s", step.Timeout)
				step.EndTime = time.Now()
				timedOut = append(timedOut, step.ID)
			}
		}
	}

	if len(timedOut) > 0 {
		p.UpdatedAt = time.Now()
	}
	return timedOut
}

// PauseStep marks a step as paused (can be resumed later).
// The plan itself stays InProgress so other non-dependent steps can continue.
func (p *Plan) PauseStep(id int, reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, step := range p.Steps {
		if step.ID == id {
			step.Status = StatusPaused
			step.Error = reason
			step.EndTime = time.Now()
			p.markStepFailedLocked(id)
			// Plan stays InProgress — other steps may still execute.
			// Use PausePlan() on Manager to explicitly pause the whole plan.
			p.UpdatedAt = time.Now()
			break
		}
	}
}

// RecordStepEffect records a tool-level side effect for the given step.
func (p *Plan) RecordStepEffect(stepID int, toolName string, args map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if stepID <= 0 {
		return
	}

	entry := p.ensureLedgerEntryLocked(stepID)
	signature := stepEffectSignature(toolName, args)
	if signature != "" {
		if containsString(entry.EffectSignatures, signature) {
			entry.DuplicateEffects++
		}
		entry.EffectSignatures = appendUniqueLimited(entry.EffectSignatures, signature, 120)
	}
	entry.ToolCalls++
	entry.PartialEffects = true
	entry.LastHeartbeat = time.Now()
	if toolName != "" {
		entry.Tools = appendUniqueLimited(entry.Tools, toolName, 20)
	}

	for _, key := range []string{"file_path", "path", "source", "destination", "src", "dst", "from", "to"} {
		if v, ok := args[key].(string); ok && v != "" {
			entry.FilesTouched = appendUniqueLimited(entry.FilesTouched, v, 50)
		}
	}

	if cmd, ok := args["command"].(string); ok && cmd != "" {
		entry.Commands = appendUniqueLimited(entry.Commands, cmd, 20)
	}
}

// HasDuplicateRisk returns true when a step repeatedly generated the same side
// effects without reaching completion, which is a strong idempotency warning.
func (p *Plan) HasDuplicateRisk(stepID int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entry, ok := p.RunLedger[stepID]
	if !ok || entry == nil || entry.Completed {
		return false
	}
	return entry.DuplicateEffects >= 2
}

// HasPartialEffects returns true when a step has recorded effects but no completion marker.
func (p *Plan) HasPartialEffects(stepID int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	entry, ok := p.RunLedger[stepID]
	return ok && entry.PartialEffects && !entry.Completed
}

func (p *Plan) markStepStartedLocked(stepID int) {
	entry := p.ensureLedgerEntryLocked(stepID)
	if entry.StartedAt.IsZero() {
		entry.StartedAt = time.Now()
	}
	entry.LastHeartbeat = time.Now()
}

func (p *Plan) markStepCompletedLocked(stepID int) {
	entry := p.ensureLedgerEntryLocked(stepID)
	now := time.Now()
	entry.LastHeartbeat = now
	entry.CompletedAt = now
	entry.Completed = true
	entry.PartialEffects = false
	entry.DuplicateEffects = 0
}

func (p *Plan) markStepFailedLocked(stepID int) {
	entry := p.ensureLedgerEntryLocked(stepID)
	entry.LastHeartbeat = time.Now()
}

func (p *Plan) ensureLedgerEntryLocked(stepID int) *RunLedgerEntry {
	if p.RunLedger == nil {
		p.RunLedger = make(map[int]*RunLedgerEntry)
	}
	entry, ok := p.RunLedger[stepID]
	if !ok || entry == nil {
		entry = &RunLedgerEntry{StepID: stepID}
		p.RunLedger[stepID] = entry
	}
	return entry
}

func appendUniqueLimited(items []string, value string, limit int) []string {
	for _, existing := range items {
		if existing == value {
			return items
		}
	}
	items = append(items, value)
	if limit > 0 && len(items) > limit {
		return items[len(items)-limit:]
	}
	return items
}

func stepEffectSignature(toolName string, args map[string]any) string {
	if toolName == "" {
		return ""
	}
	normalized := map[string]any{}
	for k, v := range args {
		switch k {
		case "timestamp", "ts", "nonce":
			continue
		default:
			normalized[k] = v
		}
	}

	encodedArgs, err := marshalDeterministic(normalized)
	if err != nil {
		return ""
	}
	sum := sha1.Sum([]byte(toolName + "|" + encodedArgs))
	return hex.EncodeToString(sum[:])
}

func marshalDeterministic(v map[string]any) (string, error) {
	// json.Marshal sorts map keys since Go 1.12.
	b, err := json.Marshal(v)
	return string(b), err
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

// HasPausedSteps returns true if any step has StatusPaused.
func (p *Plan) HasPausedSteps() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, step := range p.Steps {
		if step.Status == StatusPaused {
			return true
		}
	}
	return false
}

// ResumePausedSteps resets all paused steps back to StatusPending.
// Returns the number of steps resumed. Does not touch failed/skipped steps.
func (p *Plan) ResumePausedSteps() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	count := 0
	for _, step := range p.Steps {
		if step.Status == StatusPaused {
			if strings.Contains(strings.ToLower(step.Error), "checkpoint required") {
				step.CheckpointPassed = true
			}
			step.Status = StatusPending
			step.Error = ""
			step.StartTime = time.Time{}
			step.EndTime = time.Time{}
			count++
		}
	}
	if count > 0 {
		p.UpdatedAt = time.Now()
	}
	return count
}

// ShouldSkip evaluates the step's Condition against the plan's current state.
// Supported conditions: "step_N_failed", "step_N_succeeded".
// Returns true if the condition is NOT met (i.e., the step should be skipped).
func (s *Step) ShouldSkip(p *Plan) bool {
	if s.Condition == "" {
		return false
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	cond := strings.TrimSpace(s.Condition)

	// Parse "step_N_failed" or "step_N_succeeded"
	var refID int
	var condType string
	if n, _ := fmt.Sscanf(cond, "step_%d_%s", &refID, &condType); n == 2 {
		var refStep *Step
		for _, st := range p.Steps {
			if st.ID == refID {
				refStep = st
				break
			}
		}
		if refStep == nil {
			return true // Referenced step doesn't exist — skip
		}
		switch condType {
		case "failed":
			return refStep.Status != StatusFailed
		case "succeeded":
			return refStep.Status != StatusCompleted
		}
	}

	return false // Unknown condition format — don't skip
}

// NextReadySteps returns all steps that are ready to execute.
// A step is ready when it's pending, all its DependsOn are completed/skipped,
// and its condition (if any) is met.
// If the first ready step is not Parallel, only that step is returned.
// Otherwise, all ready Parallel steps are returned for concurrent execution.
func (p *Plan) NextReadySteps() []*Step {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Build set of completed/skipped step IDs
	done := make(map[int]bool)
	for _, step := range p.Steps {
		if step.Status == StatusCompleted || step.Status == StatusSkipped {
			done[step.ID] = true
		}
	}

	var ready []*Step
	for _, step := range p.Steps {
		if step.Status != StatusPending {
			continue
		}

		// Check all dependencies are done
		allDepsDone := true
		for _, depID := range step.DependsOn {
			if !done[depID] {
				allDepsDone = false
				break
			}
		}
		if !allDepsDone {
			continue
		}

		ready = append(ready, step)
	}

	if len(ready) == 0 {
		return nil
	}

	// If the first ready step is not parallel, return only it
	if !ready[0].Parallel {
		return ready[:1]
	}

	// Collect all leading parallel steps
	var parallel []*Step
	for _, s := range ready {
		if s.Parallel {
			parallel = append(parallel, s)
		} else {
			break
		}
	}
	return parallel
}

// ErrorCategory classifies plan step errors for retry/replan decisions.
type ErrorCategory int

const (
	ErrorTransient ErrorCategory = iota // timeout, network, rate limit — retry
	ErrorLogic                          // wrong args, validation — retry with different approach
	ErrorFatal                          // permission denied, not found — skip/replan
	ErrorUnknown
)

// String returns a human-readable name for the error category.
func (c ErrorCategory) String() string {
	switch c {
	case ErrorTransient:
		return "transient"
	case ErrorLogic:
		return "logic"
	case ErrorFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// ClassifyError categorizes an error for retry/replan decisions.
func ClassifyError(err error, errMsg string) ErrorCategory {
	if err != nil {
		// Transient: timeout, network, rate limit
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return ErrorTransient
		}
		var netErr net.Error
		if errors.As(err, &netErr) {
			return ErrorTransient
		}
	}

	msg := errMsg
	if msg == "" && err != nil {
		msg = err.Error()
	}
	lower := strings.ToLower(msg)

	// Transient patterns
	for _, p := range []string{"rate limit", "timeout", "connection refused", "eof", "503", "502", "429"} {
		if strings.Contains(lower, p) {
			return ErrorTransient
		}
	}

	// Fatal patterns
	for _, p := range []string{"permission denied", "not found", "no such file", "access denied", "forbidden"} {
		if strings.Contains(lower, p) {
			return ErrorFatal
		}
	}

	// Logic patterns
	for _, p := range []string{"validation error", "invalid argument", "wrong", "syntax error", "parse error"} {
		if strings.Contains(lower, p) {
			return ErrorLogic
		}
	}

	return ErrorUnknown
}
