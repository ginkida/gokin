package app

import (
	"testing"

	"google.golang.org/genai"

	"gokin/internal/config"
	"gokin/internal/tools"
)

// TestPlanModeToolsLocked_GatedOnPlanEnabled pins the fix for the headless
// plan-mode trap: a model that calls enter_plan_mode and then exit_plan_mode
// in a single-shot/headless run (no interactive plan approval) strands itself
// read-only and never edits. When plan.enabled is false, the plan-mode control
// tools must not be offered at all so the model can't enter that state.
func TestPlanModeToolsLocked_GatedOnPlanEnabled(t *testing.T) {
	hasEnterPlan := func(ts []*genai.Tool) bool {
		for _, tl := range ts {
			for _, d := range tl.FunctionDeclarations {
				if d.Name == "enter_plan_mode" {
					return true
				}
			}
		}
		return false
	}

	registry := tools.DefaultRegistry(t.TempDir())

	cfgOn := &config.Config{}
	cfgOn.Plan.Enabled = true
	appOn := &App{config: cfgOn, registry: registry}
	if !hasEnterPlan(appOn.planModeToolsLocked(false)) {
		t.Fatal("with plan.enabled=true, enter_plan_mode must be available")
	}

	cfgOff := &config.Config{}
	cfgOff.Plan.Enabled = false
	appOff := &App{config: cfgOff, registry: registry}
	if hasEnterPlan(appOff.planModeToolsLocked(false)) {
		t.Error("with plan.enabled=false, enter_plan_mode must NOT be available")
	}
}
