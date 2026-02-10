package plan

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gokin/internal/logging"
)

// ApprovalDecision represents the user's decision on a plan.
type ApprovalDecision int

const (
	ApprovalPending ApprovalDecision = iota
	ApprovalApproved
	ApprovalRejected
	ApprovalModified
)

// ApprovalHandler is called to get user approval for a plan.
type ApprovalHandler func(ctx context.Context, plan *Plan) (ApprovalDecision, error)

// StepHandler is called before executing each step.
// It can be used to show progress or confirm individual steps.
type StepHandler func(step *Step)

// ReplanHandler generates replacement steps when a step fails fatally.
// It receives the current plan and the failed step, and returns new steps to replace
// the remaining pending steps.
type ReplanHandler func(ctx context.Context, plan *Plan, failedStep *Step) ([]*Step, error)

// Manager manages plan mode state and execution.
type Manager struct {
	enabled         bool
	requireApproval bool

	currentPlan      *Plan
	lastRejectedPlan *Plan  // Store the last rejected plan for context
	lastFeedback     string // Store the last user feedback for plan modifications
	approvalHandler  ApprovalHandler
	replanHandler    ReplanHandler
	onStepStart      StepHandler
	onStepComplete   StepHandler
	onProgressUpdate func(progress *ProgressUpdate) // Progress update handler
	undoExtension    *ManagerUndoExtension          // Undo/redo support

	// Plan persistence
	planStore *PlanStore
	workDir   string // Current working directory for plan context

	// Context-clear signaling for plan execution
	contextClearRequested bool
	approvedPlanSnapshot  *Plan

	// Execution mode tracking - distinguishes between plan creation and plan execution phases
	executionMode   bool // true = executing approved plan, false = creating/designing plan
	currentStepID   int  // ID of the step currently being executed (-1 if none)

	mu sync.RWMutex
}

// NewManager creates a new plan manager.
func NewManager(enabled, requireApproval bool) *Manager {
	return &Manager{
		enabled:         enabled,
		requireApproval: requireApproval,
		currentStepID:   -1,
	}
}

// SetApprovalHandler sets the handler for plan approval.
func (m *Manager) SetApprovalHandler(handler ApprovalHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvalHandler = handler
}

// SetReplanHandler sets the handler for adaptive replanning on fatal errors.
func (m *Manager) SetReplanHandler(handler ReplanHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replanHandler = handler
}

// HasReplanHandler returns true if a replan handler is configured.
func (m *Manager) HasReplanHandler() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.replanHandler != nil
}

// RequestReplan invokes the replan handler to replace remaining pending steps
// after a fatal step failure. Increments plan.Version on success.
func (m *Manager) RequestReplan(ctx context.Context, failedStep *Step) error {
	m.mu.RLock()
	handler := m.replanHandler
	plan := m.currentPlan
	m.mu.RUnlock()

	if handler == nil {
		return fmt.Errorf("no replan handler configured")
	}
	if plan == nil {
		return fmt.Errorf("no active plan")
	}
	if failedStep == nil {
		return fmt.Errorf("failed step is nil")
	}

	newSteps, err := handler(ctx, plan, failedStep)
	if err != nil {
		return fmt.Errorf("replan failed: %w", err)
	}

	// Replace remaining pending steps with new steps
	plan.mu.Lock()
	defer plan.mu.Unlock()

	// Keep completed/failed/skipped steps, replace pending ones
	var kept []*Step
	for _, s := range plan.Steps {
		if s.Status != StatusPending {
			kept = append(kept, s)
		}
	}

	// Assign new IDs starting after the last kept step
	nextID := len(kept) + 1
	for _, s := range newSteps {
		s.ID = nextID
		s.Status = StatusPending
		nextID++
	}

	// Validate DependsOn references in new steps
	validIDs := make(map[int]bool)
	for _, s := range kept {
		validIDs[s.ID] = true
	}
	for _, s := range newSteps {
		validIDs[s.ID] = true
	}
	for _, s := range newSteps {
		var validDeps []int
		for _, dep := range s.DependsOn {
			if validIDs[dep] {
				validDeps = append(validDeps, dep)
			}
		}
		s.DependsOn = validDeps
	}

	plan.Steps = append(kept, newSteps...)
	plan.Version++
	plan.Status = StatusInProgress
	plan.UpdatedAt = time.Now()

	logging.Info("plan replanned",
		"plan_id", plan.ID,
		"version", plan.Version,
		"new_steps", len(newSteps),
		"total_steps", len(plan.Steps))

	return nil
}

