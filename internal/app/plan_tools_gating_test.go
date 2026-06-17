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

func hasToolNamed(ts []*genai.Tool, name string) bool {
	for _, tl := range ts {
		for _, d := range tl.FunctionDeclarations {
			if d.Name == name {
				return true
			}
		}
	}
	return false
}

// TestPlanModeToolsLocked_GatedOnMemoryEnabled pins the user-reported bug: with
// memory disabled in config, the memory tool was still offered, so the model
// called it and got a confusing "memory is unavailable" error. When
// memory.enabled is false the memory/memorize tools must not be in the schema.
func TestPlanModeToolsLocked_GatedOnMemoryEnabled(t *testing.T) {
	registry := tools.DefaultRegistry(t.TempDir())

	on := &config.Config{}
	on.Plan.Enabled = true
	on.Memory.Enabled = true
	got := (&App{config: on, registry: registry}).planModeToolsLocked(false)
	if !hasToolNamed(got, "memory") || !hasToolNamed(got, "memorize") {
		t.Fatal("with memory.enabled=true, memory/memorize must be available")
	}

	off := &config.Config{}
	off.Plan.Enabled = true // only memory is gated here
	off.Memory.Enabled = false
	got = (&App{config: off, registry: registry}).planModeToolsLocked(false)
	if hasToolNamed(got, "memory") || hasToolNamed(got, "memorize") {
		t.Error("with memory.enabled=false, memory/memorize must NOT be offered")
	}
	if !hasToolNamed(got, "enter_plan_mode") {
		t.Error("gating memory must not drop plan tools (plan.enabled=true here)")
	}
}
