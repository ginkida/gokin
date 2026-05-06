package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ============================================================
// GitDiffTool Tests
// ============================================================

func TestGitDiffTool_Name(t *testing.T) {
	tool := NewGitDiffTool("/tmp")
	if tool.Name() != "git_diff" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_diff")
	}
}

func TestGitDiffTool_Description(t *testing.T) {
	tool := NewGitDiffTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitDiffTool_Declaration(t *testing.T) {
	tool := NewGitDiffTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "git_diff" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_diff")
	}
}

func TestGitDiffTool_Validate(t *testing.T) {
	tool := NewGitDiffTool("/tmp")

	// All parameters are optional
	err := tool.Validate(nil)
	if err != nil {
		t.Errorf("Validate(nil) unexpected error: %v", err)
	}

	err = tool.Validate(map[string]any{})
	if err != nil {
		t.Errorf("Validate({}) unexpected error: %v", err)
	}
}

func TestGitDiffTool_NewGitDiffTool(t *testing.T) {
	tool := NewGitDiffTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitDiffTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGitDiffTool_Execute_NoChanges(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Commit initial empty state
	cmd := exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run() // Ignore if fails

	tool := NewGitDiffTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success && result.Content == "" {
		// No output is ok for no changes
	}
}

func TestGitDiffTool_Execute_UnstagedChanges(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create and commit a file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("original"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Modify the file
	err = os.WriteFile(filePath, []byte("modified"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}

	tool := NewGitDiffTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitDiffTool_Execute_NameStatus(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create a file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGitDiffTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"name_status": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitDiffTool_Execute_SpecificFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create two files
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	err := os.WriteFile(file1, []byte("content1"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	err = os.WriteFile(file2, []byte("content2"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	tool := NewGitDiffTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file": "file1.txt",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitDiffTool_Execute_Staged(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create and add a file (but don't commit)
	filePath := filepath.Join(tmpDir, "staged.txt")
	err := os.WriteFile(filePath, []byte("staged content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "staged.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}

	tool := NewGitDiffTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"staged": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitDiffTool_Execute_FromTo(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create initial commit
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("v1"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "v1")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Create second commit
	err = os.WriteFile(filePath, []byte("v2"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}

	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	cmd = exec.Command("git", "commit", "-m", "v2")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitDiffTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"from": "HEAD~1",
		"to":   "HEAD",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitDiffTool_Execute_NotAGitRepo(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewGitDiffTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should fail for non-git directory
	if result.Success {
		t.Error("Execute() should fail for non-git directory")
	}
}