// SetStepHandlers sets the step lifecycle handlers.
func (m *Manager) SetStepHandlers(onStart, onComplete StepHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onStepStart = onStart
	m.onStepComplete = onComplete
}

// IsEnabled returns whether plan mode is enabled.
func (m *Manager) IsEnabled() bool {
	return m.enabled
}

// SetEnabled enables or disables plan mode.
func (m *Manager) SetEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
}

// IsActive returns true if there's an active plan.
func (m *Manager) IsActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentPlan != nil && !m.currentPlan.IsComplete()
}

// GetCurrentPlan returns the current plan.
func (m *Manager) GetCurrentPlan() *Plan {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentPlan
}

// CreatePlan creates a new plan and sets it as current.
func (m *Manager) CreatePlan(title, description, request string) *Plan {
	m.mu.Lock()
	defer m.mu.Unlock()

	plan := NewPlan(title, description)
	plan.Request = request
	plan.WorkDir = m.workDir
	m.currentPlan = plan
	return plan
}

// SetPlan sets the current plan.
func (m *Manager) SetPlan(plan *Plan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentPlan = plan
}

// ClearPlan clears the current plan and returns it if it existed.
func (m *Manager) ClearPlan() *Plan {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan := m.currentPlan
	m.currentPlan = nil
	return plan
}

// GetLastRejectedPlan returns the last rejected plan (if saved).
func (m *Manager) GetLastRejectedPlan() *Plan {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastRejectedPlan
}

// SaveRejectedPlan saves a plan as rejected for later reference.
func (m *Manager) SaveRejectedPlan(plan *Plan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastRejectedPlan = plan
}

// SetFeedback stores user feedback for plan modifications.
func (m *Manager) SetFeedback(feedback string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastFeedback = feedback
}

// GetFeedback returns the last user feedback and clears it.
func (m *Manager) GetFeedback() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	feedback := m.lastFeedback
	m.lastFeedback = ""
	return feedback
}

// HasFeedback returns true if there's pending user feedback.
func (m *Manager) HasFeedback() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastFeedback != ""
}

// RequestApproval requests user approval for the current plan.
func (m *Manager) RequestApproval(ctx context.Context) (ApprovalDecision, error) {
	m.mu.RLock()
	plan := m.currentPlan
	handler := m.approvalHandler
	m.mu.RUnlock()

	if plan == nil {
		return ApprovalRejected, nil
	}

	if !m.requireApproval {
		return ApprovalApproved, nil
	}

	if handler == nil {
		// No handler, auto-approve
		return ApprovalApproved, nil
	}

	return handler(ctx, plan)
}

// StartStep marks a step as started.
func (m *Manager) StartStep(stepID int) {
	m.mu.Lock()
	plan := m.currentPlan
	store := m.planStore
	onStart := m.onStepStart
	onProgress := m.onProgressUpdate
	m.mu.Unlock()

	if plan == nil {
		return
	}

	plan.StartStep(stepID)

	// Save progress (for crash recovery - we know which step was running)
	if store != nil {
		if err := store.Save(plan); err != nil {
			logging.Warn("failed to save plan progress", "error", err)
		}
	}

	// Send progress update
	if onProgress != nil {
		step := plan.GetStep(stepID)
		if step != nil {
			// Use thread-safe methods for accessing plan data
			onProgress(&ProgressUpdate{
				PlanID:        plan.ID,
				CurrentStepID: stepID,
				CurrentTitle:  step.Title,
				TotalSteps:    plan.StepCount(),
				Completed:     plan.CompletedCount(),
				Progress:      plan.Progress(),
				Status:        "in_progress",
			})
		}
	}

	if onStart != nil {
		step := plan.GetStep(stepID)
		if step != nil {
			onStart(step)
		}
	}
}

