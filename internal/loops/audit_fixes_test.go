package loops

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Fix #3: a real persistence failure rolls the in-memory state back, so fireOne
// must NOT fire the success doneHook (phantom "completed" toast / stale markdown)
// nor act on the `done.` self-termination marker. It must surface an honest
// persist-failure warning and leave the loop running.
func TestFireOne_PersistFailureSkipsDoneHookAndStop(t *testing.T) {
	store := newMemStorage()
	mgr := NewManager(store)
	l, _ := mgr.Add("fix bugs", ModeInterval, 3600) // Save succeeds here
	store.failSave = errors.New("disk full")        // now every Save fails

	var doneCalled, persistFailed bool
	// Output ends with "done." — without the fix this would also Stop the loop.
	spawner := &fakeSpawner{output: "all fixed\n\ndone.", ok: true}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)
	r.SetIterationDoneHook(func(string, Iteration) { doneCalled = true })
	r.SetIterationPersistFailedHook(func(string, int, error) { persistFailed = true })

	r.fireOne(context.Background(), l)

	if doneCalled {
		t.Error("doneHook must NOT fire on persist failure (it would show a phantom success toast)")
	}
	if !persistFailed {
		t.Error("persistFailedHook must fire on persist failure")
	}
	got, _ := mgr.Get(l.ID)
	if got.Status != StatusRunning {
		t.Errorf("`done.` must not Stop the loop when the iteration didn't persist; status=%s", got.Status)
	}
	if got.IterationCount != 0 {
		t.Errorf("manager rolled the iteration back; IterationCount should be 0, got %d", got.IterationCount)
	}
}

// Fix #5: token budget auto-pauses when lifetime spend reaches the cap.
func TestAppendIteration_TokenBudgetAutoPauses(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning, MaxTotalTokens: 1000}
	l.AppendIteration(Iteration{N: 1, StartedAt: time.Now(), OK: true, TokensIn: 600, TokensOut: 600})
	if l.Status != StatusPaused || !l.AutoPaused {
		t.Fatalf("reaching the budget should auto-pause; status=%s auto=%v", l.Status, l.AutoPaused)
	}
	if !l.OverTokenBudget() {
		t.Error("OverTokenBudget should report true once spend >= cap")
	}
}

func TestAppendIteration_NoBudgetIgnoresTokens(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning} // MaxTotalTokens 0
	l.AppendIteration(Iteration{N: 1, StartedAt: time.Now(), OK: true, TokensIn: 1_000_000_000, TokensOut: 1_000_000_000})
	if l.Status != StatusRunning {
		t.Fatalf("no budget (0) must never pause on tokens; status=%s", l.Status)
	}
	if l.OverTokenBudget() {
		t.Error("OverTokenBudget must be false when no budget is set")
	}
}

func TestAppendIteration_UnderBudgetKeepsRunning(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning, MaxTotalTokens: 10000}
	l.AppendIteration(Iteration{N: 1, StartedAt: time.Now(), OK: true, TokensIn: 1000, TokensOut: 1000})
	if l.Status != StatusRunning || l.AutoPaused {
		t.Fatalf("under budget must keep running; status=%s auto=%v", l.Status, l.AutoPaused)
	}
}

// MaxIterations completion takes precedence over the token budget when both are
// reached on the same iteration (the Completed early-return runs first).
func TestAppendIteration_MaxIterationsBeatsTokenBudget(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning, MaxIterations: 1, MaxTotalTokens: 100}
	l.AppendIteration(Iteration{N: 1, StartedAt: time.Now(), OK: true, TokensIn: 600, TokensOut: 600})
	if l.Status != StatusCompleted {
		t.Fatalf("MaxIterations completion must win over the token budget; status=%s", l.Status)
	}
}

// Review fix: when an iteration trips BOTH the failure breaker and the token
// budget on the same call, the FIRST breaker (failures) wins and AutoPauseReason
// records THAT — so the UI can't mislabel a failing task as "budget reached".
func TestAppendIteration_PauseReasonPrecedence(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning, MaxTotalTokens: 1000}
	// 4 prior task failures (0 tokens), then a 5th that hits the streak limit AND
	// blows the budget on the same iteration.
	for i := 0; i < ConsecutiveFailureLimit-1; i++ {
		l.AppendIteration(Iteration{N: i + 1, StartedAt: time.Now(), OK: false})
	}
	l.AppendIteration(Iteration{N: ConsecutiveFailureLimit, StartedAt: time.Now(), OK: false, TokensIn: 600, TokensOut: 600})

	if l.Status != StatusPaused {
		t.Fatalf("should be paused, got %s", l.Status)
	}
	if !l.OverTokenBudget() {
		t.Fatal("precondition: spend should be over budget")
	}
	if l.AutoPauseReason != AutoPauseConsecutiveFailures {
		t.Fatalf("both failure-streak and budget tripped; reason must be %q (the breaker that fired first), got %q",
			AutoPauseConsecutiveFailures, l.AutoPauseReason)
	}
}

// A budget-only pause (on a SUCCESSFUL iteration that crossed the cap) records
// the token-budget reason.
func TestAppendIteration_PauseReasonBudgetOnly(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning, MaxTotalTokens: 1000}
	l.AppendIteration(Iteration{N: 1, StartedAt: time.Now(), OK: true, TokensIn: 600, TokensOut: 600})
	if l.AutoPauseReason != AutoPauseTokenBudget {
		t.Fatalf("budget-only pause reason = %q, want %q", l.AutoPauseReason, AutoPauseTokenBudget)
	}
}

func TestResume_ClearsAutoPauseReason(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 60, WithMaxTotalTokens(1000))
	_ = mgr.RecordIteration(l.ID, Iteration{N: 1, StartedAt: time.Now(), OK: true, TokensIn: 600, TokensOut: 600})
	got, _ := mgr.Get(l.ID)
	if got.Status != StatusPaused || got.AutoPauseReason != AutoPauseTokenBudget {
		t.Fatalf("precondition: status=%s reason=%q", got.Status, got.AutoPauseReason)
	}
	if err := mgr.Resume(l.ID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got, _ = mgr.Get(l.ID)
	if got.AutoPauseReason != "" {
		t.Errorf("Resume must clear AutoPauseReason, got %q", got.AutoPauseReason)
	}
}

func TestWithMaxTotalTokens(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 60, WithMaxTotalTokens(50000))
	if l.MaxTotalTokens != 50000 {
		t.Errorf("MaxTotalTokens = %d, want 50000", l.MaxTotalTokens)
	}
	// A zero/negative budget is ignored (unlimited stays the default).
	l2, _ := mgr.Add("t2", ModeInterval, 60, WithMaxTotalTokens(0))
	if l2.MaxTotalTokens != 0 {
		t.Errorf("zero budget should remain unlimited (0), got %d", l2.MaxTotalTokens)
	}
}
