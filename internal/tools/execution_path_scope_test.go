package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupExecutionScopeProjects(t *testing.T) (workspace, outside string) {
	t.Helper()
	root := t.TempDir()
	workspace = filepath.Join(root, "workspace")
	outside = filepath.Join(root, "outside")
	for _, dir := range []string{workspace, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/scope\n\ngo 1.25\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "scope_test.go"), []byte("package scope\n\nimport \"testing\"\n\nfunc TestScope(t *testing.T) {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var err error
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	outside, err = filepath.EvalSymlinks(outside)
	if err != nil {
		t.Fatal(err)
	}
	return workspace, outside
}

func TestExecutionToolsRejectPathsOutsideWorkspace(t *testing.T) {
	workspace, outside := setupExecutionScopeProjects(t)
	outsidePaths := []struct {
		name string
		path string
	}{
		{name: "absolute", path: outside},
		{name: "parent traversal", path: filepath.Join("..", filepath.Base(outside))},
	}
	if link := filepath.Join(workspace, "outside-link"); os.Symlink(outside, link) == nil {
		outsidePaths = append(outsidePaths, struct {
			name string
			path string
		}{name: "symlink", path: link})
	}

	for _, tc := range outsidePaths {
		t.Run("run_tests/"+tc.name, func(t *testing.T) {
			result, err := NewRunTestsTool(workspace).Execute(context.Background(), map[string]any{
				"path":      tc.path,
				"framework": "go",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || !strings.Contains(result.Error, "rejected path") {
				t.Fatalf("run_tests path escape was not rejected: success=%v error=%q", result.Success, result.Error)
			}
		})

		t.Run("verify_code/"+tc.name, func(t *testing.T) {
			result, err := NewVerifyCodeTool(workspace).Execute(context.Background(), map[string]any{"path": tc.path})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || !strings.Contains(result.Error, "rejected path") {
				t.Fatalf("verify_code path escape was not rejected: success=%v error=%q", result.Success, result.Error)
			}
		})
	}
}

func TestExecutionToolsHonorExplicitDirectoryGrant(t *testing.T) {
	workspace, outside := setupExecutionScopeProjects(t)

	runTests := NewRunTestsTool(workspace)
	runTests.SetAllowedDirs([]string{outside})
	testResult, err := runTests.Execute(context.Background(), map[string]any{
		"path":      outside,
		"framework": "go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !testResult.Success {
		t.Fatalf("run_tests rejected an explicitly granted directory: %s", testResult.Error)
	}

	verify := NewVerifyCodeTool(workspace)
	verify.SetAllowedDirs([]string{outside})
	verifyResult, err := verify.Execute(context.Background(), map[string]any{"path": outside})
	if err != nil {
		t.Fatal(err)
	}
	if !verifyResult.Success {
		t.Fatalf("verify_code rejected an explicitly granted directory: %s", verifyResult.Error)
	}
}

func TestRunTestsAutoDetectionStopsAtWorkspaceBoundary(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	// The module marker deliberately lives ABOVE the granted workspace. The Go
	// toolchain would happily use it, but doing so makes an auto-allowed tool
	// execute project configuration the user never granted.
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/outer\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "scope_test.go"), []byte("package scope\n\nimport \"testing\"\n\nfunc TestScope(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := NewRunTestsTool(workspace).Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "could not detect test framework") {
		t.Fatalf("framework detection escaped the workspace boundary: success=%v error=%q", result.Success, result.Error)
	}
}

func TestExecutionToolsFailClosedWithoutWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool Tool
	}{
		{name: "run_tests", tool: NewRunTestsTool("")},
		{name: "verify_code", tool: NewVerifyCodeTool("")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.tool.Execute(context.Background(), map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || !strings.Contains(result.Error, "path validator not initialized") {
				t.Fatalf("tool fell back to process cwd without a workspace: success=%v error=%q", result.Success, result.Error)
			}
		})
	}
}
