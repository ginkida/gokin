package tools

import (
	"strings"
	"testing"

	"gokin/internal/client"

	"google.golang.org/genai"
)

func TestShouldInjectIntentNudge_OnlyBeforeAnyToolsExecuted(t *testing.T) {
	resp := &client.Response{
		Text:          "",
		FunctionCalls: []*genai.FunctionCall{{Name: "read"}},
	}
	if !shouldInjectIntentNudge(0, "kimi-for-coding", resp) {
		t.Error("should fire when no tools have been executed yet + tool calls + empty text")
	}
	// Once tools have run, the nudge would be stale — the model already
	// committed to a direction, lecturing about plan now adds noise.
	if shouldInjectIntentNudge(1, "kimi-for-coding", resp) {
		t.Error("should NOT fire once tools have executed (even 1)")
	}
	if shouldInjectIntentNudge(5, "kimi-for-coding", resp) {
		t.Error("should NOT fire deep into the turn")
	}
}

func TestShouldInjectIntentNudge_OnlyKimiFamily(t *testing.T) {
	resp := &client.Response{
		FunctionCalls: []*genai.FunctionCall{{Name: "read"}},
	}
	if shouldInjectIntentNudge(0, "glm-5.1", resp) {
		t.Error("must not fire for non-Kimi models")
	}
	if shouldInjectIntentNudge(0, "MiniMax-M2.7", resp) {
		t.Error("must not fire for MiniMax")
	}
	if !shouldInjectIntentNudge(0, "kimi-for-coding", resp) {
		t.Error("should fire for Kimi")
	}
}

func TestShouldInjectIntentNudge_SkipsWhenPlanTextPresent(t *testing.T) {
	resp := &client.Response{
		Text:          "Plan: update the handler to handle empty input edge case.",
		FunctionCalls: []*genai.FunctionCall{{Name: "read"}},
	}
	if shouldInjectIntentNudge(0, "kimi-for-coding", resp) {
		t.Error("should NOT fire when plan text is present")
	}
}

func TestShouldInjectIntentNudge_SkipsWhenNoToolCalls(t *testing.T) {
	resp := &client.Response{
		Text:          "",
		FunctionCalls: nil,
	}
	if shouldInjectIntentNudge(0, "kimi-for-coding", resp) {
		t.Error("text-only response has nothing to plan for; must not fire")
	}
}

func TestShouldInjectIntentNudge_ThinkingCountsAsPlan(t *testing.T) {
	// Model produced a thinking trace (not shown to user) but jumped
	// to tools with no visible text. Thinking >= threshold → nudge
	// would be redundant; the model already processed a plan.
	resp := &client.Response{
		Text:          "",
		Thinking:      "The user wants X. I'll read the handler, then edit it, then run tests.",
		FunctionCalls: []*genai.FunctionCall{{Name: "read"}},
	}
	if shouldInjectIntentNudge(0, "kimi-for-coding", resp) {
		t.Error("visible thinking trace should suppress the nudge")
	}
}

func TestShouldInjectIntentNudge_FillerTextStillTriggers(t *testing.T) {
	// "OK." / "Sure." type responses that don't actually plan — still
	// counts as empty (below threshold).
	resp := &client.Response{
		Text:          "OK.",
		FunctionCalls: []*genai.FunctionCall{{Name: "read"}},
	}
	if !shouldInjectIntentNudge(0, "kimi-for-coding", resp) {
		t.Error("'OK.' filler should still trigger the nudge")
	}
}

func TestShouldInjectIntentNudge_NilRespSafe(t *testing.T) {
	if shouldInjectIntentNudge(0, "kimi-for-coding", nil) {
		t.Error("nil resp must not trigger")
	}
}

func TestIntentNudgeMessage_HasKeyMarkers(t *testing.T) {
	if !strings.Contains(intentNudgeMessage, "Plan:") {
		t.Error("reminder must name the expected prefix 'Plan:'")
	}
	if !strings.Contains(intentNudgeMessage, "before your next") &&
		!strings.Contains(intentNudgeMessage, "Before your next") {
		t.Error("reminder must direct action toward the NEXT round")
	}
}