// CompleteStep marks a step as completed.
func (m *Manager) CompleteStep(stepID int, output string) {
	m.mu.Lock()
	plan := m.currentPlan
	store := m.planStore
	onComplete := m.onStepComplete
	onProgress := m.onProgressUpdate
	m.mu.Unlock()

	if plan == nil {
		return
	}

	plan.CompleteStep(stepID, output)

	// Save progress (for crash recovery)
	if store != nil {
		if err := store.Save(plan); err != nil {
			logging.Warn("failed to save plan progress", "error", err)
		}
	}

	// Send progress update
	if onProgress != nil {
		step := plan.GetStep(stepID)
		if step != nil {
			status := "in_progress"
			if plan.Progress() >= 1.0 {
				status = "completed"
			}
			// Use thread-safe methods for accessing plan data
			onProgress(&ProgressUpdate{
				PlanID:        plan.ID,
				CurrentStepID: stepID,
				CurrentTitle:  step.Title,
				TotalSteps:    plan.StepCount(),
				Completed:     plan.CompletedCount(),
				Progress:      plan.Progress(),
				Status:        status,
			})
		}
	}

	if onComplete != nil {
		step := plan.GetStep(stepID)
		if step != nil {
			onComplete(step)
		}
	}
}

// FailStep marks a step as failed.
func (m *Manager) FailStep(stepID int, errMsg string) {
	m.mu.Lock()
	plan := m.currentPlan
	store := m.planStore
	onProgress := m.onProgressUpdate
	m.mu.Unlock()

	if plan == nil {
		return
	}

	plan.FailStep(stepID, errMsg)

	// Auto-save failed plan for potential retry
	if store != nil {
		if err := store.Save(plan); err != nil {
			logging.Warn("failed to save plan progress", "error", err)
		}
	}

	// Send progress update
	if onProgress != nil {
		step := plan.GetStep(stepID)
		if step != nil {
			// Use thread-safe methods for accessing plan data
			onProgress(&ProgressUpdate{
				PlanID:        plan.ID,
				CurrentStepID: stepID,
				CurrentTitle:  step.Title,
				TotalSteps:    plan.StepCount(),
				Completed:     plan.CompletedCount(),
				Progress:      plan.Progress(),
				Status:        "failed",
			})
		}
	}
}

// SkipStep marks a step as skipped.
func (m *Manager) SkipStep(stepID int) {
	m.mu.Lock()
	plan := m.currentPlan
	onProgress := m.onProgressUpdate
	m.mu.Unlock()

	if plan == nil {
		return
	}

	plan.SkipStep(stepID)

	// Send progress update
	if onProgress != nil {
		step := plan.GetStep(stepID)
		if step != nil {
			// Use thread-safe methods for accessing plan data
			onProgress(&ProgressUpdate{
				PlanID:        plan.ID,
				CurrentStepID: stepID,
				CurrentTitle:  step.Title,
				TotalSteps:    plan.StepCount(),
				Completed:     plan.CompletedCount(),
				Progress:      plan.Progress(),
				Status:        "skipped",
			})
		}
	}
}

// GetProgress returns the current plan's progress.
func (m *Manager) GetProgress() (current, total int, percent float64) {
	m.mu.RLock()
	plan := m.currentPlan
	m.mu.RUnlock()

	if plan == nil {
		return 0, 0, 0
	}

	total = plan.StepCount()
	current = plan.CompletedCount()
	percent = plan.Progress()
	return
}

// AddStep adds a step to the current plan.
func (m *Manager) AddStep(title, description string) *Step {
	m.mu.RLock()
	plan := m.currentPlan
	m.mu.RUnlock()

	if plan == nil {
		return nil
	}

	return plan.AddStep(title, description)
}

// NextStep returns the next pending step.
func (m *Manager) NextStep() *Step {
	m.mu.RLock()
	plan := m.currentPlan
	m.mu.RUnlock()

	if plan == nil {
		return nil
	}

	return plan.NextStep()
}

// GetPreviousStepsSummary returns a compact summary of completed steps for context injection.
// Each completed step's output is truncated to maxLen characters.
func (m *Manager) GetPreviousStepsSummary(currentStepID int, maxLen int) string {
	m.mu.RLock()
	plan := m.currentPlan
	m.mu.RUnlock()

	if plan == nil {
		return ""
	}

	var sb strings.Builder
	for _, step := range plan.Steps {
		if step.ID >= currentStepID {
			break
		}
		if step.Status == StatusCompleted {
			sb.WriteString(fmt.Sprintf("Step %d (%s): ", step.ID, step.Title))
			output := step.Output
			if len(output) > maxLen {
				output = output[:maxLen] + "..."
			}
			if output == "" {
				output = "completed"
			}
			sb.WriteString(output)
			sb.WriteString("\n")
		} else if step.Status == StatusFailed {
			errDetail := step.Error
			if len(errDetail) > 200 {
				errDetail = errDetail[:200] + "..."
			}
			if errDetail != "" {
				sb.WriteString(fmt.Sprintf("Step %d (%s): FAILED - %s\n", step.ID, step.Title, errDetail))
			} else {
				sb.WriteString(fmt.Sprintf("Step %d (%s): FAILED\n", step.ID, step.Title))
			}
		} else if step.Status == StatusSkipped {
			sb.WriteString(fmt.Sprintf("Step %d (%s): SKIPPED\n", step.ID, step.Title))
		}
	}
	return sb.String()
}

