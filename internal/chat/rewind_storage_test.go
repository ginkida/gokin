package chat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteSessionRemovesOnlyOwnedRewindSnapshots(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	root, err := RewindStorageDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"remove-me", "keep-me"} {
		session := NewSession()
		session.SetID(id)
		session.AddUserMessage("hello")
		if err := history.SaveFull(session); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "snapshot"), []byte("private"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := history.DeleteSession("remove-me"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "remove-me")); !os.IsNotExist(err) {
		t.Fatal("deleted session retained checkpoints")
	}
	if _, err := os.Stat(filepath.Join(root, "keep-me", "snapshot")); err != nil {
		t.Fatal("another session's snapshots removed", err)
	}
}
func TestDeleteSessionDoesNotFollowRewindRootLink(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	history, err := NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	session := NewSession()
	session.SetID("safe")
	session.AddUserMessage("hello")
	if err := history.SaveFull(session); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	path := filepath.Join(outside, "safe")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := RewindStorageDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, root); err != nil {
		t.Skip(err)
	}
	if err := history.DeleteSession("safe"); err == nil {
		t.Fatal("substituted checkpoint root accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("external directory deleted", err)
	}
}
