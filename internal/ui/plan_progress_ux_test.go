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
