package undo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagerUndoRefusesConcurrentModificationAndKeepsChangeRetryable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "edited.txt")
	writeUndoTestFile(t, file, "tool version")

	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("original"),
		NewContent: []byte("tool version"),
	})

	writeUndoTestFile(t, file, "external version")
	if _, err := m.Undo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Undo error=%v, want a concurrent-edit conflict", err)
	}
	assertUndoTestContent(t, file, "external version")
	if m.Count() != 1 || m.CanRedo() {
		t.Fatalf("failed conflict changed stacks: undo=%d redo=%v", m.Count(), m.CanRedo())
	}

	// Once the caller resolves the conflict, the same change remains retryable.
	writeUndoTestFile(t, file, "tool version")
	if _, err := m.Undo(); err != nil {
		t.Fatalf("retry Undo: %v", err)
	}
	assertUndoTestContent(t, file, "original")
}

func TestManagerUndoDistinguishesMissingFromEmptyFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "empty.txt")
	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("original"),
		NewContent: []byte{},
	})

	// The tool's post-state was an existing empty file. A missing path is a
	// different state and must not be treated as matching []byte{}.
	if _, err := m.Undo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Undo error=%v, want missing-vs-empty conflict", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("conflicting missing path was recreated: %v", err)
	}
	if m.Count() != 1 {
		t.Fatalf("failed conflict removed undo entry; count=%d", m.Count())
	}
}

func TestManagerUndoCreatedFileRefusesExternalReplacement(t *testing.T) {
	file := filepath.Join(t.TempDir(), "created.txt")
	writeUndoTestFile(t, file, "tool version")
	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "write",
		NewContent: []byte("tool version"),
		WasNew:     true,
	})

	writeUndoTestFile(t, file, "external replacement")
	if _, err := m.Undo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Undo error=%v, want replacement conflict", err)
	}
	assertUndoTestContent(t, file, "external replacement")
	if m.Count() != 1 {
		t.Fatalf("failed conflict removed undo entry; count=%d", m.Count())
	}
}

func TestManagerUndoDeletedFileRefusesExternalRecreation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "deleted.txt")
	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "delete",
		OldContent: []byte("deleted by tool"),
	})

	writeUndoTestFile(t, file, "external recreation")
	if _, err := m.Undo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Undo error=%v, want recreation conflict", err)
	}
	assertUndoTestContent(t, file, "external recreation")
	if m.Count() != 1 {
		t.Fatalf("failed conflict removed undo entry; count=%d", m.Count())
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Undo(); err != nil {
		t.Fatalf("retry Undo after removing conflict: %v", err)
	}
	assertUndoTestContent(t, file, "deleted by tool")
}

func TestManagerRedoRefusesConcurrentModificationAndKeepsChangeRetryable(t *testing.T) {
	file := filepath.Join(t.TempDir(), "redo.txt")
	writeUndoTestFile(t, file, "tool version")
	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("original"),
		NewContent: []byte("tool version"),
	})
	if _, err := m.Undo(); err != nil {
		t.Fatal(err)
	}

	writeUndoTestFile(t, file, "external version")
	if _, err := m.Redo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Redo error=%v, want a concurrent-edit conflict", err)
	}
	assertUndoTestContent(t, file, "external version")
	if !m.CanRedo() || m.Count() != 0 {
		t.Fatalf("failed conflict changed stacks: undo=%d redo=%v", m.Count(), m.CanRedo())
	}

	writeUndoTestFile(t, file, "original")
	if _, err := m.Redo(); err != nil {
		t.Fatalf("retry Redo: %v", err)
	}
	assertUndoTestContent(t, file, "tool version")
}

func TestManagerRedoDeleteRefusesModifiedRestoredFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "redo-delete.txt")
	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "delete",
		OldContent: []byte("deleted by tool"),
	})
	if _, err := m.Undo(); err != nil {
		t.Fatal(err)
	}

	writeUndoTestFile(t, file, "external version")
	if _, err := m.Redo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Redo error=%v, want a concurrent-edit conflict", err)
	}
	assertUndoTestContent(t, file, "external version")
	if !m.CanRedo() {
		t.Fatal("failed conflict removed redo entry")
	}
}

