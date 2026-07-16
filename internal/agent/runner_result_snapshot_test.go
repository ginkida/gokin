package agent

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"gokin/internal/tools"
)

func TestRunnerResultSnapshot_DoesNotAliasMutableFields(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	runner.results["agent"] = &AgentResult{
		AgentID:              "agent",
		Status:               AgentStatusCompleted,
		Completed:            true,
		StatefulToolAttempts: 2,
		TouchedPaths:         []string{"original.go"},
		Metadata: map[string]any{
			"nested": map[string]any{"value": "original"},
			"files":  []string{"original.go"},
			"items":  []any{map[string]any{"value": "original"}},
		},
	}

	snapshot, ok := runner.GetResult("agent")
	if !ok {
		t.Fatal("GetResult did not find result")
	}
	snapshot.Status = AgentStatusFailed
	snapshot.StatefulToolAttempts = 0
	snapshot.TouchedPaths[0] = "mutated.go"
	snapshot.Metadata["nested"].(map[string]any)["value"] = "mutated"
	snapshot.Metadata["files"].([]string)[0] = "mutated.go"
	snapshot.Metadata["items"].([]any)[0].(map[string]any)["value"] = "mutated"

	fresh, _ := runner.GetResult("agent")
	if fresh.Status != AgentStatusCompleted || fresh.StatefulToolAttempts != 2 || fresh.TouchedPaths[0] != "original.go" {
		t.Fatalf("snapshot mutation reached stored scalars/slice: %+v", fresh)
	}
	if got := fresh.Metadata["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("nested metadata mutation reached stored result: %v", got)
	}
	if got := fresh.Metadata["files"].([]string)[0]; got != "original.go" {
		t.Fatalf("metadata slice mutation reached stored result: %v", got)
	}
	if got := fresh.Metadata["items"].([]any)[0].(map[string]any)["value"]; got != "original" {
		t.Fatalf("nested metadata slice mutation reached stored result: %v", got)
	}
}

func TestCompletedResultSnapshot_ConcurrentMutationIsRaceFree(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	stored := &AgentResult{
		AgentID:      "agent",
		Status:       AgentStatusCompleted,
		Completed:    true,
		TouchedPaths: []string{"file-0"},
		Metadata:     map[string]any{"counter": 0, "nested": map[string]any{"counter": 0}},
	}
	runner.results["agent"] = stored

	const iterations = 1000
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			runner.mu.Lock()
			stored.Error = fmt.Sprintf("error-%d", i)
			stored.TouchedPaths[0] = fmt.Sprintf("file-%d", i)
			stored.Metadata["counter"] = i
			stored.Metadata["nested"].(map[string]any)["counter"] = i
			runner.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			snapshot, ok := runner.completedResultLocked("agent")
			if !ok {
				t.Error("completed result disappeared")
				return
			}
			_ = snapshot.Error
			_ = snapshot.TouchedPaths[0]
			_ = snapshot.Metadata["counter"]
			_ = snapshot.Metadata["nested"].(map[string]any)["counter"]
		}
	}()
	wg.Wait()
}

func TestRunnerResultCallbacksReceiveOwnedSnapshots(t *testing.T) {
	runner := NewRunner(context.Background(), nil, tools.NewRegistry(), t.TempDir())
	result := &AgentResult{
		AgentID: "agent", Status: AgentStatusCompleted, Completed: true,
		TouchedPaths: []string{"original.go"},
		Metadata:     map[string]any{"value": "original"},
	}

	mutate := func(_ string, callbackResult *AgentResult) {
		callbackResult.Status = AgentStatusFailed
		callbackResult.TouchedPaths[0] = "mutated.go"
		callbackResult.Metadata["value"] = "mutated"
	}
	invokeAgentComplete(mutate, result.AgentID, result)
	runner.SetOnAgentUsage(mutate)
	runner.reportAgentUsage(result.AgentID, result)

	if result.Status != AgentStatusCompleted || result.TouchedPaths[0] != "original.go" || result.Metadata["value"] != "original" {
		t.Fatalf("callback mutation reached runner-owned result: %+v", result)
	}
}
