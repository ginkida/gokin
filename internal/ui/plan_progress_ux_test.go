package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestPlanProgressPanelFitsWidthsAndNormalizesContent(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Long\n计划 "+strings.Repeat("title ", 20), "", []PlanStepInfo{
		{ID: 1, Title: "Implement\n" + strings.Repeat("请求 ", 20), Description: "Description\n" + strings.Repeat("🙂", 20)},
		{ID: 2, Title: "Verify", Description: "tests"},
	})
	panel.StartStep(1)
	panel.SetCurrentTool("grep\nTODO", strings.Repeat("/very/long/path/请求/", 10))

	for _, width := range []int{1, 4, 12, 30, 60, 140} {
		view := panel.View(width, 24)
		limit := min(max(width, 1), 100)
		for row, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > limit {
				t.Fatalf("width=%d row=%d rendered %d cells, limit=%d:\n%s", width, row, got, limit, stripAnsi(view))
			}
		}
	}
	wide := stripAnsi(panel.View(80, 24))
	if !strings.Contains(wide, "grep TODO") || strings.Contains(wide, "grep\nTODO") {
		t.Fatalf("plan tool metadata was lost or injected a row:\n%s", wide)
	}
}

func TestPlanProgressStartClearsPreviousTransientState(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("old", "Old", "", []PlanStepInfo{{ID: 1, Title: "One"}})
	panel.StartStep(1)
	panel.SetCurrentTool("write", "old.go")
	panel.AddActivity("info", "old activity")

	panel.StartPlan("new", "New", "", []PlanStepInfo{{ID: 2, Title: "Two"}})
	if panel.currentTool != "" || panel.currentInfo != "" || len(panel.activities) != 0 {
		t.Fatalf("new plan retained transient state: tool=%q info=%q activities=%v", panel.currentTool, panel.currentInfo, panel.activities)
	}
	if view := stripAnsi(panel.View(80)); strings.Contains(view, "old.go") || strings.Contains(view, "old activity") {
		t.Fatalf("new plan rendered stale state:\n%s", view)
	}
}

func TestPlanProgressStepRestartAndFinishClearStaleState(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Plan", "", []PlanStepInfo{{ID: 1, Title: "One"}})
	panel.StartStep(1)
	panel.FailStep(1, "old failure", "reason")
	panel.StartStep(1)
	step := panel.steps[0]
	if step.Error != "" || step.Output != "" || !step.CompletedAt.IsZero() {
		t.Fatalf("restarted step retained terminal state: %+v", step)
	}
	panel.SetCurrentTool("edit", "file.go")
	panel.CompleteStep(1, "done", "")
	if panel.currentTool != "" || panel.currentInfo != "" {
		t.Fatalf("finished step retained live tool: %q %q", panel.currentTool, panel.currentInfo)
	}
}

func TestPlanProgressEmptyAndFailedStatesAreExplicit(t *testing.T) {
	empty := NewPlanProgressPanel(DefaultStyles())
	empty.StartPlan("empty", "", "", nil)
	emptyView := stripAnsi(empty.View(50))
	for _, want := range []string{"Untitled plan", "No executable steps"} {
		if !strings.Contains(emptyView, want) {
			t.Fatalf("empty plan missing %q:\n%s", want, emptyView)
		}
	}

	failed := NewPlanProgressPanel(DefaultStyles())
	failed.StartPlan("failed", "Deployment", "", []PlanStepInfo{{ID: 1, Title: "Deploy"}, {ID: 2, Title: "Verify"}})
	failed.StartStep(1)
	failed.FailStep(1, "provider\nrejected request", "")
	failed.EndPlan()
	failed.collapsed = true
	view := stripAnsi(failed.View(70))
	for _, want := range []string{"Plan failed: Deployment", "1 failed", "provider rejected request"} {
		if !strings.Contains(view, want) {
			t.Fatalf("failed plan missing %q:\n%s", want, view)
		}
	}
	if compact := stripAnsi(failed.ViewCompact()); !strings.Contains(compact, "✗ Plan") {
		t.Fatalf("failed compact plan still looked active/successful: %q", compact)
	}
}

