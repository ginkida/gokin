package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"
)

// TestRefactorRenamePreservesFileMode pins the v0.85.9 fix: the refactor tool's
// write paths went through os.WriteFile(..., 0644), silently stripping the
// execute bit from 0755 source files (shell scripts, build wrappers). They now
// route through AtomicWrite(existingPerm(path)) like edit/write.
func TestRefactorRenamePreservesFileMode(t *testing.T) {
	dir := testkit.ResolvedTempDir(t)
	file := filepath.Join(dir, "main.go")
	src := "package main\n\nfunc oldName() int { return oldName() + 1 }\n"
	if err := os.WriteFile(file, []byte(src), 0o755); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := NewRefactorTool()
	tool.SetWorkDir(dir)
	res, err := tool.Execute(context.Background(), map[string]any{
		"operation": "rename",
		"file_path": file,
		"old_name":  "oldName",
		"new_name":  "newName",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.Success {
		t.Fatalf("rename failed: %s", res.Error)
	}

	// Content was actually rewritten.
	out, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if strings.Contains(string(out), "oldName") || !strings.Contains(string(out), "newName") {
		t.Fatalf("rename did not apply, got:\n%s", out)
	}

	// Mode must survive the rewrite.
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("file mode = %o after refactor rename, want 0755 (execute bit stripped)", got)
	}
}

// TestRefactorRejectsOutOfRootPath pins the v0.85.11 fix: refactor write
// operations now run file_path through PathValidator like edit/write, so a
// path escaping the workspace root is rejected instead of read+rewritten.
func TestRefactorRejectsOutOfRootPath(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	outside := testkit.ResolvedTempDir(t) // a sibling root, not under `root`
	victim := filepath.Join(outside, "secret.go")
	original := "package main\n\nfunc oldName() {}\n"
	if err := os.WriteFile(victim, []byte(original), 0o644); err != nil {
		t.Fatalf("seed victim: %v", err)
	}

	tool := NewRefactorTool()
	tool.SetWorkDir(root) // only `root` is an allowed write root

	res, err := tool.Execute(context.Background(), map[string]any{
		"operation": "rename",
		"file_path": victim, // outside the allowed root
		"old_name":  "oldName",
		"new_name":  "newName",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	// rename of a single out-of-root file should surface as a failed result
	// (or at minimum leave the victim untouched).
	out, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatalf("victim read-back: %v", readErr)
	}
	if string(out) != original {
		t.Fatalf("out-of-root file was modified by refactor; path validation bypassed.\ngot:\n%s", out)
	}
	_ = res
}

func TestClampTTLMinutes(t *testing.T) {
	cases := []struct {
		in, want int
	}{
		{0, 0},
		{-5, 0},
		{60, 60},
		{maxMemoryTTLMinutes, maxMemoryTTLMinutes},
		{maxMemoryTTLMinutes + 1, maxMemoryTTLMinutes},
		{999999999, maxMemoryTTLMinutes}, // would overflow time.Duration → negative
	}
	for _, c := range cases {
		if got := clampTTLMinutes(c.in); got != c.want {
			t.Errorf("clampTTLMinutes(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestDiffContextLinesBounds ensures extreme context_lines values don't panic
// or error — negatives are floored to 0, huge values clamped (memory guard).
func TestDiffContextLinesBounds(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.txt")
	f2 := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(f1, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f2, []byte("line1\nCHANGED\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewDiffTool()
	for _, cl := range []int{-100, 0, 3, 1_000_000} {
		res, err := tool.Execute(context.Background(), map[string]any{
			"file1":         f1,
			"file2":         f2,
			"context_lines": cl,
		})
		if err != nil {
			t.Fatalf("context_lines=%d: Execute error: %v", cl, err)
		}
		if !res.Success {
			t.Fatalf("context_lines=%d: diff failed: %s", cl, res.Error)
		}
		if !strings.Contains(res.Content, "CHANGED") {
			t.Errorf("context_lines=%d: diff missing changed line:\n%s", cl, res.Content)
		}
	}
}
