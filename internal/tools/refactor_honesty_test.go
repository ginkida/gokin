package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

func assertOnlyWrittenPath(t *testing.T, result ToolResult, want string) {
	t.Helper()
	got := WrittenPathsFromResult(result)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("written_paths = %v, want [%s]", got, want)
	}
}

// TestRefactorRename_AllErrorIsNotFalseSuccess pins the dishonest-success fix:
// when every target file ERRORS (here a non-existent in-workspace path), the
// rename produced zero changes but the old code returned a SUCCESS "No
// occurrences of X found" and discarded the error — so the agent would conclude
// the symbol is absent / the work is done. It must surface a real error.
func TestRefactorRename_AllErrorIsNotFalseSuccess(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	missing := filepath.Join(dir, "does_not_exist.go")

	tool := NewRefactorTool()
	tool.SetWorkDir(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"operation": "rename",
		"file_path": missing,
		"old_name":  "Foo",
		"new_name":  "Bar",
	})
	if err != nil {
		t.Fatalf("Execute returned a Go error: %v", err)
	}
	if res.Success {
		t.Errorf("an all-error rename must NOT be reported as success:\ncontent=%q", res.Content)
	}
	if strings.Contains(res.Content, "No occurrences") {
		t.Errorf("must not claim a benign 'No occurrences found' when the file errored:\n%s", res.Content)
	}
	if res.Error == "" {
		t.Error("the failure reason should be surfaced in result.Error")
	}
}

func TestRefactorExtractDeclaresActualWrittenPath(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	file := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(file, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewRefactorTool()
	tool.SetWorkDir(dir)
	result, err := tool.Execute(context.Background(), map[string]any{
		"operation":    "extract",
		"file_path":    file,
		"extract_name": "extracted",
		"start_line":   2,
		"end_line":     2,
	})
	if err != nil || !result.Success {
		t.Fatalf("extract result/error = %#v/%v", result, err)
	}
	assertOnlyWrittenPath(t, result, file)
}

func TestRefactorInlineDeclaresWriteButNoOpDoesNot(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	tool := NewRefactorTool()
	tool.SetWorkDir(dir)

	changedFile := filepath.Join(dir, "changed.go")
	changedSource := "package sample\n\nfunc helper() { println(\"x\") }\nfunc caller() { helper() }\n"
	if err := os.WriteFile(changedFile, []byte(changedSource), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := tool.Execute(context.Background(), map[string]any{
		"operation":   "inline",
		"file_path":   changedFile,
		"target_name": "helper",
	})
	if err != nil || !changed.Success {
		t.Fatalf("inline result/error = %#v/%v", changed, err)
	}
	assertOnlyWrittenPath(t, changed, changedFile)

	noOpFile := filepath.Join(dir, "noop.go")
	noOpSource := "package sample\n\nfunc helper() { println(\"x\") }\n"
	if err := os.WriteFile(noOpFile, []byte(noOpSource), 0o644); err != nil {
		t.Fatal(err)
	}
	noOp, err := tool.Execute(context.Background(), map[string]any{
		"operation":   "inline",
		"file_path":   noOpFile,
		"target_name": "helper",
	})
	if err != nil || !noOp.Success {
		t.Fatalf("no-op inline result/error = %#v/%v", noOp, err)
	}
	if got := WrittenPathsFromResult(noOp); len(got) != 0 {
		t.Fatalf("no-op inline declared writes: %v", got)
	}
	content, readErr := os.ReadFile(noOpFile)
	if readErr != nil || string(content) != noOpSource {
		t.Fatalf("no-op inline changed file: content=%q err=%v", content, readErr)
	}
}
