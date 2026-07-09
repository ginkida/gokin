package plan

import (
	"strings"
	"sync"
	"testing"
)

// TestResumePlan_ResumesFromExecutingLifecycle pins the round-11 fix: a plan that
// died mid-step (Esc / timeout / crash / done-gate block) is persisted with
// Lifecycle "executing". Before the fix, ResumePlan called
// TransitionLifecycle(LifecycleApproved) with no Executing->Approved edge, so
// /resume-plan permanently failed with "invalid plan lifecycle transition".
func TestResumePlan_ResumesFromExecutingLifecycle(t *testing.T) {
	m := NewManager(true, false)
	p := m.CreatePlan("t", "desc", "req")
	s := p.AddStep("s1", "do a thing")
	p.Lifecycle = LifecycleExecuting // simulate death mid-step
	s.Status = StatusPaused

	resumed, err := m.ResumePlan()
	if err != nil {
		t.Fatalf("ResumePlan must succeed from executing lifecycle, got: %v", err)
	}
	if resumed.LifecycleState() != LifecycleApproved {
		t.Errorf("lifecycle = %s, want approved", resumed.LifecycleState())
	}
	if s.Status != StatusPending {
		t.Errorf("step status = %s, want pending", s.Status)
	}
	if resumed.GetStatus() != StatusInProgress {
		t.Errorf("plan status = %s, want in_progress", resumed.GetStatus())
	}
}

// TestResumePlan_EmptyLifecycleInProgress covers the legacy-plan variant: an
// empty Lifecycle inferred from a persisted StatusInProgress is Executing, which
// also hit the missing Executing->Approved edge.
func TestResumePlan_EmptyLifecycleInProgress(t *testing.T) {
	m := NewManager(true, false)
	p := m.CreatePlan("t", "desc", "req")
	s := p.AddStep("s1", "do")
	p.Lifecycle = "" // legacy: no lifecycle recorded
	p.Status = StatusInProgress
	s.Status = StatusFailed

	if _, err := m.ResumePlan(); err != nil {
		t.Fatalf("ResumePlan must succeed for legacy in-progress plan, got: %v", err)
	}
}

// TestResumePausedSteps_ClearsPartialEffects pins the round-11 fix: auto-resume
// (ResumePausedSteps) must clear RunLedger PartialEffects, otherwise the next
// execution attempt is immediately re-paused by the idempotency guard
// (HasPartialEffects) and auto-resume is structurally dead.
func TestResumePausedSteps_ClearsPartialEffects(t *testing.T) {
	p := NewPlan("t", "desc")
	s := p.AddStep("s1", "do")
	s.Status = StatusPaused
	p.RunLedger[s.ID] = &RunLedgerEntry{StepID: s.ID, PartialEffects: true}

	if !p.HasPartialEffects(s.ID) {
		t.Fatal("precondition: step must start with partial effects")
	}

	count := p.ResumePausedSteps()
	if count != 1 {
		t.Fatalf("resumed count = %d, want 1", count)
	}
	if p.HasPartialEffects(s.ID) {
		t.Error("ResumePausedSteps must clear PartialEffects so the step is not re-paused")
	}
}

// TestGetActiveContractContext_NoRaceWithPausePlan exercises the round-11 fix
// that snapshots the plan header under p.mu. Under -race, reading
// currentPlan.Status/Title/etc. directly while PausePlan writes plan.Status
// concurrently is a data race.
func TestGetActiveContractContext_NoRaceWithPausePlan(t *testing.T) {
	m := NewManager(true, false)
	p := m.CreatePlan("Race Plan", "desc", "req")
	s := p.AddStep("s1", "do")
	s.Status = StatusInProgress
	m.SetCurrentStepID(s.ID)
	m.SetExecutionMode(true)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			m.PausePlan()
			p.mu.Lock()
			p.Status = StatusInProgress
			p.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			_ = m.GetActiveContractContext()
		}
	}()
	wg.Wait()

	if !strings.Contains(m.GetActiveContractContext(), "Race Plan") {
		t.Error("contract context should still render the plan title")
	}
}
