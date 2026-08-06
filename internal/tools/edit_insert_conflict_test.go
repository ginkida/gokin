package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Mode dispatch tests insert before replace, so a stray `insert_after_line`
// alongside `old_string` silently discarded the replacement and inserted the
// new text at that line — with 0 that is the very top of the file, ahead of the
// package clause. Observed corrupting real files in ~10% of eval runs; the model
// only recovered because it noticed the damage afterwards.
func TestEditRefusesInsertCombinedWithReplace(t *testing.T) {
	dir := resolvedEditTempDir(t)
	path := filepath.Join(dir, "policy.go")
	original := "package retry\n\nfunc Old() int { return 1 }\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := NewEditTool(dir)

	args := map[string]any{
		"file_path":         path,
		"old_string":        "func Old() int { return 1 }",
		"new_string":        "func Old() int { return 2 }",
		"insert_after_line": float64(0), // JSON numbers arrive as float64
	}

	if err := tool.Validate(args); err == nil {
		t.Fatal("Validate accepted a call that both inserts and replaces")
	} else if !strings.Contains(err.Error(), "old_string") {
		t.Fatalf("validation error does not name the conflict: %v", err)
	}

	if _, err := tool.Execute(context.Background(), args); err == nil {
		t.Fatal("Execute applied a contradictory insert+replace call")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("refused edit still modified the file:\n%s", data)
	}
	if !strings.HasPrefix(string(data), "package retry") {
		t.Fatal("package clause was displaced")
	}
}

// The same conflict with an edits array is refused too, and each mode still
// works on its own.
func TestEditInsertAndReplaceModesStillWorkAlone(t *testing.T) {
	dir := resolvedEditTempDir(t)
	tool := NewEditTool(dir)

	insertPath := filepath.Join(dir, "insert.go")
	if err := os.WriteFile(insertPath, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), map[string]any{
		"file_path":         insertPath,
		"insert_after_line": float64(1),
		"new_string":        "\nvar x = 1",
	})
	if err != nil || !res.Success {
		t.Fatalf("plain insert broke: err=%v result=%+v", err, res)
	}

	replacePath := filepath.Join(dir, "replace.go")
	if err := os.WriteFile(replacePath, []byte("package main\n\nvar y = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = tool.Execute(context.Background(), map[string]any{
		"file_path":  replacePath,
		"old_string": "var y = 1",
		"new_string": "var y = 2",
	})
	if err != nil || !res.Success {
		t.Fatalf("plain replace broke: err=%v result=%+v", err, res)
	}
	data, _ := os.ReadFile(replacePath)
	if !strings.Contains(string(data), "var y = 2") {
		t.Fatalf("replacement not applied: %s", data)
	}

	conflict := map[string]any{
		"file_path":         replacePath,
		"insert_after_line": float64(2),
		"edits":             []any{map[string]any{"old_string": "a", "new_string": "b"}},
	}
	if err := tool.Validate(conflict); err == nil {
		t.Fatal("Validate accepted insert combined with an edits array")
	}
}

// resolvedEditTempDir works around macOS /var -> /private/var, which
// PathValidator rejects as a symlink.
func resolvedEditTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}
