package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// blocks edit when tracker has no record for the target file and the
// invariant is enabled.
func TestEditTool_BlocksWithoutPriorRead(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tracker := NewFileReadTracker()
	et := NewEditTool(dir)
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(true)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success {
		t.Fatal("expected failure when file wasn't read first")
	}
	if !strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("error must name the invariant, got: %q", result.Error)
	}

	// File must be untouched.
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "Foo") {
		t.Errorf("file modified despite blocked edit: %s", data)
	}
}

// allows edit after a valid read is recorded.
func TestEditTool_AllowsAfterPriorRead(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tracker := NewFileReadTracker()
	// Simulate a prior read.
	tracker.CheckAndRecord(target, 1, 100, 25)

	et := NewEditTool(dir)
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(true)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success; got error: %s", result.Error)
	}
	data, _ := os.ReadFile(target)
	if !strings.Contains(string(data), "Bar") {
		t.Errorf("edit didn't apply: %s", data)
	}
}

// invariant off → edit passes even without prior read (backcompat path).
func TestEditTool_DisabledSkipsCheck(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tracker := NewFileReadTracker()
	et := NewEditTool(dir)
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(false) // explicitly off

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil || !result.Success {
		t.Fatalf("expected success with invariant off, got err=%v success=%v err=%s",
			err, result.Success, result.Error)
	}
}

// nil tracker (e.g. stripped-down test harness) must not panic or block.
func TestEditTool_NilTrackerIsNoop(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	et := NewEditTool(dir)
	et.SetRequireReadBeforeEdit(true) // invariant on but tracker missing

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil || !result.Success {
		t.Fatalf("nil tracker must skip check silently; err=%v success=%v", err, result.Success)
	}
}

// After InvalidateFile (called post-write/edit), a subsequent edit must
// require a fresh read. This encodes the "every mutation resets the
// read-knowledge" invariant.
func TestEditTool_InvalidatedFileRequiresReRead(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tracker := NewFileReadTracker()
	tracker.CheckAndRecord(target, 1, 100, 25)
	// Simulate what the executor does after a write/edit.
	tracker.InvalidateFile(target)

	et := NewEditTool(dir)
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(true)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "Foo",
		"new_string": "Bar",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("expected block after invalidation")
	}
	if !strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("missing invariant marker: %q", result.Error)
	}
}

// Nonexistent target must fall through to the normal "file not found"
// diagnostic, not get shadowed by the read-before-edit message.
func TestEditTool_NonexistentFileBypassesCheck(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "never-created.go")

	tracker := NewFileReadTracker()
	et := NewEditTool(dir)
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(true)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path":  target,
		"old_string": "a",
		"new_string": "b",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Fatal("expected an error for missing file")
	}
	if strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("should surface file-not-found, not read-before-edit: %q", result.Error)
	}
	if !strings.Contains(result.Error, "file not found") {
		t.Errorf("expected 'file not found' diagnostic, got: %q", result.Error)
	}
}

// The invariant must cover every edit sub-mode, not just string-replace.
// We spot-check multi-edit here — it's the mode most likely to slip past
// a single-path guard since it has its own Execute branch.
func TestEditTool_MultiEditRespectsInvariant(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "foo.go")
	if err := os.WriteFile(target, []byte("package x\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tracker := NewFileReadTracker()
	et := NewEditTool(dir)
	et.SetReadTracker(tracker)
	et.SetRequireReadBeforeEdit(true)

	result, err := et.Execute(context.Background(), map[string]any{
		"file_path": target,
		"edits": []any{
			map[string]any{"old_string": "Foo", "new_string": "Bar"},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success {
		t.Error("multi-edit must also be blocked without prior read")
	}
	if !strings.Contains(result.Error, "read-before-edit") {
		t.Errorf("expected invariant marker in multi-edit path: %q", result.Error)
	}
}

func TestFileReadTracker_HasBeenReadSemantics(t *testing.T) {
	tracker := NewFileReadTracker()
	dir := testkit.ResolvedTempDir(t)
	target := filepath.Join(dir, "x.go")
	if err := os.WriteFile(target, []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if tracker.HasBeenRead(target) {
		t.Error("fresh tracker should not report read")
	}
	tracker.CheckAndRecord(target, 1, 100, 10)
	if !tracker.HasBeenRead(target) {
		t.Error("record not registered")
	}
	tracker.InvalidateFile(target)
	if tracker.HasBeenRead(target) {
		t.Error("invalidation should drop the record")
	}
	if tracker.HasBeenRead("") {
		t.Error("empty path must return false")
	}
}
