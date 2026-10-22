package agent

import (
	"errors"
	"testing"

	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestPlannerAndReflectionShareAuxiliaryUsageAndTurnLimit(t *testing.T) {
	counter := &client.TokenUsageCounter{}
	ctx := tools.ContextWithMaxTurns(t.Context(), 2)
	ctx = client.ContextWithTokenUsageCounter(ctx, counter)
	ctx = client.ContextWithAuxiliaryRequests(ctx, client.AuxiliaryRequestPolicy{Client: ClientWithInvocationLimits})
	planning := testkit.NewMockClient().EnqueueText("STEP: explore | Inspect acceptance evidence in the repository\n")
	planning.SetModel(config.DefaultGLMModel)
	planner := NewTreePlanner(DefaultTreePlannerConfig(), nil, NewReflector(), planning)
	if actions, err := planner.generateActionsWithLLM(ctx, "inspect acceptance evidence", nil); err != nil || len(actions) == 0 {
		t.Fatalf("planning fixture failed: actions=%v err=%v", actions, err)
	}
	analysis := testkit.NewMockClient().EnqueueText(`{"category":"logic_error","suggestion":"Inspect the acceptance evidence","should_retry":false,"root_cause":"The proof needs inspection"}`)
	analysis.SetModel(config.GLMFlashModel)
	reflector := NewReflector()
	reflector.SetClient(analysis)
	reflector.SetSemanticAnalysis(true)
	if result := reflector.semanticAnalyze(ctx, "read", nil, "acceptance mismatch"); result == nil {
		t.Fatal("reflection fixture failed")
	}
	if result := reflector.semanticAnalyze(ctx, "read", nil, "acceptance mismatch"); result == nil || len(analysis.Calls()) != 1 {
		t.Fatal("cached reflection unexpectedly sent another request")
	}
	if _, err := planner.generateActionsWithLLM(ctx, "inspect again", nil); !errors.Is(err, tools.ErrMaxTurnsExceeded) {
		t.Fatalf("planner bypassed the common auxiliary cap: %v", err)
	}
	input, _ := counter.Snapshot()
	if input != 20 || len(planning.Calls()) != 1 {
		t.Fatalf("auxiliary requests were missing or doubled: input=%d planning=%d", input, len(planning.Calls()))
	}
}
