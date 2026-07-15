package plan

import (
	"context"
	"strings"
	"testing"
	"time"
)

func approveForContextClearTest(t *testing.T, m *Manager, p *Plan) {
	t.Helper()
	m.SetPlan(p)
	if err := p.TransitionLifecycle(LifecycleAwaitingApproval); err != nil {
		t.Fatal(err)
	}
	if err := m.TransitionPlanLifecycleIfCurrent(p, LifecycleApproved); err != nil {
		t.Fatal(err)
	}
}

func TestSetPlanInvalidatesOlderContextClearHandoff(t *testing.T) {
	m := NewManager(true, true)
	first := NewPlan("first", "desc")
	approveForContextClearTest(t, m, first)
	if !m.RequestContextClear(first) {
		t.Fatal("approved current plan did not acquire context-clear handoff")
	}

	replacement := NewPlan("replacement", "desc")
	m.SetPlan(replacement)
	if m.IsContextClearRequested() {
		t.Fatal("replacement retained the older plan's execution handoff")
	}
	if got := m.ConsumeContextClearRequest(); got != nil {
		t.Fatalf("consumed stale approved plan after replacement: %+v", got)
	}
	if m.RequestContextClear(first) {
		t.Fatal("stale plan reacquired execution handoff")
	}
}

func TestRequestApprovalDiscardsStaleModificationBeforeGlobalSideEffects(t *testing.T) {
	m := NewManager(true, true)
	first := NewPlan("first", "desc")
	replacement := NewPlan("replacement", "desc")
	m.SetPlan(first)
	m.SetApprovalHandler(func(_ context.Context, shown *Plan) (ApprovalDecision, error) {
		if !m.StageApprovalFeedback(shown, "change the storage API") {
			t.Fatal("failed to stage feedback for the shown plan")
		}
		m.SetPlan(replacement)
		return ApprovalModified, nil
	})

	decision, err := m.RequestApproval(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stale decision") {
		t.Fatalf("stale modification = decision %v, err %v", decision, err)
	}
	if got := m.GetCurrentPlan(); got != replacement {
		t.Fatalf("stale response replaced current plan: got %p want %p", got, replacement)
	}
	if got := m.GetLastRejectedPlan(); got != nil {
		t.Fatalf("stale response saved rejected plan globally: %+v", got)
	}
	if m.HasFeedback() {
		t.Fatal("stale response poisoned global modification feedback")
	}
	if got := m.ConsumeApprovalFeedback(first); got != "" {
		t.Fatalf("stale exact-plan feedback survived: %q", got)
	}
}

func TestRequestApprovalCommitsFeedbackForExactPlan(t *testing.T) {
	m := NewManager(true, true)
	p := NewPlan("current", "desc")
	m.SetPlan(p)
	m.SetApprovalHandler(func(_ context.Context, shown *Plan) (ApprovalDecision, error) {
		m.StageApprovalFeedback(shown, "keep the public API compatible")
		return ApprovalModified, nil
	})

	decision, err := m.RequestApproval(context.Background())
	if err != nil || decision != ApprovalModified {
		t.Fatalf("valid modification = decision %v, err %v", decision, err)
	}
	if got := m.GetLastRejectedPlan(); got != p {
		t.Fatalf("last rejected plan = %p, want %p", got, p)
	}
	if got := m.ConsumeApprovalFeedback(p); got != "keep the public API compatible" {
		t.Fatalf("exact feedback = %q", got)
	}
}

