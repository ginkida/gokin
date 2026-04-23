package router

import (
	"testing"

	"gokin/internal/tools"
)

func TestInferModelCapability(t *testing.T) {
	tests := []struct {
		provider string
		model    string
		wantTier CapabilityTier
	}{
		// Strong tier
		{"glm", "glm-5-plus", CapabilityStrong},

		// Medium tier
		{"kimi", "kimi-k2.5", CapabilityMedium},
		{"minimax", "MiniMax-M2.5", CapabilityMedium},
		{"glm", "glm-4", CapabilityMedium},

		// Weak tier
		{"ollama", "llama3.2", CapabilityWeak},
		{"unknown", "some-model", CapabilityWeak},
		{"", "", CapabilityWeak},
	}

	for _, tt := range tests {
		t.Run(tt.provider+"/"+tt.model, func(t *testing.T) {
			cap := InferModelCapability(tt.provider, tt.model)
			if cap.Tier != tt.wantTier {
				t.Errorf("InferModelCapability(%q, %q).Tier = %v, want %v",
					tt.provider, tt.model, cap.Tier, tt.wantTier)
			}
			if cap.Provider != tt.provider {
				t.Errorf("Provider = %q, want %q", cap.Provider, tt.provider)
			}
			if cap.ModelName != tt.model {
				t.Errorf("ModelName = %q, want %q", cap.ModelName, tt.model)
			}
		})
	}
}

func TestCapabilityTierAdjustments(t *testing.T) {
	weak := InferModelCapability("ollama", "llama3.2")
	if weak.DecomposeAdjust != -2 {
		t.Errorf("weak DecomposeAdjust = %d, want -2", weak.DecomposeAdjust)
	}
	if weak.ThinkingMultiplier != 1.5 {
		t.Errorf("weak ThinkingMultiplier = %f, want 1.5", weak.ThinkingMultiplier)
	}
	if !weak.SelfReviewBoost {
		t.Error("weak SelfReviewBoost should be true")
	}

	medium := InferModelCapability("kimi", "kimi-k2.5")
	if medium.DecomposeAdjust != -1 {
		t.Errorf("medium DecomposeAdjust = %d, want -1", medium.DecomposeAdjust)
	}
	if medium.ThinkingMultiplier != 1.2 {
		t.Errorf("medium ThinkingMultiplier = %f, want 1.2", medium.ThinkingMultiplier)
	}

	strong := InferModelCapability("glm", "glm-5.1")
	if strong.DecomposeAdjust != 0 {
		t.Errorf("strong DecomposeAdjust = %d, want 0", strong.DecomposeAdjust)
	}
	if strong.ThinkingMultiplier != 1.0 {
		t.Errorf("strong ThinkingMultiplier = %f, want 1.0", strong.ThinkingMultiplier)
	}
}

