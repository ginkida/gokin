package undo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileChange(t *testing.T) {
	c := NewFileChange("/tmp/test.go", "write", nil, []byte("new content"), true)
	if c.ID == "" {
		t.Error("ID should be generated")
	}
	if c.FilePath != "/tmp/test.go" {
		t.Errorf("FilePath = %q", c.FilePath)
	}
	if !c.WasNew {
		t.Error("should be WasNew")
	}
	if c.Summary() != "created /tmp/test.go" {
		t.Errorf("Summary = %q", c.Summary())
	}
}

func TestFileChangeSummaryModified(t *testing.T) {
	c := NewFileChange("/tmp/test.go", "edit", []byte("old"), []byte("new"), false)
	if c.Summary() != "modified /tmp/test.go" {
		t.Errorf("Summary = %q", c.Summary())
	}
}

func TestFileChangeSizeChange(t *testing.T) {
	c := NewFileChange("/tmp/test.go", "edit", []byte("old"), []byte("new content"), false)
	delta := c.SizeChange()
	if delta != len("new content")-len("old") {
		t.Errorf("SizeChange = %d", delta)
	}
}

// --- Manager tests ---

func TestManagerUndoRedo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")

	m := NewManager()

	// Create file
	os.WriteFile(file, []byte("version 1"), 0644)

	// Record change
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("version 1"),
		NewContent: []byte("version 2"),
	})
	os.WriteFile(file, []byte("version 2"), 0644)

	if m.Count() != 1 {
		t.Errorf("Count = %d, want 1", m.Count())
	}
	if !m.CanUndo() {
		t.Error("should be able to undo")
	}

	// Undo
	change, err := m.Undo()
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if change == nil {
		t.Fatal("change should not be nil")
	}

	data, _ := os.ReadFile(file)
	if string(data) != "version 1" {
		t.Errorf("after undo: %q, want version 1", data)
	}

	if !m.CanRedo() {
		t.Error("should be able to redo")
	}

	// Redo
	change, err = m.Redo()
	if err != nil {
		t.Fatalf("Redo: %v", err)
	}

	data, _ = os.ReadFile(file)
	if string(data) != "version 2" {
		t.Errorf("after redo: %q, want version 2", data)
	}
}

func TestManagerUndoNewFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "new.txt")
	os.WriteFile(file, []byte("content"), 0644)

	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "write",
		NewContent: []byte("content"),
		WasNew:     true,
	})

	// Undo should delete the new file
	_, err := m.Undo()
	if err != nil {
		t.Fatalf("Undo: %v", err)
	}

	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Error("new file should be deleted after undo")
	}
}

func TestManagerNothingToUndo(t *testing.T) {
	m := NewManager()
	_, err := m.Undo()
	if err == nil {
		t.Error("should error with nothing to undo")
	}
}

func TestManagerNothingToRedo(t *testing.T) {
	m := NewManager()
	_, err := m.Redo()
	if err == nil {
		t.Error("should error with nothing to redo")
	}
}

func TestManagerClear(t *testing.T) {
	m := NewManager()
	m.Record(FileChange{FilePath: "/tmp/test", Tool: "write", NewContent: []byte("x"), WasNew: true})
	m.Clear()

	if m.Count() != 0 {
		t.Errorf("after Clear, Count = %d", m.Count())
	}
	if m.CanUndo() {
		t.Error("should not be able to undo after Clear")
	}
	if m.CanRedo() {
		t.Error("should not be able to redo after Clear")
	}
}

func TestManagerRecordClearsRedo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	os.WriteFile(file, []byte("v1"), 0644)

	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("v1"),
		NewContent: []byte("v2"),
	})
	os.WriteFile(file, []byte("v2"), 0644)

	m.Undo() // now v1, redo available

	// New record should clear redo
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("v1"),
		NewContent: []byte("v3"),
	})

	if m.CanRedo() {
		t.Error("redo should be cleared after new record")
	}
}

