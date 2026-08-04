//go:build !windows && !plan9

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentRunLeaseRejectsSymlinkedDirectory(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "external")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(store.dir, "run-locks")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.acquireAgentRunFileLease("agent-1"); err == nil {
		t.Fatal("acquireAgentRunFileLease accepted a symlinked directory")
	}
	assertAgentDirMode(t, target, 0o755)
}

func TestAgentRunLeaseRepairsModeAndRejectsSymlinkFile(t *testing.T) {
	store, err := NewAgentStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const agentID = "agent-private-lock"
	lease, err := store.acquireAgentRunFileLease(agentID)
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()

	digest := sha256.Sum256([]byte(agentID))
	path := filepath.Join(store.dir, "run-locks", hex.EncodeToString(digest[:])+".lock")
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	lease, err = store.acquireAgentRunFileLease(agentID)
	if err != nil {
		t.Fatalf("reacquire existing lock: %v", err)
	}
	assertAgentDirMode(t, path, 0o600)
	lease.Release()

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := store.acquireAgentRunFileLease(agentID); err == nil {
		t.Fatal("acquireAgentRunFileLease accepted a symlink file")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertAgentDirMode(t, target, 0o644)
}
