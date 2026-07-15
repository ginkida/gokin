package plan

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// ===========================================================================
// Manager — handlers / enable / enable-undo (0% → full)
// ===========================================================================

func TestManager_SetApprovalHandler(t *testing.T) {
	m := NewManager(true, true)
	called := false
	m.SetApprovalHandler(func(ctx context.Context, p *Plan) (ApprovalDecision, error) {
		called = true
		return ApprovalApproved, nil
	})
	if m.approvalHandler == nil {
		t.Fatal("approval handler not set")
	}
	_ = called
}

func TestManager_SetLintHandler(t *testing.T) {
	m := NewManager(true, true)
	m.SetLintHandler(func(ctx context.Context, p *Plan) error { return nil })
	if m.lintHandler == nil {
		t.Fatal("lint handler not set")
	}
}

func TestManager_SetReplanHandler(t *testing.T) {
	m := NewManager(true, true)
	m.SetReplanHandler(func(ctx context.Context, p *Plan, s *Step) ([]*Step, error) {
		return nil, nil
	})
	if !m.HasReplanHandler() {
		t.Fatal("HasReplanHandler should be true after SetReplanHandler")
	}
}

func TestManager_HasReplanHandler_False(t *testing.T) {
	m := NewManager(true, true)
	if m.HasReplanHandler() {
		t.Error("HasReplanHandler should be false with no handler")
	}
}

func TestManager_SetStepHandlers(t *testing.T) {
	m := NewManager(true, true)
	m.SetStepHandlers(func(s *Step) {}, func(s *Step) {})
	if m.onStepStart == nil || m.onStepComplete == nil {
		t.Error("step handlers not set")
	}
}

func TestManager_SetProgressUpdateHandler(t *testing.T) {
	m := NewManager(true, true)
	m.SetProgressUpdateHandler(func(p *ProgressUpdate) {})
	if m.onProgressUpdate == nil {
		t.Error("progress handler not set")
	}
}

// ===========================================================================
// Manager — IsEnabled / SetEnabled
// ===========================================================================

func TestManager_IsEnabled_SetEnabled(t *testing.T) {
	m := NewManager(true, true)
	if !m.IsEnabled() {
		t.Error("should be enabled")
	}
	m.SetEnabled(false)
	if m.IsEnabled() {
		t.Error("should be disabled after SetEnabled(false)")
	}
	m.SetEnabled(true)
	if !m.IsEnabled() {
		t.Error("should be re-enabled")
	}
}

// ===========================================================================
// Manager — IsActive / GetCurrentPlan / SetPlan / ClearPlan
// ===========================================================================

func TestManager_IsActive_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.IsActive() {
		t.Error("IsActive should be false with no plan")
	}
}

func TestManager_SetPlan_GetCurrentPlan(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	if m.GetCurrentPlan() == nil {
		t.Error("GetCurrentPlan should return the set plan")
	}
	if !m.IsActive() {
		t.Error("IsActive should be true with incomplete plan")
	}
}

func TestManager_ClearPlan(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)

	returned := m.ClearPlan()
	if returned == nil || returned.ID != plan.ID {
		t.Error("ClearPlan should return the cleared plan")
	}
	if m.GetCurrentPlan() != nil {
		t.Error("GetCurrentPlan should be nil after ClearPlan")
	}
}

func TestManager_ClearPlan_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.ClearPlan() != nil {
		t.Error("ClearPlan with no plan should return nil")
	}
}

// ===========================================================================
// Manager — rejected plan / feedback
// ===========================================================================

func TestManager_SaveRejectedPlan_GetLastRejectedPlan(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("rejected", "desc")
	m.SaveRejectedPlan(plan)
	if m.GetLastRejectedPlan() == nil {
		t.Error("GetLastRejectedPlan should return saved plan")
	}
}

func TestManager_SetFeedback_GetFeedback_HasFeedback(t *testing.T) {
	m := NewManager(true, true)
	if m.HasFeedback() {
		t.Error("HasFeedback should be false initially")
	}
	m.SetFeedback("change step 2")
	if !m.HasFeedback() {
		t.Error("HasFeedback should be true after SetFeedback")
	}
	got := m.GetFeedback()
	if got != "change step 2" {
		t.Errorf("GetFeedback = %q, want 'change step 2'", got)
	}
	// GetFeedback consumes — should be empty now.
	if m.HasFeedback() {
		t.Error("HasFeedback should be false after GetFeedback (consume)")
	}
}

