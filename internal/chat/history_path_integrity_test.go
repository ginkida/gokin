package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHistoryManagerFullOperationsRejectTraversalIDs pins path containment at
// the storage layer. Callers normally validate custom names, but persistence
// must not rely on every current and future caller doing so correctly.
func TestHistoryManagerFullOperationsRejectTraversalIDs(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatalf("NewHistoryManager: %v", err)
	}
	sessionsDir, err := getSessionsDir()
	if err != nil {
		t.Fatalf("getSessionsDir: %v", err)
	}
	escapedDir := filepath.Dir(sessionsDir)

	t.Run("save", func(t *testing.T) {
		session := NewSession()
		session.SetID("../escaped-save")
		escapedPath := filepath.Join(escapedDir, "escaped-save.json")

		if err := history.SaveFull(session); err == nil {
			t.Error("SaveFull accepted a traversal session ID")
		}
		if _, err := os.Stat(escapedPath); !os.IsNotExist(err) {
			t.Errorf("SaveFull wrote outside sessions directory: stat error = %v", err)
		}
	})

	t.Run("load", func(t *testing.T) {
		const id = "../escaped-load"
		escapedPath := filepath.Join(escapedDir, "escaped-load.json")
		data, err := json.Marshal(&SessionState{ID: id})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if err := os.WriteFile(escapedPath, data, 0o600); err != nil {
			t.Fatalf("WriteFile escaped fixture: %v", err)
		}

		if _, err := history.LoadFull(id); err == nil {
			t.Error("LoadFull accepted a traversal session ID")
		}
	})

	t.Run("delete", func(t *testing.T) {
		const id = "../escaped-delete"
		escapedPath := filepath.Join(escapedDir, "escaped-delete.json")
		if err := os.WriteFile(escapedPath, []byte("outside"), 0o600); err != nil {
			t.Fatalf("WriteFile escaped fixture: %v", err)
		}

		if err := history.DeleteSession(id); err == nil {
			t.Error("DeleteSession accepted a traversal session ID")
		}
		if _, err := os.Stat(escapedPath); err != nil {
			t.Errorf("DeleteSession removed file outside sessions directory: %v", err)
		}
	})
}

func TestSessionJSONPathRejectsUnsafeIDs(t *testing.T) {
	for _, id := range []string{"", ".", "..", "../escape", "nested/escape", `..\escape`, `nested\escape`} {
		t.Run(id, func(t *testing.T) {
			if _, err := sessionJSONPath(t.TempDir(), id); err == nil {
				t.Fatalf("sessionJSONPath accepted unsafe ID %q", id)
			}
		})
	}
}