func TestManagerRedoCreatedFileRefusesExternalRecreation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "redo-created.txt")
	writeUndoTestFile(t, file, "tool version")
	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "write",
		NewContent: []byte("tool version"),
		WasNew:     true,
	})
	if _, err := m.Undo(); err != nil {
		t.Fatal(err)
	}

	writeUndoTestFile(t, file, "external recreation")
	if _, err := m.Redo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Redo error=%v, want a recreation conflict", err)
	}
	assertUndoTestContent(t, file, "external recreation")
	if !m.CanRedo() {
		t.Fatal("failed conflict removed redo entry")
	}
}

func TestManagerUndoMoveRefusesToOverwriteRecreatedSource(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.txt")
	destination := filepath.Join(dir, "destination.txt")
	writeUndoTestFile(t, destination, "moved by tool")
	writeUndoTestFile(t, source, "external recreation")

	m := NewManager()
	m.Record(FileChange{
		FilePath:   destination,
		Tool:       "move",
		OldContent: []byte(source),
	})
	if _, err := m.Undo(); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("Undo error=%v, want a recreated-source conflict", err)
	}
	assertUndoTestContent(t, source, "external recreation")
	assertUndoTestContent(t, destination, "moved by tool")
	if m.Count() != 1 {
		t.Fatalf("failed conflict removed move undo entry; count=%d", m.Count())
	}
}

func TestManagerUndoGroupPreflightsEveryPathBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	writeUndoTestFile(t, first, "first tool version")
	writeUndoTestFile(t, second, "second tool version")

	m := NewManager()
	m.Record(FileChange{
		FilePath:   first,
		Tool:       "edit",
		OldContent: []byte("first original"),
		NewContent: []byte("first tool version"),
		GroupID:    "batch",
	})
	m.Record(FileChange{
		FilePath:   second,
		Tool:       "edit",
		OldContent: []byte("second original"),
		NewContent: []byte("second tool version"),
		GroupID:    "batch",
	})

	beforeSecond, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	writeUndoTestFile(t, first, "external version")
	if _, err := m.UndoGroup("batch"); err == nil || !strings.Contains(err.Error(), "working tree changed") {
		t.Fatalf("UndoGroup error=%v, want a concurrent-edit conflict", err)
	}
	assertUndoTestContent(t, first, "external version")
	assertUndoTestContent(t, second, "second tool version")
	afterSecond, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(beforeSecond, afterSecond) {
		t.Fatal("group undo rewrote an earlier path before discovering a later conflict")
	}
	if m.Count() != 2 || m.CanRedo() {
		t.Fatalf("failed group conflict changed stacks: undo=%d redo=%v", m.Count(), m.CanRedo())
	}

	writeUndoTestFile(t, first, "first tool version")
	if _, err := m.UndoGroup("batch"); err != nil {
		t.Fatalf("retry UndoGroup: %v", err)
	}
	assertUndoTestContent(t, first, "first original")
	assertUndoTestContent(t, second, "second original")
}

func TestManagerUndoGroupPreflightSimulatesRepeatedPathTransitions(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repeated.txt")
	writeUndoTestFile(t, file, "version 3")
	m := NewManager()
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("version 1"),
		NewContent: []byte("version 2"),
		GroupID:    "repeated",
	})
	m.Record(FileChange{
		FilePath:   file,
		Tool:       "edit",
		OldContent: []byte("version 2"),
		NewContent: []byte("version 3"),
		GroupID:    "repeated",
	})

	if _, err := m.UndoGroup("repeated"); err != nil {
		t.Fatalf("UndoGroup: %v", err)
	}
	assertUndoTestContent(t, file, "version 1")

	// Group undo pushes entries so oldest is redone first. Each redo retains
	// the same CAS invariant while reconstructing the group's final state.
	if _, err := m.Redo(); err != nil {
		t.Fatalf("first Redo: %v", err)
	}
	assertUndoTestContent(t, file, "version 2")
	if _, err := m.Redo(); err != nil {
		t.Fatalf("second Redo: %v", err)
	}
	assertUndoTestContent(t, file, "version 3")
}

func writeUndoTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertUndoTestContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content=%q, want %q", path, got, want)
	}
}
