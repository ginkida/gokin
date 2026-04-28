package ssh

import (
	"testing"
	"time"
)

func TestNewSessionManager(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	if sm.Count() != 0 {
		t.Errorf("Count() = %d, want 0", sm.Count())
	}
}

func TestSessionManagerGetNonExistent(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	_, ok := sm.Get("user@host:22")
	if ok {
		t.Error("Get() on empty manager should return false")
	}
}

func TestSessionManagerCloseNonExistent(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	err := sm.Close("user@host:22")
	if err == nil {
		t.Error("Close() on non-existent session should error")
	}
}

func TestSessionManagerListEmpty(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	infos := sm.List()
	if len(infos) != 0 {
		t.Errorf("List() on empty manager returned %d items", len(infos))
	}
}

func TestSessionManagerSetMaxIdle(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	sm.SetMaxIdle(5 * time.Minute)

	sm.mu.RLock()
	maxIdle := sm.maxIdle
	sm.mu.RUnlock()

	if maxIdle != 5*time.Minute {
		t.Errorf("maxIdle = %v, want 5m", maxIdle)
	}
}

func TestSessionManagerCloseAll(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	// CloseAll on empty should not panic
	sm.CloseAll()
	if sm.Count() != 0 {
		t.Errorf("Count() after CloseAll = %d", sm.Count())
	}
}

func TestSessionManagerCleanupIdle(t *testing.T) {
	sm := NewSessionManager()
	defer sm.Stop()

	// Cleanup on empty should return 0
	cleaned := sm.CleanupIdle()
	if cleaned != 0 {
		t.Errorf("CleanupIdle() on empty = %d, want 0", cleaned)
	}
}

func TestSessionInfoFields(t *testing.T) {
	info := SessionInfo{
		Key:       "user@host:22",
		Host:      "host",
		Port:      22,
		User:      "user",
		Connected: true,
		LastUse:   time.Now(),
		IdleTime:  0,
	}

	if info.Key != "user@host:22" {
		t.Errorf("Key = %q", info.Key)
	}
	if !info.Connected {
		t.Error("Connected should be true")
	}
}

// Stop must be safe to call multiple times. Pre-fix the second call did
// close(m.stopCh) on an already-closed channel and panicked. App shutdown
// could trigger this through a signal-handler race (signals.go:281 +
// app.go:670 both call ssh tool Cleanup paths).
func TestSessionManagerStopIdempotent(t *testing.T) {
	sm := NewSessionManager()
	sm.Stop()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("second Stop panicked: %v", r)
		}
	}()
	sm.Stop()
	sm.Stop() // third for good measure
}
