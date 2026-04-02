package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================
// GitStatusTool Tests
// ============================================================

func TestGitStatusTool_Name(t *testing.T) {
	tool := NewGitStatusTool("/tmp")
	if tool.Name() != "git_status" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_status")
	}
}

func TestGitStatusTool_Description(t *testing.T) {
	tool := NewGitStatusTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitStatusTool_Declaration(t *testing.T) {
	tool := NewGitStatusTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "git_status" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_status")
	}
}

func TestGitStatusTool_Validate(t *testing.T) {
	tool := NewGitStatusTool("/tmp")

	// All parameters are optional, should always pass
	err := tool.Validate(nil)
	if err != nil {
		t.Errorf("Validate(nil) unexpected error: %v", err)
	}

	err = tool.Validate(map[string]any{})
	if err != nil {
		t.Errorf("Validate({}) unexpected error: %v", err)
	}
}

func TestGitStatusTool_NewGitStatusTool(t *testing.T) {
	tool := NewGitStatusTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitStatusTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

// initGitRepo initializes a temporary git repository
func initGitRepo(t *testing.T, dir string) {
	t.Helper()

	// git init
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	// git config
	cmd = exec.Command("git", "config", "user.email", "test@example.com")
	cmd.Dir = dir
	_ = cmd.Run() // Ignore errors

	cmd = exec.Command("git", "config", "user.name", "Test User")
	cmd.Dir = dir
	_ = cmd.Run() // Ignore errors
}

func TestGitStatusTool_Execute_NotAGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGitStatusTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should return error for non-git directory
	if result.Success {
		t.Error("Execute() should fail for non-git directory")
	}
}

func TestGitStatusTool_Execute_CleanRepo(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	tool := NewGitStatusTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitStatusTool_Execute_WithChanges(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create a file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tool := NewGitStatusTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	if !strings.Contains(result.Content, "test.txt") {
		t.Errorf("Result should contain 'test.txt', got: %s", result.Content)
	}
}

func TestGitStatusTool_Execute_ShortFormat(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create a file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tool := NewGitStatusTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"short": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitStatusTool_Execute_CustomPath(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create a file
	filePath := filepath.Join(tmpDir, "root.txt")
	err := os.WriteFile(filePath, []byte("root"), 0644)
	if err != nil {
		t.Fatalf("Failed to create root file: %v", err)
	}

	tool := NewGitStatusTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"path": tmpDir,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitStatusTool_Execute_StagedChanges(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create and stage a file
	filePath := filepath.Join(tmpDir, "staged.txt")
	err := os.WriteFile(filePath, []byte("staged content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create staged file: %v", err)
	}

	// git add
	cmd := exec.Command("git", "add", "staged.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}

	tool := NewGitStatusTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
	if !strings.Contains(result.Content, "staged.txt") {
		t.Errorf("Result should contain 'staged.txt', got: %s", result.Content)
	}
}
