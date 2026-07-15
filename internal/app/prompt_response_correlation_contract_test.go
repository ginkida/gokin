package app

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"gokin/internal/plan"
	"gokin/internal/ui"
)

const promptCorrelationTestTimeout = time.Second

// These tests are a contract for request-owned question/plan responses. A
// process-wide response channel cannot provide that ownership: after request A
// times out, A's late response can sit in the buffer and be consumed by request
// B. Registration must therefore allocate a unique ID and a dedicated buffered
// channel for every request, just like permission prompts do.

func TestQuestionResponseRoutesOnlyToMatchingRequestID(t *testing.T) {
	a := &App{}

	idA, chA, cleanupA := a.registerQuestionRequest()
	defer cleanupA()
	idB, chB, cleanupB := a.registerQuestionRequest()
	defer cleanupB()

	if idA == "" || idB == "" || idA == idB {
		t.Fatalf("request IDs must be non-empty and unique: A=%q B=%q", idA, idB)
	}

	a.handleQuestionAnswer(idB, "answer-for-B")

	select {
	case got := <-chB:
		if got != "answer-for-B" {
			t.Fatalf("request B received %q, want its own answer", got)
		}
	case <-time.After(promptCorrelationTestTimeout):
		t.Fatal("request B did not receive its matching answer")
	}

	select {
	case got := <-chA:
		t.Fatalf("request A received B's answer %q", got)
	default:
	}
}

func TestQuestionResponseForExpiredOrUnknownIDIsNoOp(t *testing.T) {
	a := &App{}

	expiredID, expiredCh, cleanupExpired := a.registerQuestionRequest()
	cleanupExpired() // models timeout/cancellation before the UI responds

	_, activeCh, cleanupActive := a.registerQuestionRequest()
	defer cleanupActive()

	a.handleQuestionAnswer(expiredID, "late-answer")
	a.handleQuestionAnswer("unknown-question", "unknown-answer")

	select {
	case got := <-expiredCh:
		t.Fatalf("expired request received a late answer %q", got)
	default:
	}
	select {
	case got := <-activeCh:
		t.Fatalf("active request was poisoned by an unrelated late answer %q", got)
	default:
	}
}

func TestQuestionResponseConcurrentRequestsHaveNoCrossTalk(t *testing.T) {
	a := &App{}
	const count = 32

	ids := make([]string, count)
	channels := make([]chan string, count)
	for i := range count {
		id, ch, cleanup := a.registerQuestionRequest()
		ids[i], channels[i] = id, ch
		defer cleanup()
	}

	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a.handleQuestionAnswer(ids[i], fmt.Sprintf("answer-%d", i))
		}(i)
	}

	for i, ch := range channels {
		select {
		case got := <-ch:
			want := fmt.Sprintf("answer-%d", i)
			if got != want {
				t.Errorf("request %d received %q, want %q", i, got, want)
			}
		case <-time.After(promptCorrelationTestTimeout):
			t.Errorf("request %d did not receive a response", i)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(promptCorrelationTestTimeout):
		t.Fatal("question response callbacks blocked instead of routing independently")
	}
}

func TestPlanApprovalResponseRoutesDecisionAndFeedbackByRequestID(t *testing.T) {
	a := &App{}

	_, chA, cleanupA := a.registerPlanApprovalRequest()
	defer cleanupA()
	idB, chB, cleanupB := a.registerPlanApprovalRequest()
	defer cleanupB()

	a.handlePlanApprovalWithFeedback(idB, ui.PlanModifyRequested, "keep compatibility")

	select {
	case got := <-chB:
		if got.Decision != ui.PlanModifyRequested || got.Feedback != "keep compatibility" {
			t.Fatalf("request B response = %+v, want its decision and feedback", got)
		}
	case <-time.After(promptCorrelationTestTimeout):
		t.Fatal("request B did not receive its matching plan response")
	}

	select {
	case got := <-chA:
		t.Fatalf("request A received B's plan response: %+v", got)
	default:
	}
}

func TestPlanApprovalResponseForExpiredIDCannotAffectActiveRequestOrPlanState(t *testing.T) {
	manager := plan.NewManager(true, true)
	activePlan := manager.CreatePlan("Active plan", "must remain current", "request")
	a := &App{
		planManager:         manager,
		planningModeEnabled: true,
	}

	expiredID, expiredCh, cleanupExpired := a.registerPlanApprovalRequest()
	cleanupExpired()
	_, activeCh, cleanupActive := a.registerPlanApprovalRequest()
	defer cleanupActive()

	// None of these callbacks owns a pending request anymore. In particular,
	// approval must not disable plan mode, rejection must not save the current
	// (newer) plan, and modification feedback must not be stored/dispatched.
	a.handlePlanApproval(expiredID, ui.PlanApproved)
	a.handlePlanApproval(expiredID, ui.PlanRejected)
	a.handlePlanApprovalWithFeedback(expiredID, ui.PlanModifyRequested, "stale feedback")
	a.handlePlanApproval("unknown-plan", ui.PlanRejected)

	if !a.IsPlanningModeEnabled() {
		t.Fatal("late approval for an expired request disabled plan mode")
	}
	if got := manager.GetCurrentPlan(); got != activePlan {
		t.Fatalf("late response changed the current plan: got %p want %p", got, activePlan)
	}
	if got := manager.GetLastRejectedPlan(); got != nil {
		t.Fatalf("late rejection saved the newer active plan as rejected: %+v", got)
	}
	if manager.HasFeedback() {
		t.Fatal("late modification stored/dispatched stale feedback")
	}

	select {
	case got := <-expiredCh:
		t.Fatalf("expired plan request received a late response: %+v", got)
	default:
	}
	select {
	case got := <-activeCh:
		t.Fatalf("active plan request was poisoned by a late response: %+v", got)
	default:
	}
}

func TestPlanApprovalConcurrentRequestsHaveNoCrossTalk(t *testing.T) {
	a := &App{}
	const count = 24

	ids := make([]string, count)
	channels := make([]chan planApprovalResponse, count)
	for i := range count {
		id, ch, cleanup := a.registerPlanApprovalRequest()
		ids[i], channels[i] = id, ch
		defer cleanup()
	}

	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a.handlePlanApprovalWithFeedback(ids[i], ui.PlanModifyRequested, fmt.Sprintf("feedback-%d", i))
		}(i)
	}

	for i, ch := range channels {
		select {
		case got := <-ch:
			wantFeedback := fmt.Sprintf("feedback-%d", i)
			if got.Decision != ui.PlanModifyRequested || got.Feedback != wantFeedback {
				t.Errorf("request %d received %+v, want modify/%q", i, got, wantFeedback)
			}
		case <-time.After(promptCorrelationTestTimeout):
			t.Errorf("request %d did not receive a response", i)
		}
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(promptCorrelationTestTimeout):
		t.Fatal("plan response callbacks blocked instead of routing independently")
	}
}
