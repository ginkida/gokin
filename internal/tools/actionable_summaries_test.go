package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// — gap3 actionable summaries (glob / list_dir / git_status / git_diff) —

func TestActionableGlobSummary_CategoriesAndNextHint(t *testing.T) {
	paths := []string{
		"internal/app/server.go",
		"internal/app/server_test.go",
		"config.yaml",
		"docs/guide.md",
		"vendor/lib/dep.go",
	}
	out := actionableGlobSummary(paths)
	for _, needle := range []string{
		"Actionable summary:",
		"Source files", "Test files", "Config files", "Docs",
		"- Next: read the most relevant source file",
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("glob summary missing %q:\n%s", needle, out)
		}
	}
	if actionableGlobSummary(nil) != "" {
		t.Fatal("empty input must produce empty summary")
	}
}

func TestActionableGitStatusSummary_ConflictsTakePriority(t *testing.T) {
	out := actionableGitStatusSummary(
		[]string{"modified a.go"},
		[]string{"modified b.go"},
		[]string{"c.go"},
		[]string{"d.go"},
	)
	if !strings.Contains(out, "CONFLICTS") || !strings.Contains(out, "resolve conflicts") {
		t.Fatalf("conflicts must dominate the summary:\n%s", out)
	}
	if strings.Contains(out, "Staged") {
		t.Fatalf("conflict summary must not dilute with staged/unstaged sections:\n%s", out)
	}

	clean := actionableGitStatusSummary([]string{"added a.go"}, nil, []string{"new.txt"}, nil)
	for _, needle := range []string{"Staged (ready to commit)", "added a.go", "Untracked (1): new.txt"} {
		if !strings.Contains(clean, needle) {
			t.Fatalf("status summary missing %q:\n%s", needle, clean)
		}
	}
}

func TestActionableGitDiffSummary_NameStatusParsing(t *testing.T) {
	raw := "M\tinternal/app/server.go\nA\tinternal/app/new.go\nD\told.go\nR100\tsrc/a.go\tsrc/b.go"
	out := actionableGitDiffSummary(raw)
	for _, needle := range []string{
		"4 file(s) changed", "1 added", "1 modified", "1 deleted", "1 renamed",
		"src/b.go", // rename reports the NEW name
	} {
		if !strings.Contains(out, needle) {
			t.Fatalf("diff summary missing %q:\n%s", needle, out)
		}
	}
}

func TestGitStatusTool_ErrorIncludesStderr(t *testing.T) {
	dir := resolvedTempDir(t) // not a git repository
	tool := NewGitStatusTool(dir)

	result, err := tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Fatal("git status outside a repo must fail")
	}
	if !strings.Contains(strings.ToLower(result.Error), "not a git repository") {
		t.Fatalf("error must carry git's stderr, got: %q", result.Error)
	}
}

func TestListDirTool_TruncationReportsTrueTotal(t *testing.T) {
	dir := resolvedTempDir(t)
	total := maxListDirEntries + 7
	for i := range total {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewListDirTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("Execute() failed: %s", result.Error)
	}
	wantHeader := fmt.Sprintf("%d entries (showing first %d", total, maxListDirEntries)
	if !strings.Contains(result.Content, wantHeader) {
		t.Fatalf("truncated header must report the TRUE total %q, got:\n%.300s", wantHeader, result.Content)
	}
	if strings.Contains(result.Content, "+ entries") {
		t.Fatalf("no invented lower-bound counts allowed:\n%.200s", result.Content)
	}
}

func TestListDirTool_SummaryAndSorting(t *testing.T) {
	dir := resolvedTempDir(t)
	if err := os.MkdirAll(filepath.Join(dir, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewListDirTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil || !result.Success {
		t.Fatalf("Execute() = %v, %s", err, result.Error)
	}
	for _, needle := range []string{"3 entries (2 dirs, 1 files)", "Actionable summary:", "2 subdir(s): adir/, zdir/"} {
		if !strings.Contains(result.Content, needle) {
			t.Fatalf("list_dir output missing %q:\n%s", needle, result.Content)
		}
	}
	// Dirs render before files.
	if strings.Index(result.Content, "zdir/") > strings.Index(result.Content, "main.go") {
		t.Fatalf("dirs must list before files:\n%s", result.Content)
	}
}

func TestGitStatusTool_SingleInvocationWithBranch(t *testing.T) {
	dir := resolvedTempDir(t)
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "init")

	tool := NewGitStatusTool(dir)

	// Clean tree mentions the branch.
	result, err := tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil || !result.Success {
		t.Fatalf("Execute() = %v, %s", err, result.Error)
	}
	if !strings.Contains(result.Content, "working tree clean") || !strings.Contains(result.Content, "main") {
		t.Fatalf("clean output must mention branch: %q", result.Content)
	}

	// Dirty tree: branch in header, every file in the summary, NO duplicated
	// human-readable tail ("On branch" prose is the old second subprocess).
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = tool.Execute(context.Background(), map[string]any{"path": dir})
	if err != nil || !result.Success {
		t.Fatalf("Execute() = %v, %s", err, result.Error)
	}
	for _, needle := range []string{"Working tree status on main", "1 untracked", "new.txt"} {
		if !strings.Contains(result.Content, needle) {
			t.Fatalf("dirty output missing %q:\n%s", needle, result.Content)
		}
	}
	if strings.Contains(result.Content, "On branch") {
		t.Fatalf("non-short output must not append the duplicated human status tail:\n%s", result.Content)
	}
}
