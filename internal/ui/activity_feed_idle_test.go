package ui

import (
	"strings"
	"testing"
	"time"
)

// TestSubAgentState_IdleTrackingOnStart verifies that a freshly started
// sub-agent has LastToolTime seeded — so idle detection doesn't immediately
// fire with "idle Xm" from the start time.
func TestSubAgentState_IdleTrackingOnStart(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "general", "Find the bug")

	state := p.GetSubAgentState("a1")
	if state == nil {
		t.Fatal("state missing after StartSubAgent")
	}
	if state.LastToolTime.IsZero() {
		t.Error("LastToolTime should be seeded at start, not zero")
	}
	// And should be close to StartTime.
	if gap := state.StartTime.Sub(state.LastToolTime).Abs(); gap > 50*time.Millisecond {
		t.Errorf("StartTime and LastToolTime should be close, gap = %v", gap)
	}
}

// TestSubAgentState_IdleTrackingOnToolUpdate: every tool event (start or
// end) bumps LastToolTime. Users see "idle" only when events stop.
func TestSubAgentState_IdleTrackingOnToolUpdate(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "general", "Find the bug")

	// Backdate the internal state so we can detect the update. GetSubAgentState
	// returns a copy for race safety, so we mutate the real map here.
	p.mu.Lock()
	p.subAgentActivities["a1"].LastToolTime = time.Now().Add(-5 * time.Minute)
	oldTime := p.subAgentActivities["a1"].LastToolTime
	p.mu.Unlock()

	p.UpdateSubAgentTool("a1", "read", map[string]any{"file_path": "/tmp/x"})

	state := p.GetSubAgentState("a1")
	if !state.LastToolTime.After(oldTime) {
		t.Errorf("LastToolTime should have advanced, got %v (was %v)",
			state.LastToolTime, oldTime)
	}
	// And should be ~now.
	if time.Since(state.LastToolTime) > time.Second {
		t.Errorf("LastToolTime too old after update: %v", state.LastToolTime)
	}
}

// TestActivityFeedPanel_RendersIdleMarker: when a sub-agent has been
// silent past SubAgentIdleThreshold, the rendered row shows "· idle Xm"
// so the user has a visible signal the agent might be stuck.
func TestActivityFeedPanel_RendersIdleMarker(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "general", "Search for TODOs")

	// Backdate LastToolTime past the threshold (mutate real state, not the
	// copy returned by GetSubAgentState).
	p.mu.Lock()
	p.subAgentActivities["a1"].LastToolTime = time.Now().Add(-SubAgentIdleThreshold - time.Minute)
	p.mu.Unlock()

	out := stripAnsi(p.View(120))
	if !strings.Contains(out, "idle") {
		t.Errorf("expected 'idle' marker in rendered panel, got:\n%s", out)
	}
}

// TestActivityFeedPanel_NoIdleMarkerWhenActive: fresh tool activity means
// no idle marker. Otherwise we'd constantly warn "idle" on perfectly
// healthy agents.
func TestActivityFeedPanel_NoIdleMarkerWhenActive(t *testing.T) {
	p := NewActivityFeedPanel(DefaultStyles())
	p.StartSubAgent("a1", "general", "Search for TODOs")
	p.UpdateSubAgentTool("a1", "grep", nil) // fresh activity

	out := stripAnsi(p.View(120))
	if strings.Contains(out, "idle") {
		t.Errorf("healthy agent should NOT show idle marker:\n%s", out)
	}
}

// TestSubAgentIdleThreshold_Reasonable pins the threshold to a window long
// enough to ignore normal LLM thinking+generate pauses (Kimi thinking can
// run 30-60s) and short enough to catch stalls before the 10-min hard
// timeout fires. Prevents future tweaks from drifting it into either
// "too noisy" or "too late" territory.
func TestSubAgentIdleThreshold_Reasonable(t *testing.T) {
	if SubAgentIdleThreshold < time.Minute {
		t.Errorf("threshold %v is too short — will flag normal thinking pauses as idle",
			SubAgentIdleThreshold)
	}
	if SubAgentIdleThreshold > 5*time.Minute {
		t.Errorf("threshold %v is too long — idle marker won't fire before hard timeout",
			SubAgentIdleThreshold)
	}
}
