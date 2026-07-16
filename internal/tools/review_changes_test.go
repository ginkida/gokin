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

// setupGitRepo creates a temporary git repo with a committed base file and returns its path.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir := resolvedTempDir(t)
	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.name", "test"},
		{"git", "config", "user.email", "test@test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", args[1], err, out)
		}
	}
	// Write and commit an initial file so there's a base
	if err := os.WriteFile(filepath.Join(dir, "base.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "base.go")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	return dir
}

// addTrackedFile creates and stages a file in the git repo, then commits it.
func addTrackedFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"git", "add", name}, {"git", "commit", "-m", "add " + name}} {
		cmd := exec.Command(a[0], a[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(a, " "), err, out)
		}
	}
}

func TestReviewChangesCleanRepo(t *testing.T) {
	dir := setupGitRepo(t)
	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "clean") {
		t.Fatalf("expected 'clean' in output, got: %s", result.Content)
	}
}

func TestReviewChangesModifiedFile(t *testing.T) {
	dir := setupGitRepo(t)
	// Modify base.go (already tracked)
	if err := os.WriteFile(filepath.Join(dir, "base.go"), []byte("package main\n\nfunc main(){}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "base.go") {
		t.Fatalf("expected output to mention base.go, got: %s", result.Content)
	}
	// Should contain diff markers
	if !strings.Contains(result.Content, "@@") {
		t.Fatalf("expected diff content, got: %s", result.Content)
	}
}

func TestReviewChangesNameOnly(t *testing.T) {
	dir := setupGitRepo(t)
	// Add tracked files first, then modify them
	addTrackedFile(t, dir, "a.go", "package main\n")
	addTrackedFile(t, dir, "b.go", "package main\n")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc a(){}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n\nfunc b(){}\n"), 0644)

	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"name_only": true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	// Should list both files
	if !strings.Contains(result.Content, "a.go") || !strings.Contains(result.Content, "b.go") {
		t.Fatalf("expected both files listed, got: %s", result.Content)
	}
	// Should not include diff hunks
	if strings.Contains(result.Content, "@@") {
		t.Fatalf("name_only should not show diffs, got: %s", result.Content)
	}
}

func TestReviewChangesSpecificFile(t *testing.T) {
	dir := setupGitRepo(t)
	addTrackedFile(t, dir, "a.go", "package main\n")
	addTrackedFile(t, dir, "b.go", "package main\n")
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc a(){}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n\nfunc b(){}\n"), 0644)

	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"file": "a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "a.go") {
		t.Fatalf("expected a.go in output, got: %s", result.Content)
	}
	if strings.Contains(result.Content, "b.go") {
		t.Fatalf("should not show b.go when file=a.go, got: %s", result.Content)
	}
}

func TestReviewChangesStaged(t *testing.T) {
	dir := setupGitRepo(t)
	addTrackedFile(t, dir, "staged.go", "package main\n")
	os.WriteFile(filepath.Join(dir, "staged.go"), []byte("package main\n\nfunc staged(){}\n"), 0644)

	// Stage the file
	cmd := exec.Command("git", "add", "staged.go")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	// Unstaged diff should be empty (file is staged)
	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"staged": false})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if strings.Contains(result.Content, "staged.go") {
		t.Fatalf("unstaged should not show staged file, got: %s", result.Content)
	}

	// Staged diff should show it
	result, err = tool.Execute(context.Background(), map[string]any{"staged": true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "staged.go") {
		t.Fatalf("staged should show staged file, got: %s", result.Content)
	}
}

