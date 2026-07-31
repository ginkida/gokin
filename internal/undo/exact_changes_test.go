package undo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerUndoChangesPreservesUnrelatedHistoryAndRedoIsExact(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.txt")
	foreignPath := filepath.Join(dir, "foreign.txt")
	if err := os.WriteFile(planPath, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignPath, []byte("foreign-before"), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager()
	planChange := NewFileChange(planPath, "write", []byte("before"), []byte("after"), false)
	if err := os.WriteFile(planPath, planChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Record(*planChange)

	foreignChange := NewFileChange(
		foreignPath, "write", []byte("foreign-before"), []byte("foreign-after"), false)
	if err := os.WriteFile(foreignPath, foreignChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Record(*foreignChange)

	undone, err := manager.UndoChanges([]string{planChange.ID})
	if err != nil {
		t.Fatalf("UndoChanges: %v", err)
	}
	if len(undone) != 1 || undone[0].ID != planChange.ID {
		t.Fatalf("undone=%+v, want only %s", undone, planChange.ID)
	}
	assertFileContent(t, planPath, "before")
	assertFileContent(t, foreignPath, "foreign-after")
	if got := manager.List(); len(got) != 1 || got[0].ID != foreignChange.ID {
		t.Fatalf("remaining history=%+v, want only unrelated change", got)
	}

	redone, err := manager.RedoChanges([]string{planChange.ID})
	if err != nil {
		t.Fatalf("RedoChanges: %v", err)
	}
	if len(redone) != 1 || redone[0].ID != planChange.ID {
		t.Fatalf("redone=%+v, want only %s", redone, planChange.ID)
	}
	assertFileContent(t, planPath, "after")
	assertFileContent(t, foreignPath, "foreign-after")
}

func TestManagerUndoChangesRefusesLaterOverlappingChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager()
	planChange := NewFileChange(path, "write", []byte("before"), []byte("plan"), false)
	if err := os.WriteFile(path, planChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Record(*planChange)

	laterChange := NewFileChange(path, "write", []byte("plan"), []byte("later"), false)
	if err := os.WriteFile(path, laterChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Record(*laterChange)

	_, err := manager.UndoChanges([]string{planChange.ID})
	if err == nil || !strings.Contains(err.Error(), "later change") {
		t.Fatalf("UndoChanges error=%v, want later-change conflict", err)
	}
	assertFileContent(t, path, "later")
	if got := manager.List(); len(got) != 2 {
		t.Fatalf("history mutated after refused undo: %+v", got)
	}
}

func TestManagerUndoChangesMissingIDIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewManager()
	change := NewFileChange(path, "write", []byte("before"), []byte("after"), false)
	if err := os.WriteFile(path, change.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	manager.Record(*change)

	if _, err := manager.UndoChanges([]string{change.ID, "trimmed-id"}); err == nil {
		t.Fatal("UndoChanges should reject an incomplete exact history")
	}
	assertFileContent(t, path, "after")
	if got := manager.List(); len(got) != 1 || got[0].ID != change.ID {
		t.Fatalf("history mutated after refused undo: %+v", got)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content=%q, want %q", path, got, want)
	}
}
