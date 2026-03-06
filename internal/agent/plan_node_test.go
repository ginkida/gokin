package agent

import (
	"testing"
)

// --- PlanNode tests ---

func TestPlanNodeIsTerminal(t *testing.T) {
	tests := []struct {
		status PlanNodeStatus
		want   bool
	}{
		{PlanNodePending, false},
		{PlanNodeExecuting, false},
		{PlanNodeSucceeded, true},
		{PlanNodeFailed, true},
		{PlanNodePruned, true},
	}
	for _, tt := range tests {
		n := &PlanNode{Status: tt.status}
		if got := n.IsTerminal(); got != tt.want {
			t.Errorf("PlanNode(%q).IsTerminal() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestPlanNodeIsLeaf(t *testing.T) {
	leaf := &PlanNode{}
	if !leaf.IsLeaf() {
		t.Error("node without children should be a leaf")
	}

	parent := &PlanNode{Children: []*PlanNode{{ID: "child"}}}
	if parent.IsLeaf() {
		t.Error("node with children should not be a leaf")
	}
}

func TestPlanNodeAverageReward(t *testing.T) {
	n := &PlanNode{}
	if n.AverageReward() != 0 {
		t.Error("zero visits should return 0")
	}

	n.VisitCount = 4
	n.TotalReward = 3.0
	if n.AverageReward() != 0.75 {
		t.Errorf("avg reward = %f, want 0.75", n.AverageReward())
	}
}

// --- PlanTree tests ---

func TestNewPlanTree(t *testing.T) {
	goal := &PlanGoal{Description: "test goal", MaxDepth: 5, MaxNodes: 100}
	tree := NewPlanTree(goal)

	if tree.Root == nil {
		t.Fatal("tree should have root")
	}
	if tree.TotalNodes != 1 {
		t.Errorf("TotalNodes = %d", tree.TotalNodes)
	}
	if tree.Goal.Description != "test goal" {
		t.Errorf("Goal = %q", tree.Goal.Description)
	}
	if tree.Root.Action == nil || tree.Root.Action.Type != ActionDecompose {
		t.Error("root action should be decompose")
	}
}

func TestPlanTreeAddNode(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})

	action := &PlannedAction{Type: ActionToolCall, ToolName: "read"}
	node := tree.AddNode(tree.Root.ID, action)
	if node == nil {
		t.Fatal("AddNode should return node")
	}
	if node.ParentID != tree.Root.ID {
		t.Errorf("ParentID = %q", node.ParentID)
	}
	if node.Depth != 1 {
		t.Errorf("Depth = %d, want 1", node.Depth)
	}
	if tree.TotalNodes != 2 {
		t.Errorf("TotalNodes = %d", tree.TotalNodes)
	}
	if tree.MaxDepth != 1 {
		t.Errorf("MaxDepth = %d", tree.MaxDepth)
	}

	// Action should have back-reference
	if action.NodeID != node.ID {
		t.Error("action should have NodeID set")
	}
}

func TestPlanTreeAddNodeInvalidParent(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	node := tree.AddNode("nonexistent", &PlannedAction{})
	if node != nil {
		t.Error("should return nil for invalid parent")
	}
}

func TestPlanTreeGetNode(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	node := tree.AddNode(tree.Root.ID, &PlannedAction{Type: ActionDelegate})

	got, ok := tree.GetNode(node.ID)
	if !ok || got == nil {
		t.Fatal("should find node")
	}
	if got.ID != node.ID {
		t.Errorf("ID = %q", got.ID)
	}

	_, ok = tree.GetNode("nonexistent")
	if ok {
		t.Error("nonexistent should not be found")
	}
}

func TestPlanTreeGetParent(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{})

	parent, ok := tree.GetParent(child.ID)
	if !ok || parent.ID != tree.Root.ID {
		t.Error("should find parent")
	}

	// Root has no parent
	_, ok = tree.GetParent(tree.Root.ID)
	if ok {
		t.Error("root should not have parent")
	}
}

func TestPlanTreePruneSubtree(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{})
	grandchild := tree.AddNode(child.ID, &PlannedAction{})

	tree.PruneSubtree(child.ID)

	if child.Status != PlanNodePruned {
		t.Errorf("child status = %v", child.Status)
	}
	if grandchild.Status != PlanNodePruned {
		t.Errorf("grandchild status = %v", grandchild.Status)
	}
}

