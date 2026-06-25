package loops

import (
	"strings"
	"testing"
	"time"
)

func TestIsMonitorTask(t *testing.T) {
	monitor := []string{
		"monitor the deploy", "watch CI", "keep an eye on the queue",
		"check the deploy every 20m", "poll the health endpoint",
		"следи за деплоем", "проверяй статус каждый час",
	}
	for _, task := range monitor {
		if !IsMonitorTask(task) {
			t.Errorf("IsMonitorTask(%q) = false, want true (monitor)", task)
		}
	}
	action := []string{
		"fix bugs in this app", "implement the cache layer",
		"refactor the parser", "add tests for the queue",
		"fix every failing test", // "every" present but a clear action verb wins
		"сделай рефакторинг", "почини баги",
		// Action verb + an incidental "check"/"проверяй" must stay ACTION — else
		// churn protection is silently lost (v0.100.51 review catch).
		"сделай это и проверяй результат",
		"check the tests then fix the failures",
	}
	for _, task := range action {
		if IsMonitorTask(task) {
			t.Errorf("IsMonitorTask(%q) = true, want false (action)", task)
		}
	}
}

// TestAppendIteration_ChurnDetection_ActionTask: an action loop succeeding without
// making changes accrues ConsecutiveNoProgress, a real change resets it, and at
// NoProgressLimit the loop auto-pauses with the no_progress reason.
func TestAppendIteration_ChurnDetection_ActionTask(t *testing.T) {
	l := &Loop{ID: "l1", Task: "fix bugs in this app", Mode: ModeSelfPaced, Status: StatusRunning}
	base := time.Now()
	okNoChange := Iteration{OK: true, MadeChanges: false, StartedAt: base}

	for i := 0; i < NoProgressLimit-1; i++ {
		l.AppendIteration(okNoChange)
	}
	if l.ConsecutiveNoProgress != NoProgressLimit-1 {
		t.Fatalf("ConsecutiveNoProgress = %d, want %d", l.ConsecutiveNoProgress, NoProgressLimit-1)
	}
	if l.Status != StatusRunning {
		t.Fatalf("loop paused too early (at %d no-change iters)", l.ConsecutiveNoProgress)
	}

	// A real change resets the streak — a slow-but-real task is never paused.
	l.AppendIteration(Iteration{OK: true, MadeChanges: true, StartedAt: base})
	if l.ConsecutiveNoProgress != 0 {
		t.Fatalf("a changing iteration must reset the streak, got %d", l.ConsecutiveNoProgress)
	}
	if l.Status != StatusRunning {
		t.Fatalf("loop must keep running after real progress")
	}

	// Climb to the limit → auto-pause with the no_progress reason.
	for i := 0; i < NoProgressLimit; i++ {
		l.AppendIteration(okNoChange)
	}
	if l.Status != StatusPaused || l.AutoPauseReason != AutoPauseNoProgress {
		t.Fatalf("want auto-pause(no_progress); got status=%s reason=%q noprog=%d",
			l.Status, l.AutoPauseReason, l.ConsecutiveNoProgress)
	}
}

// TestAppendIteration_MonitorTaskExemptFromChurn: a monitor loop is SUPPOSED to
// be no-change most iterations, so it never accrues the streak or auto-pauses.
func TestAppendIteration_MonitorTaskExemptFromChurn(t *testing.T) {
	l := &Loop{ID: "l2", Task: "monitor the deploy", Mode: ModeInterval, IntervalSeconds: 600, Status: StatusRunning}
	base := time.Now()
	for i := 0; i < NoProgressLimit*2; i++ {
		l.AppendIteration(Iteration{OK: true, MadeChanges: false, StartedAt: base})
	}
	if l.ConsecutiveNoProgress != 0 {
		t.Errorf("monitor task must not accrue ConsecutiveNoProgress, got %d", l.ConsecutiveNoProgress)
	}
	if l.Status != StatusRunning {
		t.Errorf("monitor task must NOT auto-pause on no-change; status=%s reason=%q", l.Status, l.AutoPauseReason)
	}
}

// TestBuildIterationPrompt_SmartnessSignals: the enriched prompt carries the
// loop-state telemetry, the churn warning + [no changes] markers (action), the
// scope discipline, and task-shape-aware done coaching.
func TestBuildIterationPrompt_SmartnessSignals(t *testing.T) {
	l := &Loop{
		ID: "l3", Task: "fix bugs in this app", Mode: ModeSelfPaced,
		Status: StatusRunning, IterationCount: 5, SuccessCount: 5,
		ConsecutiveNoProgress: NoProgressWarnThreshold,
		Iterations:            []Iteration{{N: 5, OK: true, MadeChanges: false, StartedAt: time.Now()}},
	}
	p := BuildIterationPrompt(l)
	for _, want := range []string{"Loop status:", "NO code changes", "[no changes]", "Stay focused on THIS task", "done."} {
		if !strings.Contains(p, want) {
			t.Errorf("action prompt missing %q\n%s", want, p)
		}
	}

	m := &Loop{ID: "l4", Task: "watch CI", Mode: ModeInterval, Status: StatusRunning, IterationCount: 20}
	pm := BuildIterationPrompt(m)
	if strings.Contains(pm, "NO code changes") {
		t.Errorf("monitor task should NOT get a churn warning:\n%s", pm)
	}
	if !strings.Contains(pm, "MONITORING task") {
		t.Errorf("monitor task should get monitoring done-coaching:\n%s", pm)
	}
}
