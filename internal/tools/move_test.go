package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/undo"
)

// End-to-end: move a file, then undo through the manager — content must be
// fully recovered at the source path. Before the v0.79 fix, MoveTool stored
// the source path in OldContent and the file content in NewContent, but the
// undo Manager treated this like any other change and AtomicWrote OldContent
// (the path string) into the dest file.
func TestMoveTool_UndoRestoresFile(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	source := filepath.Join(dir, "src.txt")
	dest := filepath.Join(dir, "dst.txt")
	original := []byte("important data")

	if err := os.WriteFile(source, original, 0644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	mgr := undo.NewManager()
	tool := NewMoveTool(dir)
	tool.SetUndoManager(mgr)

	res, err := tool.Execute(context.Background(), map[string]any{
		"source":      source,
		"destination": dest,
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("move failed: %s", res.Error)
	}

	// Sanity: file actually moved.
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("dest missing after move: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists after move: %v", err)
	}

	// Now undo.
	if _, err := mgr.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}

	got, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("source not restored after undo: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("source content = %q, want %q", got, original)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Errorf("dest still exists after undo: %v", err)
	}
}

// Move + undo + redo round-trip.
func TestMoveTool_RedoMovesForward(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	source := filepath.Join(dir, "src.txt")
	dest := filepath.Join(dir, "dst.txt")
	original := []byte("payload")

	if err := os.WriteFile(source, original, 0644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	mgr := undo.NewManager()
	tool := NewMoveTool(dir)
	tool.SetUndoManager(mgr)

	if _, err := tool.Execute(context.Background(), map[string]any{
		"source":      source,
		"destination": dest,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if _, err := mgr.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, err := mgr.Redo(); err != nil {
		t.Fatalf("Redo: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("dest missing after redo: %v", err)
	}
	if string(got) != string(original) {
		t.Errorf("dest content = %q, want %q", got, original)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Errorf("source still exists after redo: %v", err)
	}
}

// Directory moves intentionally do NOT record undo (recovering an arbitrary
// directory tree is unsafe). Verify the move still happens but no undo is
// queued.
func TestMoveTool_DirectoryMoveSkipsUndo(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	srcDir := filepath.Join(dir, "src-dir")
	dstDir := filepath.Join(dir, "dst-dir")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatalf("setup mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	mgr := undo.NewManager()
	tool := NewMoveTool(dir)
	tool.SetUndoManager(mgr)

	if _, err := tool.Execute(context.Background(), map[string]any{
		"source":      srcDir,
		"destination": dstDir,
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if mgr.CanUndo() {
		t.Error("directory move should not record an undo entry")
	}
	if _, err := os.Stat(filepath.Join(dstDir, "f.txt")); err != nil {
		t.Errorf("dest dir missing after directory move: %v", err)
	}
}
