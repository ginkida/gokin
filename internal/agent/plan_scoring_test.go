package agent

import (
	"testing"
)

func newTestTreePlanner() *TreePlanner {
	return NewTreePlanner(DefaultTreePlannerConfig(), nil, nil, nil)
}

// --- estimateActionProbability ---

func TestEstimateActionProbability(t *testing.T) {
	tests := []struct {
		name   string
		action *PlannedAction
		want   float64
	}{
		{"nil action", nil, 0.5},
		{"tool read", &PlannedAction{Type: ActionToolCall, ToolName: "read"}, 0.9},
		{"tool bash", &PlannedAction{Type: ActionToolCall, ToolName: "bash"}, 0.7},
		{"tool write", &PlannedAction{Type: ActionToolCall, ToolName: "write"}, 0.8},
		{"tool web_search", &PlannedAction{Type: ActionToolCall, ToolName: "web_search"}, 0.6},
		{"tool unknown", &PlannedAction{Type: ActionToolCall, ToolName: "custom"}, 0.7},
		{"delegate explore", &PlannedAction{Type: ActionDelegate, AgentType: AgentTypeExplore}, 0.85},
		{"delegate general", &PlannedAction{Type: ActionDelegate, AgentType: AgentTypeGeneral}, 0.75},
		{"delegate plan", &PlannedAction{Type: ActionDelegate, AgentType: AgentTypePlan}, 0.9},
		{"decompose", &PlannedAction{Type: ActionDecompose}, 0.8},
		{"verify", &PlannedAction{Type: ActionVerify}, 0.7},
		{"unknown type", &PlannedAction{Type: "custom"}, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tp := newTestTreePlanner()
			got := tp.EstimateSuccessProbability(tt.action)
			if got != tt.want {
				t.Errorf("prob = %f, want %f", got, tt.want)
			}
		})
	}
}

// --- EstimateCost ---

func TestEstimateCost(t *testing.T) {
	tp := newTestTreePlanner()

	tests := []struct {
		name   string
		action *PlannedAction
		want   float64
	}{
		{"nil", nil, 0.5},
		{"read", &PlannedAction{Type: ActionToolCall, ToolName: "read"}, 0.1},
		{"bash", &PlannedAction{Type: ActionToolCall, ToolName: "bash"}, 0.4},
		{"web_search", &PlannedAction{Type: ActionToolCall, ToolName: "web_search"}, 0.6},
		{"delegate explore", &PlannedAction{Type: ActionDelegate, AgentType: AgentTypeExplore}, 0.4},
		{"delegate general", &PlannedAction{Type: ActionDelegate, AgentType: AgentTypeGeneral}, 0.6},
		{"decompose", &PlannedAction{Type: ActionDecompose}, 0.3},
		{"verify", &PlannedAction{Type: ActionVerify}, 0.2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tp.EstimateCost(tt.action)
			if got != tt.want {
				t.Errorf("cost = %f, want %f", got, tt.want)
			}
		})
	}
}

// --- EstimateProgress ---

func TestEstimateProgress(t *testing.T) {
	tp := newTestTreePlanner()
	goal := &PlanGoal{Description: "test"}

	// nil action/goal
	if tp.EstimateProgress(nil, goal, 0.5) != 0.5 {
		t.Error("nil action should return current progress")
	}
	if tp.EstimateProgress(&PlannedAction{}, nil, 0.5) != 0.5 {
		t.Error("nil goal should return current progress")
	}

	// Decompose: +0.1
	p := tp.EstimateProgress(&PlannedAction{Type: ActionDecompose}, goal, 0.0)
	if p != 0.1 {
		t.Errorf("decompose progress = %f, want 0.1", p)
	}

	// Write tool: +0.25
	p = tp.EstimateProgress(&PlannedAction{Type: ActionToolCall, ToolName: "write"}, goal, 0.5)
	if p != 0.75 {
		t.Errorf("write progress = %f, want 0.75", p)
	}

	// Verify: completes remaining
	p = tp.EstimateProgress(&PlannedAction{Type: ActionVerify}, goal, 0.8)
	if p != 1.0 {
		t.Errorf("verify progress = %f, want 1.0", p)
	}

	// Cap at 1.0
	p = tp.EstimateProgress(&PlannedAction{Type: ActionDelegate, AgentType: AgentTypeGeneral}, goal, 0.9)
	if p > 1.0 {
		t.Errorf("progress = %f, should cap at 1.0", p)
	}
}

// --- ScoreNode ---

func TestScoreNode(t *testing.T) {
	tp := newTestTreePlanner()

	// Nil node
	if tp.ScoreNode(nil) != 0 {
		t.Error("nil node should score 0")
	}

	// High success, low cost, high progress
	node := &PlanNode{
		SuccessProb:  0.9,
		CostEstimate: 0.1,
		GoalProgress: 0.8,
		Depth:        0,
	}
	score := tp.ScoreNode(node)
	if score <= 0 || score > 1 {
		t.Errorf("score = %f, should be in (0, 1]", score)
	}

	// Deeper node should have lower score (depth penalty)
	deepNode := &PlanNode{
		SuccessProb:  0.9,
		CostEstimate: 0.1,
		GoalProgress: 0.8,
		Depth:        5,
	}
	deepScore := tp.ScoreNode(deepNode)
	if deepScore >= score {
		t.Errorf("deep score %f should be < shallow score %f", deepScore, score)
	}
}

