package router

import (
	"errors"
	"strings"
	"testing"

	"gokin/internal/config"
)

// --- HandlerType.String (0% → 100%) ---

func TestHandlerType_String(t *testing.T) {
	tests := []struct {
		h    HandlerType
		want string
	}{
		{HandlerDirect, "direct"},
		{HandlerExecutor, "executor"},
		{HandlerSubAgent, "sub_agent"},
		{HandlerCoordinated, "coordinated"},
	}
	for _, tc := range tests {
		if got := tc.h.String(); got != tc.want {
			t.Errorf("HandlerType(%q).String() = %q, want %q", tc.h, got, tc.want)
		}
	}
}

// --- AgentSideEffectError.Error (0% → 100%) ---

func TestAgentSideEffectError_Error(t *testing.T) {
	// Nil receiver
	var nilErr *AgentSideEffectError
	if got := nilErr.Error(); got == "" {
		t.Fatal("nil receiver Error() should not be empty")
	}

	// Basic: no cause, no side effects
	e := &AgentSideEffectError{AgentID: "a1"}
	if got := e.Error(); !strings.Contains(got, "sub-agent stopped") {
		t.Fatalf("basic Error() = %q, want contains 'sub-agent stopped'", got)
	}

	// With cause
	e2 := &AgentSideEffectError{AgentID: "a2", Cause: errors.New("boom")}
	if got := e2.Error(); !strings.Contains(got, "boom") {
		t.Fatalf("Error with cause = %q, want contains 'boom'", got)
	}

	// SideEffectsUnknown
	e3 := &AgentSideEffectError{SideEffectsUnknown: true}
	if got := e3.Error(); !strings.Contains(got, "unavailable") {
		t.Fatalf("SideEffectsUnknown Error = %q, want 'unavailable'", got)
	}

	// StatefulToolAttempts > 0
	e4 := &AgentSideEffectError{StatefulToolAttempts: 3}
	if got := e4.Error(); !strings.Contains(got, "3") {
		t.Fatalf("StatefulToolAttempts Error = %q, want '3'", got)
	}

	// PartialOutput
	e5 := &AgentSideEffectError{PartialOutput: "some partial text"}
	if got := e5.Error(); !strings.Contains(got, "partial output") {
		t.Fatalf("PartialOutput Error = %q, want 'partial output'", got)
	}
}

// --- AgentSideEffectError.Unwrap (66.7% → 100%) ---

func TestAgentSideEffectError_Unwrap_Nil(t *testing.T) {
	var nilErr *AgentSideEffectError
	if got := nilErr.Unwrap(); got != nil {
		t.Fatalf("nil Unwrap = %v, want nil", got)
	}
}

func TestAgentSideEffectError_Unwrap(t *testing.T) {
	cause := errors.New("root cause")
	e := &AgentSideEffectError{Cause: cause}
	if got := e.Unwrap(); got != cause {
		t.Fatalf("Unwrap = %v, want %v", got, cause)
	}
}

// --- AgentSideEffectError.AutomaticRetrySafe ---

func TestAgentSideEffectError_AutomaticRetrySafe(t *testing.T) {
	e := &AgentSideEffectError{}
	if e.AutomaticRetrySafe() {
		t.Fatal("should always be unsafe to retry")
	}
}

func TestIsAutomaticRetryUnsafe(t *testing.T) {
	// nil error
	if IsAutomaticRetryUnsafe(nil) {
		t.Fatal("nil should be safe")
	}
	// Ordinary error
	if IsAutomaticRetryUnsafe(errors.New("plain")) {
		t.Fatal("plain error should be safe")
	}
	// Wrapped side-effect error
	sideEffect := newAgentSideEffectError("a1", errors.New("boom"), nil, true)
	if !IsAutomaticRetryUnsafe(sideEffect) {
		t.Fatal("AgentSideEffectError should be unsafe")
	}
}

// --- DefaultSmartRouterConfig (0% → 100%) ---

func TestDefaultSmartRouterConfig(t *testing.T) {
	cfg := DefaultSmartRouterConfig()
	if cfg == nil {
		t.Fatal("should not be nil")
	}
	if !cfg.Enabled {
		t.Error("should be enabled")
	}
	if cfg.DecomposeThreshold != 4 {
		t.Errorf("DecomposeThreshold = %d, want 4", cfg.DecomposeThreshold)
	}
	if cfg.ParallelThreshold != 7 {
		t.Errorf("ParallelThreshold = %d, want 7", cfg.ParallelThreshold)
	}
	if !cfg.AdaptiveEnabled {
		t.Error("AdaptiveEnabled should be true")
	}
	if cfg.MinDataPoints != 5 {
		t.Errorf("MinDataPoints = %d, want 5", cfg.MinDataPoints)
	}
	if cfg.ExampleLimit != 3 {
		t.Errorf("ExampleLimit = %d, want 3", cfg.ExampleLimit)
	}
}

// --- selectSubAgentType (28.6% → 100%) ---

func TestSelectSubAgentType(t *testing.T) {
	r := &Router{}
	tests := []struct {
		taskType TaskType
		want     string
	}{
		{TaskTypeExploration, "explore"},
		{TaskTypeBackground, "bash"},
		{TaskTypeRefactoring, "general"},
		{TaskTypeComplex, "general"},
		{TaskTypeMultiTool, "general"},
		{TaskTypeQuestion, "general"}, // default
		{TaskTypeSingleTool, "general"},
	}
	for _, tc := range tests {
		if got := r.selectSubAgentType(tc.taskType); got != tc.want {
			t.Errorf("selectSubAgentType(%q) = %q, want %q", tc.taskType, got, tc.want)
		}
	}
}

