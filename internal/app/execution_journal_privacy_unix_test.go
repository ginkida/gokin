//go:build !windows && !plan9

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionJournalRepairsPrivateStorageModes(t *testing.T) {
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".gokin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(dir, "execution_journal.jsonl")
	recoveryPath := filepath.Join(dir, "recovery_snapshot.json")
	for path, content := range map[string]string{journalPath: "", recoveryPath: "{}"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	journal, err := NewExecutionJournal(workDir)
	if err != nil {
		t.Fatalf("NewExecutionJournal: %v", err)
	}
	assertJournalMode(t, dir, 0o700)
	assertJournalMode(t, journalPath, 0o600)
	assertJournalMode(t, recoveryPath, 0o600)

	if err := journal.Append("tool_start", map[string]any{"tool": "read"}); err != nil {
		t.Fatal(err)
	}
	entries, err := journal.Tail(1)
	if err != nil || len(entries) != 1 || entries[0].Event != "tool_start" {
		t.Fatalf("Tail = %+v, %v", entries, err)
	}
}

func TestExecutionJournalRejectsSymlinkedPrivatePaths(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		workDir := t.TempDir()
		target := filepath.Join(workDir, "external")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(workDir, ".gokin")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := NewExecutionJournal(workDir); err == nil {
			t.Fatal("NewExecutionJournal accepted symlinked storage directory")
		}
		assertJournalMode(t, target, 0o755)
	})

	t.Run("append target", func(t *testing.T) {
		journal, target := journalWithSymlinkTarget(t, "execution_journal.jsonl")
		if err := journal.Append("tool_start", nil); err == nil {
			t.Fatal("Append accepted symlinked journal file")
		}
		assertUntouchedJournalTarget(t, target)
	})

	t.Run("recovery target", func(t *testing.T) {
		journal, target := journalWithSymlinkTarget(t, "recovery_snapshot.json")
		if err := journal.SaveRecovery(RecoverySnapshot{SessionID: "session"}); err == nil {
			t.Fatal("SaveRecovery accepted symlinked recovery file")
		}
		assertUntouchedJournalTarget(t, target)
	})
}

func TestExecutionJournalBoundsDurableRecords(t *testing.T) {
	journal, err := NewExecutionJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oversized := strings.Repeat("x", int(maxRecoverySnapshotBytes))
	if err := journal.SaveRecovery(RecoverySnapshot{PendingMessage: oversized}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("SaveRecovery oversized error = %v", err)
	}
	if err := os.WriteFile(journal.recoveryPath, []byte(oversized+"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.LoadRecovery(); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("LoadRecovery oversized error = %v", err)
	}
	if err := journal.Append("oversized", map[string]any{"payload": strings.Repeat("x", maxJournalEntryBytes)}); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("Append oversized error = %v", err)
	}
}

func journalWithSymlinkTarget(t *testing.T, name string) (*ExecutionJournal, string) {
	t.Helper()
	workDir := t.TempDir()
	journal, err := NewExecutionJournal(workDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(workDir, ".gokin", name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	target := filepath.Join(workDir, "external")
	if err := os.WriteFile(target, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return journal, target
}

func assertUntouchedJournalTarget(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("symlink target changed: %q", data)
	}
	assertJournalMode(t, path, 0o644)
}

func assertJournalMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