// SetProgressUpdateHandler sets the progress update handler.
func (m *Manager) SetProgressUpdateHandler(handler func(progress *ProgressUpdate)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onProgressUpdate = handler
}

// EnableUndo enables undo/redo support for plan execution.
func (m *Manager) EnableUndo(maxHistory int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.undoExtension = NewManagerUndoExtension(m, maxHistory)
}

// DisableUndo disables undo/redo support.
func (m *Manager) DisableUndo() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.undoExtension = nil
}

// SavePlanCheckpoint saves a checkpoint before plan execution.
func (m *Manager) SavePlanCheckpoint() error {
	m.mu.RLock()
	undoExt := m.undoExtension
	m.mu.RUnlock()

	if undoExt == nil {
		return nil // Undo not enabled, ignore
	}

	return undoExt.SaveCheckpoint()
}

// GetUndoExtension returns the undo extension (if enabled).
func (m *Manager) GetUndoExtension() *ManagerUndoExtension {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.undoExtension
}

// RequestContextClear sets the context-clear flag and snapshots the approved plan.
// Called from tool execution when a plan is approved with context clearing enabled.
func (m *Manager) RequestContextClear(plan *Plan) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.contextClearRequested = true
	m.approvedPlanSnapshot = plan
}

// IsContextClearRequested returns whether a context clear has been requested.
func (m *Manager) IsContextClearRequested() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.contextClearRequested
}

// ConsumeContextClearRequest reads the context-clear flag and clears it (consume-once).
// Returns the approved plan snapshot, or nil if no request was pending.
func (m *Manager) ConsumeContextClearRequest() *Plan {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.contextClearRequested {
		return nil
	}
	m.contextClearRequested = false
	plan := m.approvedPlanSnapshot
	m.approvedPlanSnapshot = nil
	return plan
}

// SetExecutionMode sets whether the manager is in execution mode.
// In execution mode, new plans cannot be created (nested plans are blocked).
func (m *Manager) SetExecutionMode(executing bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executionMode = executing
	if !executing {
		m.currentStepID = -1
	}
}

// IsExecuting returns true if currently executing an approved plan.
// This is different from IsActive() - IsActive checks if a plan exists,
// IsExecuting checks if we're in the execution phase (after approval).
func (m *Manager) IsExecuting() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executionMode
}

// SetCurrentStepID sets the ID of the step currently being executed.
func (m *Manager) SetCurrentStepID(stepID int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.currentStepID = stepID
}

// GetCurrentStepID returns the ID of the step currently being executed (-1 if none).
func (m *Manager) GetCurrentStepID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentStepID
}

// SetPlanStore sets the plan store for persistence.
func (m *Manager) SetPlanStore(store *PlanStore) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.planStore = store
}

// GetPlanStore returns the plan store.
func (m *Manager) GetPlanStore() *PlanStore {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.planStore
}

// SetWorkDir sets the working directory for plan context.
func (m *Manager) SetWorkDir(workDir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workDir = workDir
}

// SaveCurrentPlan saves the current plan to persistent storage.
func (m *Manager) SaveCurrentPlan() error {
	m.mu.RLock()
	plan := m.currentPlan
	store := m.planStore
	m.mu.RUnlock()

	if store == nil {
		return nil // Persistence not enabled
	}

	if plan == nil {
		return nil // No plan to save
	}

	return store.Save(plan)
}

// LoadPausedPlan loads the most recent paused plan from storage.
func (m *Manager) LoadPausedPlan() (*Plan, error) {
	m.mu.RLock()
	store := m.planStore
	workDir := m.workDir
	m.mu.RUnlock()

	if store == nil {
		return nil, fmt.Errorf("plan store not configured")
	}

	plan, err := store.LoadLast(workDir)
	if err != nil {
		return nil, err
	}

	// Set as current plan
	m.mu.Lock()
	m.currentPlan = plan
	m.mu.Unlock()

	return plan, nil
}

