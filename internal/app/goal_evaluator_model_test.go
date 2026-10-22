package app

import (
	"io"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/goals"
	"gokin/internal/testkit"
)

func TestGoalEvaluatorModelOverrideKeepsWorkerAndSharedLimits(t *testing.T) {
	for _, test := range []struct {
		name, model string
		turns       int
		budget      float64
		kind        string
		achieved    bool
		evalCalls   int
	}{
		{name: "compatible-model", model: "glm-4.7", turns: 3, achieved: true, evalCalls: 1},
		{name: "custom-gateway", model: "gateway-small", turns: 3, achieved: true, evalCalls: 1},
		{name: "invalid-provider", model: "deepseek-v4-flash", turns: 3, kind: "execution"},
		{name: "shared-turn-cap", model: "glm-4.7", turns: 1, kind: "max_turns"},
		{name: "shared-cost-cap", model: "glm-4.7", turns: 3, budget: 1e-12, kind: "budget_exceeded", evalCalls: 1},
		{name: "unknown-cost", model: "gateway-small", turns: 3, budget: 1, kind: "cost_unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GOKIN_SMALL_FAST_MODEL", "")
			t.Setenv("ANTHROPIC_DEFAULT_HAIKU_MODEL", test.model)
			t.Setenv("ANTHROPIC_SMALL_FAST_MODEL", "")
			worker := testkit.NewMockClient().EnqueueText("The complete acceptance condition is verified")
			evaluator := testkit.NewMockClient().EnqueueText(`{"verdict":"met","reason":"The acceptance evidence demonstrates the condition."}`)
			a := newGoalTestApp(t, worker, evaluator)
			if test.budget > 0 {
				a.executor.SetCostCalculator(func(string, string, int, int, int) (float64, bool) { return 0, true })
			}
			result, err := a.RunHeadlessWithOptions(t.Context(), "/goal verify the complete acceptance condition", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: test.turns, MaxBudgetUSD: test.budget})
			if len(worker.Calls()) != 1 || worker.GetModel() != config.DefaultGLMModel || worker.Calls()[0].Model != config.DefaultGLMModel || len(evaluator.Calls()) != test.evalCalls {
				t.Fatalf("model selection changed worker or call count: %+v %v", result, err)
			}
			if test.achieved {
				if err != nil || result.Goal.Status != goals.Achieved || result.Goal.InputTokens != 20 {
					t.Fatalf("valid override failed: %+v %v", result, err)
				}
			} else if err == nil || result.Error == nil || result.Error.Kind != test.kind || result.Goal.Status != goals.Active || !result.Goal.Suspended {
				t.Fatalf("invalid/over-budget evaluator succeeded: %+v %v", result, err)
			}
			if test.evalCalls > 0 {
				call := evaluator.Calls()[0]
				if call.Model != test.model || call.ToolsCount != 0 || len(call.History) != 0 {
					t.Fatalf("evaluator selection lost isolation: %+v", call)
				}
			}
			if test.name == "invalid-provider" && !strings.Contains(result.Goal.Reason, "active provider") {
				t.Fatal("invalid model lost its actionable error")
			}
		})
	}
}