func TestManagerGroupUndo(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "a.txt")
	file2 := filepath.Join(dir, "b.txt")
	os.WriteFile(file1, []byte("a-old"), 0644)
	os.WriteFile(file2, []byte("b-old"), 0644)

	m := NewManager()
	m.SetActiveGroup("group-1")

	m.Record(FileChange{
		FilePath:   file1,
		Tool:       "edit",
		OldContent: []byte("a-old"),
		NewContent: []byte("a-new"),
	})
	os.WriteFile(file1, []byte("a-new"), 0644)

	m.Record(FileChange{
		FilePath:   file2,
		Tool:       "edit",
		OldContent: []byte("b-old"),
		NewContent: []byte("b-new"),
	})
	os.WriteFile(file2, []byte("b-new"), 0644)

	m.ClearActiveGroup()

	// Undo group
	reverted, err := m.UndoGroup("group-1")
	if err != nil {
		t.Fatalf("UndoGroup: %v", err)
	}
	if len(reverted) != 2 {
		t.Errorf("reverted %d files, want 2", len(reverted))
	}

	d1, _ := os.ReadFile(file1)
	d2, _ := os.ReadFile(file2)
	if string(d1) != "a-old" {
		t.Errorf("file1 = %q, want a-old", d1)
	}
	if string(d2) != "b-old" {
		t.Errorf("file2 = %q, want b-old", d2)
	}
}

func TestManagerUndoLastGroup(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	os.WriteFile(file, []byte("old"), 0644)

	m := NewManager()

	// Ungrouped change
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("old"),
		NewContent: []byte("new"),
	})
	os.WriteFile(file, []byte("new"), 0644)

	reverted, err := m.UndoLastGroup()
	if err != nil {
		t.Fatalf("UndoLastGroup: %v", err)
	}
	if len(reverted) != 1 {
		t.Errorf("reverted %d, want 1", len(reverted))
	}

	d, _ := os.ReadFile(file)
	if string(d) != "old" {
		t.Errorf("content = %q, want old", d)
	}
}

func TestManagerListRecent(t *testing.T) {
	m := NewManager()
	for i := 0; i < 5; i++ {
		m.Record(FileChange{FilePath: "/tmp/test", Tool: "write", NewContent: []byte("x"), WasNew: true})
	}

	recent := m.ListRecent(3)
	if len(recent) != 3 {
		t.Errorf("ListRecent(3) = %d", len(recent))
	}
}

func TestManagerRestoreChanges(t *testing.T) {
	m := NewManager()
	changes := []FileChange{
		{FilePath: "/a", Tool: "write"},
		{FilePath: "/b", Tool: "edit"},
	}
	m.RestoreChanges(changes)
	if m.Count() != 2 {
		t.Errorf("Count = %d, want 2", m.Count())
	}
}

// Move undo must rename the file back to its original source path. Before this
// fix, revertChange treated move like a normal modify and AtomicWrote OldContent
// (which holds the source PATH) into the dest file — corrupting it with a path
// string and leaving the source missing.
func TestManagerUndoMoveRenamesBack(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.txt")
	dest := filepath.Join(dir, "dst.txt")
	original := []byte("original content")

	if err := os.WriteFile(source, original, 0644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	if err := os.Rename(source, dest); err != nil {
		t.Fatalf("setup rename: %v", err)
	}

	m := NewManager()
	m.Record(FileChange{
		FilePath:   dest,
		Tool:       "move",
		OldContent: []byte(source),
		WasNew:     false,
	})

	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	// Source must exist again with original content.
	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("source not restored: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("source content = %q, want %q (regression: AtomicWrite would have written the path string)", got, original)
	}

	// Dest must no longer exist.
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest still exists after undo: %v", err)
	}
}

// Move redo must rename the file forward again. Symmetric to undo.
func TestManagerRedoMoveRenamesForward(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.txt")
	dest := filepath.Join(dir, "dst.txt")
	original := []byte("hello")

	if err := os.WriteFile(source, original, 0644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	if err := os.Rename(source, dest); err != nil {
		t.Fatalf("setup rename: %v", err)
	}

	m := NewManager()
	m.Record(FileChange{
		FilePath:   dest,
		Tool:       "move",
		OldContent: []byte(source),
		WasNew:     false,
	})

	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, err := m.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("dest not restored after redo: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("dest content = %q, want %q", got, original)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Errorf("source still exists after redo: %v", err)
	}
}

// Defensive: revertMove without OldContent (e.g. a malformed/old persisted
// change) must error out instead of os.Rename'ing to "" which would silently
// destroy the dest file.
func TestManagerUndoMoveMissingSourceErrors(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(dest, []byte("x"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	m := NewManager()
	m.Record(FileChange{
		FilePath: dest,
		Tool:     "move",
		// OldContent intentionally empty
		WasNew: false,
	})

	if _, err := m.Undo(); err == nil {
		t.Fatal("expected error for move undo without source path")
	}
	// Dest must remain untouched after the failed undo.
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("dest disappeared after failed undo: %v", err)
	}
}
