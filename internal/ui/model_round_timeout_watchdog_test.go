package ui

import (
	"testing"
	"time"
)

func TestModelRoundTimeoutKeepsUIWatchdogOutsideBackendDeadline(t *testing.T) {
	m := NewModel()

	m.SetModelRoundTimeout(20 * time.Minute)
	if got, want := m.streamTimeout, 21*time.Minute; got != want {
		t.Fatalf("stream watchdog = %v, want %v", got, want)
	}

	next, _ := m.Update(ConfigUpdateMsg{ModelRoundTimeout: 30 * time.Minute})
	updated := next.(Model)
	m = &updated
	if got, want := m.streamTimeout, 31*time.Minute; got != want {
		t.Fatalf("live-updated stream watchdog = %v, want %v", got, want)
	}
}

func TestSmallModelRoundTimeoutDoesNotMakeUIWatchdogAggressive(t *testing.T) {
	m := NewModel()
	m.SetModelRoundTimeout(5 * time.Minute)
	if got := m.streamTimeout; got != defaultStreamTimeout {
		t.Fatalf("stream watchdog = %v, want floor %v", got, defaultStreamTimeout)
	}

	// Zero means an older/partial ConfigUpdateMsg omitted this field; it must
	// not reset a previously applied live value.
	m.SetModelRoundTimeout(20 * time.Minute)
	next, _ := m.Update(ConfigUpdateMsg{})
	updated := next.(Model)
	m = &updated
	if got, want := m.streamTimeout, 21*time.Minute; got != want {
		t.Fatalf("partial config update reset stream watchdog to %v, want %v", got, want)
	}
}
