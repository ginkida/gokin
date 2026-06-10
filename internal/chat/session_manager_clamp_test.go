package chat

import (
	"testing"
	"time"
)

// TestNewSessionManager_ClampsNonPositiveSaveInterval guards the periodic-save
// busy-loop: a non-positive SaveInterval (e.g. config `session.save_interval: 0`)
// would make time.AfterFunc/Reset fire immediately, pegging a goroutine and
// thrashing the session file on disk. It must clamp to the 2m default.
func TestNewSessionManager_ClampsNonPositiveSaveInterval(t *testing.T) {
	for _, iv := range []time.Duration{0, -5 * time.Second} {
		sm, err := NewSessionManager(nil, SessionManagerConfig{Enabled: true, SaveInterval: iv})
		if err != nil {
			t.Fatalf("NewSessionManager(%v): %v", iv, err)
		}
		if sm.config.SaveInterval != 2*time.Minute {
			t.Fatalf("SaveInterval=%v clamped to %v, want 2m", iv, sm.config.SaveInterval)
		}
	}

	sm, err := NewSessionManager(nil, SessionManagerConfig{SaveInterval: 30 * time.Second})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	if sm.config.SaveInterval != 30*time.Second {
		t.Fatalf("valid interval mutated to %v, want 30s", sm.config.SaveInterval)
	}
}
