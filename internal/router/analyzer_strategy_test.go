package router

import (
	"testing"
)

// TestDetermineStrategy covers every TaskType × score branch in
// determineStrategy — the function that decides whether a task runs Direct,
// via the Executor, or via a SubAgent. Before this test it was at 19% coverage,
// meaning any refactor of the routing decision was blind. The table exercises
// every branch of the switch + every threshold comparison.
func TestDetermineStrategy(t *testing.T) {
	// Realistic thresholds matching production wiring (builder.go): decompose=6,
	// parallel=8. NewTaskAnalyzer stores the literal ints — 0,0 would route
	// every Complex task to SubAgent (score >= 0), masking the threshold logic.
	ta := NewTaskAnalyzer(6, 8)

	cases := []struct {
		name     string
		taskType TaskType
		score    int
		want     ExecutionStrategy
	}{
		// TaskTypeQuestion: score ≤2 → Direct, >2 → Executor.
		{"question simple direct", TaskTypeQuestion, 1, StrategyDirect},
		{"question boundary direct", TaskTypeQuestion, 2, StrategyDirect},
		{"question complex executor", TaskTypeQuestion, 3, StrategyExecutor},
		{"question high executor", TaskTypeQuestion, 8, StrategyExecutor},

		// TaskTypeSingleTool: always Executor regardless of score.
		{"single_tool low", TaskTypeSingleTool, 1, StrategyExecutor},
		{"single_tool high", TaskTypeSingleTool, 10, StrategyExecutor},

		// TaskTypeMultiTool: score ≤4 → Executor, >4 → SubAgent.
		{"multi_tool low executor", TaskTypeMultiTool, 3, StrategyExecutor},
		{"multi_tool boundary executor", TaskTypeMultiTool, 4, StrategyExecutor},
		{"multi_tool high subagent", TaskTypeMultiTool, 5, StrategySubAgent},
		{"multi_tool max subagent", TaskTypeMultiTool, 10, StrategySubAgent},

		// TaskTypeExploration: score ≤5 → Executor, >5 → SubAgent.
		{"exploration low executor", TaskTypeExploration, 4, StrategyExecutor},
		{"exploration boundary executor", TaskTypeExploration, 5, StrategyExecutor},
		{"exploration high subagent", TaskTypeExploration, 6, StrategySubAgent},

		// TaskTypeRefactoring: score ≤5 → Executor, >5 → SubAgent.
		{"refactoring low executor", TaskTypeRefactoring, 5, StrategyExecutor},
		{"refactoring high subagent", TaskTypeRefactoring, 7, StrategySubAgent},

		// TaskTypeBackground: always SubAgent.
		{"background always subagent", TaskTypeBackground, 1, StrategySubAgent},
		{"background high subagent", TaskTypeBackground, 10, StrategySubAgent},

		// TaskTypeComplex: depends on decomposeThreshold (6) and parallelThreshold (8).
		{"complex low executor", TaskTypeComplex, 3, StrategyExecutor},
		{"complex below decompose executor", TaskTypeComplex, 5, StrategyExecutor},
		{"complex at decompose subagent", TaskTypeComplex, 6, StrategySubAgent},
		{"complex below parallel subagent", TaskTypeComplex, 7, StrategySubAgent},
		{"complex at parallel subagent", TaskTypeComplex, 8, StrategySubAgent},
		{"complex max subagent", TaskTypeComplex, 10, StrategySubAgent},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ta.determineStrategy(tc.taskType, tc.score)
			if got != tc.want {
				t.Errorf("determineStrategy(%s, score=%d) = %s, want %s",
					tc.taskType, tc.score, got, tc.want)
			}
		})
	}
}

// TestDetermineStrategy_DefaultFallback: the trailing return after the switch
// is StrategyExecutor — reached only for an unrecognized task type. We can't
// pass an unknown type through the public API, but we can pin that the default
// branch returns Executor so the function never returns empty string.
func TestDetermineStrategy_DefaultFallback(t *testing.T) {
	ta := NewTaskAnalyzer(0, 0)
	got := ta.determineStrategy(TaskType("unknown_type"), 5)
	if got != StrategyExecutor {
		t.Errorf("unknown task type should fall back to Executor, got %s", got)
	}
}

// TestDetermineStrategy_CustomThresholds: decomposeThreshold and
// parallelThreshold are injected via the constructor (the two ints). A custom
// analyzer with lower thresholds must route to SubAgent at lower scores.
func TestDetermineStrategy_CustomThresholds(t *testing.T) {
	// decompose=3, parallel=5 → Complex routes to SubAgent earlier.
	ta := NewTaskAnalyzer(3, 5)

	cases := []struct {
		score int
		want  ExecutionStrategy
	}{
		{2, StrategyExecutor},  // below custom decompose=3
		{3, StrategySubAgent},  // at decompose=3 → sub-agent (not parallel)
		{4, StrategySubAgent},  // between decompose and parallel
		{5, StrategySubAgent},  // at parallel=5
		{10, StrategySubAgent}, // max
	}
	for _, tc := range cases {
		got := ta.determineStrategy(TaskTypeComplex, tc.score)
		if got != tc.want {
			t.Errorf("custom thresholds: Complex score=%d = %s, want %s",
				tc.score, got, tc.want)
		}
	}
}

// TestGenerateReasoning_AllTypes: generateReasoning must produce a non-empty
// string for every task type, and always include the score and strategy suffix.
// Previously at 54.5% — several type branches were never exercised.
func TestGenerateReasoning_AllTypes(t *testing.T) {
	ta := NewTaskAnalyzer(0, 0)

	types := []TaskType{
		TaskTypeQuestion,
		TaskTypeSingleTool,
		TaskTypeMultiTool,
		TaskTypeExploration,
		TaskTypeRefactoring,
		TaskTypeBackground,
		TaskTypeComplex,
	}
	for _, tt := range types {
		t.Run(string(tt), func(t *testing.T) {
			got := ta.generateReasoning(tt, 7, StrategySubAgent)
			if got == "" {
				t.Errorf("generateReasoning(%s) returned empty", tt)
			}
			// Must include the complexity score and the strategy name.
			if !contains(got, "7/10") {
				t.Errorf("reasoning missing score: %q", got)
			}
			if !contains(got, string(StrategySubAgent)) {
				t.Errorf("reasoning missing strategy: %q", got)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
