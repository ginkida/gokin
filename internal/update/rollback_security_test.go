package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCreateBackupSanitizesRemoteVersionAndStaysInDirectory(t *testing.T) {
	root := t.TempDir()
	backupDir := filepath.Join(root, "backups")
	sourceDir := t.TempDir()
	source := filepath.Join(sourceDir, "gokin")
	if err := os.WriteFile(source, []byte("trusted binary"), 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}

	version := "../../outside/evil release"
	info, err := NewRollbackManager(backupDir, 3).CreateBackup(source, version)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if filepath.Dir(info.Path) != backupDir {
		t.Fatalf("backup escaped directory: %q", info.Path)
	}
	if !isManagedBackupName(filepath.Base(info.Path)) || strings.Contains(filepath.Base(info.Path), "..") {
		t.Fatalf("unsafe backup name: %q", filepath.Base(info.Path))
	}
	if info.Version != version {
		t.Fatalf("diagnostic version changed: %q", info.Version)
	}
	if _, err := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(err) {
		t.Fatalf("version traversal created an outside path: %v", err)
	}
}

func TestCreateBackupConcurrentCallsAreExclusive(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "backups")
	source := filepath.Join(t.TempDir(), "gokin")
	content := []byte("concurrent source binary")
	if err := os.WriteFile(source, content, 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}
	rm := NewRollbackManager(backupDir, 32)

	const workers = 16
	infos := make(chan *BackupInfo, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			info, err := rm.CreateBackup(source, "v1.2.3")
			if err != nil {
				errs <- err
				return
			}
			infos <- info
		}()
	}
	wg.Wait()
	close(errs)
	close(infos)
	for err := range errs {
		t.Errorf("CreateBackup: %v", err)
	}

	seenIDs := make(map[string]bool)
	seenPaths := make(map[string]bool)
	for info := range infos {
		if seenIDs[info.ID] || seenPaths[info.Path] {
			t.Fatalf("duplicate concurrent backup: %+v", info)
		}
		seenIDs[info.ID] = true
		seenPaths[info.Path] = true
		got, err := os.ReadFile(info.Path)
		if err != nil {
			t.Fatalf("read backup: %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("backup was overwritten: %q", got)
		}
	}
	if len(seenIDs) != workers {
		t.Fatalf("created backups = %d, want %d", len(seenIDs), workers)
	}
}

func TestRollbackRejectsTamperedBackupAndPreservesTarget(t *testing.T) {
	target := filepath.Join(t.TempDir(), "gokin")
	if err := os.WriteFile(target, []byte("original-content"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	rm := NewRollbackManager(t.TempDir(), 3)
	info, err := rm.CreateBackup(target, "v1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if err := os.WriteFile(target, []byte("broken--content"), 0o755); err != nil {
		t.Fatalf("write broken target: %v", err)
	}
	if err := os.WriteFile(info.Path, []byte("tampered-content"), 0o755); err != nil {
		t.Fatalf("tamper backup: %v", err)
	}

	err = rm.Rollback(info.ID)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Rollback error = %v, want checksum rejection", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read target: %v", readErr)
	}
	if string(got) != "broken--content" {
		t.Fatalf("failed rollback modified target: %q", got)
	}
}

func TestLoadBackupInfoRejectsPathSubstitutionAndOversize(t *testing.T) {
	target := filepath.Join(t.TempDir(), "gokin")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	rm := NewRollbackManager(t.TempDir(), 3)
	info, err := rm.CreateBackup(target, "v1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	infoPath := info.Path + ".json"

	substituted := *info
	substituted.Path = filepath.Join(t.TempDir(), "gokin-outside")
	data, err := json.Marshal(&substituted)
	if err != nil {
		t.Fatalf("marshal substituted metadata: %v", err)
	}
	if err := os.WriteFile(infoPath, data, 0o600); err != nil {
		t.Fatalf("write substituted metadata: %v", err)
	}
	if _, err := rm.LoadBackupInfo(info.Path); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("path substitution error = %v", err)
	}

	if err := os.WriteFile(infoPath, []byte(strings.Repeat("x", int(maxBackupInfoBytes)+1)), 0o600); err != nil {
		t.Fatalf("write oversized metadata: %v", err)
	}
	if _, err := rm.LoadBackupInfo(info.Path); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized metadata error = %v", err)
	}
}

func TestSaveBackupInfoRejectsMismatchedIntegrityAndPreservesFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "gokin")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}
	rm := NewRollbackManager(t.TempDir(), 3)
	info, err := rm.CreateBackup(target, "v1.0.0")
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	infoPath := info.Path + ".json"
	before, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	info.Checksum = strings.Repeat("0", 64)
	if err := rm.SaveBackupInfo(info); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("SaveBackupInfo error = %v, want checksum rejection", err)
	}
	after, err := os.ReadFile(infoPath)
	if err != nil {
		t.Fatalf("read preserved metadata: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("failed metadata save replaced the previous file")
	}
}

func TestDeleteBackupRejectsOutsidePath(t *testing.T) {
	rm := NewRollbackManager(t.TempDir(), 3)
	external := filepath.Join(t.TempDir(), "gokin-external")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := rm.DeleteBackup(&BackupInfo{Path: external}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("DeleteBackup error = %v, want containment rejection", err)
	}
	if data, err := os.ReadFile(external); err != nil || string(data) != "keep" {
		t.Fatalf("external file changed: %q, %v", data, err)
	}
}
