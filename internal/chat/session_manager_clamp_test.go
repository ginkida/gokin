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
	t.Setenv("XDG_DATA_HOME", t.TempDir())
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

func TestNewSessionManagerClampsZeroRetentionToDocumentedDefaults(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	sm, err := NewSessionManager(NewSession(), SessionManagerConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultSessionManagerConfig()
	if sm.config.MaxSessionAge != defaults.MaxSessionAge || sm.config.MaxSessionCount != defaults.MaxSessionCount {
		t.Fatalf("retention = (%v, %d), want defaults (%v, %d)",
			sm.config.MaxSessionAge, sm.config.MaxSessionCount, defaults.MaxSessionAge, defaults.MaxSessionCount)
	}
}

func TestZeroRetentionConfigDoesNotDeleteSavedSessions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	current := NewSession()
	sm, err := NewSessionManager(current, SessionManagerConfig{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved := NewSession()
	saved.SetID("must-survive-default-retention")
	if err := sm.historyManager.SaveFull(saved); err != nil {
		t.Fatal(err)
	}
	if err := sm.CleanupOldSessions(); err != nil {
		t.Fatal(err)
	}
	if _, err := sm.historyManager.LoadFull(saved.GetID()); err != nil {
		t.Fatalf("zero-valued retention deleted a fresh saved session: %v", err)
	}
}
