package agent

import (
	"context"
	"testing"
	"time"

	"gokin/internal/config"
)

func TestAgentResultWaitTimeoutCoversThoroughAgentBudget(t *testing.T) {
	a := &Agent{ID: "bash-agent", timeout: 35 * time.Minute}
	runner := &Runner{agents: map[string]*Agent{a.ID: a}}

	got := runner.agentResultWaitTimeout(a.ID)
	want := 35*time.Minute + agentResultWaitGrace
	if got != want {
		t.Fatalf("result wait timeout = %v, want %v", got, want)
	}
}

func TestAgentResultWaitTimeoutHonorsLongerExplicitRunDeadline(t *testing.T) {
	a := &Agent{ID: "loop-agent", timeout: 10 * time.Minute}
	runCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	a.stateMu.Lock()
	a.runCtx = runCtx
	a.stateMu.Unlock()
	runner := &Runner{agents: map[string]*Agent{a.ID: a}}

	got := runner.agentResultWaitTimeout(a.ID)
	if got < 34*time.Minute || got > 36*time.Minute {
		t.Fatalf("result wait timeout = %v, want ~35m including grace", got)
	}
}

func TestAgentResultWaitTimeoutCoversRaisedNormalModelRoundBudgetBeforeRunStarts(t *testing.T) {
	a := &Agent{
		ID:                "raised-normal-agent",
		timeout:           config.DefaultAgentTimeout,
		modelRoundTimeout: 30 * time.Minute,
	}
	runner := &Runner{agents: map[string]*Agent{a.ID: a}}

	want := 30*time.Minute + config.DefaultAgentTimeoutHeadroom + agentResultWaitGrace
	if got := runner.agentResultWaitTimeout(a.ID); got != want {
		t.Fatalf("pre-run result wait timeout = %v, want %v", got, want)
	}
}

func TestAgentResultWaitTimeoutHasFallbackForUnknownAgent(t *testing.T) {
	runner := &Runner{agents: make(map[string]*Agent)}
	want := config.DefaultAgentTimeout + agentResultWaitGrace
	if got := runner.agentResultWaitTimeout("missing"); got != want {
		t.Fatalf("unknown-agent wait timeout = %v, want %v", got, want)
	}
}