func TestCapabilityTierString(t *testing.T) {
	tests := []struct {
		tier CapabilityTier
		want string
	}{
		{CapabilityWeak, "weak"},
		{CapabilityMedium, "medium"},
		{CapabilityStrong, "strong"},
		{CapabilityTier(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("CapabilityTier(%d).String() = %q, want %q", tt.tier, got, tt.want)
		}
	}
}

func TestExecutionStrategyHelpers(t *testing.T) {
	// IsValid
	if !StrategyDirect.IsValid() {
		t.Error("StrategyDirect should be valid")
	}
	if ExecutionStrategy("invalid").IsValid() {
		t.Error("invalid strategy should not be valid")
	}

	// RequiresTools
	if StrategyDirect.RequiresTools() {
		t.Error("StrategyDirect should not require tools")
	}
	if !StrategyExecutor.RequiresTools() {
		t.Error("StrategyExecutor should require tools")
	}
	if !StrategySingleTool.RequiresTools() {
		t.Error("StrategySingleTool should require tools")
	}

	// GetDescription
	if desc := StrategyDirect.GetDescription(); desc != "Direct AI response" {
		t.Errorf("StrategyDirect.GetDescription() = %q", desc)
	}
}

func TestTaskTypeHelpers(t *testing.T) {
	if desc := TaskTypeQuestion.GetDescription(); desc != "Simple question" {
		t.Errorf("TaskTypeQuestion.GetDescription() = %q", desc)
	}
	if desc := TaskTypeComplex.GetDescription(); desc != "Complex task" {
		t.Errorf("TaskTypeComplex.GetDescription() = %q", desc)
	}
	if desc := TaskType("unknown").GetDescription(); desc != "Unknown type" {
		t.Errorf("unknown type GetDescription() = %q", desc)
	}
}

func TestFilterToolSetsByCapability(t *testing.T) {
	allSets := []tools.ToolSet{
		tools.ToolSetCore,
		tools.ToolSetGit,
		tools.ToolSetFileOps,
		tools.ToolSetAdvanced,
		tools.ToolSetWeb,
		tools.ToolSetPlanning,
		tools.ToolSetMemory,
	}

	// Strong: all sets pass through
	r := &Router{modelCapability: &ModelCapability{Tier: CapabilityStrong}}
	filtered := r.filterToolSetsByCapability(allSets)
	if len(filtered) != len(allSets) {
		t.Errorf("strong tier: got %d sets, want %d", len(filtered), len(allSets))
	}

	// Medium: Core, Git, FileOps, Advanced + Memory (always-on for continuity)
	r.modelCapability.Tier = CapabilityMedium
	filtered = r.filterToolSetsByCapability(allSets)
	for _, s := range filtered {
		if s != tools.ToolSetCore && s != tools.ToolSetGit && s != tools.ToolSetFileOps &&
			s != tools.ToolSetAdvanced && s != tools.ToolSetMemory {
			t.Errorf("medium tier: unexpected tool set %v", s)
		}
	}
	if len(filtered) != 5 {
		t.Errorf("medium tier: got %d sets, want 5", len(filtered))
	}

	// Weak: Core, Git, FileOps + Memory
	r.modelCapability.Tier = CapabilityWeak
	filtered = r.filterToolSetsByCapability(allSets)
	for _, s := range filtered {
		if s != tools.ToolSetCore && s != tools.ToolSetGit &&
			s != tools.ToolSetFileOps && s != tools.ToolSetMemory {
			t.Errorf("weak tier: unexpected tool set %v", s)
		}
	}
	if len(filtered) != 4 {
		t.Errorf("weak tier: got %d sets, want 4", len(filtered))
	}

	// Nil capability: all sets pass through
	r.modelCapability = nil
	filtered = r.filterToolSetsByCapability(allSets)
	if len(filtered) != len(allSets) {
		t.Errorf("nil capability: got %d sets, want %d", len(filtered), len(allSets))
	}
}

func TestSelectThinkingBudget(t *testing.T) {
	r := &Router{}

	tests := []struct {
		name     string
		analysis *TaskComplexity
		wantZero bool
	}{
		{
			name:     "direct strategy has no budget",
			analysis: &TaskComplexity{Strategy: StrategyDirect, Score: 1},
			wantZero: true,
		},
		{
			name:     "simple single tool has no budget",
			analysis: &TaskComplexity{Strategy: StrategySingleTool, Score: 1},
			wantZero: true,
		},
		{
			name:     "complex single tool gets budget",
			analysis: &TaskComplexity{Strategy: StrategySingleTool, Score: 3},
			wantZero: false,
		},
		{
			name:     "executor gets budget",
			analysis: &TaskComplexity{Strategy: StrategyExecutor, Score: 4},
			wantZero: false,
		},
		{
			name:     "sub-agent gets largest budget",
			analysis: &TaskComplexity{Strategy: StrategySubAgent, Score: 8},
			wantZero: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget := r.selectThinkingBudget(tt.analysis)
			if tt.wantZero && budget != 0 {
				t.Errorf("budget = %d, want 0", budget)
			}
			if !tt.wantZero && budget == 0 {
				t.Error("budget = 0, want > 0")
			}
		})
	}

	// Test thinking multiplier for weak model
	r.modelCapability = &ModelCapability{Tier: CapabilityWeak, ThinkingMultiplier: 1.5}
	budget := r.selectThinkingBudget(&TaskComplexity{Strategy: StrategySubAgent, Score: 8})
	if budget != 6144 { // 4096 * 1.5
		t.Errorf("weak model budget = %d, want 6144", budget)
	}
}

