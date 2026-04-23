package router

import (
	"testing"

	"gokin/internal/tools"
)

// Regression: before v0.70.1, Memory toolset was only added on
// StrategySubAgent AND dropped by filterToolSetsByCapability for
// Medium-tier models (Kimi). The combined effect: running gokin with
// Kimi, asking it "запомни это" / "remember X", and getting an honest
// but wrong "I can't access persistent memory" because the tool list
// the model actually saw for that request didn't include memory.
//
// Contract: Memory tools must be present for ALL strategies under
// Medium-tier (Kimi/MiniMax/GLM-4.x) AND Weak-tier (ollama). Tested
// against every Strategy × TaskType combination that selectToolSets
// handles.

func containsToolSet(sets []tools.ToolSet, target tools.ToolSet) bool {
	for _, s := range sets {
		if s == target {
			return true
		}
	}
	return false
}

func TestRouter_MemoryPresentForKimiDirectStrategy(t *testing.T) {
	r := &Router{
		modelCapability: &ModelCapability{Tier: CapabilityMedium},
	}
	sets := r.selectToolSets(&TaskComplexity{
		Strategy: StrategyDirect,
		Type:     TaskTypeQuestion,
		Score:    1,
	})
	if !containsToolSet(sets, tools.ToolSetMemory) {
		t.Errorf("Direct strategy on Medium-tier must include Memory, got: %v", sets)
	}
}

func TestRouter_MemoryPresentForKimiSingleTool(t *testing.T) {
	r := &Router{
		modelCapability: &ModelCapability{Tier: CapabilityMedium},
	}
	sets := r.selectToolSets(&TaskComplexity{
		Strategy: StrategySingleTool,
		Score:    2,
	})
	if !containsToolSet(sets, tools.ToolSetMemory) {
		t.Errorf("SingleTool on Medium-tier must include Memory, got: %v", sets)
	}
}

func TestRouter_MemoryPresentForKimiExecutorAllTaskTypes(t *testing.T) {
	types := []TaskType{
		TaskTypeQuestion,
		TaskTypeExploration,
		TaskTypeRefactoring,
		TaskTypeComplex,
		TaskType("arbitrary-unknown-default"),
	}
	for _, taskType := range types {
		t.Run(string(taskType), func(t *testing.T) {
			r := &Router{
				modelCapability: &ModelCapability{Tier: CapabilityMedium},
			}
			sets := r.selectToolSets(&TaskComplexity{
				Strategy: StrategyExecutor,
				Type:     taskType,
				Score:    4,
			})
			if !containsToolSet(sets, tools.ToolSetMemory) {
				t.Errorf("Executor/%s on Medium-tier must include Memory, got: %v", taskType, sets)
			}
		})
	}
}

func TestRouter_MemoryPresentForKimiSubAgent(t *testing.T) {
	r := &Router{
		modelCapability: &ModelCapability{Tier: CapabilityMedium},
	}
	sets := r.selectToolSets(&TaskComplexity{
		Strategy: StrategySubAgent,
		Score:    8,
	})
	if !containsToolSet(sets, tools.ToolSetMemory) {
		t.Errorf("SubAgent on Medium-tier must include Memory, got: %v", sets)
	}
}

func TestRouter_MemoryPresentForWeakTier(t *testing.T) {
	// Weak cloud models go through the router too (e.g. hypothetical
	// remote llama). Memory must survive capability filtering.
	r := &Router{
		modelCapability: &ModelCapability{Tier: CapabilityWeak},
	}
	sets := r.selectToolSets(&TaskComplexity{
		Strategy: StrategyExecutor,
		Type:     TaskTypeQuestion,
		Score:    2,
	})
	if !containsToolSet(sets, tools.ToolSetMemory) {
		t.Errorf("Weak-tier must keep Memory, got: %v", sets)
	}
}

func TestRouter_MemoryPresentForStrongTier(t *testing.T) {
	// Strong-tier has no capability filtering — still make sure Memory
	// lands via the base append (the actual bug wasn't tier-specific).
	r := &Router{
		modelCapability: &ModelCapability{Tier: CapabilityStrong},
	}
	sets := r.selectToolSets(&TaskComplexity{
		Strategy: StrategyDirect,
		Score:    1,
	})
	if !containsToolSet(sets, tools.ToolSetMemory) {
		t.Errorf("Strong-tier Direct must include Memory, got: %v", sets)
	}
}

func TestRouter_MemoryPresentWhenNilCapability(t *testing.T) {
	// nil capability = no filter applied; base set still contains Memory.
	r := &Router{modelCapability: nil}
	sets := r.selectToolSets(&TaskComplexity{
		Strategy: StrategyExecutor,
		Type:     TaskTypeExploration,
		Score:    3,
	})
	if !containsToolSet(sets, tools.ToolSetMemory) {
		t.Errorf("nil capability must include Memory, got: %v", sets)
	}
}
