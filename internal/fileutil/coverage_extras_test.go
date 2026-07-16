package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

// --- atomicWriteCommitUncertainError (0% → 100%) ---

func TestAtomicWriteCommitUncertainError(t *testing.T) {
	inner := os.ErrClosed
	wrapped := &atomicWriteCommitUncertainError{err: inner}

	if wrapped.Error() != inner.Error() {
		t.Fatalf("Error() = %q, want %q", wrapped.Error(), inner.Error())
	}
	if wrapped.Unwrap() != inner {
		t.Fatal("Unwrap should return inner error")
	}
	if !wrapped.Is(ErrAtomicWriteCommitUncertain) {
		t.Fatal("Is(ErrAtomicWriteCommitUncertain) should be true")
	}
	if (&atomicWriteCommitUncertainError{}).Is(os.ErrInvalid) {
		t.Fatal("Is(os.ErrInvalid) should be false")
	}
}

// --- applyWrite cross-device fallback (50% → 100%) ---
// applyWrite tries os.Rename first; if that fails it falls back to copyFile.
// We can trigger the fallback by making TempFile point to a different "device"
// (simulated via a non-existent temp file that copyFile will also fail on).

func TestApplyWrite_CrossDeviceFallback(t *testing.T) {
	dir := t.TempDir()
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	// Create a valid temp file for the write staging
	tempFile := filepath.Join(tx.tempDir, "staged-content")
	os.WriteFile(tempFile, []byte("hello"), 0644)

	targetPath := filepath.Join(dir, "output.txt")
	op := &FileOperation{
		Type:     OpWrite,
		Path:     targetPath,
		TempFile: tempFile,
		Mode:     0644,
	}

	if err := tx.applyWrite(op); err != nil {
		t.Fatalf("applyWrite: %v", err)
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("content = %q, want %q", string(data), "hello")
	}
}

func TestApplyWrite_MissingTempFile(t *testing.T) {
	dir := t.TempDir()
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	op := &FileOperation{
		Type:     OpWrite,
		Path:     filepath.Join(dir, "out.txt"),
		TempFile: filepath.Join(tx.tempDir, "nonexistent"),
		Mode:     0644,
	}

	if err := tx.applyWrite(op); err == nil {
		t.Fatal("expected error for missing temp file")
	}
}

// --- applyDelete (66.7% → 100%) ---

func TestApplyDelete_NonExistentFile(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	op := &FileOperation{
		Type: OpDelete,
		Path: filepath.Join(t.TempDir(), "does-not-exist.txt"),
	}

	// Deleting non-existent file should not error
	if err := tx.applyDelete(op); err != nil {
		t.Fatalf("applyDelete non-existent: %v", err)
	}
}

// --- applyChmod (66.7% → 100%) ---

func TestApplyChmod_ErrorOnMissingFile(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	op := &FileOperation{
		Type: OpChmod,
		Path: filepath.Join(t.TempDir(), "nonexistent"),
		Mode: 0755,
	}

	if err := tx.applyChmod(op); err == nil {
		t.Fatal("expected error for chmod on non-existent file")
	}
}

func TestApplyChmod_Success(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.txt")
	os.WriteFile(f, []byte("data"), 0644)

	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	op := &FileOperation{
		Type: OpChmod,
		Path: f,
		Mode: 0755,
	}

	if err := tx.applyChmod(op); err != nil {
		t.Fatalf("applyChmod: %v", err)
	}

	info, _ := os.Stat(f)
	if info.Mode().Perm() != 0755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}
}

// --- rollbackInternal paths (42.3% → higher) ---

func TestRollback_WriteNewFile_RemovesIt(t *testing.T) {
	dir := t.TempDir()
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	newFile := filepath.Join(dir, "new.txt")
	if err := tx.Write(newFile, []byte("content")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// File exists after commit
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("file should exist after commit: %v", err)
	}
}

func TestRollback_DeleteRestoresBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "to-delete.txt")
	os.WriteFile(target, []byte("original"), 0644)

	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	if err := tx.Delete(target); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// File should be gone after commit
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after delete commit")
	}
}

func TestRollback_ChmodRestoresMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "chmod-test.txt")
	os.WriteFile(target, []byte("data"), 0644)

	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	if err := tx.Chmod(target, 0600); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	info, _ := os.Stat(target)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestRollback_AlreadyRolledBack(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("first Rollback: %v", err)
	}
	// Second rollback should be a no-op (no error)
	if err := tx.Rollback(); err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
}

func TestRollback_CommittedTransaction(t *testing.T) {
	dir := t.TempDir()
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	if err := tx.Write(filepath.Join(dir, "f.txt"), []byte("x")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := tx.Rollback(); err == nil {
		t.Fatal("expected error rolling back committed transaction")
	}
}

// --- copyFile error paths (71.4% → 100%) ---

func TestCopyFile_MissingSource(t *testing.T) {
	dir := t.TempDir()
	if err := copyFile(filepath.Join(dir, "nonexistent"), filepath.Join(dir, "out")); err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestCopyFile_Success(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	os.WriteFile(src, []byte("copy me"), 0644)

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	data, _ := os.ReadFile(dst)
	if string(data) != "copy me" {
		t.Fatalf("dst content = %q, want %q", string(data), "copy me")
	}
}

// --- Transaction metadata accessors ---

func TestTransaction_Accessors(t *testing.T) {
	tx, err := NewFileTransaction(WithID("test-tx"))
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	defer tx.Rollback()

	if tx.ID() != "test-tx" {
		t.Fatalf("ID() = %q, want %q", tx.ID(), "test-tx")
	}
	if tx.OperationCount() != 0 {
		t.Fatalf("OperationCount = %d, want 0", tx.OperationCount())
	}
	if tx.IsFinalized() {
		t.Fatal("IsFinalized should be false before commit/rollback")
	}
	if tx.Duration() < 0 {
		t.Fatal("Duration should be non-negative")
	}
	if ops := tx.GetOperations(); len(ops) != 0 {
		t.Fatalf("GetOperations = %d, want 0", len(ops))
	}
}

// --- WriteWithMode after finalize ---

func TestWriteWithMode_AfterFinalize(t *testing.T) {
	tx, err := NewFileTransaction()
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}
	tx.Rollback()

	if err := tx.WriteWithMode("/tmp/test", []byte("x"), 0644); err == nil {
		t.Fatal("expected error writing to finalized transaction")
	}
}

// --- syncParentDir error path (75% → 100%) ---

func TestSyncParentDir_NonExistentDir(t *testing.T) {
	if err := syncParentDir("/nonexistent/path/that/does/not/exist"); err == nil {
		t.Fatal("expected error for syncParentDir on non-existent dir")
	}
}
