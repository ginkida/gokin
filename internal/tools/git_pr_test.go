package tools

import (
	"context"
	"testing"
)

// ============================================================
// GitPRTool Tests
// ============================================================

func TestGitPRTool_Name(t *testing.T) {
	tool := NewGitPRTool("/tmp")
	if tool.Name() != "git_pr" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "git_pr")
	}
}

func TestGitPRTool_Description(t *testing.T) {
	tool := NewGitPRTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGitPRTool_Declaration(t *testing.T) {
	tool := NewGitPRTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "git_pr" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "git_pr")
	}
}

func TestGitPRTool_Validate(t *testing.T) {
	tool := NewGitPRTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"create with title", map[string]any{"action": "create", "title": "Feature PR"}, false},
		{"create without title", map[string]any{"action": "create"}, false}, // title can be auto-generated
		{"list", map[string]any{"action": "list"}, false},
		{"view with pr_number", map[string]any{"action": "view", "pr_number": "123"}, false},
		{"checks with pr_number", map[string]any{"action": "checks", "pr_number": "123"}, false},
		{"merge with pr_number", map[string]any{"action": "merge", "pr_number": "123"}, false},
		{"close with pr_number", map[string]any{"action": "close", "pr_number": "123"}, false},
		{"missing action", map[string]any{}, true},
		{"view without pr_number", map[string]any{"action": "view"}, true},
		{"merge without pr_number", map[string]any{"action": "merge"}, true},
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

func TestGitPRTool_NewGitPRTool(t *testing.T) {
	tool := NewGitPRTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGitPRTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGitPRTool_Execute_RequiresGH(t *testing.T) {
	// This test verifies the tool exists and can be executed
	// Actual gh commands require gh CLI to be installed
	t.Skip("Requires gh CLI to be installed")

	tmpDir := t.TempDir()
	initGitRepo(t, tmpDir)

	tool := NewGitPRTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"action": "list",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}
