package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/tools"
)

// v0.100.103 field report: a /loop iteration budgeted at 30m (iterationCtx)
// was silently clipped at 10m by DefaultAgentTimeout stacked UNDER it inside
// SpawnWithContext ("general ✗ 10.0m" mid-write). The agent's own timeout is
// a default safety cap for UNDEADLINED parents only — an explicit caller
// deadline wins.
func TestAgentRunContext_ExplicitParentDeadlineWins(t *testing.T) {
	a := &Agent{timeout: config.DefaultAgentTimeout}

	// Parent WITH a deadline (the loop iterationCtx shape): the agent's
	// smaller default must NOT be stacked under it.
	parent, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	runCtx, runCancel := agentRunContext(parent, a)
	defer runCancel()
	deadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("run ctx must inherit the parent deadline")
	}
	if remaining := time.Until(deadline); remaining < 25*time.Minute {
		t.Fatalf("agent default timeout clipped the explicit parent budget: %v remaining, want ~30m", remaining)
	}

	// Parent WITHOUT a deadline (foreground task-tool spawn): the agent's
	// timeout is the safety cap and must apply.
	runCtx2, runCancel2 := agentRunContext(context.Background(), a)
	defer runCancel2()
	deadline2, ok := runCtx2.Deadline()
	if !ok {
		t.Fatal("undeadlined parent must get the agent's default timeout cap")
	}
	if remaining := time.Until(deadline2); remaining > config.DefaultAgentTimeout+time.Minute {
		t.Fatalf("cap too generous: %v", remaining)
	}
	if remaining := time.Until(deadline2); remaining < config.DefaultModelRoundTimeout+time.Minute {
		t.Fatalf("default agent cap leaves no room for a full model round: %v", remaining)
	}

	// Zero agent timeout + undeadlined parent: plain cancelable ctx.
	b := &Agent{}
	runCtx3, runCancel3 := agentRunContext(context.Background(), b)
	defer runCancel3()
	if _, ok := runCtx3.Deadline(); ok {
		t.Fatal("zero timeout + undeadlined parent must not invent a deadline")
	}
}

func TestAgentRunContext_ExplicitQuickBudgetStillWins(t *testing.T) {
	a := &Agent{timeout: 2 * time.Minute, thoroughness: tools.ThoroughnessQuick}
	runCtx, cancel := agentRunContext(context.Background(), a)
	defer cancel()

	deadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("quick agent must keep its explicit deadline")
	}
	if remaining := time.Until(deadline); remaining > 3*time.Minute {
		t.Fatalf("quick agent budget was widened to %v", remaining)
	}
}

func TestAgentRunContext_NormalBudgetFollowsRaisedModelRoundTimeout(t *testing.T) {
	a := &Agent{
		timeout:           config.DefaultAgentTimeout,
		modelRoundTimeout: 30 * time.Minute,
		thoroughness:      tools.ThoroughnessNormal,
	}
	runCtx, cancel := agentRunContext(context.Background(), a)
	defer cancel()

	deadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("normal agent must have an outer deadline")
	}
	want := 30*time.Minute + config.DefaultAgentTimeoutHeadroom
	if remaining := time.Until(deadline); remaining < want-time.Minute || remaining > want+time.Minute {
		t.Fatalf("normal agent run budget = %v, want ~%v", remaining, want)
	}
}

func TestApplyThoroughnessIsNeverShorterThanNormal(t *testing.T) {
	for _, agentType := range []AgentType{
		AgentTypeExplore,
		AgentTypeBash,
		AgentTypeGeneral,
		AgentTypePlan,
		AgentTypeGuide,
		AgentType("dynamic-reviewer"),
	} {
		t.Run(string(agentType), func(t *testing.T) {
			a := &Agent{Type: agentType, timeout: config.DefaultAgentTimeout, maxTurns: 15}
			a.ApplyThoroughness(tools.ThoroughnessThorough, 15)
			if got := a.GetTimeout(); got != config.DefaultThoroughAgentTimeout {
				t.Fatalf("thorough timeout = %v, want %v", got, config.DefaultThoroughAgentTimeout)
			}

			a.SetModelRoundTimeout(40 * time.Minute)
			want := 40*time.Minute + config.DefaultAgentTimeoutHeadroom
			if got := a.EffectiveRunTimeout(); got != want {
				t.Fatalf("raised thorough effective timeout = %v, want %v", got, want)
			}
		})
	}
}