// ===========================================================================
// Manager — RequestApproval
// ===========================================================================

func TestManager_RequestApproval_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	decision, err := m.RequestApproval(context.Background())
	// No plan → returns (ApprovalRejected, nil), not an error.
	if err != nil {
		t.Errorf("RequestApproval with no plan: unexpected error %v", err)
	}
	if decision != ApprovalRejected {
		t.Errorf("decision = %v, want ApprovalRejected", decision)
	}
}

func TestManager_RequestApproval_NoHandlerAutoApprove(t *testing.T) {
	m := NewManager(true, false) // requireApproval=false
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	decision, err := m.RequestApproval(context.Background())
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if decision != ApprovalApproved {
		t.Errorf("decision = %v, want ApprovalApproved", decision)
	}
}

func TestManager_RequestApproval_RequiredWithoutHandlerFailsClosed(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	decision, err := m.RequestApproval(context.Background())
	if err == nil {
		t.Fatal("required approval without a handler must return an error")
	}
	if decision != ApprovalRejected {
		t.Errorf("decision = %v, want ApprovalRejected", decision)
	}
}

func TestManager_RequestApproval_LintFails(t *testing.T) {
	m := NewManager(true, false)
	m.SetLintHandler(func(ctx context.Context, p *Plan) error {
		return errFake("lint failure")
	})
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	decision, err := m.RequestApproval(context.Background())
	if err == nil {
		t.Error("RequestApproval should fail when lint fails")
	}
	if decision != ApprovalRejected {
		t.Errorf("decision = %v, want ApprovalRejected", decision)
	}
}

func TestManager_RequestApproval_HandlerApproves(t *testing.T) {
	m := NewManager(true, true)
	m.SetApprovalHandler(func(ctx context.Context, p *Plan) (ApprovalDecision, error) {
		return ApprovalApproved, nil
	})
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	decision, _ := m.RequestApproval(context.Background())
	if decision != ApprovalApproved {
		t.Errorf("decision = %v, want ApprovalApproved", decision)
	}
}

func TestManager_RequestApproval_DiscardsDecisionForReplacedPlan(t *testing.T) {
	m := NewManager(true, true)
	original := NewPlan("original", "desc")
	m.SetPlan(original)
	entered := make(chan struct{})
	release := make(chan struct{})
	m.SetApprovalHandler(func(context.Context, *Plan) (ApprovalDecision, error) {
		close(entered)
		<-release
		return ApprovalApproved, nil
	})

	type outcome struct {
		decision ApprovalDecision
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		decision, err := m.RequestApproval(context.Background())
		done <- outcome{decision: decision, err: err}
	}()
	<-entered
	replacement := NewPlan("replacement", "desc")
	m.SetPlan(replacement)
	close(release)

	got := <-done
	if got.err == nil || got.decision != ApprovalRejected {
		t.Fatalf("stale approval = decision %v, err %v", got.decision, got.err)
	}
	if current := m.GetCurrentPlan(); current != replacement {
		t.Fatalf("stale approval replaced/cleared the new plan: %+v", current)
	}
}

func TestManager_ExactPlanLifecycleAndClearPreserveReplacement(t *testing.T) {
	m := NewManager(true, true)
	original := NewPlan("original", "desc")
	m.SetPlan(original)
	replacement := NewPlan("replacement", "desc")
	m.SetPlan(replacement)

	if err := m.TransitionPlanLifecycleIfCurrent(original, LifecycleApproved); err == nil {
		t.Fatal("stale plan transition should fail")
	}
	if m.ClearPlanIfCurrent(original) {
		t.Fatal("stale plan clear should fail")
	}
	if current := m.GetCurrentPlan(); current != replacement {
		t.Fatalf("newer plan was mutated by stale owner: %+v", current)
	}
}

