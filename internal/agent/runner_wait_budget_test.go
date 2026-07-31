package agent

import (
	"context"
	"testing"
	"time"
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

func TestAgentResultWaitTimeoutHasFallbackForUnknownAgent(t *testing.T) {
	runner := &Runner{agents: make(map[string]*Agent)}
	if got := runner.agentResultWaitTimeout("missing"); got != 15*time.Minute {
		t.Fatalf("unknown-agent wait timeout = %v, want 15m", got)
	}
}