func TestPlanProgressShortTerminalAutoCompactsHonestly(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Plan", "", planSteps(3))
	panel.StartStep(1)
	view := stripAnsi(panel.View(50, 10))
	if !strings.Contains(view, "resize to expand") || strings.Contains(view, "Ctrl+X to expand") {
		t.Fatalf("height-driven compact mode advertised an ineffective toggle:\n%s", view)
	}
}

func TestPlanProgressCompactHeightKeepsCurrentStepWithoutDuplicateTool(t *testing.T) {
	for _, height := range []int{8, 10, 15} {
		panel := NewPlanProgressPanel(DefaultStyles())
		panel.StartPlan("p", "Workspace migration", "", planSteps(6))
		panel.StartStep(3)
		panel.SetCurrentTool("grep TODO", strings.Repeat("deep/path/", 12))

		view := panel.View(60, height)
		plain := stripAnsi(view)
		for _, want := range []string{"Step 3", "grep TODO", "hidden"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d compact plan missing %q:\n%s", height, want, plain)
			}
		}
		if count := strings.Count(plain, "grep TODO"); count != 1 {
			t.Fatalf("height=%d rendered current tool %d times:\n%s", height, count, plain)
		}
		if got := lipgloss.Height(view); got > height {
			t.Fatalf("height=%d compact plan rendered %d rows:\n%s", height, got, plain)
		}
	}
}

func TestPlanProgressSmallestPausedViewPrioritizesResumeAndReason(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Workspace migration", "", planSteps(6))
	panel.StartStep(3)
	panel.PauseStep(3, "Credentials need review before continuing")

	view := panel.View(60, 8)
	plain := stripAnsi(view)
	for _, want := range []string{"Plan paused", "Step 3", "Credentials need review", "/resume-plan"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("smallest paused plan missing %q:\n%s", want, plain)
		}
	}
	if got := lipgloss.Height(view); got > 8 {
		t.Fatalf("smallest paused plan rendered %d rows:\n%s", got, plain)
	}
	if roomier := stripAnsi(panel.View(60, 10)); !strings.Contains(roomier, "5 hidden") {
		t.Fatalf("roomier paused plan did not restore folded summary:\n%s", roomier)
	}
}