func TestManager_ApprovalCommitRunsOnlyAfterExactTransition(t *testing.T) {
	m := NewManager(true, true)
	p := NewPlan("current", "desc")
	m.SetPlan(p)
	if err := p.TransitionLifecycle(LifecycleAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	commits := 0
	m.SetApprovalCommitHandler(func() { commits++ })
	if err := m.TransitionPlanLifecycleIfCurrent(p, LifecycleApproved); err != nil {
		t.Fatal(err)
	}
	if commits != 1 {
		t.Fatalf("approval commits = %d, want 1", commits)
	}
}

// errFake is a test-only error type for lint/replan handlers.
type errFake string

func (e errFake) Error() string { return string(e) }

// ===========================================================================
// Manager — step lifecycle (StartStep / CompleteStep / FailStep / SkipStep)
// ===========================================================================

func TestManager_StepLifecycle(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("step1", "do thing 1")
	plan.AddStep("step2", "do thing 2")
	m.SetPlan(plan)

	// Track progress callbacks.
	var progressUpdates []*ProgressUpdate
	m.SetProgressUpdateHandler(func(p *ProgressUpdate) {
		progressUpdates = append(progressUpdates, p)
	})

	// Start + complete step 1.
	m.StartStep(1)
	m.CompleteStep(1, "output1")

	// Fail step 2 then skip nothing.
	m.StartStep(2)
	m.FailStep(2, "boom")

	if len(progressUpdates) < 2 {
		t.Errorf("expected at least 2 progress updates, got %d", len(progressUpdates))
	}

	// Verify step states.
	steps := plan.GetStepsSnapshot()
	if steps[0].Status != StatusCompleted {
		t.Errorf("step1 status = %v, want Completed", steps[0].Status)
	}
	if steps[1].Status != StatusFailed {
		t.Errorf("step2 status = %v, want Failed", steps[1].Status)
	}
}

func TestManager_SkipStep(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("step1", "do thing 1")
	m.SetPlan(plan)

	m.SkipStep(1)
	steps := plan.GetStepsSnapshot()
	if steps[0].Status != StatusSkipped {
		t.Errorf("step1 status = %v, want Skipped", steps[0].Status)
	}
}

func TestManager_StartStep_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	// Should not panic.
	m.StartStep(1)
	m.CompleteStep(1, "out")
	m.FailStep(1, "err")
	m.SkipStep(1)
}

// ===========================================================================
// Manager — RecordStepVerification
// ===========================================================================

func TestManager_RecordStepVerification(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("step1", "do thing 1")
	m.SetPlan(plan)

	ok := m.RecordStepVerification(1, []string{"test passed"}, "verified")
	if !ok {
		t.Error("RecordStepVerification should return true")
	}
}

func TestManager_RecordStepVerification_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.RecordStepVerification(1, []string{"x"}, "y") {
		t.Error("should return false with no plan")
	}
}

// ===========================================================================
// Manager — GetProgress / AddStep / NextStep / GetPreviousStepsSummary
// ===========================================================================

func TestManager_GetProgress(t *testing.T) {
	m := NewManager(true, true)
	// No plan.
	cur, total, pct := m.GetProgress()
	if cur != 0 || total != 0 || pct != 0 {
		t.Errorf("no-plan progress = %d/%d/%f, want 0/0/0", cur, total, pct)
	}

	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	plan.AddStep("s2", "d2")
	m.SetPlan(plan)

	m.StartStep(1)
	m.CompleteStep(1, "done")

	cur, total, pct = m.GetProgress()
	if cur != 1 || total != 2 {
		t.Errorf("progress = %d/%d, want 1/2", cur, total)
	}
	if pct <= 0 {
		t.Errorf("percent = %f, want > 0", pct)
	}
}

func TestManager_AddStep_NextStep(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)

	s := m.AddStep("new step", "description")
	if s == nil {
		t.Fatal("AddStep returned nil")
	}
	next := m.NextStep()
	if next == nil || next.ID != s.ID {
		t.Error("NextStep should return the added step")
	}
}

func TestManager_AddStep_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.AddStep("x", "y") != nil {
		t.Error("AddStep with no plan should return nil")
	}
}

func TestManager_NextStep_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.NextStep() != nil {
		t.Error("NextStep with no plan should return nil")
	}
}

func TestManager_GetPreviousStepsSummary(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	plan.AddStep("s2", "d2")
	m.SetPlan(plan)

	m.StartStep(1)
	m.CompleteStep(1, "first output")

	summary := m.GetPreviousStepsSummary(2, 100)
	if summary == "" {
		t.Error("GetPreviousStepsSummary should not be empty")
	}
}

func TestManager_GetPreviousStepsSummary_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if s := m.GetPreviousStepsSummary(1, 100); s != "" {
		t.Error("GetPreviousStepsSummary with no plan should return empty")
	}
}

// ===========================================================================
// Manager — undo extension
// ===========================================================================

