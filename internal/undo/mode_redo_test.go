package undo

import (
	"os"
	"path/filepath"
	"testing"
)

// TestManagerUndoPreservesMode pins bug #4: undo of a modified file used to
// hardcode 0644 in revertChange, silently stripping an executable's 0755 bits.
// With FileChange.Mode recorded, undo restores the original perm.
func TestManagerUndoPreservesMode(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(file, []byte("v1"), 0755); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("v1"),
		NewContent: []byte("v2"),
		Mode:       0755,
	})
	if err := os.WriteFile(file, []byte("v2"), 0755); err != nil {
		t.Fatal(err)
	}

	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Errorf("after undo mode = %o, want 0755 (mode stripped)", got)
	}

	// Redo must also restore 0755 (applyChange used to hardcode 0644 too).
	if _, err := m.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	info, _ = os.Stat(file)
	if got := info.Mode().Perm(); got != 0755 {
		t.Errorf("after redo mode = %o, want 0755", got)
	}
}

// TestManagerUndoModeDefaultsTo0644 pins the legacy fallback: a change with no
// recorded Mode (Mode == 0, e.g. deserialized from an old session) must still
// produce a sane 0644 file, never a 0000 unreadable one.
func TestManagerUndoModeDefaultsTo0644(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("v1"),
		NewContent: []byte("v2"),
		// Mode deliberately left 0 (legacy change).
	})
	os.WriteFile(file, []byte("v2"), 0600)

	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0644 {
		t.Errorf("zero-mode fallback = %o, want 0644", got)
	}
}

// TestManagerRedoMkdirRecreatesDirectory pins bug #5: redo of a "mkdir" used to
// fall into the generic AtomicWrite and create a regular EMPTY FILE at the path.
func TestManagerRedoMkdirRecreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "newdir")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	m.Record(FileChange{FilePath: target, Tool: "mkdir", WasNew: true})

	// Undo removes the dir.
	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("after undo, dir should be gone: %v", err)
	}

	// Redo must recreate it AS A DIRECTORY.
	if _, err := m.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("after redo, expected dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("redo of mkdir created a file, not a directory")
	}
}

// TestManagerRedoDeleteReRemoves pins bug #5: redo of a "delete" used to fall
// into the generic AtomicWrite and RESURRECT the file as a 0-byte stub.
func TestManagerRedoDeleteReRemoves(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "gone.txt")
	if err := os.WriteFile(file, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "delete",
		OldContent: []byte("content"),
		Mode:       0644,
	})
	os.Remove(file)

	// Undo restores the file.
	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if data, err := os.ReadFile(file); err != nil || string(data) != "content" {
		t.Fatalf("after undo: data=%q err=%v, want 'content'", data, err)
	}

	// Redo must RE-DELETE, not leave a 0-byte stub.
	if _, err := m.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("redo of delete left the file on disk (resurrected as stub)")
	}
}

// TestManagerRedoBatchDeleteReRemoves pins the adversarial-review catch: the
// batch tool records deletes as Tool="batch_delete" (NOT "delete"), so without
// that case in applyChange's switch, redo fell through to AtomicWrite and
// resurrected a 0-byte file.
func TestManagerRedoBatchDeleteReRemoves(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(file, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "batch_delete",
		OldContent: []byte("content"),
		Mode:       0644,
	})
	os.Remove(file)

	if _, err := m.Undo(); err != nil { // restores
		t.Fatalf("Undo: %v", err)
	}
	if _, err := m.Redo(); err != nil { // must re-delete
		t.Fatalf("Redo: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("redo of batch_delete left a 0-byte stub on disk")
	}
}
