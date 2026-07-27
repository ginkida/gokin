package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
)

// The curated hint rotation (hints.go) used to be unreachable — nothing
// called GetContextualHint from production paths. It now feeds a dim
// status-bar segment in the idle state, advancing from the UI heartbeat and
// on submit before the StateProcessing transition.

func TestIdleHintHeartbeatSeedsFirstRender(t *testing.T) {
	m := *NewModel()
	if m.idleHint != "" {
		t.Fatalf("precondition: fresh model should wait for the first UI tick, got %q", m.idleHint)
	}

	next, _ := m.Update(spinner.TickMsg{})
	m = next.(Model)
	want := "Shift+Tab — break complex tasks into reviewable plan steps"
	if m.idleHint != want {
		t.Fatalf("first UI tick should seed onboarding hint %q, got %q", want, m.idleHint)
	}
}

func TestIdleHintAdvancesOnSubmit(t *testing.T) {
	m := *NewModel()

	if m.idleHint != "" {
		t.Fatalf("fresh model should have no idle hint, got %q", m.idleHint)
	}

	m.advanceIdleHint()
	// Session is seconds old → the onboarding hint wins over the general
	// rotation (hints.go: sessionDuration < 2min branch).
	want := "Shift+Tab — break complex tasks into reviewable plan steps"
	if m.idleHint != want {
		t.Fatalf("first advance want %q, got %q", want, m.idleHint)
	}

	segments := strings.Join(m.statusBarHintSegments(false), " ")
	if !strings.Contains(segments, want) {
		t.Errorf("status bar should render the idle hint %q, segments: %q", want, segments)
	}
}

func TestIdleHintRotationFollowsCorpus(t *testing.T) {
	m := *NewModel()
	m.sessionStart = time.Now().Add(-3 * time.Minute) // past the onboarding window

	m.advanceIdleHint()
	want := "? — show all keyboard shortcuts" // first general corpus entry
	if m.idleHint != want {
		t.Fatalf("first general hint want %q, got %q", want, m.idleHint)
	}

	m.hintSystem.lastHintTime = time.Now().Add(-time.Minute) // bypass the 30s rate limit
	m.advanceIdleHint()
	want = "Shift+Tab — break complex tasks into reviewable plan steps" // corpus entry #2
	if m.idleHint != want {
		t.Fatalf("second general hint want %q, got %q", want, m.idleHint)
	}
}

func TestIdleHintRateLimited(t *testing.T) {
	m := *NewModel()
	m.advanceIdleHint()
	first := m.idleHint

	// Within the hint system's 30s rate limit the corpus returns "", and the
	// current hint must persist rather than blank out the segment.
	m.advanceIdleHint()
	if m.idleHint != first {
		t.Fatalf("rate-limited advance should keep the current hint %q, got %q", first, m.idleHint)
	}
}

func TestIdleHintRespectsDisabled(t *testing.T) {
	m := *NewModel()
	m.SetHintsEnabled(false)

	if m.input.placeholderTipsOn {
		t.Fatal("disabling contextual hints should also stop composer tip rotation")
	}
	if m.hintSystem.enabled {
		t.Fatal("disabling contextual hints should disable the shared hint system")
	}
	m.advanceIdleHint()
	if m.idleHint != "" {
		t.Fatalf("disabled hints should not advance, got %q", m.idleHint)
	}

	// Even with a hint stashed, the segment must not render while disabled.
	m.idleHint = "? — show all keyboard shortcuts"
	segments := strings.Join(m.statusBarHintSegments(false), " ")
	if strings.Contains(segments, "keyboard shortcuts") {
		t.Errorf("disabled hints must not render, segments: %q", segments)
	}
}

func TestIdleHintNotRenderedOutsideInput(t *testing.T) {
	m := *NewModel()
	m.advanceIdleHint()
	if m.idleHint == "" {
		t.Fatal("precondition: hint should be stashed")
	}

	m.state = StateProcessing
	segments := strings.Join(m.statusBarHintSegments(false), " ")
	if strings.Contains(segments, m.idleHint) {
		t.Errorf("idle hint must stay out of the processing status bar, segments: %q", segments)
	}
}

// ConfigUpdateMsg carries the user's hints_enabled config to the model — the
// wiring that makes /set hints on|off (and the YAML field) actually reach the
// idle status-bar hint.
func TestIdleHintConfigUpdateMsgToggles(t *testing.T) {
	m := *NewModel()
	if !m.hintsEnabled {
		t.Fatal("precondition: hints enabled by default")
	}

	next, _ := m.Update(ConfigUpdateMsg{HintsEnabled: false})
	m = next.(Model)
	if m.hintsEnabled {
		t.Error("ConfigUpdateMsg{HintsEnabled: false} should disable hints")
	}

	next, _ = m.Update(ConfigUpdateMsg{HintsEnabled: true})
	m = next.(Model)
	if !m.hintsEnabled {
		t.Error("ConfigUpdateMsg{HintsEnabled: true} should re-enable hints")
	}
}
