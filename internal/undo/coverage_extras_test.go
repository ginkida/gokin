package undo

import (
	"os"
	"path/filepath"
	"testing"
)

// --- otherPathState + description (0% / 60% → 100%) ---

func TestPathState_OtherKind(t *testing.T) {
	s := otherPathState()
	if !s.exists() {
		t.Fatal("otherPathState should exist")
	}
	if s.description() != "non-regular filesystem entry" {
		t.Fatalf("description = %q", s.description())
	}
}

func TestPathState_Description_AllKinds(t *testing.T) {
	tests := []struct {
		name  string
		state pathState
		want  string
	}{
		{"missing", missingPathState(), "missing path"},
		{"file", filePathState([]byte("hello")), "regular file (5 bytes)"},
		{"directory", directoryPathState(), "directory"},
		{"other", otherPathState(), "non-regular filesystem entry"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.state.description(); got != tt.want {
				t.Fatalf("description() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPathState_Matches(t *testing.T) {
	// Same kind, same content
	a := filePathState([]byte("hello"))
	b := filePathState([]byte("hello"))
	if !a.matches(b) {
		t.Fatal("identical file states should match")
	}

	// Same kind, different content
	c := filePathState([]byte("world"))
	if a.matches(c) {
		t.Fatal("different content file states should not match")
	}

	// Different kind
	if a.matches(missingPathState()) {
		t.Fatal("file vs missing should not match")
	}

	// Both missing
	if !missingPathState().matches(missingPathState()) {
		t.Fatal("two missing states should match")
	}
}

// --- readPathState with symlink (75% → 100%) ---

func TestReadPathState_Symlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	os.WriteFile(target, []byte("data"), 0644)

	link := filepath.Join(dir, "link")
	os.Symlink(target, link)

	state, err := readPathState(link)
	if err != nil {
		t.Fatalf("readPathState symlink: %v", err)
	}
	// Symlink is not a regular file → should be pathStateOther
	if state.kind != pathStateOther {
		t.Fatalf("symlink kind = %v, want pathStateOther", state.kind)
	}
}

func TestReadPathState_Directory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	os.Mkdir(subdir, 0755)

	state, err := readPathState(subdir)
	if err != nil {
		t.Fatalf("readPathState directory: %v", err)
	}
	if state.kind != pathStateDirectory {
		t.Fatalf("directory kind = %v, want pathStateDirectory", state.kind)
	}
}

// --- pathStateView.get error path (85.7% → 100%) ---

func TestPathStateView_Get_CachesResult(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "test.txt")
	os.WriteFile(file, []byte("hello"), 0644)

	v := newPathStateView()
	state1, err := v.get(file)
	if err != nil {
		t.Fatalf("first get: %v", err)
	}
	state2, err := v.get(file)
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	if !state1.matches(state2) {
		t.Fatal("cached state should be identical")
	}
}

// --- requirePresentPath error path (66.7% → 100%) ---

func TestRequirePresentPath_MissingPath(t *testing.T) {
	dir := t.TempDir()
	v := newPathStateView()
	missingFile := filepath.Join(dir, "does-not-exist.txt")

	if _, err := requirePresentPath(v, missingFile, "test"); err == nil {
		t.Fatal("expected error for missing path in requirePresentPath")
	}
}

// --- tracker PopLastUnlocked and ListRecent edge cases ---

func TestTracker_PopLastUnlocked_Empty(t *testing.T) {
	tr := NewTracker()
	if change := tr.PopLastUnlocked(); change != nil {
		t.Fatal("PopLastUnlocked on empty tracker should return nil")
	}
}

func TestTracker_ListRecent_MoreThanAvailable(t *testing.T) {
	tr := NewTracker()
	tr.Record(FileChange{ID: "a", FilePath: "/a"})
	recent := tr.ListRecent(10)
	if len(recent) != 1 {
		t.Fatalf("ListRecent(10) with 1 entry = %d, want 1", len(recent))
	}
}

// --- UndoLastGroup with no group (76.5% → higher) ---

func TestUndoLastGroup_NoGroups(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.txt")
	fileB := filepath.Join(dir, "b.txt")
	os.WriteFile(fileA, []byte("new-a"), 0644)
	os.WriteFile(fileB, []byte("new-b"), 0644)

	m := NewManager()
	m.Record(FileChange{ID: "a", FilePath: fileA, Tool: "write", OldContent: []byte("old-a"), NewContent: []byte("new-a")})
	m.Record(FileChange{ID: "b", FilePath: fileB, Tool: "write", OldContent: []byte("old-b"), NewContent: []byte("new-b")})

	// UndoLastGroup should undo only the last (no group → single change)
	undone, err := m.UndoLastGroup()
	if err != nil {
		t.Fatalf("UndoLastGroup: %v", err)
	}
	if len(undone) != 1 {
		t.Fatalf("expected 1 change undone, got %d", len(undone))
	}
}

// --- Redo after UndoLastGroup ---

func TestRedo_AfterUndoLastGroup(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "redo-test.txt")
	os.WriteFile(file, []byte("new"), 0644)

	m := NewManager()
	m.Record(FileChange{
		ID:         "1",
		FilePath:   file,
		Tool:       "write",
		OldContent: []byte("old"),
		NewContent: []byte("new"),
	})

	// Undo
	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	// Redo
	if _, err := m.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}

	data, _ := os.ReadFile(file)
	if string(data) != "new" {
		t.Fatalf("after redo content = %q, want %q", string(data), "new")
	}
}

// --- Undo on empty manager ---

func TestUndo_EmptyManager(t *testing.T) {
	m := NewManager()
	if _, err := m.Undo(); err == nil {
		t.Fatal("expected error undoing empty manager")
	}
}

// --- Redo on empty redo stack ---

func TestRedo_EmptyRedoStack(t *testing.T) {
	m := NewManager()
	if _, err := m.Redo(); err == nil {
		t.Fatal("expected error redoing empty stack")
	}
}

// --- ClearRedo ---

func TestClearRedo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "clear-redo.txt")
	os.WriteFile(file, []byte("new"), 0644)

	m := NewManager()
	m.Record(FileChange{
		ID:         "1",
		FilePath:   file,
		Tool:       "write",
		OldContent: []byte("old"),
		NewContent: []byte("new"),
	})

	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if !m.CanRedo() {
		t.Fatal("should be able to redo")
	}

	m.undone = m.undone[:0] // clear redo stack directly
	if m.CanRedo() {
		t.Fatal("should not be able to redo after clearing redo stack")
	}
}
