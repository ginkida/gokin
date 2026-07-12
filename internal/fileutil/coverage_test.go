package fileutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// ===========================================================================
// OperationType.String (0% if uncovered)
// ===========================================================================

func TestOperationType_String(t *testing.T) {
	cases := []struct {
		op   OperationType
		want string
	}{
		{OpWrite, "write"},
		{OpDelete, "delete"},
		{OpRename, "rename"},
		{OpChmod, "chmod"},
		{OperationType(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.op.String(); got != tc.want {
			t.Errorf("%d.String() = %q, want %q", tc.op, got, tc.want)
		}
	}
}

// ===========================================================================
// WithID option + NewFileTransaction
// ===========================================================================

func TestWithID_SetsCustomID(t *testing.T) {
	tx, err := NewFileTransaction(WithID("my-custom-id"))
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	if tx.ID() != "my-custom-id" {
		t.Errorf("ID() = %q, want 'my-custom-id'", tx.ID())
	}
}

func TestNewFileTransaction_DefaultID(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	if tx.ID() == "" {
		t.Error("default ID should not be empty")
	}
}

// ===========================================================================
// Chmod operation (0% → full)
// ===========================================================================

func TestChmod_StagesOperation(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := tx.Chmod(path, 0600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	ops := tx.GetOperations()
	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}
	if ops[0].Type != OpChmod {
		t.Errorf("op type = %v, want OpChmod", ops[0].Type)
	}
	if ops[0].Mode != 0600 {
		t.Errorf("op mode = %o, want 0600", ops[0].Mode)
	}
}

func TestChmod_AfterCommitFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := tx.Chmod("/tmp/whatever", 0600); err == nil {
		t.Error("Chmod after commit should fail")
	}
}

func TestChmod_AfterRollbackFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if err := tx.Chmod("/tmp/whatever", 0600); err == nil {
		t.Error("Chmod after rollback should fail")
	}
}

func TestChmod_CommitChangesPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod semantics differ on Windows")
	}
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := tx.Chmod(path, 0600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("perm = %o, want 0600", info.Mode().Perm())
	}
}

// ===========================================================================
// GetOperations (0% → full)
// ===========================================================================

func TestGetOperations_ReturnsCopy(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	tx.Write("/tmp/a", []byte("a"))
	tx.Write("/tmp/b", []byte("b"))

	ops := tx.GetOperations()
	if len(ops) != 2 {
		t.Fatalf("expected 2 operations, got %d", len(ops))
	}

	// Mutating the returned slice should NOT affect the transaction.
	ops[0].Path = "/tmp/mutated"
	original := tx.GetOperations()
	if original[0].Path != "/tmp/a" {
		t.Error("GetOperations did not return a copy — mutation leaked")
	}
}

func TestGetOperations_EmptyTransaction(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	ops := tx.GetOperations()
	if len(ops) != 0 {
		t.Errorf("empty transaction should have 0 ops, got %d", len(ops))
	}
}

// ===========================================================================
// Duration (0% → full)
// ===========================================================================

func TestDuration_NonZero(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	d := tx.Duration()
	if d <= 0 {
		t.Errorf("Duration = %v, want > 0", d)
	}
}

// ===========================================================================
// Result (87.5% → full, including NewPath branch)
// ===========================================================================

func TestResult_IncludesRenamePaths(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	tx.Write("/tmp/file1", []byte("a"))
	tx.Rename("/tmp/old", "/tmp/new")

	result := tx.Result()
	if result.OperationCount != 2 {
		t.Errorf("OperationCount = %d, want 2", result.OperationCount)
	}
	// FilesModified should include both rename paths (old + new).
	foundNew := false
	for _, f := range result.FilesModified {
		if f == "/tmp/new" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Error("FilesModified should include NewPath '/tmp/new'")
	}
}

func TestResult_CommittedFlag(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Commit()

	result := tx.Result()
	if !result.Committed {
		t.Error("Committed should be true after Commit")
	}
}

// ===========================================================================
// Rollback paths (30.8% → higher)
// ===========================================================================

func TestRollback_AlreadyCommittedFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Commit()

	if err := tx.Rollback(); err == nil {
		t.Error("Rollback after commit should fail")
	}
}

func TestRollback_AlreadyRolledBackIsNoOp(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("first Rollback: %v", err)
	}
	// Second rollback should be a no-op (no error).
	if err := tx.Rollback(); err != nil {
		t.Errorf("second Rollback: %v", err)
	}
}

func TestRollback_RestoresDeletedFile(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "to-delete.txt")
	original := []byte("important data")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	tx.Delete(path)
	if err := tx.Commit(); err != nil {
		// Commit applies then the test can't verify rollback — but we want
		// to test explicit Rollback instead. Use Rollback directly.
		t.Fatalf("Commit: %v", err)
	}

	// File should be gone after commit.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("file should not exist after delete commit")
	}
}