func TestManager_EnableUndo_DisableUndo(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	m.EnableUndo(5)
	ext := m.GetUndoExtension()
	if ext == nil {
		t.Fatal("GetUndoExtension should return non-nil after EnableUndo")
	}

	m.DisableUndo()
	if m.GetUndoExtension() != nil {
		t.Error("GetUndoExtension should return nil after DisableUndo")
	}
}

func TestManager_SavePlanCheckpoint(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)
	m.EnableUndo(5)

	if err := m.SavePlanCheckpoint(); err != nil {
		t.Errorf("SavePlanCheckpoint: %v", err)
	}
}

func TestManager_SavePlanCheckpoint_NoUndo(t *testing.T) {
	m := NewManager(true, true)
	// No undo enabled — should be a no-op (nil error).
	if err := m.SavePlanCheckpoint(); err != nil {
		t.Errorf("SavePlanCheckpoint without undo should be nil, got %v", err)
	}
}

// ===========================================================================
// ManagerUndoExtension — direct tests
// ===========================================================================

func TestManagerUndoExtension_DefaultMaxHistory(t *testing.T) {
	m := NewManager(true, true)
	ext := NewManagerUndoExtension(m, 0) // 0 → default 10
	if ext.maxHistory != 10 {
		t.Errorf("maxHistory = %d, want 10 (default)", ext.maxHistory)
	}
}

func TestManagerUndoExtension_SaveCheckpoint_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	ext := NewManagerUndoExtension(m, 5)
	if err := ext.SaveCheckpoint(); err == nil {
		t.Error("SaveCheckpoint with no plan should error")
	}
}

func TestManagerUndoExtension_RecordExecutedStep_EmptyHistory(t *testing.T) {
	m := NewManager(true, true)
	ext := NewManagerUndoExtension(m, 5)
	// No checkpoints — should be a no-op (not panic).
	ext.RecordExecutedStep(1)
}

func TestManagerUndoExtension_GetLastCheckpoint_Empty(t *testing.T) {
	m := NewManager(true, true)
	ext := NewManagerUndoExtension(m, 5)
	if ext.GetLastCheckpoint() != nil {
		t.Error("GetLastCheckpoint with empty history should return nil")
	}
}

func TestManagerUndoExtension_FullLifecycle(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	ext := NewManagerUndoExtension(m, 5)
	if err := ext.SaveCheckpoint(); err != nil {
		t.Fatalf("SaveCheckpoint: %v", err)
	}
	if !ext.CanUndo() {
		t.Error("CanUndo should be true after checkpoint")
	}
	ext.RecordExecutedStep(1)

	last := ext.GetLastCheckpoint()
	if last == nil {
		t.Fatal("GetLastCheckpoint returned nil")
	}
	if len(last.Executed) != 1 || last.Executed[0] != 1 {
		t.Errorf("Executed = %v, want [1]", last.Executed)
	}

	history := ext.GetHistory()
	if len(history) != 1 {
		t.Errorf("GetHistory len = %d, want 1", len(history))
	}

	ext.ClearHistory()
	if ext.CanUndo() {
		t.Error("CanUndo should be false after ClearHistory")
	}
	if len(ext.GetHistory()) != 0 {
		t.Error("GetHistory should be empty after ClearHistory")
	}
}

func TestManagerUndoExtension_HistoryTrim(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	ext := NewManagerUndoExtension(m, 2) // small max
	ext.SaveCheckpoint()
	ext.SaveCheckpoint()
	ext.SaveCheckpoint() // should trim to 2

	if len(ext.GetHistory()) != 2 {
		t.Errorf("history len = %d, want 2 (trimmed)", len(ext.GetHistory()))
	}
}

// ===========================================================================
// Manager — execution mode / current step ID
// ===========================================================================

func TestManager_SetExecutionMode_IsExecuting(t *testing.T) {
	m := NewManager(true, true)
	if m.IsExecuting() {
		t.Error("should not be executing initially")
	}
	m.SetExecutionMode(true)
	if !m.IsExecuting() {
		t.Error("should be executing after SetExecutionMode(true)")
	}
	m.SetExecutionMode(false)
	if m.IsExecuting() {
		t.Error("should not be executing after SetExecutionMode(false)")
	}
}

