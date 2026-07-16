package ui

import (
	"strings"
	"testing"
)

// ContextHealthMsg arrives right behind every TokenUsageMsg
// (sendTokenUsageUpdate sends both) and used to rebuild m.tokenUsage from
// scratch — erasing IsEstimate (the ≈ marker silently went exact-looking for
// an estimated number) and OutputTokens (the projected "+N" band vanished on
// every between-rounds refresh mid-stream). Both must survive (v0.100.90).
func TestContextHealthMsgPreservesEstimateAndStreamingOutput(t *testing.T) {
	m := NewModel()

	m.Update(TokenUsageMsg{Tokens: 56000, MaxTokens: 1_000_000, PercentUsed: 0.056, IsEstimate: true})
	m.Update(StreamTokenUpdateMsg{EstimatedOutputTokens: 800})
	m.Update(ContextHealthMsg{TotalTokens: 56200, MaxTokens: 1_000_000, PercentUsed: 0.0562})

	if m.tokenUsage == nil {
		t.Fatal("tokenUsage dropped entirely")
	}
	if m.tokenUsage.Tokens != 56200 {
		t.Fatalf("Tokens = %d, want the health update's 56200", m.tokenUsage.Tokens)
	}
	if !m.tokenUsage.IsEstimate {
		t.Fatal("ContextHealthMsg erased IsEstimate — the ≈ marker would silently disappear")
	}
	if m.tokenUsage.OutputTokens != 800 {
		t.Fatalf("OutputTokens = %d, want the live streaming estimate 800 preserved", m.tokenUsage.OutputTokens)
	}
}

// The absolute label keeps the streaming "+N" tail so the user sees the
// context growing during generation, and the ≈ prefix marks estimates.
func TestContextBarLabelShowsEstimateAndLiveOutput(t *testing.T) {
	label := formatAbsoluteTokens(56000, 1_000_000, 800)
	if !strings.Contains(label, "56.0K/1.0M") || !strings.Contains(label, "+800") {
		t.Fatalf("label = %q, want tokens/max plus the +N streaming tail", label)
	}
}
