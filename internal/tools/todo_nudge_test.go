package tools

import (
	"strings"
	"testing"
)

func TestShouldInjectTodoNudge_DeepSeekMutationWithoutTodo(t *testing.T) {
	if !shouldInjectTodoNudge("deepseek-v4-pro", []string{"read", "edit"}) {
		t.Error("DeepSeek should get todo nudge after multi-tool mutation without todo")
	}
}

func TestShouldInjectTodoNudge_SuppressedForTrivialExploration(t *testing.T) {
	if shouldInjectTodoNudge("deepseek-v4-pro", []string{"grep", "read", "read"}) {
		t.Error("read-only exploration should not trigger todo nudge")
	}
}

func TestShouldInjectTodoNudge_SuppressedAfterTodo(t *testing.T) {
	if shouldInjectTodoNudge("deepseek-v4-pro", []string{"todo", "read", "edit"}) {
		t.Error("existing todo should suppress todo nudge")
	}
}

func TestShouldInjectTodoNudge_OnlyNudgeEligibleFamilies(t *testing.T) {
	if shouldInjectTodoNudge("glm-5.1", []string{"read", "edit"}) {
		t.Error("GLM should not get runtime todo nudge")
	}
	if !shouldInjectTodoNudge("kimi-for-coding", []string{"read", "edit"}) {
		t.Error("Kimi should get todo nudge after multi-tool mutation without todo")
	}
}

func TestTodoNudgeMessage_HasClaudeCodeMarkers(t *testing.T) {
	for _, needle := range []string{"todo", "in_progress", "Claude Code"} {
		if !strings.Contains(todoNudgeMessage, needle) {
			t.Fatalf("todoNudgeMessage missing %q: %q", needle, todoNudgeMessage)
		}
	}
}
