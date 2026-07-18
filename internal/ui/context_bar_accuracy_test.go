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

	// Model.Update is a value receiver — thread the returned model through.
	apply := func(msg interface{}) {
		updated, _ := m.Update(msg)
		*m = updated.(Model)
	}
	apply(TokenUsageMsg{Tokens: 56000, MaxTokens: 1_000_000, PercentUsed: 0.056, IsEstimate: true})
	apply(StreamTokenUpdateMsg{EstimatedOutputTokens: 800})
	apply(ContextHealthMsg{TotalTokens: 56200, MaxTokens: 1_000_000, PercentUsed: 0.0562})

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

// The absolute label is ONE number — the full live context (prompt +
// streaming/final completion), so the user sees the context growing during
// generation without mentally summing a "+N" tail (v0.100.108).
func TestContextBarLabelShowsEstimateAndLiveOutput(t *testing.T) {
	label := formatAbsoluteTokens(56000, 1_000_000, 800)
	if label != "56.8K/1.0M" {
		t.Fatalf("label = %q, want the summed 56.8K/1.0M", label)
	}
}

func TestProviderMeasuredContextRemainsExactAfterHealthRefresh(t *testing.T) {
	m := NewModel()
	apply := func(msg interface{}) {
		updated, _ := m.Update(msg)
		*m = updated.(Model)
	}
	apply(TokenUsageMsg{
		Tokens: 59_600, OutputTokens: 1_250, MaxTokens: 1_000_000,
		PercentUsed: 0.0596, IsEstimate: false,
	})
	apply(ContextHealthMsg{TotalTokens: 59_600, MaxTokens: 1_000_000, PercentUsed: 0.0596})

	if m.tokenUsage == nil || m.tokenUsage.IsEstimate {
		t.Fatalf("provider measurement became estimated: %+v", m.tokenUsage)
	}
	if m.tokenUsage.OutputTokens != 1_250 {
		t.Fatalf("exact completion tail was lost: %+v", m.tokenUsage)
	}
	rendered := stripAnsi(renderContextBar(
		m.getContextPercent(), 16, m.tokenUsage.Tokens, m.tokenUsage.MaxTokens,
		m.tokenUsage.OutputTokens, m.tokenUsage.IsEstimate,
	))
	if strings.Contains(rendered, "≈") {
		t.Fatalf("exact provider measurement rendered as estimate: %q", rendered)
	}
	if !strings.Contains(rendered, "60.9K/1.0M") {
		t.Fatalf("summed full-context label missing (59.6K prompt + 1.25K output): %q", rendered)
	}
}
