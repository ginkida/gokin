package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	outsideFileSecret = "OUTSIDE_FILE_SECRET_7f31"
	outsideCommitMark = "OUTSIDE_COMMIT_SECRET_8c42"
)

// setupNestedWorkspaceRepo creates the layout that exposed the original bug:
// git discovers a repository above workDir, so an unchecked ../ pathspec can
// read a tracked sibling even though that sibling is outside the workspace.
func setupNestedWorkspaceRepo(t *testing.T) (workDir, insideFile, outsideFile string) {
	t.Helper()
	root := resolvedTempDir(t)
	initGitRepo(t, root)

	outsideFile = filepath.Join(root, "outside-secret.txt")
	if err := os.WriteFile(outsideFile, []byte(outsideFileSecret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForContainmentTest(t, root, "add", "outside-secret.txt")
	runGitForContainmentTest(t, root, "commit", "-m", outsideCommitMark)

	workDir = filepath.Join(root, "workspace")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	insideFile = filepath.Join(workDir, "inside.txt")
	if err := os.WriteFile(insideFile, []byte("inside-v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForContainmentTest(t, root, "add", "workspace/inside.txt")
	runGitForContainmentTest(t, root, "commit", "-m", "inside workspace commit")

	// Leave both files modified. Unfiltered diff/review calls must show only the
	// workspace change, never the sibling change.
	if err := os.WriteFile(insideFile, []byte("inside-v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outsideFile, []byte(outsideFileSecret+"-modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return workDir, insideFile, outsideFile
}

func runGitForContainmentTest(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestReadOnlyToolsRejectPathsOutsideNestedWorkspace(t *testing.T) {
	workDir, _, outsideFile := setupNestedWorkspaceRepo(t)
	ctx := context.Background()

	tools := []struct {
		name    string
		execute func(string) (ToolResult, error)
	}{
		{
			name: "diff",
			execute: func(path string) (ToolResult, error) {
				return NewDiffTool(workDir).Execute(ctx, map[string]any{
					"file1":   path,
					"content": "safe comparison\n",
				})
			},
		},
		{
			name: "git_diff",
			execute: func(path string) (ToolResult, error) {
				return NewGitDiffTool(workDir).Execute(ctx, map[string]any{"file": path})
			},
		},
		{
			name: "git_log",
			execute: func(path string) (ToolResult, error) {
				return NewGitLogTool(workDir).Execute(ctx, map[string]any{"file": path})
			},
		},
		{
			name: "git_blame",
			execute: func(path string) (ToolResult, error) {
				return NewGitBlameTool(workDir).Execute(ctx, map[string]any{"file": path})
			},
		},
		{
			name: "review_changes",
			execute: func(path string) (ToolResult, error) {
				return NewReviewChangesTool(workDir).Execute(ctx, map[string]any{"file": path})
			},
		},
	}

	outsidePaths := []struct {
		name string
		path string
	}{
		{name: "absolute", path: outsideFile},
		{name: "parent traversal", path: filepath.Join("..", filepath.Base(outsideFile))},
	}
	if symlink := filepath.Join(workDir, "outside-link.txt"); os.Symlink(outsideFile, symlink) == nil {
		outsidePaths = append(outsidePaths, struct {
			name string
			path string
		}{name: "symlink escape", path: "outside-link.txt"})
	}

	for _, tool := range tools {
		tool := tool
		for _, outside := range outsidePaths {
			outside := outside
			t.Run(tool.name+"/"+outside.name, func(t *testing.T) {
				result, err := tool.execute(outside.path)
				if err != nil {
					t.Fatalf("Execute returned transport error: %v", err)
				}
				if result.Success {
					t.Fatalf("outside path unexpectedly succeeded: %s", result.Content)
				}
				if !strings.Contains(result.Error, "path validation failed") {
					t.Fatalf("expected path-validation failure, got: %q", result.Error)
				}
				if strings.Contains(result.Content, outsideFileSecret) || strings.Contains(result.Error, outsideFileSecret) {
					t.Fatal("outside file contents leaked through tool result")
				}
			})
		}
	}
}

func TestReadOnlyToolsPreserveInWorkspacePathsAndDefaultScope(t *testing.T) {
	workDir, insideFile, _ := setupNestedWorkspaceRepo(t)
	ctx := context.Background()

	// diff resolves a relative path from the tool workDir, not the process CWD.
	diffResult, err := NewDiffTool(workDir).Execute(ctx, map[string]any{
		"file1":   "inside.txt",
		"content": "comparison\n",
	})
	assertContainedToolSuccess(t, diffResult, err, "inside-v2")

	// An absolute path is also valid when it resolves inside the workspace.
	gitDiffResult, err := NewGitDiffTool(workDir).Execute(ctx, map[string]any{"file": insideFile})
	assertContainedToolSuccess(t, gitDiffResult, err, "inside-v2")

	gitLogResult, err := NewGitLogTool(workDir).Execute(ctx, map[string]any{"file": "inside.txt"})
	assertContainedToolSuccess(t, gitLogResult, err, "inside workspace commit")

	gitBlameResult, err := NewGitBlameTool(workDir).Execute(ctx, map[string]any{"file": insideFile})
	assertContainedToolSuccess(t, gitBlameResult, err, "inside-v2")

	reviewResult, err := NewReviewChangesTool(workDir).Execute(ctx, map[string]any{"file": "inside.txt"})
	assertContainedToolSuccess(t, reviewResult, err, "inside-v2")

	// No-filter calls are scoped too. They must not expose sibling changes or
	// commits merely because git found a repository above workDir.
	gitDiffAll, err := NewGitDiffTool(workDir).Execute(ctx, map[string]any{})
	assertContainedToolSuccess(t, gitDiffAll, err, "inside-v2")
	assertNoOutsideMarkers(t, gitDiffAll)

	gitLogAll, err := NewGitLogTool(workDir).Execute(ctx, map[string]any{})
	assertContainedToolSuccess(t, gitLogAll, err, "inside workspace commit")
	assertNoOutsideMarkers(t, gitLogAll)

	reviewAll, err := NewReviewChangesTool(workDir).Execute(ctx, map[string]any{})
	assertContainedToolSuccess(t, reviewAll, err, "inside-v2")
	assertNoOutsideMarkers(t, reviewAll)
}

func TestGitPathspecMagicCannotEscapeNestedWorkspace(t *testing.T) {
	workDir, _, _ := setupNestedWorkspaceRepo(t)
	ctx := context.Background()
	// This is a valid Git pathspec meaning "from repository root". Filesystem
	// containment alone sees it as an ordinary (nonexistent) filename under
	// workDir, so every git invocation must also force literal pathspec parsing.
	magic := ":(top)outside-secret.txt"

	cases := []struct {
		name string
		run  func() (ToolResult, error)
	}{
		{name: "git_diff", run: func() (ToolResult, error) {
			return NewGitDiffTool(workDir).Execute(ctx, map[string]any{"file": magic})
		}},
		{name: "git_log", run: func() (ToolResult, error) {
			return NewGitLogTool(workDir).Execute(ctx, map[string]any{"file": magic})
		}},
		{name: "git_blame", run: func() (ToolResult, error) {
			return NewGitBlameTool(workDir).Execute(ctx, map[string]any{"file": magic})
		}},
		{name: "review_changes", run: func() (ToolResult, error) {
			return NewReviewChangesTool(workDir).Execute(ctx, map[string]any{"file": magic})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.run()
			if err != nil {
				t.Fatalf("Execute returned transport error: %v", err)
			}
			combined := result.Content + "\n" + result.Error
			if strings.Contains(combined, outsideFileSecret) || strings.Contains(combined, outsideCommitMark) {
				t.Fatalf("git pathspec magic leaked sibling data:\n%s", combined)
			}
		})
	}
}

func TestDiffToolCloneUsesIsolatedWorkDir(t *testing.T) {
	foreground := resolvedTempDir(t)
	isolated := resolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(foreground, "same.txt"), []byte("foreground-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(isolated, "same.txt"), []byte("isolated-only\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cloned, ok := CloneToolForWorkDir(NewDiffTool(foreground), isolated).(*DiffTool)
	if !ok {
		t.Fatal("cloned tool is not a DiffTool")
	}
	result, err := cloned.Execute(context.Background(), map[string]any{
		"file1":   "same.txt",
		"content": "comparison\n",
	})
	assertContainedToolSuccess(t, result, err, "isolated-only")
	if strings.Contains(result.Content, "foreground-only") {
		t.Fatalf("cloned diff read from foreground workspace:\n%s", result.Content)
	}
}

func TestDiffToolExplicitAllowedDirCanBeRevoked(t *testing.T) {
	workDir := resolvedTempDir(t)
	grantedDir := resolvedTempDir(t)
	target := filepath.Join(grantedDir, "granted.txt")
	if err := os.WriteFile(target, []byte("granted-content\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewDiffTool(workDir)
	tool.SetAllowedDirs([]string{grantedDir})
	result, err := tool.Execute(context.Background(), map[string]any{
		"file1":   target,
		"content": "comparison\n",
	})
	assertContainedToolSuccess(t, result, err, "granted-content")

	tool.SetAllowedDirs(nil)
	result, err = tool.Execute(context.Background(), map[string]any{
		"file1":   target,
		"content": "comparison\n",
	})
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if result.Success || !strings.Contains(result.Error, "path validation failed") {
		t.Fatalf("revoked directory unexpectedly remained readable: success=%v error=%q", result.Success, result.Error)
	}
}

func TestReadOnlyToolsFailClosedWithoutWorkDir(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func() (ToolResult, error)
	}{
		{name: "diff", run: func() (ToolResult, error) {
			return NewDiffTool("").Execute(ctx, map[string]any{"file1": "go.mod", "content": "x"})
		}},
		{name: "git_diff", run: func() (ToolResult, error) {
			return NewGitDiffTool("").Execute(ctx, map[string]any{})
		}},
		{name: "git_log", run: func() (ToolResult, error) {
			return NewGitLogTool("").Execute(ctx, map[string]any{})
		}},
		{name: "git_blame", run: func() (ToolResult, error) {
			return NewGitBlameTool("").Execute(ctx, map[string]any{"file": "go.mod"})
		}},
		{name: "review_changes", run: func() (ToolResult, error) {
			return NewReviewChangesTool("").Execute(ctx, map[string]any{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.run()
			if err != nil {
				t.Fatalf("Execute returned transport error: %v", err)
			}
			if result.Success || !strings.Contains(result.Error, "path validator not initialized") {
				t.Fatalf("expected fail-closed security error, got success=%v content=%q error=%q", result.Success, result.Content, result.Error)
			}
		})
	}
}

func assertContainedToolSuccess(t *testing.T, result ToolResult, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("Execute returned transport error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %s", result.Error)
	}
	if !strings.Contains(result.Content, want) {
		t.Fatalf("result does not contain %q:\n%s", want, result.Content)
	}
}

func assertNoOutsideMarkers(t *testing.T, result ToolResult) {
	t.Helper()
	if strings.Contains(result.Content, outsideFileSecret) || strings.Contains(result.Content, outsideCommitMark) {
		t.Fatalf("default workspace-scoped call leaked sibling data:\n%s", result.Content)
	}
}