func TestScoreNodeZeroWeights(t *testing.T) {
	config := DefaultTreePlannerConfig()
	config.SuccessProbWeight = 0
	config.CostWeight = 0
	config.ProgressWeight = 0
	tp := NewTreePlanner(config, nil, nil, nil)

	node := &PlanNode{SuccessProb: 0.5, CostEstimate: 0.5, GoalProgress: 0.5}
	score := tp.ScoreNode(node)
	// With all-zero weights, totalWeight defaults to 1.0
	if score < 0 {
		t.Errorf("score = %f, should be >= 0", score)
	}
}

// --- RankNodes ---

func TestRankNodes(t *testing.T) {
	tp := newTestTreePlanner()

	nodes := []*PlanNode{
		{ID: "low", Score: 0.2},
		{ID: "high", Score: 0.9},
		{ID: "mid", Score: 0.5},
	}

	ranked := tp.RankNodes(nodes)
	if ranked[0].ID != "high" {
		t.Errorf("first = %q, want high", ranked[0].ID)
	}
	if ranked[1].ID != "mid" {
		t.Errorf("second = %q, want mid", ranked[1].ID)
	}
	if ranked[2].ID != "low" {
		t.Errorf("third = %q, want low", ranked[2].ID)
	}

	// Original should not be modified
	if nodes[0].ID != "low" {
		t.Error("RankNodes should not modify original slice")
	}
}

func TestRankNodesEmpty(t *testing.T) {
	tp := newTestTreePlanner()
	ranked := tp.RankNodes(nil)
	if len(ranked) != 0 {
		t.Error("ranking nil should return empty")
	}
}

// --- FilterByMinProbability ---

func TestFilterByMinProbability(t *testing.T) {
	config := DefaultTreePlannerConfig()
	config.MinSuccessProb = 0.5
	tp := NewTreePlanner(config, nil, nil, nil)

	nodes := []*PlanNode{
		{ID: "low", SuccessProb: 0.1},
		{ID: "high", SuccessProb: 0.9},
		{ID: "edge", SuccessProb: 0.5},
	}

	filtered := tp.FilterByMinProbability(nodes)
	if len(filtered) != 2 {
		t.Fatalf("filtered = %d, want 2", len(filtered))
	}
	for _, n := range filtered {
		if n.ID == "low" {
			t.Error("should filter out low probability node")
		}
	}
}

// --- buildStrategyKey ---

func TestBuildStrategyKey(t *testing.T) {
	tests := []struct {
		action *PlannedAction
		want   string
	}{
		{nil, "unknown"},
		{&PlannedAction{Type: ActionToolCall, ToolName: "read"}, "tool:read"},
		{&PlannedAction{Type: ActionToolCall}, "tool:unknown"},
		{&PlannedAction{Type: ActionDelegate, AgentType: AgentTypeExplore}, "delegate:explore"},
		{&PlannedAction{Type: ActionDecompose}, "decompose"},
		{&PlannedAction{Type: ActionVerify}, "verify"},
		{&PlannedAction{Type: "custom"}, "custom"},
	}
	for _, tt := range tests {
		got := buildStrategyKey(tt.action)
		if got != tt.want {
			t.Errorf("buildStrategyKey(%v) = %q, want %q", tt.action, got, tt.want)
		}
	}
}

// --- DefaultTreePlannerConfig ---

func TestDefaultTreePlannerConfig(t *testing.T) {
	cfg := DefaultTreePlannerConfig()

	if cfg.Algorithm != SearchAlgorithmBeam {
		t.Errorf("Algorithm = %v", cfg.Algorithm)
	}
	if cfg.BeamWidth != 5 {
		t.Errorf("BeamWidth = %d", cfg.BeamWidth)
	}

	// Weights should sum to 1.0
	sum := cfg.SuccessProbWeight + cfg.CostWeight + cfg.ProgressWeight
	if sum < 0.99 || sum > 1.01 {
		t.Errorf("weights sum = %f, should be 1.0", sum)
	}
}

// --- RecalculateScores ---

func TestRecalculateScores(t *testing.T) {
	tp := newTestTreePlanner()
	tree := NewPlanTree(&PlanGoal{Description: "test"})

	tree.AddNode(tree.Root.ID, &PlannedAction{Type: ActionToolCall, ToolName: "read"})
	tree.AddNode(tree.Root.ID, &PlannedAction{Type: ActionDelegate, AgentType: AgentTypeGeneral})

	tp.RecalculateScores(tree)

	// All nodes should have scores set
	for _, child := range tree.Root.Children {
		if child.SuccessProb == 0 {
			t.Errorf("child %q SuccessProb not set", child.ID)
		}
		if child.CostEstimate == 0 {
			t.Errorf("child %q CostEstimate not set", child.ID)
		}
	}

	// Nil/empty should not panic
	tp.RecalculateScores(nil)
	tp.RecalculateScores(&PlanTree{})
}
