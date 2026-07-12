package undo

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewManagerWithTracker pins that the custom tracker is wired in (not
// replaced by a fresh default tracker) — GetTracker must return the same
// instance the caller handed in. This is the seam session-restore relies on.
func TestNewManagerWithTracker(t *testing.T) {
	custom := NewTrackerWithSize(7)
	custom.Record(FileChange{ID: "seed", FilePath: "/seed"})
	m := NewManagerWithTracker(custom)

	got := m.GetTracker()
	if got != custom {
		t.Fatal("GetTracker() returned a different instance; NewManagerWithTracker must keep the caller's tracker")
	}
	if got.maxSize != 7 {
		t.Errorf("tracker maxSize = %d, want 7 (custom tracker fields must be preserved)", got.maxSize)
	}
	if got.GetByID("seed") == nil {
		t.Error("seeded change missing from the wired tracker")
	}
}

// TestManagerList covers Manager.List (delegates to tracker.List) and confirms
// it returns a snapshot ordered oldest-first, not a live view.
func TestManagerList(t *testing.T) {
	m := NewManager()
	if all := m.List(); len(all) != 0 {
		t.Errorf("empty List len = %d, want 0", len(all))
	}
	m.Record(FileChange{ID: "a", FilePath: "/a"})
	m.Record(FileChange{ID: "b", FilePath: "/b"})

	all := m.List()
	if len(all) != 2 || all[0].ID != "a" || all[1].ID != "b" {
		t.Fatalf("List = %v, want [a b]", ids(all))
	}
	// Mutating the result must not affect the manager.
	all[0] = FileChange{ID: "mutated"}
	fresh := m.List()
	if fresh[0].ID != "a" {
		t.Error("Manager.List did not return a defensive copy")
	}
}

// TestManagerGetUndoneAndSetRedoStack covers the redo-stack accessors: after
// an Undo the undone change appears in GetUndone; SetRedoStack replaces the
// stack wholesale (used by session restore); GetUndone returns a copy.
func TestManagerGetUndoneAndSetRedoStack(t *testing.T) {
	m := NewManager()
	if got := m.GetUndone(); len(got) != 0 {
		t.Errorf("empty GetUndone len = %d, want 0", len(got))
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "f.txt")
	os.WriteFile(file, []byte("v1"), 0644)
	m.Record(FileChange{FilePath: file, Tool: "edit", OldContent: []byte("v1"), NewContent: []byte("v2")})
	os.WriteFile(file, []byte("v2"), 0644)

	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	undone := m.GetUndone()
	if len(undone) != 1 || undone[0].FilePath != file {
		t.Fatalf("GetUndone after one Undo = %v, want the undone change", undone)
	}

	// SetRedoStack replaces the stack; CanRedo must reflect it, and the
	// previous redo entry must be gone.
	m.SetRedoStack([]FileChange{{ID: "r1", FilePath: "/r1"}, {ID: "r2", FilePath: "/r2"}})
	if !m.CanRedo() {
		t.Error("CanRedo false after SetRedoStack with non-empty slice")
	}
	got := m.GetUndone()
	if len(got) != 2 || got[0].ID != "r1" || got[1].ID != "r2" {
		t.Errorf("GetUndone after SetRedoStack = %v, want [r1 r2]", ids(got))
	}

	// SetRedoStack with nil/empty clears it.
	m.SetRedoStack(nil)
	if m.CanRedo() {
		t.Error("CanRedo true after SetRedoStack(nil)")
	}
}

// TestManagerUndoLastGroup_NoGroup covers the branch where the most recent
// change has no GroupID: UndoLastGroup must behave like a single Undo (revert
// it and push onto the redo stack), not error out.
func TestManagerUndoLastGroup_NoGroup(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "g.txt")
	os.WriteFile(file, []byte("v1"), 0644)

	m := NewManager()
	m.Record(FileChange{FilePath: file, Tool: "edit", OldContent: []byte("v1"), NewContent: []byte("v2")})
	os.WriteFile(file, []byte("v2"), 0644)

	reverted, err := m.UndoLastGroup()
	if err != nil {
		t.Fatalf("UndoLastGroup (no group): %v", err)
	}
	if len(reverted) != 1 || reverted[0].FilePath != file {
		t.Fatalf("reverted = %v, want the single change", reverted)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "v1" {
		t.Errorf("file content after undo = %q, want v1", data)
	}
	if !m.CanRedo() {
		t.Error("CanRedo should be true; the single undo must push onto the redo stack")
	}
}

// TestManagerUndoLastGroup_WithGroup covers the branch where the most recent
// change carries a GroupID: UndoLastGroup delegates to undoGroupLocked and
// reverts every member of that group.
func TestManagerUndoLastGroup_WithGroup(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "1.txt")
	f2 := filepath.Join(dir, "2.txt")
	os.WriteFile(f1, []byte("a1"), 0644)
	os.WriteFile(f2, []byte("b1"), 0644)

	m := NewManager()
	m.SetActiveGroup("batch")
	m.Record(FileChange{FilePath: f1, Tool: "edit", OldContent: []byte("a1"), NewContent: []byte("a2")})
	os.WriteFile(f1, []byte("a2"), 0644)
	m.Record(FileChange{FilePath: f2, Tool: "edit", OldContent: []byte("b1"), NewContent: []byte("b2")})
	os.WriteFile(f2, []byte("b2"), 0644)
	m.ClearActiveGroup()

	reverted, err := m.UndoLastGroup()
	if err != nil {
		t.Fatalf("UndoLastGroup: %v", err)
	}
	if len(reverted) != 2 {
		t.Fatalf("reverted %d changes, want 2", len(reverted))
	}
	for _, f := range []string{f1, f2} {
		if _, err := os.Stat(f); os.IsNotExist(err) {
			t.Errorf("group undo lost track of %s", f)
		}
	}
	d1, _ := os.ReadFile(f1)
	d2, _ := os.ReadFile(f2)
	if string(d1) != "a1" || string(d2) != "b1" {
		t.Errorf("after group undo contents = %q/%q, want a1/b1", d1, d2)
	}
	// Both reverted changes must be on the redo stack.
	if len(m.GetUndone()) != 2 {
		t.Errorf("redo stack = %d, want 2 after group undo", len(m.GetUndone()))
	}
}

