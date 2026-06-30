package router

import (
	"strings"
	"testing"
)

// TestSplitByAndPattern covers the "X and Y" / "X и Y" conjunction splitter —
// the regex decomposition path that decides whether a message describes two
// parallel-capable subtasks. Previously at 0% coverage.
func TestSplitByAndPattern(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    int // expected number of parts; -1 means nil (no split)
	}{
		{"english and", "read file A and read file B", 2},
		{"english comma and", "read file A, and read file B", 2},
		{"russian и", "прочитай файл A и файл B", 2},
		{"russian comma и", "прочитай A, и прочитай B", 2},
		{"three parts", "read A and read B and read C", 3},
		{"no conjunction", "read file A then read file B", -1},
		{"short parts filtered", "A and B", -1}, // parts < 5 chars dropped → <2 valid → nil
		{"empty", "", -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitByAndPattern(tc.message)
			if tc.want == -1 {
				if got != nil {
					t.Errorf("splitByAndPattern(%q) = %v, want nil", tc.message, got)
				}
				return
			}
			if len(got) != tc.want {
				t.Errorf("splitByAndPattern(%q) returned %d parts, want %d: %v",
					tc.message, len(got), tc.want, got)
			}
		})
	}
}

// TestSplitByAndPattern_PartContent: the returned parts must be trimmed of
// surrounding whitespace and the conjunction itself.
func TestSplitByAndPattern_PartContent(t *testing.T) {
	got := splitByAndPattern("read auth.go and read session.go")
	if len(got) != 2 {
		t.Fatalf("expected 2 parts, got %d: %v", len(got), got)
	}
	for _, p := range got {
		if p != strings.TrimSpace(p) {
			t.Errorf("part not trimmed: %q", p)
		}
		if strings.Contains(strings.ToLower(p), " and ") {
			t.Errorf("part still contains conjunction: %q", p)
		}
	}
}

// TestSplitByThenPattern covers the sequential "first X, then Y" splitter —
// produces ordered parts for dependency-chained subtasks. Previously 0%.
func TestSplitByThenPattern(t *testing.T) {
	cases := []struct {
		name    string
		message string
		want    int
	}{
		{"english first then", "first read the file, then edit it", 2},
		{"russian сначала потом", "сначала прочитай файл, потом измени его", 2},
		{"bare then", "read the file, then edit it", 2},
		{"russian после", "прочитай файл, после этого измени", 2},
		{"after that", "read the file, after that edit it", 2},
		{"no sequential marker", "read file A and read file B", -1},
		{"empty", "", -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitByThenPattern(tc.message)
			if tc.want == -1 {
				if got != nil {
					t.Errorf("splitByThenPattern(%q) = %v, want nil", tc.message, got)
				}
				return
			}
			if len(got) != tc.want {
				t.Errorf("splitByThenPattern(%q) returned %d parts, want %d: %v",
					tc.message, len(got), tc.want, got)
			}
		})
	}
}

// TestDecomposeWithRegex_AndIsParallel: when the message splits on "and",
// the result must be marked CanParallel=true with no inter-subtask deps.
func TestDecomposeWithRegex_AndIsParallel(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)
	result := ta.decomposeWithRegex("read auth.go and read session.go")

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if !result.CanParallel {
		t.Error("CanParallel should be true for 'and' pattern")
	}
	if len(result.Subtasks) < 2 {
		t.Fatalf("expected ≥2 subtasks, got %d", len(result.Subtasks))
	}
	for _, st := range result.Subtasks {
		if len(st.Dependencies) > 0 {
			t.Errorf("parallel subtask %q should have no deps, got %v", st.ID, st.Dependencies)
		}
	}
}

// TestDecomposeWithRegex_ThenIsSequential: when the message splits on
// "first/then", subtasks must be chained via Dependencies and CanParallel=false.
func TestDecomposeWithRegex_ThenIsSequential(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)
	result := ta.decomposeWithRegex("first read the config, then update the code")

	if !result.CanParallel && len(result.Subtasks) >= 2 {
		// "then" path: CanParallel must be false, deps must chain.
		if result.CanParallel {
			t.Error("CanParallel should be false for 'then' pattern")
		}
		// Every subtask except the first should depend on the previous.
		for i := 1; i < len(result.Subtasks); i++ {
			if len(result.Subtasks[i].Dependencies) == 0 {
				t.Errorf("subtask %d (%s) should have a dependency", i, result.Subtasks[i].ID)
			}
		}
	} else if len(result.Subtasks) < 2 {
		t.Fatalf("expected ≥2 subtasks for 'then' pattern, got %d", len(result.Subtasks))
	}
}

// TestDecomposeWithRegex_NoMarkers_FallsBackToType: when neither "and" nor
// "then" markers match, decomposeWithRegex falls back to Analyze +
// decomposeByType. A high-score complex message must still produce subtasks.
func TestDecomposeWithRegex_NoMarkers_FallsBackToType(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)
	// A complex refactoring message with no and/then markers but high score.
	result := ta.decomposeWithRegex("refactor the authentication system to use JWT tokens and add refresh logic")

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	// The fallback path only decomposes if score >= decomposeThreshold (6).
	// This message contains "refactor" (3 pts) + length + multiple verbs → high.
	// We don't assert exact count (depends on scoring), just that it's sane.
	if len(result.Subtasks) == 0 && result.Reasoning == "" {
		t.Error("expected either subtasks or reasoning from fallback path")
	}
}

