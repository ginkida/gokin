package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/tools"
)

// TestWithModelRoundTimeout_GenerousCap pins that the round timeout is the shared
// generous cap (~14m), not the old too-tight 5m that killed long thinking rounds,
// and that it clamps to a stricter parent deadline.
func TestWithModelRoundTimeout_GenerousCap(t *testing.T) {
	a := &Agent{} // helper uses only the const + parent, no agent state

	ctx, cancel := a.withModelRoundTimeout(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("round ctx should have a deadline")
	}
	if rem := time.Until(deadline); rem < 13*time.Minute || rem > 15*time.Minute {
		t.Errorf("round timeout = %v, want ~14m (the too-tight 5m caused the 7m-stop incident)", rem)
	}

	// Parent stricter -> clamp to the parent's remaining.
	parent, pcancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer pcancel()
	cctx, ccancel := a.withModelRoundTimeout(parent)
	defer ccancel()
	cd, ok := cctx.Deadline()
	if !ok || time.Until(cd) > 200*time.Millisecond {
		t.Error("should clamp to the stricter parent deadline")
	}

	// Already-expired parent -> cancel-only (no zero-length WithTimeout); ctx done.
	exp, ecancel := context.WithTimeout(context.Background(), time.Nanosecond)
	ecancel()
	time.Sleep(time.Millisecond)
	dctx, dcancel := a.withModelRoundTimeout(exp)
	defer dcancel()
	select {
	case <-dctx.Done():
	default:
		t.Error("an already-expired parent should yield an already-done round ctx")
	}
}

func TestModelRoundTimeoutFollowsRunnerConfigForNewAndExistingAgents(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	runner.SetModelRoundTimeout(40 * time.Millisecond)

	agent := runner.newConfiguredAgent(
		context.Background(), runner.snapshotAgentDeps(), string(AgentTypeGeneral), 1, "", nil)
	agent.stateMu.RLock()
	got := agent.modelRoundTimeout
	agent.stateMu.RUnlock()
	if got != 40*time.Millisecond {
		t.Fatalf("new agent round timeout = %v, want 40ms", got)
	}

	runner.mu.Lock()
	runner.agents[agent.ID] = agent
	runner.mu.Unlock()
	runner.SetModelRoundTimeout(75 * time.Millisecond)
	agent.stateMu.RLock()
	got = agent.modelRoundTimeout
	agent.stateMu.RUnlock()
	if got != 75*time.Millisecond {
		t.Fatalf("existing agent round timeout = %v, want 75ms", got)
	}

	roundCtx, cancel := agent.withModelRoundTimeout(context.Background())
	defer cancel()
	deadline, ok := roundCtx.Deadline()
	if !ok {
		t.Fatal("configured agent round has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 150*time.Millisecond {
		t.Fatalf("configured round deadline remaining = %v, want approximately 75ms", remaining)
	}
}

func TestModelRoundTimeoutRuntimeUpdateIsRaceSafe(t *testing.T) {
	a := NewAgent(AgentTypeGeneral, nil, tools.NewRegistry(), t.TempDir(), 1, "", nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if offset%2 == 0 {
					a.SetModelRoundTimeout(time.Duration(n+1) * time.Millisecond)
					continue
				}
				ctx, cancel := a.withModelRoundTimeout(context.Background())
				cancel()
				_ = ctx
			}
		}(i)
	}
	wg.Wait()
}

// TestModelRoundTimeoutCauseIsTypedNonRetryable pins that when the round timeout
// fires, the error surfaced (via ContextErr) is the typed ErrModelRoundTimeout —
// which DecideStreamRetry treats as NON-retryable — not a raw retryable
// context.DeadlineExceeded that would be pointlessly retried into the same cap.
func TestModelRoundTimeoutCauseIsTypedNonRetryable(t *testing.T) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Millisecond, client.NewModelRoundTimeoutError(client.DefaultModelRoundTimeout))
	defer cancel()
	<-ctx.Done()
	err := client.ContextErr(ctx)
	if !errors.Is(err, client.ErrModelRoundTimeout) {
		t.Fatalf("fired round-timeout cause should be ErrModelRoundTimeout, got %v", err)
	}
}
