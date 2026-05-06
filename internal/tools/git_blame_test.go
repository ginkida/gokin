package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ============================================================
// GitBlameTool Tests
// ============================================================

func TestGitBlameTool_Name(t *testing.T) {
	tool := NewGitBlameTool("/tmp")
	if tool.Name() != "git_blame" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_blame")
	}
}

func TestGitBlameTool_Description(t *testing.T) {
	tool := NewGitBlameTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitBlameTool_Declaration(t *testing.T) {
	tool := NewGitBlameTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "git_blame" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_blame")
	}
}

func TestGitBlameTool_Validate(t *testing.T) {
	tool := NewGitBlameTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"with file", map[string]any{"file": "test.go"}, false},
		{"with file and lines", map[string]any{"file": "test.go", "start_line": 1, "end_line": 10}, false},
		{"missing file", map[string]any{}, true},
		{"empty file", map[string]any{"file": ""}, true},
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

func TestGitBlameTool_NewGitBlameTool(t *testing.T) {
	tool := NewGitBlameTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitBlameTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGitBlameTool_Execute_NotAGitRepo(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewGitBlameTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file": "test.txt",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for non-git directory")
	}
}

func TestGitBlameTool_Execute_UntrackedFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create but don't commit
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGitBlameTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file": "test.txt",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Untracked file might fail or return empty
	_ = result.Success
}

func TestGitBlameTool_Execute_TrackedFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create and commit
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("line1\nline2\nline3\n"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "add file")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitBlameTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file": "test.txt",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitBlameTool_Execute_LineRange(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create file with multiple lines
	filePath := filepath.Join(tmpDir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "add file")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitBlameTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file":       "test.txt",
		"start_line": 2,
		"end_line":   4,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitBlameTool_Execute_NonexistentFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	tool := NewGitBlameTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file": "nonexistent.txt",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for nonexistent file")
	}
}
