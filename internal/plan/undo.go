package plan

import (
	"fmt"
	"sync"
	"time"

	"gokin/internal/undo"
)

// UndoState stores the state before plan execution for undo functionality.
type UndoState struct {
	PlanID      string    `json:"plan_id"`
	PlanTitle   string    `json:"plan_title"`
	Description string    `json:"description"`
	Request     string    `json:"request"`
	Steps       []*Step   `json:"steps"`
	Timestamp   time.Time `json:"timestamp"`
	Executed    []int     `json:"executed"` // IDs of steps that were executed
	ChangeIDs   []string  `json:"change_ids,omitempty"`
	Undone      bool      `json:"undone,omitempty"`
	CaptureOK   bool      `json:"capture_ok"`
	CaptureErr  string    `json:"capture_error,omitempty"`
}

// ManagerUndoExtension extends the plan Manager with undo/redo capabilities.
type ManagerUndoExtension struct {
	manager    *Manager
	mu         sync.Mutex
	history    []*UndoState
	maxHistory int
	active     *undoCapture
}

type undoCapture struct {
	planID    string
	baseline  map[string]struct{}
	records   uint64
	mutations uint64
}

// NewManagerUndoExtension creates a new undo extension for a plan manager.
func NewManagerUndoExtension(manager *Manager, maxHistory int) *ManagerUndoExtension {
	if maxHistory <= 0 {
		maxHistory = 10 // Default to 10 history entries
	}
	return &ManagerUndoExtension{
		manager:    manager,
		history:    make([]*UndoState, 0, maxHistory),
		maxHistory: maxHistory,
	}
}

// SaveCheckpoint saves the current plan state before execution for potential undo.
func (e *ManagerUndoExtension) SaveCheckpoint() error {
	plan := e.manager.GetCurrentPlan()
	if plan == nil {
		return fmt.Errorf("no active plan to checkpoint")
	}

	// Snapshot plan fields under plan's read lock
	plan.mu.RLock()
	state := &UndoState{
		PlanID:      plan.ID,
		PlanTitle:   plan.Title,
		Description: plan.Description,
		Request:     plan.Request,
		Steps:       make([]*Step, len(plan.Steps)),
		Timestamp:   time.Now(),
		Executed:    make([]int, 0),
		CaptureOK:   true,
	}

	// Deep copy steps to isolate checkpoint from future modifications
	for i, step := range plan.Steps {
		if step != nil {
			state.Steps[i] = deepCopyStep(step)
		}
	}
	plan.mu.RUnlock()

	// Add to history under own lock
	e.mu.Lock()
	e.history = append(e.history, state)

	// Trim history if needed
	if len(e.history) > e.maxHistory {
		e.history = e.history[1:]
	}
	e.mu.Unlock()

	return nil
}

// RecordExecutedStep records that a step has been executed.
func (e *ManagerUndoExtension) RecordExecutedStep(stepID int) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.history) == 0 {
		return
	}

	lastState := e.history[len(e.history)-1]
	for _, executedID := range lastState.Executed {
		if executedID == stepID {
			return
		}
	}
	lastState.Executed = append(lastState.Executed, stepID)
}

// GetLastCheckpoint returns the most recent checkpoint.
func (e *ManagerUndoExtension) GetLastCheckpoint() *UndoState {
	e.mu.Lock()
	defer e.mu.Unlock()

	if len(e.history) == 0 {
		return nil
	}
	return cloneUndoState(e.history[len(e.history)-1])
}

// GetHistory returns all saved checkpoints.
func (e *ManagerUndoExtension) GetHistory() []*UndoState {
	e.mu.Lock()
	defer e.mu.Unlock()

	result := make([]*UndoState, len(e.history))
	for i, state := range e.history {
		result[i] = cloneUndoState(state)
	}
	return result
}

// ClearHistory clears all saved checkpoints.
func (e *ManagerUndoExtension) ClearHistory() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = make([]*UndoState, 0, e.maxHistory)
	e.active = nil
}

// CanUndo returns true if there's a checkpoint to restore.
func (e *ManagerUndoExtension) CanUndo() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.history) > 0 &&
		!e.history[len(e.history)-1].Undone &&
		e.history[len(e.history)-1].CaptureOK
}

