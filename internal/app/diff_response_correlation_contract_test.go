package app

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"gokin/internal/ui"
)

const diffCorrelationTestTimeout = time.Second

// The UI carries RequestID on diff request/response messages. These tests pin
// the corresponding app-side ownership boundary: draining a process-wide
// channel before opening B is not sufficient because A can respond after that
// drain and while B is waiting.

func TestDiffDecisionRoutesOnlyToMatchingRequestID(t *testing.T) {
	a := &App{}

	idA, chA, cleanupA := a.registerDiffRequest()
	defer cleanupA()
	idB, chB, cleanupB := a.registerDiffRequest()
	defer cleanupB()
	if idA == "" || idB == "" || idA == idB {
		t.Fatalf("diff request IDs must be non-empty and unique: A=%q B=%q", idA, idB)
	}

	a.handleDiffDecision(idB, ui.DiffRejectAll)

	select {
	case got := <-chB:
		if got != ui.DiffRejectAll {
			t.Fatalf("request B received %v, want DiffRejectAll", got)
		}
	case <-time.After(diffCorrelationTestTimeout):
		t.Fatal("request B did not receive its matching diff decision")
	}
	select {
	case got := <-chA:
		t.Fatalf("request A received B's diff decision: %v", got)
	default:
	}
}

func TestDiffDecisionForExpiredOrUnknownIDIsNoOp(t *testing.T) {
	a := &App{diffBatchDecision: ui.DiffPending}

	expiredID, expiredCh, cleanupExpired := a.registerDiffRequest()
	cleanupExpired() // timeout/cancellation completed before the UI response
	_, activeCh, cleanupActive := a.registerDiffRequest()
	defer cleanupActive()

	a.handleDiffDecision(expiredID, ui.DiffApplyAll)
	a.handleDiffDecision("unknown-diff", ui.DiffRejectAll)

	if a.diffBatchDecision != ui.DiffPending {
		t.Fatalf("late response changed batch side effects: got %v want DiffPending", a.diffBatchDecision)
	}
	select {
	case got := <-expiredCh:
		t.Fatalf("expired request received a late decision: %v", got)
	default:
	}
	select {
	case got := <-activeCh:
		t.Fatalf("active request was poisoned by a late decision: %v", got)
	default:
	}
}

func TestDiffDecisionConcurrentRequestsHaveNoCrossTalk(t *testing.T) {
	a := &App{}
	const count = 24

	ids := make([]string, count)
	channels := make([]chan ui.DiffDecision, count)
	wants := make([]ui.DiffDecision, count)
	valid := []ui.DiffDecision{ui.DiffApply, ui.DiffReject, ui.DiffApplyAll, ui.DiffRejectAll}
	for i := range count {
		id, ch, cleanup := a.registerDiffRequest()
		ids[i], channels[i], wants[i] = id, ch, valid[i%len(valid)]
		defer cleanup()
	}

	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a.handleDiffDecision(ids[i], wants[i])
		}(i)
	}

	for i, ch := range channels {
		select {
		case got := <-ch:
			if got != wants[i] {
				t.Errorf("request %d received %v, want %v", i, got, wants[i])
			}
		case <-time.After(diffCorrelationTestTimeout):
			t.Errorf("request %d did not receive a diff decision", i)
		}
	}
	waitForDiffCallbacks(t, &wg)
}

func TestMultiDiffDecisionRoutesOnlyToMatchingRequestID(t *testing.T) {
	a := &App{}

	_, chA, cleanupA := a.registerMultiDiffRequest()
	defer cleanupA()
	idB, chB, cleanupB := a.registerMultiDiffRequest()
	defer cleanupB()
	want := map[string]ui.DiffDecision{"b.go": ui.DiffApply, "b_test.go": ui.DiffReject}

	a.handleMultiDiffDecision(idB, want)

	select {
	case got := <-chB:
		if len(got) != len(want) || got["b.go"] != ui.DiffApply || got["b_test.go"] != ui.DiffReject {
			t.Fatalf("request B received %+v, want %+v", got, want)
		}
	case <-time.After(diffCorrelationTestTimeout):
		t.Fatal("request B did not receive its matching multi-diff decisions")
	}
	select {
	case got := <-chA:
		t.Fatalf("request A received B's multi-diff decisions: %+v", got)
	default:
	}
}

func TestMultiDiffDecisionForExpiredOrUnknownIDCannotPoisonActiveRequest(t *testing.T) {
	a := &App{}

	expiredID, expiredCh, cleanupExpired := a.registerMultiDiffRequest()
	cleanupExpired()
	_, activeCh, cleanupActive := a.registerMultiDiffRequest()
	defer cleanupActive()

	a.handleMultiDiffDecision(expiredID, map[string]ui.DiffDecision{"late.go": ui.DiffApply})
	a.handleMultiDiffDecision("unknown-multi-diff", map[string]ui.DiffDecision{"unknown.go": ui.DiffReject})

	select {
	case got := <-expiredCh:
		t.Fatalf("expired multi-diff request received a late response: %+v", got)
	default:
	}
	select {
	case got := <-activeCh:
		t.Fatalf("active multi-diff request was poisoned by a late response: %+v", got)
	default:
	}
}

func TestMultiDiffDecisionConcurrentRequestsHaveNoCrossTalk(t *testing.T) {
	a := &App{}
	const count = 20

	ids := make([]string, count)
	channels := make([]chan map[string]ui.DiffDecision, count)
	for i := range count {
		id, ch, cleanup := a.registerMultiDiffRequest()
		ids[i], channels[i] = id, ch
		defer cleanup()
	}

	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := fmt.Sprintf("owner-%d.go", i)
			a.handleMultiDiffDecision(ids[i], map[string]ui.DiffDecision{owner: ui.DiffApply})
		}(i)
	}

	for i, ch := range channels {
		select {
		case got := <-ch:
			owner := fmt.Sprintf("owner-%d.go", i)
			if len(got) != 1 || got[owner] != ui.DiffApply {
				t.Errorf("request %d received another request's decisions: %+v", i, got)
			}
		case <-time.After(diffCorrelationTestTimeout):
			t.Errorf("request %d did not receive multi-diff decisions", i)
		}
	}
	waitForDiffCallbacks(t, &wg)
}

func waitForDiffCallbacks(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(diffCorrelationTestTimeout):
		t.Fatal("diff response callbacks blocked instead of routing independently")
	}
}
