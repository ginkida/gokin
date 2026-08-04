//go:build !windows

package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackStorageIsPrivate(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backups")
	if err := os.Mkdir(backupDir, 0o755); err != nil {
		t.Fatalf("create permissive backup dir: %v", err)
	}
	target := filepath.Join(t.TempDir(), "gokin")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	info, err := NewRollbackManager(backupDir, 3).CreateBackup(target, "v1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	dirInfo, err := os.Stat(backupDir)
	if err != nil {
		t.Fatalf("stat backup dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Errorf("backup dir mode = %o, want 700", got)
	}
	metadataInfo, err := os.Stat(info.Path + ".json")
	if err != nil {
		t.Fatalf("stat metadata: %v", err)
	}
	if got := metadataInfo.Mode().Perm(); got != 0o600 {
		t.Errorf("metadata mode = %o, want 600", got)
	}
}

func TestRollbackRejectsSymlinkBackupAndMetadata(t *testing.T) {
	backupDir := t.TempDir()
	target := filepath.Join(t.TempDir(), "gokin")
	if err := os.WriteFile(target, []byte("original"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	rm := NewRollbackManager(backupDir, 3)
	info, err := rm.CreateBackup(target, "v1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("original"), 0o755); err != nil {
		t.Fatalf("write external: %v", err)
	}
	if err := os.Remove(info.Path); err != nil {
		t.Fatalf("remove backup: %v", err)
	}
	if err := os.Symlink(external, info.Path); err != nil {
		t.Fatalf("symlink backup: %v", err)
	}
	if err := rm.RollbackToBackup(info); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink backup error = %v", err)
	}

	if err := os.Remove(info.Path); err != nil {
		t.Fatalf("remove backup symlink: %v", err)
	}
	if err := os.WriteFile(info.Path, []byte("original"), 0o755); err != nil {
		t.Fatalf("restore backup fixture: %v", err)
	}
	if err := os.Remove(info.Path + ".json"); err != nil {
		t.Fatalf("remove metadata: %v", err)
	}
	if err := os.Symlink(external, info.Path+".json"); err != nil {
		t.Fatalf("symlink metadata: %v", err)
	}
	if _, err := rm.LoadBackupInfo(info.Path); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink metadata error = %v", err)
	}
}

func TestCreateBackupRejectsSymlinkSource(t *testing.T) {
	realSource := filepath.Join(t.TempDir(), "real-gokin")
	if err := os.WriteFile(realSource, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write real source: %v", err)
	}
	symlinkSource := filepath.Join(t.TempDir(), "gokin")
	if err := os.Symlink(realSource, symlinkSource); err != nil {
		t.Fatalf("symlink source: %v", err)
	}
	backupDir := t.TempDir()
	if _, err := NewRollbackManager(backupDir, 3).CreateBackup(symlinkSource, "v1"); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink source error = %v", err)
	}
}
