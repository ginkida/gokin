package plan

import "testing"

// TestSetStepUsage_PersistsToRealStep pins the round-12 fix for dead token
// accounting: plan steps execute against DEEP COPIES from NextReadySteps, so
// writing step.TokensUsed on the copy (as message_processor did) never reached
// the real step and the plan execution summary always reported 0 tokens.
func TestSetStepUsage_PersistsToRealStep(t *testing.T) {
	p := NewPlan("t", "")
	s := p.AddStep("s1", "do")

	// Demonstrate the bug: NextReadySteps hands back a COPY; writing on it does
	// NOT reach the real step.
	copies := p.NextReadySteps()
	if len(copies) == 0 {
		t.Fatal("expected a ready step")
	}
	copies[0].TokensUsed = 500
	copies[0].AgentMetrics = &StepAgentMetrics{TotalNodes: 7, MaxDepth: 3}

	snap := p.GetStepsSnapshot()
	if snap[0].TokensUsed != 0 {
		t.Fatalf("copy write leaked to the real step (%d) — test premise wrong", snap[0].TokensUsed)
	}
	if snap[0].AgentMetrics != nil {
		t.Fatal("copy AgentMetrics leaked to the real step")
	}

	// The fix: SetStepUsage / SetStepAgentMetrics persist by ID.
	p.SetStepUsage(s.ID, 500)
	p.SetStepAgentMetrics(s.ID, &StepAgentMetrics{TotalNodes: 7, MaxDepth: 3})

	snap = p.GetStepsSnapshot()
	if snap[0].TokensUsed != 500 {
		t.Errorf("SetStepUsage did not persist: got %d, want 500", snap[0].TokensUsed)
	}
	if snap[0].AgentMetrics == nil || snap[0].AgentMetrics.TotalNodes != 7 {
		t.Error("SetStepAgentMetrics did not persist")
	}

	// Unknown step ID is a safe no-op.
	p.SetStepUsage(999, 42)
	p.SetStepAgentMetrics(999, &StepAgentMetrics{TotalNodes: 1})
}
