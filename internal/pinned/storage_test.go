package pinned

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoadAndClear(t *testing.T) {
	workDir := t.TempDir()
	want := "  exact pinned context\nwith trailing space  "
	if err := Save(workDir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(workDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != want {
		t.Fatalf("Load = %q, want exact %q", got, want)
	}

	if err := Save(workDir, ""); err != nil {
		t.Fatalf("clear Save: %v", err)
	}
	got, err = Load(workDir)
	if err != nil {
		t.Fatalf("Load after clear: %v", err)
	}
	if got != "" {
		t.Fatalf("Load after clear = %q, want empty", got)
	}
}

func TestSaveRejectsOversizedContentBeforeCreatingStorage(t *testing.T) {
	workDir := t.TempDir()
	err := Save(workDir, strings.Repeat("x", MaxContentBytes+1))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Save oversized error = %v, want limit error", err)
	}
	if _, statErr := os.Lstat(filepath.Join(workDir, ".gokin")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("oversized Save created storage: %v", statErr)
	}
}

func TestLoadRejectsOversizedFile(t *testing.T) {
	workDir := t.TempDir()
	dir := filepath.Join(workDir, ".gokin")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", MaxContentBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(workDir); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load oversized error = %v, want limit error", err)
	}
}

func TestLoadMissingDoesNotCreateStorage(t *testing.T) {
	workDir := t.TempDir()
	if _, err := Load(workDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load missing error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Lstat(filepath.Join(workDir, ".gokin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load missing created storage: %v", err)
	}
}
