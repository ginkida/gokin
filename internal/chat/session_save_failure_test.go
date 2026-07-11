package chat

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// blockSessionsDir points XDG_DATA_HOME at a dir whose getSessionsDir() target
// is un-creatable — a regular FILE planted where the "gokin" directory would go
// makes SaveFull's MkdirAll deterministically fail. Must be called AFTER the
// SessionManager is constructed (NewHistoryManager itself MkdirAll's the dir).
func blockSessionsDir(t *testing.T) {
	t.Helper()
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	if err := os.WriteFile(filepath.Join(xdg, "gokin"), []byte("x"), 0600); err != nil {
		t.Fatalf("plant blocking file: %v", err)
	}
}

// TestSessionManager_OnSaveFailed_FiresOncePerFailingStreak pins the #9
// autosave-visibility fix: a background-save failure must surface to the user
// via the SetOnSaveFailed callback, but only ONCE per failing streak (on the
// healthy->failing transition) so a persistent disk-full/permission problem
// can't spam a per-message toast — and a NEW streak after a recovery must
// notify again.
func TestSessionManager_OnSaveFailed_FiresOncePerFailingStreak(t *testing.T) {
	good := t.TempDir()
	t.Setenv("XDG_DATA_HOME", good) // writable at construction time

	s := NewSession()
	s.SetWorkDir(t.TempDir())
	sm, err := NewSessionManager(s, SessionManagerConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	blockSessionsDir(t) // now swap to a blocked dir so saves fail

	var mu sync.Mutex
	fires := 0
	sm.SetOnSaveFailed(func(error) { mu.Lock(); fires++; mu.Unlock() })
	fireCount := func() int { mu.Lock(); defer mu.Unlock(); return fires }

	// First failing save -> callback fires once.
	if err := sm.Save(); err == nil {
		t.Fatal("Save() expected to fail against a blocked sessions dir")
	}
	// Second consecutive failure -> must NOT re-fire (transition-only).
	if err := sm.Save(); err == nil {
		t.Fatal("Save() expected to still fail")
	}
	if got := fireCount(); got != 1 {
		t.Fatalf("onSaveFailed must fire exactly once per failing streak, fired %d times", got)
	}

	// Recover: point at a writable dir so SaveFull succeeds, resetting the streak.
	t.Setenv("XDG_DATA_HOME", good)
	if err := sm.Save(); err != nil {
		t.Fatalf("Save() expected to succeed after recovery: %v", err)
	}
	if got := fireCount(); got != 1 {
		t.Fatalf("a successful save must not fire the failure callback, fires = %d", got)
	}

	// A brand-new failing streak must notify again.
	blockSessionsDir(t)
	if err := sm.Save(); err == nil {
		t.Fatal("Save() expected to fail again after re-blocking")
	}
	if got := fireCount(); got != 2 {
		t.Fatalf("a new failing streak must re-fire the callback, total fires = %d", got)
	}
}

// TestSessionManager_OnSaveFailed_NotFiredWhenUnset guards that a nil callback
// (the default) is a safe no-op on failure.
func TestSessionManager_OnSaveFailed_NotFiredWhenUnset(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := NewSession()
	s.SetWorkDir(t.TempDir())
	sm, err := NewSessionManager(s, SessionManagerConfig{Enabled: true})
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	blockSessionsDir(t)
	// No callback registered — a failing Save must not panic and must still
	// return the error.
	if err := sm.Save(); err == nil {
		t.Fatal("Save() expected to fail")
	}
}