// LoadPlanByID loads a specific plan by ID from storage.
func (m *Manager) LoadPlanByID(planID string) (*Plan, error) {
	m.mu.RLock()
	store := m.planStore
	m.mu.RUnlock()

	if store == nil {
		return nil, fmt.Errorf("plan store not configured")
	}

	plan, err := store.Load(planID)
	if err != nil {
		return nil, err
	}

	// Set as current plan
	m.mu.Lock()
	m.currentPlan = plan
	m.mu.Unlock()

	return plan, nil
}

// ListSavedPlans returns info about all saved plans.
func (m *Manager) ListSavedPlans() ([]PlanInfo, error) {
	m.mu.RLock()
	store := m.planStore
	m.mu.RUnlock()

	if store == nil {
		return nil, fmt.Errorf("plan store not configured")
	}

	return store.List()
}

// ListResumablePlans returns info about resumable plans.
func (m *Manager) ListResumablePlans() ([]PlanInfo, error) {
	m.mu.RLock()
	store := m.planStore
	workDir := m.workDir
	m.mu.RUnlock()

	if store == nil {
		return nil, fmt.Errorf("plan store not configured")
	}

	return store.ListResumable(workDir)
}

// DeleteSavedPlan deletes a plan from storage.
func (m *Manager) DeleteSavedPlan(planID string) error {
	m.mu.RLock()
	store := m.planStore
	m.mu.RUnlock()

	if store == nil {
		return fmt.Errorf("plan store not configured")
	}

	return store.Delete(planID)
}

// HasPausedPlan checks if there's a paused plan (in memory or storage).
func (m *Manager) HasPausedPlan() bool {
	m.mu.RLock()
	plan := m.currentPlan
	store := m.planStore
	workDir := m.workDir
	m.mu.RUnlock()

	// Check current plan in memory
	if plan != nil && plan.Status == StatusPaused {
		return true
	}

	// Check storage
	if store != nil {
		plans, err := store.ListResumable(workDir)
		if err == nil && len(plans) > 0 {
			return true
		}
	}

	return false
}

// PauseStep marks a step as paused with a reason.
func (m *Manager) PauseStep(stepID int, reason string) {
	m.mu.Lock()
	plan := m.currentPlan
	store := m.planStore
	onProgress := m.onProgressUpdate
	m.mu.Unlock()

	if plan == nil {
		return
	}

	plan.PauseStep(stepID, reason)

	// Auto-save paused plan for later resume
	if store != nil {
		if err := store.Save(plan); err != nil {
			logging.Warn("failed to save plan progress", "error", err)
		}
	}

	// Send progress update
	if onProgress != nil {
		step := plan.GetStep(stepID)
		if step != nil {
			onProgress(&ProgressUpdate{
				PlanID:        plan.ID,
				CurrentStepID: stepID,
				CurrentTitle:  step.Title,
				TotalSteps:    plan.StepCount(),
				Completed:     plan.CompletedCount(),
				Progress:      plan.Progress(),
				Status:        "paused",
			})
		}
	}
}

// ResumePlan resumes a paused plan by resetting paused/failed steps to pending.
// Returns the plan if it can be resumed, or an error if no plan is available.
func (m *Manager) ResumePlan() (*Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.currentPlan == nil {
		return nil, fmt.Errorf("no active plan to resume")
	}

	plan := m.currentPlan

	// Check if there are any resumable steps
	hasPending := false
	for _, step := range plan.Steps {
		if step.Status == StatusPending || step.Status == StatusPaused || step.Status == StatusFailed {
			hasPending = true
			break
		}
	}

	if !hasPending {
		return nil, fmt.Errorf("plan already completed, no steps to resume")
	}

	// Reset paused and failed steps to pending for retry
	for _, step := range plan.Steps {
		if step.Status == StatusPaused || step.Status == StatusFailed {
			step.Status = StatusPending
			step.Error = ""
		}
	}

	// Reset plan status to in_progress
	plan.Status = StatusInProgress

	return plan, nil
}

// IsPlanPaused returns true if the current plan is paused.
func (m *Manager) IsPlanPaused() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.currentPlan == nil {
		return false
	}
	return m.currentPlan.Status == StatusPaused
}
