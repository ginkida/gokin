package loops

import (
	"testing"
	"time"
)

func TestTransientBackoffSeconds(t *testing.T) {
	cases := []struct {
		streak int
		want   int64
	}{
		{0, 60},   // clamped to 1 → base
		{1, 60},   // base
		{2, 120},  // ×2
		{3, 240},  // ×4
		{4, 480},  // ×8
		{5, 960},  // ×16
		{6, 1800}, // would be 1920 → capped
		{20, 1800},
		{1000, 1800}, // hostile input — no overflow, stays capped
	}
	for _, tc := range cases {
		if got := transientBackoffSeconds(tc.streak); got != tc.want {
			t.Errorf("transientBackoffSeconds(%d) = %d, want %d", tc.streak, got, tc.want)
		}
	}
}

// A transient failure must NOT trip the task-failure auto-pause breaker, must
// increment the separate transient streak, and must push NextRunAt out by the
// loop-level backoff — even far past the 5-failure task limit.
func TestAppendIteration_TransientDoesNotTripTaskBreaker(t *testing.T) {
	l := &Loop{
		ID: "x", Task: "t", Mode: ModeSelfPaced, MinDelaySeconds: 60,
		Status: StatusRunning,
	}
	start := time.Now()
	for i := 0; i < ConsecutiveFailureLimit+3; i++ {
		l.AppendIteration(Iteration{
			N: i + 1, StartedAt: start.Add(time.Duration(i) * time.Hour),
			Duration: time.Second, OK: false, Transient: true,
		})
	}
	if l.Status != StatusRunning {
		t.Fatalf("transient failures must not auto-pause below the transient backstop; status=%s", l.Status)
	}
	if l.ConsecutiveFailures != 0 {
		t.Errorf("task-failure counter must stay 0 on transient failures, got %d", l.ConsecutiveFailures)
	}
	if l.ConsecutiveTransientFailures != ConsecutiveFailureLimit+3 {
		t.Errorf("transient streak = %d, want %d", l.ConsecutiveTransientFailures, ConsecutiveFailureLimit+3)
	}
	if l.FailureCount != ConsecutiveFailureLimit+3 {
		t.Errorf("FailureCount = %d, want %d (transient failures still count for stats)", l.FailureCount, ConsecutiveFailureLimit+3)
	}
}

// The loop-level backoff must push the next run out for a fast self-paced loop.
func TestAppendIteration_TransientAppliesBackoff(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeSelfPaced, MinDelaySeconds: 60, Status: StatusRunning}
	start := time.Now()

	// 3rd consecutive transient → backoff 240s, well past the 60s floor.
	for i := 0; i < 3; i++ {
		it := Iteration{N: i + 1, StartedAt: start, Duration: time.Second, OK: false, Transient: true}
		l.AppendIteration(it)
		start = l.LastRunAt
	}
	gap := l.NextRunAt.Sub(l.LastRunAt)
	if gap < 240*time.Second {
		t.Fatalf("expected backoff >= 240s after 3 transient failures, got %v", gap)
	}
}

// A long-interval loop is unaffected by backoff — its cadence already exceeds it.
func TestAppendIteration_TransientBackoffRespectsLongInterval(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 3600, Status: StatusRunning}
	l.AppendIteration(Iteration{N: 1, StartedAt: time.Now(), Duration: time.Second, OK: false, Transient: true})
	gap := l.NextRunAt.Sub(l.LastRunAt)
	if gap != time.Hour {
		t.Fatalf("long interval must win over backoff, got %v want 1h", gap)
	}
}

// The transient backstop eventually auto-pauses a permanently-dead provider.
func TestAppendIteration_TransientBackstopAutoPauses(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	for i := 0; i < TransientFailureLimit; i++ {
		l.AppendIteration(Iteration{N: i + 1, StartedAt: time.Now(), Duration: time.Second, OK: false, Transient: true})
	}
	if l.Status != StatusPaused || !l.AutoPaused {
		t.Fatalf("expected auto-pause after %d transient failures, status=%s auto=%v", TransientFailureLimit, l.Status, l.AutoPaused)
	}
}

