package ui

import (
	"strings"
	"testing"
)

func planSteps(n int) []PlanStepInfo {
	steps := make([]PlanStepInfo, 0, n)
	for i := 1; i <= n; i++ {
		steps = append(steps, PlanStepInfo{ID: i, Title: "step"})
	}
	return steps
}

func TestPlanPanel_AutoCollapsesLargePlans(t *testing.T) {
	p := NewPlanProgressPanel(nil)
	p.StartPlan("p1", "Big plan", "", planSteps(10))
	if !p.collapsed {
		t.Fatal("10-step plan must start collapsed — full list pinned ~17 lines to the screen")
	}

	p.StartPlan("p2", "Small plan", "", planSteps(3))
	if p.collapsed {
		t.Fatal("3-step plan fits on screen — must start expanded")
	}

	p.StartPlan("p3", "Boundary plan", "", planSteps(planAutoCollapseSteps))
	if p.collapsed {
		t.Fatalf("%d-step plan is at the threshold — must start expanded", planAutoCollapseSteps)
	}
}

func TestPlanPanel_CollapsedViewIsCompactAndHonest(t *testing.T) {
	p := NewPlanProgressPanel(nil)
	p.StartPlan("p1", "Big plan", "", planSteps(10))
	p.StartStep(1)
	p.CompleteStep(1, "done", "")
	p.StartStep(2)

	view := p.View(100)
	lines := strings.Count(view, "\n") + 1
	// Borders(3) + header + current step (≤2 lines) + summary + footer.
	if lines > 9 {
		t.Fatalf("collapsed view is %d lines, want ≤9:\n%s", lines, view)
	}
	if !strings.Contains(view, "Ctrl+X to expand") {
		t.Fatalf("collapsed summary must advertise the REAL expand key:\n%s", view)
	}
	if strings.Contains(view, "press Tab") {
		t.Fatalf("collapsed summary still advertises the never-wired Tab binding:\n%s", view)
	}
	if !strings.Contains(view, "✓ 1 done") {
		t.Fatalf("collapsed summary missing completed count:\n%s", view)
	}

	p.Toggle()
	expanded := p.View(100)
	if strings.Count(expanded, "\n") <= strings.Count(view, "\n") {
		t.Fatal("Toggle() must expand the panel to the full step list")
	}
}