// --- sessionDepthMultiplier (42.9% → 100%) ---

func TestSessionDepthMultiplier(t *testing.T) {
	// Zero turns/tools → 1.0
	r := &Router{}
	if got := r.sessionDepthMultiplier(); got != 1.0 {
		t.Errorf("zero state multiplier = %f, want 1.0", got)
	}

	// With some turns
	r2 := &Router{}
	r2.depthMu.Lock()
	r2.depthTurns = 10
	r2.depthTools = 0
	r2.depthMu.Unlock()
	got := r2.sessionDepthMultiplier()
	if got != 2.0 { // 1 + 10/10 + 0/20 = 2.0
		t.Errorf("10 turns multiplier = %f, want 2.0", got)
	}

	// Clamp at 2.0
	r3 := &Router{}
	r3.depthMu.Lock()
	r3.depthTurns = 100
	r3.depthTools = 100
	r3.depthMu.Unlock()
	if got := r3.sessionDepthMultiplier(); got != 2.0 {
		t.Errorf("clamped multiplier = %f, want 2.0", got)
	}
}

// --- adjustStrategyFromHistory (18.2% → higher) ---

func TestAdjustStrategyFromHistory_NoData(t *testing.T) {
	// With < 3 records, getStrategySuccessRate returns 0.5 → no adjustment
	r := &Router{}
	analysis := &TaskComplexity{Strategy: StrategyDirect, Score: 2}
	r.adjustStrategyFromHistory(analysis)
	if analysis.Strategy != StrategyDirect {
		t.Fatalf("with no data, strategy should stay Direct, got %v", analysis.Strategy)
	}
}

func TestAdjustStrategyFromHistory_PoorRate(t *testing.T) {
	// Direct has poor rate (1/5 = 0.2 < 0.3), Executor has good rate (4/5 = 0.8)
	r := &Router{
		routingHistory: []routingRecord{
			{strategy: StrategyDirect, success: true},
			{strategy: StrategyDirect, success: false},
			{strategy: StrategyDirect, success: false},
			{strategy: StrategyDirect, success: false},
			{strategy: StrategyDirect, success: false},
			{strategy: StrategyExecutor, success: true},
			{strategy: StrategyExecutor, success: true},
			{strategy: StrategyExecutor, success: true},
			{strategy: StrategyExecutor, success: true},
			{strategy: StrategyExecutor, success: false},
		},
	}
	analysis := &TaskComplexity{Strategy: StrategyDirect, Score: 2}
	r.adjustStrategyFromHistory(analysis)
	if analysis.Strategy != StrategyExecutor {
		t.Fatalf("poor Direct rate should upgrade to Executor, got %v", analysis.Strategy)
	}
}

func TestAdjustStrategyFromHistory_GoodRate(t *testing.T) {
	// Direct has good rate → no change
	r := &Router{
		routingHistory: []routingRecord{
			{strategy: StrategyDirect, success: true},
			{strategy: StrategyDirect, success: true},
			{strategy: StrategyDirect, success: true},
		},
	}
	analysis := &TaskComplexity{Strategy: StrategyDirect, Score: 2}
	r.adjustStrategyFromHistory(analysis)
	if analysis.Strategy != StrategyDirect {
		t.Fatalf("good rate should keep Direct, got %v", analysis.Strategy)
	}
}

// --- selectThinkingBudget additional branches (92.6% → higher) ---

func TestSelectThinkingBudget_SingleTool(t *testing.T) {
	// Score ≤ 2 → 0 budget
	low := &TaskComplexity{Strategy: StrategySingleTool, Score: 2}
	r := &Router{thinkingMode: config.ThinkingModeAuto}
	if b := r.selectThinkingBudget(low); b != 0 {
		t.Errorf("SingleTool score=2 = %d, want 0", b)
	}

	// Score > 2 → 2048
	high := &TaskComplexity{Strategy: StrategySingleTool, Score: 4}
	if b := r.selectThinkingBudget(high); b != 2048 {
		t.Errorf("SingleTool score=4 = %d, want 2048", b)
	}
}

func TestSelectThinkingBudget_SubAgent(t *testing.T) {
	tc := &TaskComplexity{Strategy: StrategySubAgent, Score: 8}
	r := &Router{thinkingMode: config.ThinkingModeAuto}
	if b := r.selectThinkingBudget(tc); b != 8192 {
		t.Errorf("SubAgent = %d, want 8192", b)
	}
}

func TestSelectThinkingBudget_ExecutorLow(t *testing.T) {
	tc := &TaskComplexity{Strategy: StrategyExecutor, Score: 3}
	r := &Router{thinkingMode: config.ThinkingModeAuto}
	if b := r.selectThinkingBudget(tc); b != 4096 {
		t.Errorf("Executor score=3 = %d, want 4096", b)
	}
}

// --- isStrongKimiCodingModel (88.9% → 100%) ---

func TestIsStrongKimiCodingModel(t *testing.T) {
	strong := []string{
		"kimi-for-coding",
		"kimi-for-coding-v2",
		"kimi-k2.6",
		"kimi-k2.7",
		"kimi-k2.10",
	}
	for _, m := range strong {
		if !isStrongKimiCodingModel(m) {
			t.Errorf("isStrongKimiCodingModel(%q) = false, want true", m)
		}
	}
	weak := []string{
		"kimi-vision",
		"gpt-4",
		"claude-3",
		"kimi-k2.5",    // minor < 6
		"kimi-k2",      // no minor
		"kimi-k2-0711", // dash not dot
		"moonshot-v1-128k",
	}
	for _, m := range weak {
		if isStrongKimiCodingModel(m) {
			t.Errorf("isStrongKimiCodingModel(%q) = true, want false", m)
		}
	}
}
