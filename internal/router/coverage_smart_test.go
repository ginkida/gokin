package router

import (
	"context"
	"testing"
	"time"

	"gokin/internal/config"
)

// --- Router setters that are 0% ---

func TestSetPlanChecker(t *testing.T) {
	r := &Router{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("SetPlanChecker(nil) panicked: %v", rec)
		}
	}()
	r.SetPlanChecker(nil)
}

func TestSetPlanChecker_WithChecker(t *testing.T) {
	r := &Router{}
	r.SetPlanChecker(&mockPlanChecker{active: true})
	r.historyMu.RLock()
	has := r.planChecker != nil
	r.historyMu.RUnlock()
	if !has {
		t.Fatal("planChecker should be set")
	}
}

func TestSetThinkingMode(t *testing.T) {
	r := &Router{}
	r.SetThinkingMode("on")
	if mode := r.getThinkingMode(); mode != config.ThinkingModeOn {
		t.Errorf("mode = %q, want %q", mode, config.ThinkingModeOn)
	}
}

func TestSetThinkingMode_Invalid(t *testing.T) {
	r := &Router{}
	r.SetThinkingMode("bogus")
	if mode := r.getThinkingMode(); mode != config.ThinkingModeAuto {
		t.Errorf("mode = %q, want %q (auto)", mode, config.ThinkingModeAuto)
	}
}

func TestCurrentModelCapability_None(t *testing.T) {
	r := &Router{}
	_, ok := r.CurrentModelCapability()
	if ok {
		t.Error("expected false when no capability set")
	}
}

func TestCurrentModelCapability_Set(t *testing.T) {
	r := &Router{}
	r.SetModelCapability(&ModelCapability{Tier: CapabilityStrong})
	cap, ok := r.CurrentModelCapability()
	if !ok {
		t.Fatal("expected true when capability is set")
	}
	if cap.Tier != CapabilityStrong {
		t.Errorf("Tier = %v, want CapabilityStrong", cap.Tier)
	}
}

func TestSetModelCapability(t *testing.T) {
	r := &Router{}
	mc := &ModelCapability{Tier: CapabilityWeak, ThinkingMultiplier: 0.5}
	r.SetModelCapability(mc)
	got, ok := r.CurrentModelCapability()
	if !ok {
		t.Fatal("expected capability to be set")
	}
	if got.ThinkingMultiplier != 0.5 {
		t.Errorf("ThinkingMultiplier = %v, want 0.5", got.ThinkingMultiplier)
	}
}

// --- SmartRouter 0% functions ---

func TestSmartRouter_SetExampleStore(t *testing.T) {
	sr := &SmartRouter{}
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("SetExampleStore(nil) panicked: %v", rec)
		}
	}()
	sr.SetExampleStore(nil)
}

func TestSmartRouter_SetExampleStore_WithStore(t *testing.T) {
	sr := &SmartRouter{}
	store := &mockExampleStore{}
	sr.SetExampleStore(store)
	sr.mu.RLock()
	got := sr.exampleStore
	sr.mu.RUnlock()
	if got == nil {
		t.Fatal("exampleStore should be set")
	}
}

func TestSmartRouter_Route(t *testing.T) {
	sr := &SmartRouter{
		Router: &Router{
			analyzer: NewTaskAnalyzer(6, 8),
		},
	}
	decision := sr.Route("hello")
	if decision == nil {
		t.Fatal("expected non-nil decision")
	}
}

func TestSmartRouter_RouteWithContext(t *testing.T) {
	sr := &SmartRouter{
		Router: &Router{
			analyzer: NewTaskAnalyzer(6, 8),
		},
	}
	decision := sr.RouteWithContext(context.Background(), "hello")
	if decision == nil {
		t.Fatal("expected non-nil decision")
	}
}

