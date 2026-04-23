package router

import "testing"

// Regression: short memory commands used to be classified as
// TaskTypeQuestion → StrategyDirect, which ships no tools, so the
// memory tool never fired. Fix was to route them to TaskTypeSingleTool
// so StrategyExecutor sees the memory toolset.
func TestTaskAnalyzer_MemoryCommandsRouteToSingleTool(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)

	cases := []struct {
		name    string
		message string
	}{
		// English
		{"english remember", "remember that the API key is stored in env"},
		{"english recall", "recall what I told you about the handler"},
		{"english forget", "forget what I said earlier"},
		{"english memorize", "memorize this address"},
		{"english pin this", "pin this to your context: always use tabs"},
		// Russian
		{"ru запомни", "запомни путь к проекту"},
		{"ru вспомни", "вспомни где лежит handler.go"},
		{"ru забудь", "забудь что я говорил про X"},
		{"ru закрепи", "закрепи это: мы работаем над Kimi reliability"},
		{"ru в память", "запиши в память что makefile уже есть"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := ta.Analyze(tc.message)
			if result.Type != TaskTypeSingleTool {
				t.Errorf("%q → Type=%v, want %v", tc.message, result.Type, TaskTypeSingleTool)
			}
			// The strategy should be executor (not direct), guaranteeing
			// the tool list is handed to the model.
			if result.Strategy == StrategyDirect {
				t.Errorf("%q → Strategy=Direct (no tools!), want Executor or better", tc.message)
			}
		})
	}
}

func TestTaskAnalyzer_NonMemoryShortMessagesStayInQuestion(t *testing.T) {
	ta := NewTaskAnalyzer(6, 8)

	// Confirm we haven't over-broadened — plain questions should still
	// classify as questions, not fake memory ops.
	cases := []string{
		"what is 2+2?",
		"how do I install this?",
		"привет",
		"explain the code",
	}
	for _, msg := range cases {
		t.Run(msg, func(t *testing.T) {
			result := ta.Analyze(msg)
			if result.Type == TaskTypeSingleTool {
				t.Errorf("%q misclassified as memory op: %+v", msg, result)
			}
		})
	}
}