func TestPlanTreeGetPendingNodes(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	tree.AddNode(tree.Root.ID, &PlannedAction{})
	tree.AddNode(tree.Root.ID, &PlannedAction{})

	pending := tree.GetPendingNodes()
	// Root + 2 children = 3 pending
	if len(pending) != 3 {
		t.Errorf("pending = %d, want 3", len(pending))
	}
}

func TestPlanTreeGetReadyNodes(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child1 := tree.AddNode(tree.Root.ID, &PlannedAction{})
	child2 := tree.AddNode(tree.Root.ID, &PlannedAction{})

	// Root is pending, no prerequisites — root is ready
	// Children require parent to succeed
	ready := tree.GetReadyNodes()
	// Only root should be ready (children's parent is pending)
	found := false
	for _, n := range ready {
		if n.ID == tree.Root.ID {
			found = true
		}
		if n.ID == child1.ID || n.ID == child2.ID {
			t.Error("children should not be ready while parent is pending")
		}
	}
	if !found {
		t.Error("root should be ready")
	}
}

func TestPlanTreeGetReadyNodesAfterParentSuccess(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{})

	tree.Root.Status = PlanNodeSucceeded

	ready := tree.GetReadyNodes()
	foundChild := false
	for _, n := range ready {
		if n.ID == child.ID {
			foundChild = true
		}
	}
	if !foundChild {
		t.Error("child should be ready after parent succeeds")
	}
}

func TestPlanTreeGetBlockedNodes(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{})

	// Root is pending → child is blocked
	blocked := tree.GetBlockedNodes()
	foundChild := false
	for _, b := range blocked {
		if b.Node.ID == child.ID {
			foundChild = true
			if b.Reason == "" {
				t.Error("blocked reason should not be empty")
			}
		}
	}
	if !foundChild {
		t.Error("child should be blocked")
	}
}

func TestPlanTreeGetBlockedNodesFailedParent(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{Prompt: "do thing"})
	tree.Root.Status = PlanNodeFailed
	tree.Root.Action = &PlannedAction{Prompt: "root task"}

	blocked := tree.GetBlockedNodes()
	for _, b := range blocked {
		if b.Node.ID == child.ID {
			if b.Reason == "" {
				t.Error("should have reason for failed parent")
			}
		}
	}
}

func TestPlanTreeReconstructPath(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{})
	grandchild := tree.AddNode(child.ID, &PlannedAction{})

	path := tree.ReconstructPath(grandchild.ID)
	if len(path) != 3 {
		t.Fatalf("path length = %d, want 3", len(path))
	}
	if path[0].ID != tree.Root.ID {
		t.Error("path should start at root")
	}
	if path[2].ID != grandchild.ID {
		t.Error("path should end at grandchild")
	}
}

func TestPlanTreeGetSucceededPath(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{})
	tree.AddNode(tree.Root.ID, &PlannedAction{}) // sibling

	tree.Root.Status = PlanNodeSucceeded
	child.Status = PlanNodeSucceeded

	path := tree.GetSucceededPath()
	if len(path) != 2 {
		t.Errorf("succeeded path = %d, want 2", len(path))
	}
}

func TestPlanTreeEnsureIndex(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	child := tree.AddNode(tree.Root.ID, &PlannedAction{})

	// Simulate deserialization — nil out index
	tree.nodeIndex = nil
	tree.EnsureIndex()

	got, ok := tree.GetNode(child.ID)
	if !ok || got == nil {
		t.Error("should find node after index rebuild")
	}
}

func TestPlanTreePrerequisites(t *testing.T) {
	tree := NewPlanTree(&PlanGoal{Description: "test"})
	tree.Root.Status = PlanNodeSucceeded

	child1 := tree.AddNode(tree.Root.ID, &PlannedAction{})
	child2 := tree.AddNode(tree.Root.ID, &PlannedAction{
		Prerequisites: []string{child1.ID},
	})

	ready := tree.GetReadyNodes()
	for _, n := range ready {
		if n.ID == child2.ID {
			t.Error("child2 should not be ready — prerequisite child1 is pending")
		}
	}

	// Mark child1 as succeeded
	child1.Status = PlanNodeSucceeded
	ready = tree.GetReadyNodes()
	foundChild2 := false
	for _, n := range ready {
		if n.ID == child2.ID {
			foundChild2 = true
		}
	}
	if !foundChild2 {
		t.Error("child2 should be ready after prerequisite succeeded")
	}
}
