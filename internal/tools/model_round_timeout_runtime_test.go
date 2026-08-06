package tools

import (
	"context"
	"sync"
	"testing"
	"time"

	"gokin/internal/config"
)

func TestExecutorModelRoundTimeoutRuntimeUpdateIsAppliedAndRaceSafe(t *testing.T) {
	executor := NewExecutor(NewRegistry(), nil, time.Second)
	executor.SetModelRoundTimeout(60 * time.Millisecond)

	ctx, cancel := executor.withModelRoundTimeout(context.Background())
	deadline, ok := ctx.Deadline()
	cancel()
	if !ok {
		t.Fatal("configured executor round has no deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > 150*time.Millisecond {
		t.Fatalf("configured round deadline remaining = %v, want approximately 60ms", remaining)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				if offset%2 == 0 {
					executor.SetModelRoundTimeout(time.Duration(n+1) * time.Millisecond)
					continue
				}
				roundCtx, roundCancel := executor.withModelRoundTimeout(context.Background())
				roundCancel()
				_ = roundCtx
			}
		}(i)
	}
	wg.Wait()
}

func TestNormalTaskOuterTimeoutFollowsRaisedModelRoundTimeout(t *testing.T) {
	executor := NewExecutor(NewRegistry(), nil, time.Second)
	executor.SetModelRoundTimeout(30 * time.Minute)

	got, bounded := executor.resolveToolExecutionTimeout(
		30*time.Second,
		0,
		false,
		"task",
		map[string]any{"subagent_type": "general"},
	)
	want := 30*time.Minute + config.DefaultAgentTimeoutHeadroom + toolTimeoutCompletionGrace
	if !bounded || got != want {
		t.Fatalf("normal task outer timeout = %v bounded=%v, want %v/true", got, bounded, want)
	}

	got, bounded = executor.resolveToolExecutionTimeout(
		30*time.Second,
		0,
		false,
		"task",
		map[string]any{"subagent_type": "general", "thoroughness": "quick"},
	)
	want = 2*time.Minute + toolTimeoutCompletionGrace
	if !bounded || got != want {
		t.Fatalf("quick task outer timeout = %v bounded=%v, want %v/true", got, bounded, want)
	}

	got, bounded = executor.resolveToolExecutionTimeout(
		30*time.Second,
		0,
		false,
		"task",
		map[string]any{"subagent_type": "general", "thoroughness": "thorough"},
	)
	want = 30*time.Minute + config.DefaultAgentTimeoutHeadroom + toolTimeoutCompletionGrace
	if !bounded || got != want {
		t.Fatalf("raised thorough task outer timeout = %v bounded=%v, want %v/true", got, bounded, want)
	}
}

func TestImplicitCoordinateOuterTimeoutFollowsRaisedModelRoundTimeout(t *testing.T) {
	executor := NewExecutor(NewRegistry(), nil, time.Second)
	executor.SetModelRoundTimeout(40 * time.Minute)

	got, bounded := executor.resolveToolExecutionTimeout(
		30*time.Second, 0, false, "coordinate", nil,
	)
	want := 40*time.Minute + config.DefaultAgentTimeoutHeadroom +
		coordinateCleanupTimeout + toolTimeoutCompletionGrace
	if !bounded || got != want {
		t.Fatalf("implicit coordinate outer timeout = %v bounded=%v, want %v/true", got, bounded, want)
	}

	chain := map[string]any{"tasks": []any{
		coordinateTask("deploy", "build"),
		coordinateTask("build"),
	}}
	got, bounded = executor.resolveToolExecutionTimeout(
		30*time.Second, 0, false, "coordinate", chain,
	)
	want = 2*(40*time.Minute+config.DefaultAgentTimeoutHeadroom) +
		coordinateCleanupTimeout + toolTimeoutCompletionGrace
	if !bounded || got != want {
		t.Fatalf("dependent coordinate outer timeout = %v bounded=%v, want %v/true", got, bounded, want)
	}

	got, bounded = executor.resolveToolExecutionTimeout(
		30*time.Second, 0, false, "coordinate",
		map[string]any{"timeout_minutes": 7},
	)
	want = 7*time.Minute + coordinateCleanupTimeout + toolTimeoutCompletionGrace
	if !bounded || got != want {
		t.Fatalf("explicit coordinate outer timeout = %v bounded=%v, want %v/true", got, bounded, want)
	}
}