func TestManager_SetCurrentStepID_GetCurrentStepID(t *testing.T) {
	m := NewManager(true, true)
	if m.GetCurrentStepID() != -1 {
		t.Errorf("initial currentStepID = %d, want -1", m.GetCurrentStepID())
	}
	m.SetCurrentStepID(5)
	if m.GetCurrentStepID() != 5 {
		t.Errorf("currentStepID = %d, want 5", m.GetCurrentStepID())
	}
	// SetExecutionMode(false) resets it to -1.
	m.SetExecutionMode(false)
	if m.GetCurrentStepID() != -1 {
		t.Errorf("currentStepID after exec mode reset = %d, want -1", m.GetCurrentStepID())
	}
}

// ===========================================================================
// Manager — context clear
// ===========================================================================

func TestManager_ContextClear(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	if err := plan.TransitionLifecycle(LifecycleAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	if err := m.TransitionPlanLifecycleIfCurrent(plan, LifecycleApproved); err != nil {
		t.Fatal(err)
	}

	if m.IsContextClearRequested() {
		t.Error("should not be requested initially")
	}

	if !m.RequestContextClear(plan) {
		t.Fatal("current approved plan should own context-clear request")
	}
	if !m.IsContextClearRequested() {
		t.Error("should be requested after RequestContextClear")
	}

	returned := m.ConsumeContextClearRequest()
	if returned == nil || returned.ID != plan.ID {
		t.Error("ConsumeContextClearRequest should return the plan")
	}
	if m.IsContextClearRequested() {
		t.Error("should not be requested after consume")
	}

	// Second consume returns nil.
	if m.ConsumeContextClearRequest() != nil {
		t.Error("second consume should return nil")
	}
}

// ===========================================================================
// Manager — lifecycle transitions
// ===========================================================================

func TestManager_TransitionCurrentPlanLifecycle(t *testing.T) {
	m := NewManager(true, true)
	// No plan → error.
	if err := m.TransitionCurrentPlanLifecycle(LifecycleApproved); err == nil {
		t.Error("TransitionCurrentPlanLifecycle with no plan should error")
	}

	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	// Plan starts in Draft (or empty lifecycle). Transition to AwaitingApproval.
	if err := m.TransitionCurrentPlanLifecycle(LifecycleAwaitingApproval); err != nil {
		t.Errorf("Transition to AwaitingApproval: %v", err)
	}
	if m.GetCurrentPlanLifecycle() != LifecycleAwaitingApproval {
		t.Errorf("lifecycle = %v, want AwaitingApproval", m.GetCurrentPlanLifecycle())
	}
}

func TestManager_GetCurrentPlanLifecycle_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.GetCurrentPlanLifecycle() != "" {
		t.Errorf("lifecycle with no plan = %q, want empty", m.GetCurrentPlanLifecycle())
	}
}

// ===========================================================================
// Manager — RequestReplan
// ===========================================================================

func TestManager_RequestReplan_NoHandler(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	if err := m.RequestReplan(context.Background(), &Step{ID: 1}); err == nil {
		t.Error("RequestReplan without handler should error")
	}
}

func TestManager_RequestReplan_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	m.SetReplanHandler(func(ctx context.Context, p *Plan, s *Step) ([]*Step, error) {
		return nil, nil
	})
	if err := m.RequestReplan(context.Background(), &Step{ID: 1}); err == nil {
		t.Error("RequestReplan without plan should error")
	}
}

func TestManager_RequestReplan_NilStep(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	m.SetReplanHandler(func(ctx context.Context, p *Plan, s *Step) ([]*Step, error) {
		return nil, nil
	})
	if err := m.RequestReplan(context.Background(), nil); err == nil {
		t.Error("RequestReplan with nil step should error")
	}
}

func TestManager_RequestReplan_Success(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	m.SetReplanHandler(func(ctx context.Context, p *Plan, s *Step) ([]*Step, error) {
		return []*Step{
			{Title: "new step", Description: "replacement"},
		}, nil
	})

	failed := &Step{ID: 1, Status: StatusFailed}
	if err := m.RequestReplan(context.Background(), failed); err != nil {
		t.Fatalf("RequestReplan: %v", err)
	}

	steps := plan.GetStepsSnapshot()
	// s1 was pending → replaced by the new step. Total = 1 (new step only).
	if len(steps) != 1 {
		t.Errorf("steps after replan = %d, want 1 (pending replaced)", len(steps))
	}
	if steps[0].Title != "new step" {
		t.Errorf("step title = %q, want 'new step'", steps[0].Title)
	}
}

// ===========================================================================
// Manager — pause / resume
// ===========================================================================

