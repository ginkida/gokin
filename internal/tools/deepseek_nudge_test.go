package tools

import (
	"testing"

	"gokin/internal/client"

	"google.golang.org/genai"
)

// DeepSeek V4 exhibits the same drift patterns as Kimi (eagerness to
// re-read, skipping verification, jumping into tools without a plan).
// These tests pin that the nudge-eligibility extends to deepseek so
// every runtime guardrail that targets "kimi" also fires for deepseek.

func TestIsNudgeEligibleFamily_DeepSeekCovered(t *testing.T) {
	cases := map[string]bool{
		"deepseek-v4-pro":   true,
		"deepseek-v4-flash": true,
		"deepseek-chat":     true,
		"deepseek-reasoner": true,
		"kimi-for-coding":   true,
		"kimi-k2.5":         true,
		// Not eligible — these models self-consolidate.
		"glm-5.1":           false,
		"glm-4.7":           false,
		"MiniMax-M2.7":      false,
		"llama3.2":          false,
		"":                  false,
		"random-model-name": false,
	}
	for model, want := range cases {
		t.Run(model, func(t *testing.T) {
			if got := isNudgeEligibleFamily(model); got != want {
				t.Errorf("isNudgeEligibleFamily(%q) = %v, want %v", model, got, want)
			}
		})
	}
}

func TestShouldInjectSynthesisNudge_DeepSeek(t *testing.T) {
	// Reaches threshold AND family is deepseek → fire.
	if !shouldInjectSynthesisNudge("deepseek-v4-pro", synthesisNudgeThreshold) {
		t.Error("synthesis nudge should fire for deepseek at threshold")
	}
	if shouldInjectSynthesisNudge("deepseek-v4-pro", synthesisNudgeThreshold-1) {
		t.Error("synthesis nudge must NOT fire below threshold even for eligible family")
	}
	// GLM is not eligible — should stay silent.
	if shouldInjectSynthesisNudge("glm-5.1", synthesisNudgeThreshold+5) {
		t.Error("synthesis nudge should NOT fire for GLM (Strong-tier self-consolidates)")
	}
}

func TestShouldInjectIntentNudge_DeepSeek(t *testing.T) {
	respNoPlan := &client.Response{
		Text:          "",
		Thinking:      "",
		FunctionCalls: []*genai.FunctionCall{{Name: "read"}},
	}
	// First tool call of the turn, no plan text, deepseek family → fire.
	if !shouldInjectIntentNudge(0, "deepseek-v4-pro", respNoPlan) {
		t.Error("intent nudge should fire for deepseek on first tool call without plan")
	}
	// Same response, GLM → should NOT fire.
	if shouldInjectIntentNudge(0, "glm-5.1", respNoPlan) {
		t.Error("intent nudge should NOT fire for GLM")
	}
}

func TestShouldInjectKimiWorkingMemory_DeepSeekCovered(t *testing.T) {
	// Name retained for log-marker stability; eligibility extended
	// to deepseek. A single exploration tool with sizeable response
	// should trigger the working-memory injection for deepseek.
	calls := []*genai.FunctionCall{{Name: "grep"}}
	results := []*genai.FunctionResponse{
		{Response: map[string]any{
			"content": string(make([]byte, 800)),
		}},
	}
	if !shouldInjectKimiWorkingMemory("deepseek-v4-pro", calls, results) {
		t.Error("working memory injection should fire for deepseek on large exploration result")
	}
	// Non-eligible family — stays silent.
	if shouldInjectKimiWorkingMemory("glm-5.1", calls, results) {
		t.Error("working memory injection should NOT fire for GLM")
	}
}

func TestBuildKimiToolErrorRecoveryNotification_DeepSeekFires(t *testing.T) {
	errResults := []*genai.FunctionResponse{
		{Response: map[string]any{
			"success": false,
			"error":   "read-before-edit: call the read tool first",
		}},
	}
	msg := buildKimiToolErrorRecoveryNotification("deepseek-v4-pro", errResults)
	if msg == "" {
		t.Error("recovery notification should fire for deepseek on read-before-edit error")
	}
	// GLM — should stay empty.
	if got := buildKimiToolErrorRecoveryNotification("glm-5.1", errResults); got != "" {
		t.Errorf("recovery notification should NOT fire for GLM, got %q", got)
	}
}
