package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := AtomicWrite(path, []byte("hello world"), 0644)
	if err != nil {
		t.Fatalf("AtomicWrite failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("content = %q, want %q", string(data), "hello world")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}
	if info.Mode().Perm() != 0644 {
		t.Errorf("permissions = %o, want 0644", info.Mode().Perm())
	}
}

func TestAtomicWriteOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	AtomicWrite(path, []byte("first"), 0644)
	AtomicWrite(path, []byte("second"), 0644)

	data, _ := os.ReadFile(path)
	if string(data) != "second" {
		t.Errorf("content = %q, want %q", string(data), "second")
	}
}

func TestAtomicWriteString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := AtomicWriteString(path, "string content", 0644)
	if err != nil {
		t.Fatalf("AtomicWriteString failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "string content" {
		t.Errorf("content = %q, want %q", string(data), "string content")
	}
}

func TestAtomicWriteNoTempLeftOnSuccess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	AtomicWrite(path, []byte("data"), 0644)

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "test.txt" {
			t.Errorf("unexpected file left: %s", e.Name())
		}
	}
}

func TestAtomicWriteInvalidDir(t *testing.T) {
	err := AtomicWrite("/nonexistent/dir/file.txt", []byte("data"), 0644)
	if err == nil {
		t.Error("should fail for nonexistent directory")
	}
}

// --- FileTransaction tests ---

func TestOperationTypeString(t *testing.T) {
	tests := []struct {
		op   OperationType
		want string
	}{
		{OpWrite, "write"},
		{OpDelete, "delete"},
		{OpRename, "rename"},
		{OpChmod, "chmod"},
		{OperationType(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.op.String(); got != tt.want {
			t.Errorf("OperationType(%d).String() = %q, want %q", tt.op, got, tt.want)
		}
	}
}

func TestFileTransactionWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tx-write.txt")

	tx, err := NewFileTransaction(WithID("test-write"))
	if err != nil {
		t.Fatalf("NewFileTransaction: %v", err)
	}

	if tx.ID() != "test-write" {
		t.Errorf("ID = %q, want test-write", tx.ID())
	}

	if err := tx.Write(path, []byte("transaction content")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if tx.OperationCount() != 1 {
		t.Errorf("OperationCount = %d, want 1", tx.OperationCount())
	}

	// File should NOT exist before commit
	if _, err := os.Stat(path); err == nil {
		t.Error("file should not exist before commit")
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// File should exist after commit
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after commit: %v", err)
	}
	if string(data) != "transaction content" {
		t.Errorf("content = %q", string(data))
	}

	if !tx.IsFinalized() {
		t.Error("should be finalized after commit")
	}
}

func TestFileTransactionDelete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "to-delete.txt")
	os.WriteFile(path, []byte("delete me"), 0644)

	tx, _ := NewFileTransaction()
	tx.Delete(path)
	tx.Commit()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("file should be deleted after commit")
	}
}

func TestFileTransactionRename(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.txt")
	newPath := filepath.Join(dir, "new.txt")
	os.WriteFile(oldPath, []byte("rename me"), 0644)

	tx, _ := NewFileTransaction()
	tx.Rename(oldPath, newPath)
	tx.Commit()

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old file should not exist")
	}
	data, _ := os.ReadFile(newPath)
	if string(data) != "rename me" {
		t.Errorf("new file content = %q", string(data))
	}
}

func TestFileTransactionRollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollback.txt")
	os.WriteFile(path, []byte("original"), 0644)

	tx, _ := NewFileTransaction()
	tx.Write(path, []byte("modified"))

	// Rollback before commit — file should stay original
	tx.Rollback()

	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Errorf("after rollback content = %q, want original", string(data))
	}

	if !tx.IsFinalized() {
		t.Error("should be finalized after rollback")
	}
}

func TestFileTransactionDoubleCommit(t *testing.T) {
	tx, _ := NewFileTransaction()
	tx.Commit()

	err := tx.Commit()
	if err == nil {
		t.Error("double commit should error")
	}
}

func TestFileTransactionCommitAfterRollback(t *testing.T) {
	tx, _ := NewFileTransaction()
	tx.Rollback()

	err := tx.Commit()
	if err == nil {
		t.Error("commit after rollback should error")
	}
}

func TestFileTransactionWriteAfterFinalize(t *testing.T) {
	dir := t.TempDir()
	tx, _ := NewFileTransaction()
	tx.Commit()

	err := tx.Write(filepath.Join(dir, "late.txt"), []byte("too late"))
	if err == nil {
		t.Error("write after commit should error")
	}
}

func TestFileTransactionResult(t *testing.T) {
	dir := t.TempDir()
	tx, _ := NewFileTransaction(WithID("result-test"))

	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	tx.Write(path1, []byte("a"))
	tx.Write(path2, []byte("b"))
	tx.Commit()

	result := tx.Result()
	if result.ID != "result-test" {
		t.Errorf("Result.ID = %q", result.ID)
	}
	if !result.Committed {
		t.Error("should be committed")
	}
	if result.OperationCount != 2 {
		t.Errorf("OperationCount = %d, want 2", result.OperationCount)
	}
	if len(result.FilesModified) != 2 {
		t.Errorf("FilesModified len = %d, want 2", len(result.FilesModified))
	}
}

func TestFileTransactionEmptyCommit(t *testing.T) {
	tx, _ := NewFileTransaction()
	if err := tx.Commit(); err != nil {
		t.Errorf("empty commit should succeed: %v", err)
	}
}

func TestFileTransactionMultiOps(t *testing.T) {
	dir := t.TempDir()
	writeFile := filepath.Join(dir, "write.txt")
	deleteFile := filepath.Join(dir, "delete.txt")
	renameOld := filepath.Join(dir, "rename-old.txt")
	renameNew := filepath.Join(dir, "rename-new.txt")

	os.WriteFile(deleteFile, []byte("delete me"), 0644)
	os.WriteFile(renameOld, []byte("rename me"), 0644)

	tx, _ := NewFileTransaction()
	tx.Write(writeFile, []byte("created"))
	tx.Delete(deleteFile)
	tx.Rename(renameOld, renameNew)
	tx.Commit()

	// write.txt exists
	if data, err := os.ReadFile(writeFile); err != nil || string(data) != "created" {
		t.Errorf("write.txt: %v %q", err, data)
	}
	// delete.txt gone
	if _, err := os.Stat(deleteFile); !os.IsNotExist(err) {
		t.Error("delete.txt should be gone")
	}
	// rename-new.txt exists
	if data, err := os.ReadFile(renameNew); err != nil || string(data) != "rename me" {
		t.Errorf("rename-new.txt: %v %q", err, data)
	}
}
