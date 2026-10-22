package tools

import (
	"strings"
	"testing"
)

func TestShouldInjectSynthesisNudge_OnlyDeepSeekFamily(t *testing.T) {
	// DeepSeek at threshold → fire.
	if !shouldInjectSynthesisNudge("deepseek-v4-pro", 3) {
		t.Error("deepseek at threshold must trigger nudge")
	}
	// GLM / Ollama at threshold → no nudge (Strong-tier models
	// self-consolidate; nudging regresses their behaviour).
	for _, model := range []string{"glm-5.1", "glm-4.7", "llama3.2", "gpt-4"} {
		if shouldInjectSynthesisNudge(model, 10) {
			t.Errorf("non-deepseek %q should not trigger synthesis nudge", model)
		}
	}
}

func TestShouldInjectSynthesisNudge_BelowThresholdSuppressed(t *testing.T) {
	for i := 0; i < synthesisNudgeThreshold; i++ {
		if shouldInjectSynthesisNudge("deepseek-v4-pro", i) {
			t.Errorf("nudge fired at count=%d (<%d threshold)", i, synthesisNudgeThreshold)
		}
	}
}

func TestShouldInjectSynthesisNudge_FiresAtExactThreshold(t *testing.T) {
	if !shouldInjectSynthesisNudge("deepseek-v4-pro", synthesisNudgeThreshold) {
		t.Errorf("must fire at exact threshold %d", synthesisNudgeThreshold)
	}
	if !shouldInjectSynthesisNudge("deepseek-v4-pro", synthesisNudgeThreshold+10) {
		t.Error("must continue firing past threshold (gate is >=)")
	}
}

func TestBuildSynthesisNudgeMessage_StructureAndMarkers(t *testing.T) {
	msg := buildSynthesisNudgeMessage(5)
	// Must reference the actual tool count so the model sees this is
	// about its current state, not a generic reminder.
	if !strings.Contains(msg, "5") {
		t.Errorf("message should include the tool count: %q", msg)
	}
	// The 3 anchors are what makes synthesis useful — without them the
	// model reverts to a vague "I'll consolidate" and continues.
	for _, needle := range []string{"Established", "Unknown", "Next"} {
		if !strings.Contains(msg, needle) {
			t.Errorf("message missing %q anchor: %q", needle, msg)
		}
	}
	// An explicit stop directive — the point is to get the model to
	// finalise when it already has enough, not just journal more.
	if !strings.Contains(strings.ToLower(msg), "stop") {
		t.Errorf("message must include a stop directive: %q", msg)
	}
}