func TestManager_PauseStep_IsPlanPaused(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	m.PauseStep(1, "waiting for input")
	if m.IsPlanPaused() {
		t.Error("plan-level pause should not be set by PauseStep")
	}
}

func TestManager_PausePlan_IsPlanPaused(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	m.PausePlan()
	if !m.IsPlanPaused() {
		t.Error("IsPlanPaused should be true after PausePlan")
	}
}

func TestManager_IsPlanPaused_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.IsPlanPaused() {
		t.Error("IsPlanPaused with no plan should be false")
	}
}

func TestManager_HasPausedPlan_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if m.HasPausedPlan() {
		t.Error("HasPausedPlan with no plan should be false")
	}
}

func TestManager_HasPausedPlan_PausedInMemory(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)
	m.PausePlan()

	if !m.HasPausedPlan() {
		t.Error("HasPausedPlan should be true when plan is paused in memory")
	}
}

func TestManager_ResumePlan_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	if _, err := m.ResumePlan(); err == nil {
		t.Error("ResumePlan with no plan should error")
	}
}

func TestManager_ResumePlan_AllCompleted(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	m.StartStep(1)
	m.CompleteStep(1, "done")

	if _, err := m.ResumePlan(); err == nil {
		t.Error("ResumePlan with all completed should error")
	}
}

func TestManager_ResumePlan_Success(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	plan.AddStep("s2", "d2")
	m.SetPlan(plan)

	// Transition through the lifecycle so ResumePlan's transition back to
	// Approved is valid (Paused → Approved is allowed).
	m.TransitionCurrentPlanLifecycle(LifecycleAwaitingApproval)
	m.TransitionCurrentPlanLifecycle(LifecycleApproved)
	m.TransitionCurrentPlanLifecycle(LifecycleExecuting)

	// Fail step 1, leave step 2 pending.
	m.StartStep(1)
	m.FailStep(1, "error")
	m.PausePlan()

	resumed, err := m.ResumePlan()
	if err != nil {
		t.Fatalf("ResumePlan: %v", err)
	}
	if resumed == nil {
		t.Fatal("ResumePlan returned nil plan")
	}
}

// ===========================================================================
// Manager — persistence (store)
// ===========================================================================

func TestManager_SetPlanStore_GetPlanStore(t *testing.T) {
	m := NewManager(true, true)
	store, err := NewPlanStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}
	m.SetPlanStore(store)
	if m.GetPlanStore() == nil {
		t.Error("GetPlanStore should return the store")
	}
}

func TestManager_SetWorkDir(t *testing.T) {
	m := NewManager(true, true)
	m.SetWorkDir("/tmp/test")
	// No direct getter — verify via SaveCurrentPlan not erroring with a store.
}

func TestManager_SaveCurrentPlan_NoStore(t *testing.T) {
	m := NewManager(true, true)
	plan := NewPlan("test", "desc")
	m.SetPlan(plan)
	// No store configured — should be a no-op (nil error).
	if err := m.SaveCurrentPlan(); err != nil {
		t.Errorf("SaveCurrentPlan without store should be nil, got %v", err)
	}
}

func TestManager_SaveCurrentPlan_WithStore(t *testing.T) {
	m := NewManager(true, true)
	dir := t.TempDir()
	store, err := NewPlanStore(dir)
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}
	m.SetPlanStore(store)

	plan := NewPlan("test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)

	if err := m.SaveCurrentPlan(); err != nil {
		t.Errorf("SaveCurrentPlan: %v", err)
	}
}

func TestManager_SaveCurrentPlan_NoPlan(t *testing.T) {
	m := NewManager(true, true)
	store, _ := NewPlanStore(t.TempDir())
	m.SetPlanStore(store)
	if err := m.SaveCurrentPlan(); err != nil {
		t.Errorf("SaveCurrentPlan with no plan should be nil, got %v", err)
	}
}

func TestManager_LoadPausedPlan_NoStore(t *testing.T) {
	m := NewManager(true, true)
	if _, err := m.LoadPausedPlan(); err == nil {
		t.Error("LoadPausedPlan without store should error")
	}
}

func TestManager_LoadPlanByID_NoStore(t *testing.T) {
	m := NewManager(true, true)
	if _, err := m.LoadPlanByID("xyz"); err == nil {
		t.Error("LoadPlanByID without store should error")
	}
}

func TestManager_ListSavedPlans_NoStore(t *testing.T) {
	m := NewManager(true, true)
	if _, err := m.ListSavedPlans(); err == nil {
		t.Error("ListSavedPlans without store should error")
	}
}