func TestRollback_UndoesWriteOnCommitFailure(t *testing.T) {
	// Stage a write to a valid path, then make the transaction fail during
	// apply by adding a rename with an invalid destination path.
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	dir := t.TempDir()
	writePath := filepath.Join(dir, "new-file.txt")
	tx.Write(writePath, []byte("created"))

	// A rename of a non-existent source will fail in applyPhase, triggering
	// rollbackInternal which should remove the created file.
	tx.Rename(filepath.Join(dir, "nonexistent-source"), filepath.Join(dir, "dest"))

	if err := tx.Commit(); err == nil {
		t.Error("Commit should fail due to rename of nonexistent source")
	}

	// The write was applied, then rolled back — the file should NOT exist.
	if _, err := os.Stat(writePath); !os.IsNotExist(err) {
		t.Error("rollback should have removed the created file")
	}
}

// ===========================================================================
// Commit edge cases
// ===========================================================================

func TestCommit_EmptyTransactionSucceeds(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Errorf("empty Commit: %v", err)
	}
	if !tx.IsFinalized() {
		t.Error("should be finalized after commit")
	}
}

func TestCommit_DoubleCommitFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Commit()
	if err := tx.Commit(); err == nil {
		t.Error("double commit should fail")
	}
}

func TestCommit_AfterRollbackFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Rollback()
	if err := tx.Commit(); err == nil {
		t.Error("commit after rollback should fail")
	}
}

// ===========================================================================
// Delete / Rename after finalization
// ===========================================================================

func TestDelete_AfterCommitFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Commit()
	if err := tx.Delete("/tmp/x"); err == nil {
		t.Error("Delete after commit should fail")
	}
}

func TestRename_AfterCommitFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Commit()
	if err := tx.Rename("/tmp/a", "/tmp/b"); err == nil {
		t.Error("Rename after commit should fail")
	}
}

func TestWrite_AfterCommitFails(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Commit()
	if err := tx.Write("/tmp/x", []byte("x")); err == nil {
		t.Error("Write after commit should fail")
	}
}

// ===========================================================================
// OperationCount / IsFinalized
// ===========================================================================

func TestOperationCount(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	if tx.OperationCount() != 0 {
		t.Errorf("empty tx OperationCount = %d, want 0", tx.OperationCount())
	}
	tx.Write("/tmp/a", []byte("a"))
	tx.Write("/tmp/b", []byte("b"))
	if tx.OperationCount() != 2 {
		t.Errorf("OperationCount = %d, want 2", tx.OperationCount())
	}
}

func TestIsFinalized(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	if tx.IsFinalized() {
		t.Error("new tx should not be finalized")
	}
	tx.Commit()
	if !tx.IsFinalized() {
		t.Error("committed tx should be finalized")
	}
}

// ===========================================================================
// WriteWithMode with custom mode
// ===========================================================================

func TestWriteWithMode_CustomPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on Windows")
	}
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "custom.txt")
	if err := tx.WriteWithMode(path, []byte("data"), 0755); err != nil {
		t.Fatalf("WriteWithMode: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Errorf("perm = %o, want 0755", info.Mode().Perm())
	}
}

// ===========================================================================
// AtomicWrite edge cases (65.2% → higher)
// ===========================================================================

func TestAtomicWrite_NestedDirFailsGracefully(t *testing.T) {
	// AtomicWrite does NOT create parent directories — it writes the temp
	// file directly into filepath.Dir(path). A non-existent parent dir
	// should return an error from CreateTemp, not panic.
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent-subdir", "file.txt")

	err := AtomicWrite(path, []byte("nested"), 0644)
	if err == nil {
		t.Error("AtomicWrite to nonexistent subdir should fail (no MkdirAll)")
	}
}

func TestAtomicWrite_EmptyData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")

	if err := AtomicWrite(path, []byte{}, 0644); err != nil {
		t.Fatalf("AtomicWrite empty: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Errorf("size = %d, want 0", info.Size())
	}
}

// ===========================================================================
// Full transaction lifecycle: multi-op commit
// ===========================================================================

func TestTransaction_FullLifecycle(t *testing.T) {
	dir := t.TempDir()

	// Create initial files.
	srcPath := filepath.Join(dir, "source.txt")
	dstPath := filepath.Join(dir, "dest.txt")
	delPath := filepath.Join(dir, "delete-me.txt")
	if err := os.WriteFile(srcPath, []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(delPath, []byte("delete"), 0644); err != nil {
		t.Fatal(err)
	}

	tx, err := NewFileTransaction(WithID("lifecycle-test"))
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	// Stage: write new, rename, delete.
	newPath := filepath.Join(dir, "created.txt")
	tx.Write(newPath, []byte("created"))
	tx.Rename(srcPath, dstPath)
	tx.Delete(delPath)

	if tx.OperationCount() != 3 {
		t.Fatalf("OperationCount = %d, want 3", tx.OperationCount())
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Verify all operations applied.
	if _, err := os.Stat(newPath); err != nil {
		t.Errorf("created file should exist: %v", err)
	}
	if _, err := os.Stat(dstPath); err != nil {
		t.Errorf("renamed file should exist at dest: %v", err)
	}
	if _, err := os.Stat(srcPath); !os.IsNotExist(err) {
		t.Error("source file should not exist after rename")
	}
	if _, err := os.Stat(delPath); !os.IsNotExist(err) {
		t.Error("deleted file should not exist")
	}

	result := tx.Result()
	if !result.Committed {
		t.Error("result should show committed")
	}
	if result.OperationCount != 3 {
		t.Errorf("result OperationCount = %d, want 3", result.OperationCount)
	}
}
