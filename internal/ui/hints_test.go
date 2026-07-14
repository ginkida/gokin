package ui

import (
	"strings"
	"testing"
	"time"
)

func TestGeneralHintsMatchCurrentBindings(t *testing.T) {
	h := NewHintSystem(DefaultStyles())

	// Drain enough hints to cycle through all of generalHints. Each call
	// has to reset lastHintTime so the 30s rate-limit doesn't gate the
	// loop. After ~12 calls every general hint should have been emitted
	// at least once.
	var seen []string
	for range 12 {
		h.lastHintTime = time.Now().Add(-time.Minute)
		seen = append(seen, h.GetContextualHint(StateInput, "", 10*time.Minute))
	}
	joined := strings.Join(seen, "\n")

	// Stale wordings must not return.
	staleStrings := []string{
		"Option+C",                        // terminal binding is Alt+C
		"track background tasks",          // Ctrl+T shows the task list, doesn't "track"
		"step-by-step with planning mode", // older first-message hint
	}
	for _, stale := range staleStrings {
		if strings.Contains(joined, stale) {
			t.Errorf("hint corpus should not contain stale string %q:\n%s", stale, joined)
		}
	}

	// Every binding the welcome panel + shortcuts overlay advertises
	// should appear at least once in the hint rotation. If a future
	// commit adds a binding to one surface but forgets the others, the
	// hint corpus diverges from the rest of the UI.
	wantBindings := []string{
		"Alt+C",     // copy last response
		"Ctrl+P",    // command palette
		"Ctrl+K",    // model selector
		"Ctrl+E",    // expand last tool output
		"Ctrl+T",    // toggle task list
		"Ctrl+O",    // agents in real time
		"Shift+Tab", // cycle mode
		"task list",
		"shortcuts", // for the `?` hint
	}
	for _, want := range wantBindings {
		if !strings.Contains(joined, want) {
			t.Errorf("hint corpus missing binding %q:\n%s", want, joined)
		}
	}
}

func TestContextualHintPrioritizesCancellationDuringNewSession(t *testing.T) {
	h := NewHintSystem(DefaultStyles())
	for _, state := range []State{StateProcessing, StateStreaming} {
		h.Reset()
		got := h.GetContextualHint(state, "", 10*time.Second)
		if !strings.Contains(got, "Esc") || !strings.Contains(got, "cancel") {
			t.Fatalf("state %v hid immediate cancellation behind onboarding: %q", state, got)
		}
		if strings.Contains(got, "Shift+Tab") {
			t.Fatalf("state %v showed planning onboarding instead of recovery: %q", state, got)
		}
	}
}

func TestHintResetAllowsImmediateRediscovery(t *testing.T) {
	h := NewHintSystem(DefaultStyles())
	if got := h.GetContextualHint(StateInput, "", time.Minute); got == "" {
		t.Fatal("initial hint missing")
	}
	h.Reset()
	if got := h.GetContextualHint(StateInput, "", time.Minute); got == "" {
		t.Fatal("Reset retained the 30-second cooldown")
	}
}
