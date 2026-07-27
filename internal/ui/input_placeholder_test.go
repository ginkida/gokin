package ui

import (
	"strings"
	"testing"
)

// The empty composer is the one surface every user sees between turns, so its
// placeholder doubles as a discovery channel: each Reset (send or Ctrl+U)
// advances to the next tip instead of repeating a static string.

func TestPlaceholderRotationAdvancesOnReset(t *testing.T) {
	m := NewInputModel(nil, "")

	if got := m.textarea.Placeholder; got != placeholderTips[0] {
		t.Fatalf("new input should start with the classic placeholder %q, got %q", placeholderTips[0], got)
	}

	for i := 1; i < len(placeholderTips); i++ {
		m.Reset()
		if got := m.textarea.Placeholder; got != placeholderTips[i] {
			t.Fatalf("after %d Reset(s) want placeholder %q, got %q", i, placeholderTips[i], got)
		}
	}

	// A full cycle wraps back to the classic placeholder.
	m.Reset()
	if got := m.textarea.Placeholder; got != placeholderTips[0] {
		t.Fatalf("rotation should wrap to %q, got %q", placeholderTips[0], got)
	}
}

func TestPlaceholderActiveTaskOverridesRotation(t *testing.T) {
	m := NewInputModel(nil, "")
	m.Reset() // advance off the default so the override is observable

	m.SetActiveTask("fix the flaky test")
	if got := m.textarea.Placeholder; got != "Continue: fix the flaky test" {
		t.Fatalf("active task should override rotation, got %q", got)
	}

	// Reset during an active task keeps the task placeholder while the
	// underlying index still advances.
	m.Reset()
	if got := m.textarea.Placeholder; got != "Continue: fix the flaky test" {
		t.Fatalf("Reset during an active task should keep the task placeholder, got %q", got)
	}

	m.SetActiveTask("")
	if got := m.textarea.Placeholder; got != placeholderTips[2] {
		t.Fatalf("clearing the task should resume rotation at the current index %q, got %q", placeholderTips[2], got)
	}
}

func TestPlaceholderHintsCanBeDisabledWithoutHidingTaskContext(t *testing.T) {
	m := NewInputModel(nil, "")
	m.Reset()
	if got := m.textarea.Placeholder; got == placeholderTips[0] {
		t.Fatalf("precondition: rotation did not advance, got %q", got)
	}

	m.SetHintsEnabled(false)
	if got := m.textarea.Placeholder; got != placeholderTips[0] {
		t.Fatalf("disabled hints should restore the classic placeholder, got %q", got)
	}
	m.Reset()
	if got := m.textarea.Placeholder; got != placeholderTips[0] {
		t.Fatalf("disabled hints should stay static across Reset, got %q", got)
	}

	m.SetActiveTask("finish config wiring")
	if got := m.textarea.Placeholder; got != "Continue: finish config wiring" {
		t.Fatalf("task context should remain visible with hints disabled, got %q", got)
	}
	m.SetActiveTask("")
	m.SetHintsEnabled(true)
	if got := m.textarea.Placeholder; got != placeholderTips[1] {
		t.Fatalf("re-enabled hints should resume at the previous tip, got %q", got)
	}
}

// Drift guard in the spirit of TestGeneralHintsMatchCurrentBindings: every
// binding the placeholder advertises must exist in the real key map — the
// welcome panel and shortcuts overlay advertise the same set, so a binding
// rename needs the same edit in all three surfaces.
func TestPlaceholderTipsMatchAdvertisedBindings(t *testing.T) {
	if placeholderTips[0] != "Message or /command" {
		t.Errorf("index 0 must stay the classic first-run placeholder, got %q", placeholderTips[0])
	}
	joined := strings.Join(placeholderTips, "\n")
	for _, want := range []string{"Ctrl+K", "Shift+Tab", "Ctrl+P", "Ctrl+E", "@", "/"} {
		if !strings.Contains(joined, want) {
			t.Errorf("placeholder tips lost binding %q:\n%s", want, joined)
		}
	}
}
