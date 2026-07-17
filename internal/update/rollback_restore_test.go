package update

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeBinary creates an executable stand-in for the installed binary.
func writeFakeBinary(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
}

func TestRollbackManager_GetBackupDir(t *testing.T) {
	dir := t.TempDir()
	rm := NewRollbackManager(dir, 3)
	if rm.GetBackupDir() != dir {
		t.Errorf("GetBackupDir = %q, want %q", rm.GetBackupDir(), dir)
	}
}

func TestRollbackManager_RollbackToLatest_RestoresContent(t *testing.T) {
	binDir := t.TempDir()
	binaryPath := filepath.Join(binDir, "gokin")
	writeFakeBinary(t, binaryPath, "original-binary-content")

	rm := NewRollbackManager(t.TempDir(), 3)
	if _, err := rm.CreateBackup(binaryPath, "1.0.0"); err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Simulate a bad update clobbering the installed binary.
	writeFakeBinary(t, binaryPath, "corrupted-update-content")

	if err := rm.RollbackToLatest(); err != nil {
		t.Fatalf("RollbackToLatest: %v", err)
	}

	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read restored binary: %v", err)
	}
	if string(data) != "original-binary-content" {
		t.Errorf("restored content = %q, want original", string(data))
	}
}

func TestRollbackManager_RollbackToLatest_NoBackups(t *testing.T) {
	rm := NewRollbackManager(filepath.Join(t.TempDir(), "does-not-exist"), 3)
	err := rm.RollbackToLatest()
	if !errors.Is(err, ErrNoBackup) {
		t.Fatalf("err = %v, want ErrNoBackup", err)
	}
}

func TestRollbackManager_RollbackToBackup_MissingFile(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	backup := &BackupInfo{Path: filepath.Join(t.TempDir(), "gone")}
	if err := rm.RollbackToBackup(backup); !errors.Is(err, ErrRollbackFailed) {
		t.Fatalf("err = %v, want ErrRollbackFailed", err)
	}
}

func TestRollbackManager_Rollback_ByIDRestoresContent(t *testing.T) {
	binDir := t.TempDir()
	binaryPath := filepath.Join(binDir, "gokin")
	writeFakeBinary(t, binaryPath, "release-1.0.0")

	rm := NewRollbackManager(t.TempDir(), 3)
	info, err := rm.CreateBackup(binaryPath, "1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	writeFakeBinary(t, binaryPath, "broken-2.0.0")

	if err := rm.Rollback(info.ID); err != nil {
		t.Fatalf("Rollback(%q): %v", info.ID, err)
	}

	data, err := os.ReadFile(binaryPath)
	if err != nil {
		t.Fatalf("read restored binary: %v", err)
	}
	if string(data) != "release-1.0.0" {
		t.Errorf("restored content = %q, want release-1.0.0", string(data))
	}
}
