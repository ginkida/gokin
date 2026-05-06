package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ============================================================
// GitAddTool Tests
// ============================================================

func TestGitAddTool_Name(t *testing.T) {
	tool := NewGitAddTool("/tmp")
	if tool.Name() != "git_add" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_add")
	}
}

func TestGitAddTool_Description(t *testing.T) {
	tool := NewGitAddTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitAddTool_Declaration(t *testing.T) {
	tool := NewGitAddTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "git_add" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_add")
	}
}

func TestGitAddTool_Validate(t *testing.T) {
	tool := NewGitAddTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"with paths", map[string]any{"paths": []any{"file.txt"}}, false},
		{"with all", map[string]any{"all": true}, false},
		{"with update", map[string]any{"update": true}, false},
		{"empty args", map[string]any{}, true},
		{"nil args", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tool.Validate(tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestGitAddTool_NewGitAddTool(t *testing.T) {
	tool := NewGitAddTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitAddTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGitAddTool_Execute_NotAGitRepo(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewGitAddTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"paths": []any{"file.txt"},
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for non-git directory")
	}
}

func TestGitAddTool_Execute_SingleFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create a file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGitAddTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"paths": []any{"test.txt"},
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitAddTool_Execute_MultipleFiles(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create multiple files
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

	tool := NewGitAddTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"paths": []any{"file1.txt", "file2.txt"},
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitAddTool_Execute_All(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create multiple files
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

	tool := NewGitAddTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"all": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitAddTool_Execute_Update(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create and commit a file
	filePath := filepath.Join(tmpDir, "tracked.txt")
	err := os.WriteFile(filePath, []byte("original"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "tracked.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Modify tracked file
	err = os.WriteFile(filePath, []byte("modified"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}

	tool := NewGitAddTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"update": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitAddTool_Execute_NonexistentFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	tool := NewGitAddTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"paths": []any{"nonexistent.txt"},
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should handle gracefully
	_ = result.Success
}