func TestPlanProgressTerminalSummaryDistinguishesDoneSkippedAndUnfinished(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Migration", "", []PlanStepInfo{
		{ID: 1, Title: "Apply"},
		{ID: 2, Title: "Optional cleanup"},
		{ID: 3, Title: "Verify"},
	})
	panel.StartStep(1)
	panel.CompleteStep(1, "done", "")
	panel.SkipStep(2, "not needed")
	panel.EndPlan()

	view := stripAnsi(panel.View(80))
	for _, want := range []string{"Plan incomplete: Migration", "1/3", "1 skipped", "1 unfinished"} {
		if !strings.Contains(view, want) {
			t.Fatalf("terminal summary missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Plan complete:") {
		t.Fatalf("unfinished plan claimed completion:\n%s", view)
	}
	compact := stripAnsi(panel.ViewCompact())
	if !strings.Contains(compact, "! Plan 1/3 · 1 skipped") {
		t.Fatalf("compact summary hid terminal incompleteness: %q", compact)
	}
}

func TestPlanProgressAllResolvedCanCompleteWithSeparateSkippedCount(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Cleanup", "", []PlanStepInfo{{ID: 1, Title: "Remove"}, {ID: 2, Title: "Optional"}})
	panel.CompleteStep(1, "done", "")
	panel.SkipStep(2, "not applicable")
	panel.EndPlan()

	view := stripAnsi(panel.View(70))
	if !strings.Contains(view, "Plan complete: Cleanup") || !strings.Contains(view, "1/2") || !strings.Contains(view, "1 skipped") {
		t.Fatalf("resolved plan summary is not honest:\n%s", view)
	}
	if panel.CompletedCount() != 1 || panel.Progress() != 1 {
		t.Fatalf("counts disagree: completed=%d progress=%v", panel.CompletedCount(), panel.Progress())
	}
}

func TestPlanProgressDuplicateAndUnknownEventsDoNotRewriteLifecycle(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Plan", "", []PlanStepInfo{{ID: 1, Title: "One"}})
	panel.StartStep(1)
	started := panel.steps[0].StartedAt
	timelineLen := len(panel.timeline)
	panel.StartStep(1)
	if !panel.steps[0].StartedAt.Equal(started) || len(panel.timeline) != timelineLen {
		t.Fatalf("duplicate start rewrote lifecycle: step=%+v timeline=%d", panel.steps[0], len(panel.timeline))
	}

	panel.CompleteStep(1, "first", "")
	completed := panel.steps[0].CompletedAt
	timelineLen = len(panel.timeline)
	panel.CompleteStep(1, "duplicate", "duplicate event")
	if !panel.steps[0].CompletedAt.Equal(completed) || panel.steps[0].Output != "first" || len(panel.timeline) != timelineLen {
		t.Fatalf("duplicate completion rewrote lifecycle: step=%+v timeline=%d", panel.steps[0], len(panel.timeline))
	}

	panel.EndPlan()
	finished := panel.finishedAt
	panel.StartStep(404)
	if !panel.finishedAt.Equal(finished) {
		t.Fatal("unknown step event reopened a finished plan")
	}

	running := NewPlanProgressPanel(DefaultStyles())
	running.StartPlan("running", "Running", "", []PlanStepInfo{{ID: 1, Title: "One"}})
	running.StartStep(1)
	runningStarted := running.steps[0].StartedAt
	running.EndPlan()
	running.StartStep(1)
	if !running.finishedAt.IsZero() || !running.steps[0].StartedAt.Equal(runningStarted) {
		t.Fatal("same-step resume should reopen the plan without rewriting its start time")
	}
}

func TestPlanProgressRepeatedTerminalEventsAreIdempotent(t *testing.T) {
	tests := []struct {
		name   string
		status PlanStepStatus
		apply  func(*PlanProgressPanel)
	}{
		{name: "completed", status: PlanStepCompleted, apply: func(p *PlanProgressPanel) { p.CompleteStep(1, "done", "") }},
		{name: "failed", status: PlanStepFailed, apply: func(p *PlanProgressPanel) { p.FailStep(1, "failed", "") }},
		{name: "skipped", status: PlanStepSkipped, apply: func(p *PlanProgressPanel) { p.SkipStep(1, "optional") }},
		{name: "paused", status: PlanStepPaused, apply: func(p *PlanProgressPanel) { p.PauseStep(1, "waiting") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			panel := NewPlanProgressPanel(DefaultStyles())
			panel.StartPlan("p", "Plan", "", []PlanStepInfo{{ID: 1, Title: "One"}})
			panel.StartStep(1)
			tt.apply(panel)
			completedAt := panel.steps[0].CompletedAt
			timelineLen := len(panel.timeline)
			tt.apply(panel)
			if panel.steps[0].Status != tt.status || !panel.steps[0].CompletedAt.Equal(completedAt) || len(panel.timeline) != timelineLen {
				t.Fatalf("duplicate %s rewrote lifecycle: step=%+v timeline=%d", tt.name, panel.steps[0], len(panel.timeline))
			}
		})
	}
}

func TestPlanProgressUnknownStatusRendersExplicitly(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.StartPlan("p", "Plan", "", []PlanStepInfo{{ID: 1, Title: "One"}})
	panel.steps[0].Status = PlanStepStatus(99)
	panel.EndPlan()

	view := stripAnsi(panel.View(60))
	if !strings.Contains(view, "Plan incomplete:") || !strings.Contains(view, "? Step 1") {
		t.Fatalf("unknown status was rendered as a normal terminal state:\n%s", view)
	}
}

func TestPlanProgressElapsedNeverNegativeOrAbsurd(t *testing.T) {
	panel := NewPlanProgressPanel(DefaultStyles())
	panel.visible = true
	panel.steps = []PlanStepState{{ID: 1, Title: "One", Status: PlanStepInProgress, StartedAt: time.Now().Add(time.Minute)}}
	panel.startedAt = time.Now().Add(time.Minute)
	view := stripAnsi(panel.View(60))
	if strings.Contains(view, "-") || strings.Contains(view, "2562047h") {
		t.Fatalf("invalid timestamps leaked into plan elapsed:\n%s", view)
	}
}