// TestManagerUndoLastGroup_Empty covers the "nothing to undo" branch.
func TestManagerUndoLastGroup_Empty(t *testing.T) {
	m := NewManager()
	if _, err := m.UndoLastGroup(); err == nil {
		t.Error("UndoLastGroup on empty tracker should error")
	}
}

// TestManagerUndoGroup_RollbackOnFailure covers the rollback path in
// undoGroupLocked: when a revert fails partway through a group, the changes
// already reverted must be RE-APPLIED so the working tree matches its
// pre-undo state. We construct a group of two changes where the newest reverts
// cleanly and the oldest (a "move" with no recorded source path) fails, forcing
// the rollback loop to re-apply the newest change.
func TestManagerUndoGroup_RollbackOnFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "keep.txt")
	os.WriteFile(file, []byte("v2"), 0644) // current on-disk content == NewContent of `good`

	m := NewManager()
	m.SetActiveGroup("g")
	// `bad` is OLDEST: a move whose revert fails (missing original source path).
	m.Record(FileChange{ID: "bad", FilePath: filepath.Join(dir, "moved"), Tool: "move", OldContent: nil, GroupID: "g"})
	// `good` is NEWEST: a normal modify that reverts (and re-applies) cleanly.
	m.Record(FileChange{ID: "good", FilePath: file, Tool: "edit", OldContent: []byte("v1"), NewContent: []byte("v2"), GroupID: "g"})
	m.ClearActiveGroup()

	_, err := m.UndoGroup("g")
	if err == nil {
		t.Fatal("UndoGroup should have failed because the move revert errors")
	}
	// The good change was reverted (file → "v1") then the rollback re-applied it
	// (file → "v2"). Net effect on disk: file back to "v2".
	data, _ := os.ReadFile(file)
	if string(data) != "v2" {
		t.Errorf("after failed group undo file = %q, want v2 (rollback must re-apply)", data)
	}
	// On failure the group is NOT removed from the tracker (removal happens only
	// after a fully successful revert), so both changes must still be listed.
	remaining := m.List()
	if len(remaining) != 2 {
		t.Errorf("tracker after failed group undo = %d changes, want 2 (group must not be dropped on failure)", len(remaining))
	}
}

// TestManagerUndoGroup_NoMatchingGroup covers the "no changes found for group"
// error branch of undoGroupLocked.
func TestManagerUndoGroup_NoMatchingGroup(t *testing.T) {
	m := NewManager()
	m.Record(FileChange{ID: "x", FilePath: "/x"}) // no group
	if _, err := m.UndoGroup("nonexistent"); err == nil {
		t.Error("UndoGroup with no matching changes should error")
	}
}

// TestApplyMove_MissingSource covers the redo-side error branch of applyMove
// (empty OldContent → "move redo: missing original source path"). Called
// directly since it's package-private.
func TestApplyMove_MissingSource(t *testing.T) {
	if err := applyMove(&FileChange{OldContent: nil}); err == nil {
		t.Error("applyMove with empty source should error")
	}
	// revertMove symmetric error branch.
	if err := revertMove(&FileChange{OldContent: nil}); err == nil {
		t.Error("revertMove with empty source should error")
	}
}