func TestSelectCostAwareModel(t *testing.T) {
	r := &Router{
		costAware: true,
		fastModel: "gemini-flash",
	}

	// Direct uses fast model
	model := r.selectCostAwareModel(&TaskComplexity{Strategy: StrategyDirect})
	if model != "gemini-flash" {
		t.Errorf("direct strategy model = %q, want %q", model, "gemini-flash")
	}

	// Simple single tool uses fast model
	model = r.selectCostAwareModel(&TaskComplexity{Strategy: StrategySingleTool, Score: 1})
	if model != "gemini-flash" {
		t.Errorf("simple single tool model = %q, want %q", model, "gemini-flash")
	}

	// Complex single tool uses default
	model = r.selectCostAwareModel(&TaskComplexity{Strategy: StrategySingleTool, Score: 5})
	if model != "" {
		t.Errorf("complex single tool model = %q, want empty", model)
	}

	// Executor uses default
	model = r.selectCostAwareModel(&TaskComplexity{Strategy: StrategyExecutor, Score: 5})
	if model != "" {
		t.Errorf("executor model = %q, want empty", model)
	}
}

func TestRouterErrorRateTracking(t *testing.T) {
	r := &Router{}

	// No operations: error rate is 0
	if rate := r.GetErrorRate(); rate != 0 {
		t.Errorf("initial error rate = %f, want 0", rate)
	}

	// Track some operations
	r.TrackOperation("read", true)
	r.TrackOperation("write", true)
	r.TrackOperation("bash", false)
	r.TrackOperation("bash", false)

	rate := r.GetErrorRate()
	expected := 2.0 / 4.0 // 2 errors out of 4 ops
	if rate != expected {
		t.Errorf("error rate = %f, want %f", rate, expected)
	}

	// Conversation mode should track tool usage
	r.TrackOperation("grep", true)
	if mode := r.GetConversationMode(); mode != "exploring" {
		t.Errorf("mode after grep = %q, want %q", mode, "exploring")
	}

	r.TrackOperation("edit", true)
	if mode := r.GetConversationMode(); mode != "implementing" {
		t.Errorf("mode after edit = %q, want %q", mode, "implementing")
	}
}

func TestRouterHistoryBounded(t *testing.T) {
	r := &Router{routingHistory: make([]routingRecord, 0, 100)}

	// Fill routing history beyond capacity
	for i := 0; i < 150; i++ {
		analysis := &TaskComplexity{Type: TaskTypeQuestion, Strategy: StrategyDirect}
		r.RecordRoutingOutcome("test", analysis, true)
	}

	r.historyMu.RLock()
	histLen := len(r.routingHistory)
	r.historyMu.RUnlock()

	if histLen > 100 {
		t.Errorf("history length = %d, should be bounded at 100", histLen)
	}
}

func TestToolHint(t *testing.T) {
	r := &Router{}

	// Exploration should get a hint
	hint := r.toolHint(&TaskComplexity{Type: TaskTypeExploration})
	if hint == "" {
		t.Error("exploration should get a tool hint")
	}

	// Refactoring should get a hint
	hint = r.toolHint(&TaskComplexity{Type: TaskTypeRefactoring})
	if hint == "" {
		t.Error("refactoring should get a tool hint")
	}

	// Question should not get a hint
	hint = r.toolHint(&TaskComplexity{Type: TaskTypeQuestion})
	if hint != "" {
		t.Errorf("question should not get a hint, got %q", hint)
	}
}