func TestSmartRouter_RouteWithLearning(t *testing.T) {
	sr := &SmartRouter{
		Router: &Router{
			analyzer: NewTaskAnalyzer(6, 8),
		},
	}
	decision := sr.RouteWithLearning("hello")
	if decision == nil {
		t.Fatal("expected non-nil decision")
	}
}

func TestSmartRouter_GetExamplesContext_NilStore(t *testing.T) {
	sr := &SmartRouter{}
	if got := sr.GetExamplesContext("question", "test"); got != "" {
		t.Errorf("expected empty string for nil store, got %q", got)
	}
}

func TestSmartRouter_GetExamplesContext_WithStore(t *testing.T) {
	sr := &SmartRouter{
		exampleStore: &mockExampleStore{contextResult: "some examples"},
	}
	got := sr.GetExamplesContext("question", "test")
	if got != "some examples" {
		t.Errorf("expected 'some examples', got %q", got)
	}
}

func TestSmartRouter_GetAdaptiveStats(t *testing.T) {
	sr := &SmartRouter{
		adaptiveEnabled: true,
		minDataPoints:   5,
	}
	stats := sr.GetAdaptiveStats()
	if !stats.Enabled {
		t.Error("expected Enabled = true")
	}
	if stats.MinDataPoints != 5 {
		t.Errorf("MinDataPoints = %d, want 5", stats.MinDataPoints)
	}
}

// --- applyCapabilityAdjustments (42.9% → higher) ---

func TestApplyCapabilityAdjustments_NilCapability(t *testing.T) {
	r := &Router{}
	analysis := &TaskComplexity{Score: 8, Strategy: StrategySubAgent}
	r.applyCapabilityAdjustments(analysis)
	if analysis.Score != 8 {
		t.Errorf("Score = %d, want 8 (no adjustment with nil capability)", analysis.Score)
	}
}

func TestApplyCapabilityAdjustments_StrongCapability(t *testing.T) {
	r := &Router{}
	r.SetModelCapability(&ModelCapability{Tier: CapabilityStrong})
	analysis := &TaskComplexity{Score: 8, Strategy: StrategySubAgent}
	r.applyCapabilityAdjustments(analysis)
	if analysis.Score < 8 {
		t.Errorf("Score = %d, should not be reduced for strong capability", analysis.Score)
	}
}

func TestApplyCapabilityAdjustments_WeakCapability(t *testing.T) {
	r := &Router{}
	r.SetModelCapability(&ModelCapability{Tier: CapabilityWeak})
	analysis := &TaskComplexity{Score: 8, Strategy: StrategySubAgent}
	r.applyCapabilityAdjustments(analysis)
	if analysis.Score > 8 {
		t.Errorf("Score = %d, should be reduced or same for weak capability", analysis.Score)
	}
}

// --- adjustStrategyFromHistory (63.6% → higher) ---

func TestAdjustStrategyFromHistory_NoHistory(t *testing.T) {
	r := &Router{
		routingHistory: make([]routingRecord, 0),
	}
	analysis := &TaskComplexity{Strategy: StrategyExecutor, Score: 5}
	r.adjustStrategyFromHistory(analysis)
	if analysis.Score != 5 {
		t.Errorf("Score = %d, want 5 (no history to adjust from)", analysis.Score)
	}
}

// --- mock types ---

type mockPlanChecker struct {
	active  bool
	enabled bool
}

func (m *mockPlanChecker) IsActive() bool  { return m.active }
func (m *mockPlanChecker) IsEnabled() bool { return m.enabled }

type mockExampleStore struct {
	contextResult string
	examples      []ExampleSummary
}

func (m *mockExampleStore) GetSimilarExamples(prompt string, limit int) []ExampleSummary {
	return m.examples
}

func (m *mockExampleStore) GetExamplesForContext(taskType, prompt string, limit int) string {
	return m.contextResult
}

func (m *mockExampleStore) LearnFromSuccess(taskType, prompt, agentType, output string, duration time.Duration, tokens int) error {
	return nil
}
