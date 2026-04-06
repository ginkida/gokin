package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ============================================================
// GitLogTool Tests
// ============================================================

func TestGitLogTool_Name(t *testing.T) {
	tool := NewGitLogTool("/tmp")
	if tool.Name() != "git_log" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_log")
	}
}

func TestGitLogTool_Description(t *testing.T) {
	tool := NewGitLogTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitLogTool_Declaration(t *testing.T) {
	tool := NewGitLogTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "git_log" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_log")
	}
}

func TestGitLogTool_Validate(t *testing.T) {
	tool := NewGitLogTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"no args", map[string]any{}, false},
		{"valid count", map[string]any{"count": 5}, false},
		{"count 0", map[string]any{"count": 0}, true},
		{"count 101", map[string]any{"count": 101}, true},
		{"negative count", map[string]any{"count": -1}, true},
		{"nil args", nil, false},
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

func TestGitLogTool_NewGitLogTool(t *testing.T) {
	tool := NewGitLogTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitLogTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGitLogTool_Execute_NotAGitRepo(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewGitLogTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for non-git directory")
	}
}

func TestGitLogTool_Execute_EmptyHistory(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	tool := NewGitLogTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Empty repo returns failure
	if result.Success {
		t.Error("Execute() should return error for empty repo")
	}
}

func TestGitLogTool_Execute_WithCommits(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create first commit
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("v1"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "first commit")
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
	cmd = exec.Command("git", "commit", "-m", "second commit")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitLogTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitLogTool_Execute_LimitCount(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	filePath := filepath.Join(tmpDir, "test.txt")

	// Create multiple commits
	for i := 1; i <= 5; i++ {
		err := os.WriteFile(filePath, []byte{byte('0' + i)}, 0644)
		if err != nil {
			t.Fatalf("Failed to write file: %v", err)
		}
		cmd := exec.Command("git", "add", "test.txt")
		cmd.Dir = tmpDir
		_ = cmd.Run()
		cmd = exec.Command("git", "commit", "-m", "commit number "+string(rune('0'+i)))
		cmd.Dir = tmpDir
		_ = cmd.Run()
	}

	tool := NewGitLogTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"count": 3,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitLogTool_Execute_OnelineFormat(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "test commit")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitLogTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"oneline": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitLogTool_Execute_FileHistory(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	initGitRepo(t, tmpDir)

	// Create file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("v1"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "add file")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Modify file
	err = os.WriteFile(filePath, []byte("v2"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}
	cmd = exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "update file")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitLogTool(tmpDir)

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
