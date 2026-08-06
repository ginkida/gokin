package app

import (
	"testing"
	"time"

	"gokin/internal/config"
	"gokin/internal/plan"
	"gokin/internal/tools"
)

func TestImplicitPlanStepTimeoutFollowsModelRoundBudget(t *testing.T) {
	cfg := config.DefaultConfig()
	executor := tools.NewExecutor(tools.NewRegistry(), nil, time.Second)
	executor.SetModelRoundTimeout(40 * time.Minute)
	application := &App{config: cfg, executor: executor}

	if got, want := application.getStepTimeout(&plan.Step{}),
		40*time.Minute+config.DefaultAgentTimeoutHeadroom; got != want {
		t.Fatalf("implicit step timeout = %v, want %v", got, want)
	}

	cfg.Plan.DefaultStepTimeout = 7 * time.Minute
	if got := application.getStepTimeout(&plan.Step{}); got != 7*time.Minute {
		t.Fatalf("explicit plan default timeout = %v, want 7m", got)
	}
	if got := application.getStepTimeout(&plan.Step{Timeout: 3 * time.Minute}); got != 3*time.Minute {
		t.Fatalf("explicit step timeout = %v, want 3m", got)
	}
}

func TestPlanStuckWatchdogStaysOutsideModelRoundDeadline(t *testing.T) {
	executor := tools.NewExecutor(tools.NewRegistry(), nil, time.Second)
	application := &App{config: config.DefaultConfig(), executor: executor}

	executor.SetModelRoundTimeout(40 * time.Minute)
	if got, want := application.planStepStuckBudget(), 41*time.Minute; got != want {
		t.Fatalf("raised plan stuck budget = %v, want %v", got, want)
	}
	executor.SetModelRoundTimeout(time.Minute)
	if got := application.planStepStuckBudget(); got != stepStuckTimeout {
		t.Fatalf("small-round plan stuck budget = %v, want floor %v", got, stepStuckTimeout)
	}
}
