//go:build !windows && !plan9

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAgentStoreRejectsSymlinkedPrivateDirectories(t *testing.T) {
	t.Run("agents", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "external")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "agents")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewAgentStore(root); err == nil {
			t.Fatal("NewAgentStore accepted symlinked agents directory")
		}
		assertAgentDirMode(t, target, 0o755)
	})

	t.Run("checkpoints", func(t *testing.T) {
		root := t.TempDir()
		store, err := NewAgentStore(root)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, "external")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(store.dir, "checkpoints")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		cp := &AgentCheckpoint{CheckpointID: "checkpoint-1", AgentState: &AgentState{ID: "agent-1"}}
		if err := store.SaveCheckpoint(cp); err == nil {
			t.Fatal("SaveCheckpoint accepted symlinked checkpoints directory")
		}
		assertAgentDirMode(t, target, 0o755)
	})
}

func assertAgentDirMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
