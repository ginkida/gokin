package app

import "testing"

func TestPlanningModeToggleReturnsCommittedRevision(t *testing.T) {
	a := newSessionModeTestApp(false, true)
	a.configRevision = 12

	enabled, revision := a.togglePlanningModeWithRevision()
	if !enabled {
		t.Fatal("planning mode did not toggle on")
	}
	if revision != 13 || a.configRevision != 13 {
		t.Fatalf("planning-mode ownership revision=%d app=%d, want 13", revision, a.configRevision)
	}
	if !a.planningModeEnabled {
		t.Fatal("returned planning-mode state does not match committed app state")
	}
}