// Success resets BOTH streaks; a task failure resets the transient streak.
func TestAppendIteration_StreakResets(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	now := time.Now()
	l.AppendIteration(Iteration{N: 1, StartedAt: now, OK: false, Transient: true})
	l.AppendIteration(Iteration{N: 2, StartedAt: now, OK: false, Transient: true})
	if l.ConsecutiveTransientFailures != 2 {
		t.Fatalf("transient streak = %d, want 2", l.ConsecutiveTransientFailures)
	}
	// Success clears everything.
	l.AppendIteration(Iteration{N: 3, StartedAt: now, OK: true})
	if l.ConsecutiveTransientFailures != 0 || l.ConsecutiveFailures != 0 {
		t.Fatalf("success must clear both streaks, got transient=%d task=%d", l.ConsecutiveTransientFailures, l.ConsecutiveFailures)
	}
	// Transient then a real task failure: task streak starts, transient resets.
	l.AppendIteration(Iteration{N: 4, StartedAt: now, OK: false, Transient: true})
	l.AppendIteration(Iteration{N: 5, StartedAt: now, OK: false, Transient: false})
	if l.ConsecutiveFailures != 1 {
		t.Errorf("task-failure streak = %d, want 1", l.ConsecutiveFailures)
	}
	if l.ConsecutiveTransientFailures != 0 {
		t.Errorf("task failure must reset transient streak, got %d", l.ConsecutiveTransientFailures)
	}
}

// A transient blip in the MIDDLE of a task-failure streak must not reset the
// task streak — the breaker still trips once enough real failures accumulate.
func TestAppendIteration_TransientBlipPreservesTaskStreak(t *testing.T) {
	l := &Loop{ID: "x", Task: "t", Mode: ModeInterval, IntervalSeconds: 60, Status: StatusRunning}
	now := time.Now()
	// 3 task failures, 1 transient blip, then 2 more task failures = 5 task → pause.
	steps := []struct{ transient bool }{
		{false}, {false}, {false}, {true}, {false}, {false},
	}
	for i, s := range steps {
		l.AppendIteration(Iteration{N: i + 1, StartedAt: now, OK: false, Transient: s.transient})
	}
	if l.ConsecutiveFailures != 5 {
		t.Errorf("task streak should be 5 (transient blip preserved it), got %d", l.ConsecutiveFailures)
	}
	if l.Status != StatusPaused || !l.AutoPaused {
		t.Fatalf("expected task-breaker auto-pause, status=%s auto=%v", l.Status, l.AutoPaused)
	}
}

// Resume must clear the transient streak too (manual resume = clean slate).
func TestResume_ClearsTransientStreak(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 60)
	for i := 0; i < TransientFailureLimit; i++ {
		_ = mgr.RecordIteration(l.ID, Iteration{N: i + 1, StartedAt: time.Now(), OK: false, Transient: true})
	}
	got, _ := mgr.Get(l.ID)
	if got.Status != StatusPaused {
		t.Fatalf("precondition: expected auto-pause, got %s", got.Status)
	}
	if err := mgr.Resume(l.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, _ = mgr.Get(l.ID)
	if got.ConsecutiveTransientFailures != 0 {
		t.Errorf("Resume must clear transient streak, got %d", got.ConsecutiveTransientFailures)
	}
}

// The runner must carry SpawnResult.Transient into the recorded Iteration.
func TestRunner_PropagatesTransientFlag(t *testing.T) {
	mgr := NewManager(newMemStorage())
	l, _ := mgr.Add("t", ModeInterval, 3600)
	spawner := &fakeSpawner{output: "GLM overloaded", ok: false, transient: true}
	r := NewRunner(mgr, spawner.spawn, (&fakeIdle{}).check)
	r.fireOne(t.Context(), l)

	got, _ := mgr.Get(l.ID)
	if len(got.Iterations) != 1 {
		t.Fatalf("expected 1 iteration, got %d", len(got.Iterations))
	}
	if !got.Iterations[0].Transient {
		t.Error("iteration should be marked Transient from SpawnResult")
	}
	if got.ConsecutiveFailures != 0 {
		t.Errorf("transient failure must not increment task-failure streak, got %d", got.ConsecutiveFailures)
	}
	if got.ConsecutiveTransientFailures != 1 {
		t.Errorf("transient streak = %d, want 1", got.ConsecutiveTransientFailures)
	}
}