func TestManager_ListResumablePlans_NoStore(t *testing.T) {
	m := NewManager(true, true)
	if _, err := m.ListResumablePlans(); err == nil {
		t.Error("ListResumablePlans without store should error")
	}
}

func TestManager_DeleteSavedPlan_NoStore(t *testing.T) {
	m := NewManager(true, true)
	if err := m.DeleteSavedPlan("xyz"); err == nil {
		t.Error("DeleteSavedPlan without store should error")
	}
}

func TestManager_CleanupOldPlans_NoStore(t *testing.T) {
	m := NewManager(true, true)
	n, err := m.CleanupOldPlans(time.Hour)
	if err != nil {
		t.Errorf("CleanupOldPlans without store should be nil error, got %v", err)
	}
	if n != 0 {
		t.Errorf("CleanupOldPlans without store = %d, want 0", n)
	}
}

func TestManager_Persistence_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPlanStore(dir)
	if err != nil {
		t.Fatalf("NewPlanStore: %v", err)
	}
	m := NewManager(true, true)
	m.SetPlanStore(store)
	m.SetWorkDir(dir)

	plan := NewPlan("roundtrip", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)
	m.StartStep(1)
	m.CompleteStep(1, "done")

	// Save then load by ID.
	if err := m.SaveCurrentPlan(); err != nil {
		t.Fatalf("SaveCurrentPlan: %v", err)
	}

	m2 := NewManager(true, true)
	store2, _ := NewPlanStore(dir)
	m2.SetPlanStore(store2)
	m2.SetWorkDir(dir)

	loaded, err := m2.LoadPlanByID(plan.ID)
	if err != nil {
		t.Fatalf("LoadPlanByID: %v", err)
	}
	if loaded.ID != plan.ID {
		t.Errorf("loaded ID = %q, want %q", loaded.ID, plan.ID)
	}

	// List should include it.
	plans, err := m2.ListSavedPlans()
	if err != nil {
		t.Fatalf("ListSavedPlans: %v", err)
	}
	found := false
	for _, p := range plans {
		if p.ID == plan.ID {
			found = true
		}
	}
	if !found {
		t.Error("saved plan not found in ListSavedPlans")
	}

	// Exists check.
	if !store2.Exists(plan.ID) {
		t.Error("Exists should return true for saved plan")
	}

	// Delete.
	if err := m2.DeleteSavedPlan(plan.ID); err != nil {
		t.Fatalf("DeleteSavedPlan: %v", err)
	}
	if store2.Exists(plan.ID) {
		t.Error("Exists should return false after delete")
	}
}

func TestManager_LoadPausedPlan_FromStore(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewPlanStore(dir)
	m := NewManager(true, true)
	m.SetPlanStore(store)
	m.SetWorkDir(dir)

	plan := NewPlan("paused-test", "desc")
	plan.AddStep("s1", "d1")
	m.SetPlan(plan)
	m.PausePlan()
	m.SaveCurrentPlan()

	// New manager, load paused.
	m2 := NewManager(true, true)
	store2, _ := NewPlanStore(dir)
	m2.SetPlanStore(store2)
	m2.SetWorkDir(dir)

	loaded, err := m2.LoadPausedPlan()
	if err != nil {
		t.Fatalf("LoadPausedPlan: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadPausedPlan returned nil")
	}
}

// ===========================================================================
// PlanStore — direct tests
// ===========================================================================

func TestPlanStore_Save_NilPlan(t *testing.T) {
	store, _ := NewPlanStore(t.TempDir())
	if err := store.Save(nil); err == nil {
		t.Error("Save(nil) should error")
	}
}

func TestPlanStore_Load_NotFound(t *testing.T) {
	store, _ := NewPlanStore(t.TempDir())
	if _, err := store.Load("nonexistent"); err == nil {
		t.Error("Load of nonexistent should error")
	}
}

func TestPlanStore_Delete_NotExistNoError(t *testing.T) {
	store, _ := NewPlanStore(t.TempDir())
	// Deleting a non-existent plan should be a no-op (nil error).
	if err := store.Delete("nonexistent"); err != nil {
		t.Errorf("Delete nonexistent = %v, want nil", err)
	}
}

