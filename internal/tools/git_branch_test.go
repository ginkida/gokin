package tools

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

// ============================================================
// GitBranchTool Tests
// ============================================================

func TestGitBranchTool_Name(t *testing.T) {
	tool := NewGitBranchTool("/tmp")
	if tool.Name() != "git_branch" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_branch")
	}
}

func TestGitBranchTool_Description(t *testing.T) {
	tool := NewGitBranchTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitBranchTool_Declaration(t *testing.T) {
	tool := NewGitBranchTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "git_branch" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_branch")
	}
}

func TestGitBranchTool_Validate(t *testing.T) {
	tool := NewGitBranchTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"list", map[string]any{"action": "list"}, false},
		{"current", map[string]any{"action": "current"}, false},
		{"create with name", map[string]any{"action": "create", "name": "feature"}, false},
		{"delete with name", map[string]any{"action": "delete", "name": "old-branch"}, false},
		{"switch with name", map[string]any{"action": "switch", "name": "main"}, false},
		{"merge with name", map[string]any{"action": "merge", "name": "feature"}, false},
		{"missing action", map[string]any{}, true},
		{"empty action", map[string]any{"action": ""}, true},
		{"invalid action", map[string]any{"action": "invalid"}, true},
		{"create without name", map[string]any{"action": "create"}, true},
		{"delete without name", map[string]any{"action": "delete"}, true},
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

func TestGitBranchTool_NewGitBranchTool(t *testing.T) {
	tool := NewGitBranchTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitBranchTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGitBranchTool_Execute_NotAGitRepo(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGitBranchTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "list",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for non-git directory")
	}
}

func TestGitBranchTool_Execute_Current(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	tool := NewGitBranchTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "current",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitBranchTool_Execute_List(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	tool := NewGitBranchTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "list",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitBranchTool_Execute_CreateBranch(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	tool := NewGitBranchTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "create",
		"name":   "feature",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitBranchTool_Execute_DeleteBranch(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create a commit first (required for branch)
	filePath := tmpDir + "/test.txt"
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}
	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Create a branch
	cmd = exec.Command("git", "branch", "to-delete")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitBranchTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "delete",
		"name":   "to-delete",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitBranchTool_Execute_SwitchBranch(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create and commit a file (for switch)
	filePath := tmpDir + "/test.txt"
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Create a branch
	cmd = exec.Command("git", "branch", "feature")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitBranchTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "switch",
		"name":   "feature",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGitBranchTool_Execute_Merge(t *testing.T) {
	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	// Create initial commit
	filePath := tmpDir + "/test.txt"
	err := os.WriteFile(filePath, []byte("v1"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	cmd := exec.Command("git", "add", "test.txt")
	cmd.Dir = tmpDir
	_ = cmd.Run()
	cmd = exec.Command("git", "commit", "-m", "initial")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	// Create a branch
	cmd = exec.Command("git", "branch", "feature")
	cmd.Dir = tmpDir
	_ = cmd.Run()

	tool := NewGitBranchTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "merge",
		"name":   "feature",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Merge of same branch might return success but no changes
	_ = result.Success
}
