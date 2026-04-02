package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ============================================================
// GitCommitTool Tests
// ============================================================

func TestGitCommitTool_Name(t *testing.T) {
	tool := NewGitCommitTool("/tmp")
	if tool.Name() != "git_commit" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_commit")
	}
}

func TestGitCommitTool_Description(t *testing.T) {
	tool := NewGitCommitTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitCommitTool_Declaration(t *testing.T) {
	tool := NewGitCommitTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "git_commit" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_commit")
	}
}

func TestGitCommitTool_Validate(t *testing.T) {
	tool := NewGitCommitTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"with message", map[string]any{"message": "fix: something"}, false},
		{"with empty message", map[string]any{"message": ""}, true},
		{"missing message", map[string]any{}, true},
		{"auto_message true", map[string]any{"auto_message": true}, false},
		{"auto_message false, no message", map[string]any{"auto_message": false}, true},
		{"whitespace only message", map[string]any{"message": "   "}, true},
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

func TestGitCommitTool_NewGitCommitTool(t *testing.T) {
	tool := NewGitCommitTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitCommitTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGitCommitTool_Execute_NotAGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGitCommitTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"message": "test commit",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for non-git directory")
	}
}

func TestGitCommitTool_Execute_NoStagedChanges(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	tool := NewGitCommitTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"message": "test commit",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should fail - nothing to commit
	if result.Success {
		t.Error("Execute() should fail when nothing to commit")
	}
}

func TestGitCommitTool_Execute_Success(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create and stage a file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add failed: %v", err)
	}

	tool := NewGitCommitTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"message": "feat: add test file",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitCommitTool_Execute_AllFlag(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create a file
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	// Also create a tracked file (already committed)
	filePath2 := filepath.Join(tmpDir, "test2.txt")
	err = os.WriteFile(filePath2, []byte("content2"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	cmd := exec.Command("git", "add", "test2.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Modify tracked file
	err = os.WriteFile(filePath2, []byte("modified"), 0644)
	if err != nil {
		t.Fatalf("Failed to modify file2: %v", err)
	}

	tool := NewGitCommitTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"message": "chore: update file",
		"all":     true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitCommitTool_Execute_Amend(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create initial commit
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitCommitTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"message": "amended commit",
		"amend":   true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitCommitTool_Execute_NilArgs(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	tool := NewGitCommitTool(tmpDir)

	err := tool.Validate(nil)
	if err == nil {
		t.Error("Validate(nil) should fail - message is required")
	}
}