func TestReviewChangesUntrackedFile(t *testing.T) {
	dir := setupGitRepo(t)
	// A genuinely new (untracked) file — git diff does NOT show these, but the
	// tool must surface it (it's exactly what an agent's `write` just created).
	if err := os.WriteFile(filepath.Join(dir, "brand_new.go"), []byte("package main\n\nfunc Added() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if strings.Contains(result.Content, "clean") {
		t.Fatalf("untracked file must not be reported as clean: %s", result.Content)
	}
	if !strings.Contains(result.Content, "brand_new.go") {
		t.Fatalf("expected brand_new.go in output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "new file") {
		t.Fatalf("expected new-file label, got: %s", result.Content)
	}
	// The new file's content should be shown as an added block.
	if !strings.Contains(result.Content, "func Added()") {
		t.Fatalf("expected new file content, got: %s", result.Content)
	}
}

func TestReviewChangesUntrackedNameOnly(t *testing.T) {
	dir := setupGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "fresh.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{"name_only": true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "fresh.go") || !strings.Contains(result.Content, "(new)") {
		t.Fatalf("expected fresh.go marked (new), got: %s", result.Content)
	}
}

func TestReviewChangesNotARepo(t *testing.T) {
	// Outside a git repo, git fails — the tool must surface an honest error,
	// not a false "Working tree is clean".
	dir := t.TempDir()
	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("expected an error outside a git repo, got success: %s", result.Content)
	}
	if strings.Contains(result.Content, "clean") {
		t.Fatalf("must not report 'clean' when git failed: %s", result.Content)
	}
	if !strings.Contains(result.Error, "review_changes failed") {
		t.Fatalf("expected an honest git error, got: %s", result.Error)
	}
}

func TestReviewChangesPerFileTruncation(t *testing.T) {
	dir := setupGitRepo(t)
	addTrackedFile(t, dir, "big.go", "package main\n")
	// Replace with a >70-line file so the per-file 60-line cap engages.
	var b strings.Builder
	b.WriteString("package main\n")
	for i := range 90 {
		fmt.Fprintf(&b, "var v%d = %d\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "more lines)") {
		t.Fatalf("expected per-file truncation marker, got: %s", result.Content)
	}
}

func TestReviewChangesNewFile(t *testing.T) {
	dir := setupGitRepo(t)
	// Add and commit a file, then modify it (not create a new untracked one,
	// since git diff doesn't show untracked files)
	addTrackedFile(t, dir, "existing.go", "package main\n")
	os.WriteFile(filepath.Join(dir, "existing.go"), []byte("package main\n\nfunc newFunc(){}\n"), 0644)

	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "existing.go") {
		t.Fatalf("expected existing.go in output, got: %s", result.Content)
	}
}

// TestReviewChangesWorkDirIsRepoSubdir pins the --relative fix: when workDir is
// a SUBDIRECTORY of the repo, `git diff --name-only` without --relative prints
// repo-root-relative names, which the per-file diff then re-resolves
// cwd-relative — every tracked file rendered an EMPTY diff body (review-
// confirmed empirically). With --relative, paths are cwd-relative and the
// per-file diffs carry real hunks; changes outside the subdir are out of scope.
func TestReviewChangesWorkDirIsRepoSubdir(t *testing.T) {
	root := setupGitRepo(t)
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	addTrackedFile(t, root, "sub/inner.go", "package sub\n\nvar A = 1\n")
	addTrackedFile(t, root, "top.go", "package main\n\nvar T = 1\n")
	// Modify both; only the subdir one is in scope for workDir=sub.
	if err := os.WriteFile(filepath.Join(sub, "inner.go"), []byte("package sub\n\nvar A = 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.go"), []byte("package main\n\nvar T = 2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReviewChangesTool(sub)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "inner.go") {
		t.Fatalf("expected inner.go in output, got: %s", result.Content)
	}
	// The diff BODY must be present, not just the header (the pre-fix symptom).
	if !strings.Contains(result.Content, "var A = 2") {
		t.Fatalf("expected real diff hunk for inner.go, got: %s", result.Content)
	}
}

// TestReviewChangesNonASCIIFilenames pins the core.quotepath fix: with git's
// default quotepath, файл.go comes back C-quoted ("\321\204..."), the quoted
// literal matches no pathspec and no file on disk — tracked files rendered
// empty diffs and untracked files silently vanished (review-confirmed
// empirically; the user works with Russian filenames).
func TestReviewChangesNonASCIIFilenames(t *testing.T) {
	dir := setupGitRepo(t)
	addTrackedFile(t, dir, "файл.go", "package main\n\nvar X = 1\n")
	if err := os.WriteFile(filepath.Join(dir, "файл.go"), []byte("package main\n\nvar X = 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "новый.go"), []byte("package main\n\nvar N = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewReviewChangesTool(dir)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "файл.go") {
		t.Fatalf("expected файл.go (unquoted) in output, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "var X = 2") {
		t.Fatalf("expected real diff hunk for файл.go, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "новый.go") || !strings.Contains(result.Content, "var N = 1") {
		t.Fatalf("expected untracked новый.go with content, got: %s", result.Content)
	}
}
