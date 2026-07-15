package commands

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/undo"
)

type undoFakeApp struct {
	*fakeAppForMCP
	mgr *undo.Manager
}

func (f *undoFakeApp) GetUndoManager() *undo.Manager { return f.mgr }

func TestUndoListOrderMatchesNextUndo(t *testing.T) {
	mgr := undo.NewManager()
	for _, name := range []string{"oldest.go", "middle.go", "newest.go"} {
		mgr.Record(undo.FileChange{FilePath: name, Tool: "edit"})
	}
	app := &undoFakeApp{fakeAppForMCP: &fakeAppForMCP{}, mgr: mgr}

	got, err := (&UndoCommand{}).Execute(context.Background(), []string{"list"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	newest := strings.Index(got, "1. modified newest.go")
	middle := strings.Index(got, "2. modified middle.go")
	oldest := strings.Index(got, "3. modified oldest.go")
	if newest < 0 || middle < 0 || oldest < 0 || !(newest < middle && middle < oldest) {
		t.Fatalf("list order does not match undo order:\n%s", got)
	}
	if !strings.Contains(got, "1 is the next change /undo will revert") {
		t.Fatalf("list does not explain its numbering:\n%s", got)
	}
}

func TestUndoListSanitizesPersistedPath(t *testing.T) {
	mgr := undo.NewManager()
	mgr.Record(undo.FileChange{FilePath: "safe\nFORGED\x1b[31m.go", Tool: "edit"})
	app := &undoFakeApp{fakeAppForMCP: &fakeAppForMCP{}, mgr: mgr}

	got, err := (&UndoCommand{}).Execute(context.Background(), []string{"list"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if strings.Contains(got, "safe\nFORGED") || strings.ContainsRune(got, '\x1b') {
		t.Fatalf("path injected a forged terminal row or escape sequence: %q", got)
	}
	if !strings.Contains(got, "safe FORGED [31m.go") {
		t.Fatalf("sanitized path is not readable: %q", got)
	}
}

func TestUndoRedoOutcomesExposeImmediateRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edited.go")
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	mgr := undo.NewManager()
	mgr.Record(*undo.NewFileChange(path, "edit", []byte("old"), []byte("new"), false))
	app := &undoFakeApp{fakeAppForMCP: &fakeAppForMCP{}, mgr: mgr}

	got, err := (&UndoCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("undo Execute returned error: %v", err)
	}
	if !strings.Contains(got, "Redo this change: /redo") {
		t.Fatalf("undo outcome lacks recovery action: %q", got)
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "old" {
		t.Fatalf("undo content = %q, err = %v; want old", data, readErr)
	}

	got, err = (&RedoCommand{}).Execute(context.Background(), nil, app)
	if err != nil {
		t.Fatalf("redo Execute returned error: %v", err)
	}
	if !strings.Contains(got, "Undo this change again: /undo") {
		t.Fatalf("redo outcome lacks reverse action: %q", got)
	}
	if data, readErr := os.ReadFile(path); readErr != nil || string(data) != "new" {
		t.Fatalf("redo content = %q, err = %v; want new", data, readErr)
	}
}

func TestMultiUndoReportsPartialCompletionAndExactRedo(t *testing.T) {
	dir := t.TempDir()
	mgr := undo.NewManager()
	for _, name := range []string{"first.go", "second.go"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
			t.Fatal(err)
		}
		mgr.Record(*undo.NewFileChange(path, "edit", []byte("old"), []byte("new"), false))
	}
	app := &undoFakeApp{fakeAppForMCP: &fakeAppForMCP{}, mgr: mgr}

	got, err := (&UndoCommand{}).Execute(context.Background(), []string{"3"}, app)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !strings.Contains(got, "Undone 2 of 3 requested") || !strings.Contains(got, "nothing to undo") {
		t.Fatalf("partial outcome is not explicit: %q", got)
	}
	if !strings.Contains(got, "Redo these changes: /redo 2") {
		t.Fatalf("partial outcome lacks exact recovery command: %q", got)
	}
}