// CanRedo reports whether the latest plan transaction was undone and still has
// exact file-change identities available for a safe redo.
func (e *ManagerUndoExtension) CanRedo() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.history) > 0 &&
		e.history[len(e.history)-1].Undone &&
		e.history[len(e.history)-1].CaptureOK &&
		len(e.history[len(e.history)-1].ChangeIDs) > 0
}

// BeginCapture checkpoints the active plan and remembers the exact undo history
// boundary for this execution segment. Resuming the same plan appends to its
// existing transaction instead of creating a checkpoint that could only undo
// the final segment.
func (e *ManagerUndoExtension) BeginCapture(snapshot undo.HistorySnapshot) error {
	plan := e.manager.GetCurrentPlan()
	if plan == nil {
		return fmt.Errorf("no active plan to checkpoint")
	}

	e.mu.Lock()
	reuse := len(e.history) > 0 &&
		e.history[len(e.history)-1].PlanID == plan.ID &&
		!e.history[len(e.history)-1].Undone
	e.mu.Unlock()
	if !reuse {
		if err := e.SaveCheckpoint(); err != nil {
			return err
		}
	}

	baseline := make(map[string]struct{}, len(snapshot.ChangeIDs))
	for _, id := range snapshot.ChangeIDs {
		baseline[id] = struct{}{}
	}
	e.mu.Lock()
	e.active = &undoCapture{
		planID:    plan.ID,
		baseline:  baseline,
		records:   snapshot.RecordCount,
		mutations: snapshot.Mutations,
	}
	e.mu.Unlock()
	return nil
}

// FinishCapture records only changes created after BeginCapture. The IDs stay
// stable even if unrelated edits are recorded later, allowing plan undo to
// preserve the rest of the shared history.
func (e *ManagerUndoExtension) FinishCapture(snapshot undo.HistorySnapshot) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.active == nil || len(e.history) == 0 {
		return
	}
	state := e.history[len(e.history)-1]
	if state.PlanID != e.active.planID || state.Undone {
		e.active = nil
		return
	}

	known := make(map[string]struct{}, len(state.ChangeIDs))
	for _, id := range state.ChangeIDs {
		known[id] = struct{}{}
	}
	captured := 0
	for _, id := range snapshot.ChangeIDs {
		if _, existed := e.active.baseline[id]; existed {
			continue
		}
		if _, alreadyCaptured := known[id]; alreadyCaptured {
			continue
		}
		state.ChangeIDs = append(state.ChangeIDs, id)
		known[id] = struct{}{}
		captured++
	}

	if snapshot.RecordCount < e.active.records || snapshot.Mutations < e.active.mutations {
		state.CaptureOK = false
		state.CaptureErr = "undo history revision moved backwards during plan execution"
	} else {
		newRecords := snapshot.RecordCount - e.active.records
		newMutations := snapshot.Mutations - e.active.mutations
		if newMutations != newRecords {
			state.CaptureOK = false
			state.CaptureErr = "undo history changed during plan execution"
		} else if uint64(captured) != newRecords {
			state.CaptureOK = false
			state.CaptureErr = "plan change history exceeded the undo retention limit"
		}
	}
	e.active = nil
}

// MarkUndone transitions the exact latest checkpoint to redoable state.
func (e *ManagerUndoExtension) MarkUndone(planID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.history) == 0 || e.history[len(e.history)-1].PlanID != planID {
		return fmt.Errorf("plan checkpoint %s is no longer current", planID)
	}
	e.history[len(e.history)-1].Undone = true
	return nil
}

// MarkRedone transitions the exact latest checkpoint back to undoable state.
func (e *ManagerUndoExtension) MarkRedone(planID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.history) == 0 || e.history[len(e.history)-1].PlanID != planID {
		return fmt.Errorf("plan checkpoint %s is no longer current", planID)
	}
	e.history[len(e.history)-1].Undone = false
	return nil
}

func cloneUndoState(state *UndoState) *UndoState {
	if state == nil {
		return nil
	}
	cloned := *state
	cloned.Executed = append([]int(nil), state.Executed...)
	cloned.ChangeIDs = append([]string(nil), state.ChangeIDs...)
	cloned.Steps = make([]*Step, len(state.Steps))
	for i, step := range state.Steps {
		if step != nil {
			cloned.Steps[i] = deepCopyStep(step)
		}
	}
	return &cloned
}