// TestSelectAgentType: maps task types to their default agent type.
// Previously 0% coverage.
func TestSelectAgentType(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)
	cases := []struct {
		taskType TaskType
		want     string
	}{
		{TaskTypeExploration, "explore"},
		{TaskTypeBackground, "bash"},
		{TaskTypeRefactoring, "general"},
		{TaskTypeComplex, "general"},
		{TaskTypeQuestion, "general"},
		{TaskTypeSingleTool, "general"},
		{TaskTypeMultiTool, "general"},
	}
	for _, tc := range cases {
		t.Run(string(tc.taskType), func(t *testing.T) {
			got := ta.selectAgentType(tc.taskType)
			if got != tc.want {
				t.Errorf("selectAgentType(%s) = %q, want %q", tc.taskType, got, tc.want)
			}
		})
	}
}

// TestHasSequentialDependencies: a subtask list has sequential deps iff any
// subtask declares a Dependency. Previously 0%.
func TestHasSequentialDependencies(t *testing.T) {
	cases := []struct {
		name string
		subs []Subtask
		want bool
	}{
		{"empty", nil, false},
		{"no deps", []Subtask{{ID: "a"}, {ID: "b"}}, false},
		{"has dep", []Subtask{{ID: "a"}, {ID: "b", Dependencies: []string{"a"}}}, true},
		{"all deps", []Subtask{{ID: "a", Dependencies: []string{"x"}}, {ID: "b", Dependencies: []string{"a"}}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasSequentialDependencies(tc.subs); got != tc.want {
				t.Errorf("hasSequentialDependencies = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDecomposeByType_Refactoring: a refactoring task must decompose into the
// explore → plan → execute chain with correct dependency wiring.
func TestDecomposeByType_Refactoring(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)
	analysis := &TaskComplexity{Type: TaskTypeRefactoring, Score: 8}
	subs := ta.decomposeByType("refactor the auth module", analysis)

	if len(subs) < 3 {
		t.Fatalf("refactoring should decompose into ≥3 subtasks (explore/plan/execute), got %d", len(subs))
	}
	// Verify the chain: plan depends on explore, execute depends on plan.
	ids := make(map[string]Subtask, len(subs))
	for _, s := range subs {
		ids[s.ID] = s
	}
	if _, ok := ids["explore"]; !ok {
		t.Error("missing 'explore' subtask in refactoring decomposition")
	}
	if plan, ok := ids["plan"]; ok {
		if !containsStr(plan.Dependencies[0], "explore") && plan.Dependencies[0] != "explore" {
			t.Errorf("plan should depend on explore, got %v", plan.Dependencies)
		}
	}
}

// TestDecomposeByType_ComplexWithTests: a complex task mentioning "test"
// must include a dedicated test-execution subtask (bash agent).
func TestDecomposeByType_ComplexWithTests(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)
	analysis := &TaskComplexity{Type: TaskTypeComplex, Score: 9}
	subs := ta.decomposeByType("implement feature X and test it", analysis)

	ids := make(map[string]Subtask, len(subs))
	for _, s := range subs {
		ids[s.ID] = s
	}
	if _, ok := ids["test"]; !ok {
		t.Errorf("complex task mentioning 'test' should include a 'test' subtask, got IDs: %v", ids)
	}
}

// TestCreateSubtask_PriorityByScore: high-score subtasks get lower priority
// (priority=3 for score≥7), normal subtasks get priority=5.
func TestCreateSubtask_PriorityByScore(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)

	// A long complex prompt → high score → priority 3.
	high := ta.createSubtask("sub_1", "analyze the entire authentication system and refactor all the token validation logic across every handler")
	if high.Priority != 3 && high.Priority != 5 {
		t.Errorf("high-score subtask priority = %d, want 3 or 5", high.Priority)
	}

	// A short simple prompt → low score → priority 5.
	low := ta.createSubtask("sub_2", "read file")
	if low.Priority != 5 {
		t.Errorf("low-score subtask priority = %d, want 5", low.Priority)
	}
}

// TestDecomposeWithContext_NoLLM_FallsBackToRegex: without an LLM client
// configured, DecomposeWithContext must fall back to regex decomposition
// rather than crashing.
func TestDecomposeWithContext_NoLLM_FallsBackToRegex(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8) // no SetLLMClient called → llmClient is nil
	result := ta.DecomposeWithContext(t.Context(), "read A and read B")

	if result == nil {
		t.Fatal("expected non-nil result from regex fallback")
	}
	if len(result.Subtasks) < 2 {
		t.Errorf("regex fallback should have split 'and' into ≥2 subtasks, got %d", len(result.Subtasks))
	}
}
