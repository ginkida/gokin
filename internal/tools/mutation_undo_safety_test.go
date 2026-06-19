package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/testkit"
	"gokin/internal/undo"
)

// TestCopyRefusesExistingDestination pins bug #1: copy used to O_TRUNC over an
// existing destination and record the undo as a NEW file (WasNew=true), so /undo
// would then DELETE the destination with its original content captured nowhere —
// unrecoverable data loss. It must now refuse and leave the destination intact.
func TestCopyRefusesExistingDestination(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	if err := os.WriteFile(src, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("PRECIOUS destination"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewCopyTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"source":      src,
		"destination": dst,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatalf("copy onto existing dest must fail, got success: %s", res.Content)
	}
	// The destination's original content must be untouched.
	if b, _ := os.ReadFile(dst); string(b) != "PRECIOUS destination" {
		t.Errorf("destination clobbered: %q", string(b))
	}
}

// TestBatchRenameUndoRenamesBack pins bug #2: batch rename used to record the
// undo as undo.NewFileChange(path, "batch_rename", []byte(newPath), nil, false),
// so /undo wrote the destination-path STRING as the renamed file's content (and
// left the renamed file orphaned). It must now reverse via os.Rename.
func TestBatchRenameUndoRenamesBack(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	orig := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(orig, []byte("real content"), 0644); err != nil {
		t.Fatal(err)
	}

	m := undo.NewManager()
	bt := NewBatchTool(dir)
	bt.SetUndoManager(m)

	res := bt.executeRename(context.Background(), []string{orig}, "old", "new", false, false)
	if len(res.Failed) != 0 {
		t.Fatalf("rename failed: %v", res.Failed)
	}
	renamed := filepath.Join(dir, "new.txt")
	if _, err := os.Stat(renamed); err != nil {
		t.Fatalf("renamed file should exist: %v", err)
	}

	// Undo must restore old.txt with its REAL content, and remove new.txt.
	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	data, err := os.ReadFile(orig)
	if err != nil {
		t.Fatalf("original should be restored: %v", err)
	}
	if string(data) != "real content" {
		t.Errorf("undo wrote path-string instead of renaming back: %q", string(data))
	}
	if _, err := os.Stat(renamed); !os.IsNotExist(err) {
		t.Errorf("renamed file should be gone after undo: %v", err)
	}
}

// TestMultiEditAmbiguousOldStringNoDiskWrite pins bug #6: executeMultiEdit's
// per-edit replace counted matches but only aborted on count==0; an old_string
// matching MORE than once silently replaced ALL occurrences. It must now abort
// with an explicit "not unique" error and write NOTHING to disk.
func TestMultiEditAmbiguousOldStringNoDiskWrite(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	path := filepath.Join(dir, "f.go")
	original := "x := 1\ny := 1\nz := 9\n" // "1" appears twice
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewEditTool(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path": path,
		"edits": []any{
			map[string]any{"old_string": "1", "new_string": "2"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success {
		t.Fatalf("ambiguous multi-edit must fail, got success: %s", res.Content)
	}
	// Disk must be untouched.
	if b, _ := os.ReadFile(path); string(b) != original {
		t.Errorf("file was modified despite ambiguous edit: %q", string(b))
	}
}

// TestEditUndoPreservesExecBit pins bug #4 end-to-end through the edit tool:
// editing a 0755 script then /undo must restore 0755, not silently strip to 0644.
func TestEditUndoPreservesExecBit(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	path := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}

	m := undo.NewManager()
	tool := NewEditTool(dir)
	tool.SetUndoManager(m)

	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path":  path,
		"old_string": "echo hi",
		"new_string": "echo bye",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("edit failed: %s", res.Error)
	}
	// Forward write already preserves perm; the regression is on undo.
	if _, err := m.Undo(); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0755 {
		t.Errorf("undo stripped exec bit: mode = %o, want 0755", got)
	}
}