func TestApprovalCommitSerializesActivePlanReplacement(t *testing.T) {
	m := NewManager(true, true)
	first := NewPlan("first", "desc")
	replacement := NewPlan("replacement", "desc")
	m.SetPlan(first)
	if err := first.TransitionLifecycle(LifecycleAwaitingApproval); err != nil {
		t.Fatal(err)
	}

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	callbackDone := make(chan struct{})
	callbackPlan := make(chan *Plan, 1)
	m.SetApprovalCommitHandler(func() {
		// Re-entering ordinary Manager state must remain safe while the
		// replacement gate protects the exact plan/callback handoff.
		m.SetEnabled(false)
		close(callbackEntered)
		<-releaseCallback
		callbackPlan <- m.GetCurrentPlan()
		close(callbackDone)
	})

	approvalDone := make(chan error, 1)
	go func() {
		approvalDone <- m.ApprovePlanAndRequestContextClearIfCurrent(first)
	}()
	<-callbackEntered
	if m.planSlotMu.TryLock() {
		m.planSlotMu.Unlock()
		t.Fatal("approval callback did not retain the active-plan replacement gate")
	}

	replacementStarted := make(chan struct{})
	replacementDone := make(chan struct{})
	go func() {
		close(replacementStarted)
		m.SetPlan(replacement)
		close(replacementDone)
	}()
	<-replacementStarted

	// SetPlan must not complete while first's commit callback is in flight.
	// Without the plan-slot gate it deterministically replaces first here and
	// the stale callback observes (and globally acts on) replacement.
	select {
	case <-replacementDone:
		t.Fatal("replacement crossed an in-flight approval commit callback")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseCallback)
	select {
	case <-callbackDone:
	case <-time.After(time.Second):
		t.Fatal("approval callback deadlocked while re-entering Manager state")
	}
	if got := <-callbackPlan; got != first {
		t.Fatalf("approval callback observed plan %p, want original %p", got, first)
	}
	if err := <-approvalDone; err != nil {
		t.Fatalf("approval commit failed: %v", err)
	}
	select {
	case <-replacementDone:
	case <-time.After(time.Second):
		t.Fatal("replacement did not resume after approval callback")
	}
	if got := m.GetCurrentPlan(); got != replacement {
		t.Fatalf("current plan = %p, want replacement %p", got, replacement)
	}
	if m.IsContextClearRequested() {
		t.Fatal("replacement retained first plan's context-clear handoff")
	}
}

func TestRequestApprovalWithoutPromptRejectsStaleOwner(t *testing.T) {
	m := NewManager(true, false)
	first := NewPlan("first", "desc")
	replacement := NewPlan("replacement", "desc")
	m.SetPlan(first)
	m.SetLintHandler(func(_ context.Context, shown *Plan) error {
		if shown != first {
			t.Fatalf("lint received plan %p, want %p", shown, first)
		}
		m.SetPlan(replacement)
		return nil
	})

	decision, err := m.RequestApproval(context.Background())
	if err == nil || !strings.Contains(err.Error(), "active plan changed") {
		t.Fatalf("stale no-prompt approval = decision %v, err %v", decision, err)
	}
	if decision != ApprovalRejected {
		t.Fatalf("decision = %v, want rejected", decision)
	}
	if got := m.GetCurrentPlan(); got != replacement {
		t.Fatalf("stale no-prompt approval mutated replacement: got %p want %p", got, replacement)
	}
}

func TestRequestApprovalDiscardsStaleRejection(t *testing.T) {
	m := NewManager(true, true)
	first := NewPlan("first", "desc")
	replacement := NewPlan("replacement", "desc")
	m.SetPlan(first)
	m.SetApprovalHandler(func(_ context.Context, shown *Plan) (ApprovalDecision, error) {
		if shown != first {
			t.Fatalf("approval received plan %p, want %p", shown, first)
		}
		m.SetPlan(replacement)
		return ApprovalRejected, nil
	})

	decision, err := m.RequestApproval(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stale decision") {
		t.Fatalf("stale rejection = decision %v, err %v", decision, err)
	}
	if got := m.GetCurrentPlan(); got != replacement {
		t.Fatalf("stale rejection mutated replacement: got %p want %p", got, replacement)
	}
	if got := m.GetLastRejectedPlan(); got != nil {
		t.Fatalf("stale rejection was committed globally: %+v", got)
	}
}
