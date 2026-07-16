package agent

import (
	"context"
	"slices"
	"testing"
)

func TestBuildTreeUsesStructuralRootAndOrderedSteps(t *testing.T) {
	planner := NewTreePlanner(DefaultTreePlannerConfig(), nil, nil, nil)
	tree, err := planner.BuildTree(context.Background(), "implement a reliable cache", &PlanGoal{
		Description: "implement a reliable cache",
	})
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	if tree.Root.Status != PlanNodeSucceeded {
		t.Fatalf("root status = %s, want structural succeeded root", tree.Root.Status)
	}
	if len(tree.Root.Children) < 3 {
		t.Fatalf("generated children = %d, want multi-step implementation plan", len(tree.Root.Children))
	}

	for i, child := range tree.Root.Children {
		if i == 0 {
			if len(child.Action.Prerequisites) != 0 {
				t.Fatalf("first step prerequisites = %v, want none", child.Action.Prerequisites)
			}
			continue
		}
		previousID := tree.Root.Children[i-1].ID
		if !slices.Contains(child.Action.Prerequisites, previousID) {
			t.Fatalf("step %d prerequisites = %v, want previous step %s", i+1, child.Action.Prerequisites, previousID)
		}
	}

	ready, err := planner.GetReadyActions(tree)
	if err != nil {
		t.Fatalf("GetReadyActions: %v", err)
	}
	if len(ready) != 1 || ready[0] != tree.Root.Children[0].Action {
		t.Fatalf("initial ready actions = %#v, want only first generated step", ready)
	}
	if ready[0].Type == ActionDecompose {
		t.Fatal("synthetic root escaped as a second decompose action")
	}

	if err := planner.RecordResult(tree, ready[0].NodeID, &AgentResult{Status: AgentStatusCompleted}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	next, err := planner.GetReadyActions(tree)
	if err != nil {
		t.Fatalf("GetReadyActions after first result: %v", err)
	}
	if len(next) != 1 || next[0] != tree.Root.Children[1].Action {
		t.Fatalf("next ready actions = %#v, want only second step", next)
	}
}

func TestReplanReplacesFailedActionAndRewiresOrderedSuccessor(t *testing.T) {
	planner := NewTreePlanner(DefaultTreePlannerConfig(), nil, nil, nil)
	tree, err := planner.BuildTree(context.Background(), "implement a reliable cache", &PlanGoal{
		Description: "implement a reliable cache",
	})
	if err != nil {
		t.Fatalf("BuildTree: %v", err)
	}
	if len(tree.Root.Children) < 2 {
		t.Fatalf("need an ordered successor, got %d children", len(tree.Root.Children))
	}

	ready, err := planner.GetReadyActions(tree)
	if err != nil || len(ready) != 1 {
		t.Fatalf("initial ready = %#v, err=%v", ready, err)
	}
	var failed, successor *PlanNode
	for _, child := range tree.Root.Children {
		if child.ID == ready[0].NodeID {
			failed = child
			break
		}
	}
	if failed == nil {
		t.Fatalf("ready node %s not found under root", ready[0].NodeID)
	}
	for _, child := range tree.Root.Children {
		if child.Action != nil && slices.Contains(child.Action.Prerequisites, failed.ID) {
			successor = child
			break
		}
	}
	if successor == nil {
		t.Fatalf("no ordered successor depends on failed node %s", failed.ID)
	}
	originalIDs := make(map[string]bool, len(tree.Root.Children))
	for _, child := range tree.Root.Children {
		originalIDs[child.ID] = true
	}
	if err := planner.RecordResult(tree, failed.ID, &AgentResult{
		Status: AgentStatusFailed, Error: "transient implementation failure", Completed: true,
	}); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}
	if err := planner.Replan(context.Background(), tree, &ReplanContext{
		FailedNode: failed, Error: failed.Error, AttemptNumber: 0,
	}); err != nil {
		t.Fatalf("Replan: %v", err)
	}
	if failed.Status != PlanNodePruned {
		t.Fatalf("failed node status = %s, want pruned", failed.Status)
	}

	var replacement *PlanNode
	for _, child := range tree.Root.Children {
		if !originalIDs[child.ID] && child.Status == PlanNodePending {
			replacement = child
			break
		}
	}
	if replacement == nil {
		t.Fatal("replan reported success without attaching a recovery replacement")
	}
	if !slices.Contains(successor.Action.Prerequisites, replacement.ID) ||
		slices.Contains(successor.Action.Prerequisites, failed.ID) {
		t.Fatalf("successor prerequisites = %v, want replacement %s and no failed %s",
			successor.Action.Prerequisites, replacement.ID, failed.ID)
	}

	ready, err = planner.GetReadyActions(tree)
	if err != nil || len(ready) != 1 || ready[0].NodeID != replacement.ID {
		t.Fatalf("ready after replan = %#v, err=%v, want replacement %s", ready, err, replacement.ID)
	}
	if err := planner.RecordResult(tree, replacement.ID, &AgentResult{
		Status: AgentStatusCompleted, Completed: true,
	}); err != nil {
		t.Fatalf("RecordResult replacement: %v", err)
	}
	next, err := planner.GetReadyActions(tree)
	if err != nil || len(next) != 1 || next[0].NodeID != successor.ID {
		t.Fatalf("next after recovery = %#v, err=%v, want successor %s", next, err, successor.ID)
	}
}