func TestPlanStore_Cleanup_EmptyDir(t *testing.T) {
	store, _ := NewPlanStore(t.TempDir())
	n, err := store.Cleanup(time.Hour)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 0 {
		t.Errorf("Cleanup empty dir = %d, want 0", n)
	}
}

func TestPlanStore_Cleanup_OldPlan(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewPlanStore(dir)

	plan := NewPlan("old", "desc")
	plan.AddStep("s1", "d1")
	plan.UpdatedAt = time.Now().Add(-2 * time.Hour) // make it old
	if err := store.Save(plan); err != nil {
		t.Fatalf("Save: %v", err)
	}

	n, err := store.Cleanup(time.Hour)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 1 {
		t.Errorf("Cleanup = %d, want 1", n)
	}
}

func TestPlanStore_Exists(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewPlanStore(dir)

	plan := NewPlan("exists-test", "desc")
	store.Save(plan)

	if !store.Exists(plan.ID) {
		t.Error("Exists should be true")
	}
	if store.Exists("nope") {
		t.Error("Exists should be false for nonexistent")
	}
}

func TestPlanStore_ListResumable(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewPlanStore(dir)

	// Paused plan is resumable.
	p1 := NewPlan("paused", "desc")
	p1.AddStep("s1", "d1")
	p1.WorkDir = dir
	p1.Status = StatusPaused
	store.Save(p1)

	// Completed plan is NOT resumable.
	p2 := NewPlan("done", "desc")
	p2.AddStep("s2", "d2")
	p2.WorkDir = dir
	p2.Status = StatusCompleted
	store.Save(p2)

	resumable, err := store.ListResumable(dir)
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}
	if len(resumable) != 1 {
		t.Fatalf("resumable count = %d, want 1", len(resumable))
	}
	if resumable[0].ID != p1.ID {
		t.Errorf("resumable ID = %q, want %q", resumable[0].ID, p1.ID)
	}
}

// ===========================================================================
// Plan.Format (0% → full)
// ===========================================================================

func TestPlan_Format(t *testing.T) {
	plan := NewPlan("Test Plan", "description")
	plan.AddStep("step1", "do thing 1")
	plan.AddStep("step2", "do thing 2")

	got := plan.Format()
	if got == "" {
		t.Error("Format returned empty string")
	}
}

// ===========================================================================
// truncateString (50% → full)
// ===========================================================================

func TestTruncateString(t *testing.T) {
	cases := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly ten", 11, "exactly ten"},
		{"this is too long", 8, "this ..."},
		{"ab", 2, "ab"},
		{"abc", 2, "ab"}, // maxLen <= 3 → no ellipsis
		{"", 5, ""},
	}
	for _, tc := range cases {
		got := truncateString(tc.input, tc.maxLen)
		if got != tc.want {
			t.Errorf("truncateString(%q, %d) = %q, want %q", tc.input, tc.maxLen, got, tc.want)
		}
	}
}

// ===========================================================================
// PlanStore — LoadLast
// ===========================================================================

func TestPlanStore_LoadLast_EmptyDir(t *testing.T) {
	store, _ := NewPlanStore(t.TempDir())
	if _, err := store.LoadLast(""); err == nil {
		t.Error("LoadLast on empty dir should error (no resumable plan)")
	}
}

func TestPlanStore_LoadLast_FindsLatest(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewPlanStore(dir)

	// Save a paused plan.
	p1 := NewPlan("older", "desc")
	p1.AddStep("s1", "d1")
	p1.Status = StatusPaused
	p1.UpdatedAt = time.Now().Add(-1 * time.Hour)
	store.Save(p1)

	// Save a newer paused plan.
	p2 := NewPlan("newer", "desc")
	p2.AddStep("s2", "d2")
	p2.Status = StatusPaused
	p2.UpdatedAt = time.Now()
	store.Save(p2)

	loaded, err := store.LoadLast("")
	if err != nil {
		t.Fatalf("LoadLast: %v", err)
	}
	// Should return the most recently updated resumable plan.
	if loaded.ID != p2.ID {
		t.Errorf("LoadLast ID = %q, want %q", loaded.ID, p2.ID)
	}
}

// ===========================================================================
// PlanStore — NewPlanStore error path
// ===========================================================================

func TestNewPlanStore_BadDir(t *testing.T) {
	// A path under /dev/null should fail MkdirAll.
	_, err := NewPlanStore(filepath.Join("/dev/null", "subdir"))
	if err == nil {
		t.Error("NewPlanStore under /dev/null should fail")
	}
}
