package tools

import (
	"testing"

	"google.golang.org/genai"
)

func TestGeminiToolsExcludingPlanMode(t *testing.T) {
	names := func(ts []*genai.Tool) map[string]bool {
		m := map[string]bool{}
		for _, tl := range ts {
			for _, d := range tl.FunctionDeclarations {
				m[d.Name] = true
			}
		}
		return m
	}

	reg := DefaultRegistry(t.TempDir())
	full := names(reg.GeminiTools())
	excl := names(reg.GeminiToolsExcludingPlanMode())

	planControl := []string{
		"enter_plan_mode", "exit_plan_mode", "update_plan_progress",
		"get_plan_status", "undo_plan", "redo_plan",
	}
	dropped := 0
	for _, n := range planControl {
		if full[n] {
			dropped++
		}
		if excl[n] {
			t.Errorf("%q must be excluded by GeminiToolsExcludingPlanMode", n)
		}
	}
	if dropped == 0 {
		t.Fatal("precondition: the full tool set should contain plan-mode control tools")
	}

	// task* (also members of ToolSetPlanning) and the editing tools must
	// REMAIN — only the interactive plan-mode CONTROL tools are dropped.
	for _, n := range []string{"task", "task_output", "edit", "write", "read", "bash"} {
		if full[n] && !excl[n] {
			t.Errorf("%q must NOT be excluded — only plan-mode control tools are dropped", n)
		}
	}

	if len(excl) != len(full)-dropped {
		t.Errorf("excluded set size = %d, want %d (full %d minus %d plan-mode tools)",
			len(excl), len(full)-dropped, len(full), dropped)
	}
}
