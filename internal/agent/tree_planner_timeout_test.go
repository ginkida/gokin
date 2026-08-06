package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/config"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestTreePlannerPlanningTimeoutUsesModelRoundBudget(t *testing.T) {
	planner := NewTreePlanner(nil, nil, nil, nil)
	if got := planner.PlanningTimeout(); got != config.DefaultModelRoundTimeout {
		t.Fatalf("default planning timeout = %v, want %v", got, config.DefaultModelRoundTimeout)
	}

	planner.SetPlanningTimeout(40 * time.Minute)
	for _, thoroughness := range []tools.Thoroughness{
		tools.ThoroughnessQuick,
		tools.ThoroughnessNormal,
		tools.ThoroughnessThorough,
	} {
		planner.ApplyThoroughness(thoroughness)
		if got := planner.PlanningTimeout(); got != 40*time.Minute {
			t.Fatalf("%s thoroughness reset planning timeout to %v", thoroughness, got)
		}
	}

	clone := cloneTreePlanner(planner)
	if got := clone.PlanningTimeout(); got != 40*time.Minute {
		t.Fatalf("cloned planning timeout = %v, want 40m", got)
	}
}

func TestTreePlannerPreservesTypedTimeoutWhenStreamClosesSilently(t *testing.T) {
	mc := testkit.NewMockClient().EnqueueScript(testkit.ResponseScript{
		DelayBeforeFirstChunk: time.Hour,
	})
	planner := NewTreePlanner(nil, nil, nil, mc)
	planner.SetPlanningTimeout(25 * time.Millisecond)

	started := time.Now()
	_, err := planner.generateActionsWithLLM(context.Background(), "inspect timeout handling", &PlanGoal{MaxDepth: 5})
	if !errors.Is(err, client.ErrModelRoundTimeout) {
		t.Fatalf("generateActionsWithLLM() error = %v, want typed model-round timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("25ms planning timeout took %v", elapsed)
	}
	telemetry := client.DetectFailureTelemetry(err)
	if telemetry.Timeout != 25*time.Millisecond {
		t.Fatalf("planning timeout telemetry = %#v, want 25ms", telemetry)
	}
}

type partialPlanThenSilentCloseClient struct {
	*testkit.MockClient
}

func (c *partialPlanThenSilentCloseClient) SendMessage(ctx context.Context, _ string) (*client.StreamingResponse, error) {
	chunks := make(chan client.ResponseChunk)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(chunks)
		select {
		case chunks <- client.ResponseChunk{Text: "STEP: explore | inspect the timeout path\n"}:
		case <-ctx.Done():
			return
		}
		<-ctx.Done()
	}()
	return &client.StreamingResponse{Chunks: chunks, Done: done}, nil
}

func TestTreePlannerKeepsParsedPartialPlanOnTimeout(t *testing.T) {
	c := &partialPlanThenSilentCloseClient{MockClient: testkit.NewMockClient()}
	planner := NewTreePlanner(nil, nil, nil, c)
	planner.SetPlanningTimeout(25 * time.Millisecond)

	actions, err := planner.generateActionsWithLLM(context.Background(), "inspect timeout handling", &PlanGoal{MaxDepth: 5})
	if err != nil {
		t.Fatalf("partial plan should remain usable: %v", err)
	}
	if len(actions) != 2 || actions[0].AgentType != AgentTypeExplore || actions[0].Prompt != "inspect the timeout path" || actions[1].Type != ActionVerify {
		t.Fatalf("partial actions = %#v", actions)
	}
}

func TestTreePlannerPreservesParentCancellation(t *testing.T) {
	mc := testkit.NewMockClient().EnqueueScript(testkit.ResponseScript{
		DelayBeforeFirstChunk: time.Hour,
	})
	planner := NewTreePlanner(nil, nil, nil, mc)
	planner.SetPlanningTimeout(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := planner.generateActionsWithLLM(ctx, "cancel this plan", &PlanGoal{MaxDepth: 5})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("parent cancellation = %v, want context.Canceled", err)
	}
	if errors.Is(err, client.ErrModelRoundTimeout) {
		t.Fatalf("parent cancellation was misclassified as model timeout: %v", err)
	}
}
