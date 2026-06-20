package tools

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

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
