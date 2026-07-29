package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// CleanupOldSessions deletes from the SHARED session store, so it can reach a
// session that a CONCURRENTLY RUNNING gokin process is actively writing: the
// count limit deletes by position in the newest-first list, and only THIS
// process's own session is exempt. A session held by a live writer lease must
// be skipped — that lease is exactly how the other process announces "mine".
func TestCleanupOldSessionsSkipsLeasedSessions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	hm, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}

	sessionsDir := filepath.Join(dir, "gokin", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Three sessions: ours (current), one held by "another process", one free.
	// Written directly so LastActive can be backdated (ToState stamps now()).
	save := func(id string, age time.Duration) {
		body := fmt.Sprintf(`{"id":%q,"start_time":%q,"last_active":%q,"work_dir":%q,"history":[]}`,
			id,
			time.Now().Add(-age).Format(time.RFC3339Nano),
			time.Now().Add(-age).Format(time.RFC3339Nano),
			dir)
		if err := os.WriteFile(filepath.Join(sessionsDir, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", id, err)
		}
	}
	save("current-session", 0)
	save("other-live", 2*time.Hour)
	save("plain-old", 3*time.Hour)

	// The other process holds its writer lease.
	lease, err := AcquireSessionWriterLease("other-live")
	if err != nil {
		t.Fatalf("AcquireSessionWriterLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })

	current := NewSession()
	current.ID = "current-session"
	sm := &SessionManager{
		session:        current,
		historyManager: hm,
		config: SessionManagerConfig{
			Enabled:         true,
			MaxSessionCount: 1, // forces deletion of everything past the newest
			MaxSessionAge:   30 * time.Minute,
		},
	}

	if err := sm.CleanupOldSessions(); err != nil {
		t.Fatalf("CleanupOldSessions: %v", err)
	}

	if _, err := os.Stat(filepath.Join(sessionsDir, "other-live.json")); err != nil {
		t.Fatalf("deleted a session held by another process's writer lease: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionsDir, "plain-old.json")); !os.IsNotExist(err) {
		t.Fatalf("unleased old session should still be cleaned up (err=%v)", err)
	}
}
