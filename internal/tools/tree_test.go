package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ============================================================
// TreeTool Tests
// ============================================================

func TestTreeTool_Name(t *testing.T) {
	tool := NewTreeTool("/tmp")
	if tool.Name() != "tree" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "tree")
	}
}

func TestTreeTool_Description(t *testing.T) {
	tool := NewTreeTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestTreeTool_Declaration(t *testing.T) {
	tool := NewTreeTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "tree" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "tree")
	}
}

func TestTreeTool_Validate(t *testing.T) {
	tool := NewTreeTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"no args", map[string]any{}, false},
		{"with depth", map[string]any{"depth": 3}, false},
		{"with dir", map[string]any{"dir": "src"}, false},
		{"with all", map[string]any{"all": true}, false},
		{"negative depth", map[string]any{"depth": -1}, true},
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

func TestTreeTool_NewTreeTool(t *testing.T) {
	tool := NewTreeTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewTreeTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestTreeTool_SetAllowedDirs(t *testing.T) {
	tool := NewTreeTool("/tmp")

	tool.SetAllowedDirs([]string{"/another/dir"})

	if tool.pathValidator == nil {
		t.Error("SetAllowedDirs() did not set pathValidator")
	}
}

func TestTreeTool_Execute_EmptyDir(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	tool := NewTreeTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestTreeTool_Execute_WithFiles(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create some files
	files := []string{"file1.txt", "file2.go"}
	for _, f := range files {
		filePath := filepath.Join(tmpDir, f)
		err := os.WriteFile(filePath, []byte("content"), 0644)
		if err != nil {
			t.Fatalf("Failed to create file %s: %v", f, err)
		}
	}

	tool := NewTreeTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestTreeTool_Execute_NestedDirs(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create nested structure
	subDir := filepath.Join(tmpDir, "src")
	nestedDir := filepath.Join(subDir, "nested")
	err := os.MkdirAll(nestedDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create dirs: %v", err)
	}

	// Create files at different levels
	err = os.WriteFile(filepath.Join(tmpDir, "root.txt"), []byte("root"), 0644)
	if err != nil {
		t.Fatalf("Failed to create root file: %v", err)
	}
	err = os.WriteFile(filepath.Join(subDir, "sub.txt"), []byte("sub"), 0644)
	if err != nil {
		t.Fatalf("Failed to create sub file: %v", err)
	}
	err = os.WriteFile(filepath.Join(nestedDir, "nested.txt"), []byte("nested"), 0644)
	if err != nil {
		t.Fatalf("Failed to create nested file: %v", err)
	}

	tool := NewTreeTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestTreeTool_Execute_WithDepth(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create nested structure
	subDir := filepath.Join(tmpDir, "level1")
	subSubDir := filepath.Join(subDir, "level2")
	err := os.MkdirAll(subSubDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create dirs: %v", err)
	}

	err = os.WriteFile(filepath.Join(tmpDir, "l0.txt"), []byte("l0"), 0644)
	if err != nil {
		t.Fatalf("Failed to create l0 file: %v", err)
	}
	err = os.WriteFile(filepath.Join(subDir, "l1.txt"), []byte("l1"), 0644)
	if err != nil {
		t.Fatalf("Failed to create l1 file: %v", err)
	}
	err = os.WriteFile(filepath.Join(subSubDir, "l2.txt"), []byte("l2"), 0644)
	if err != nil {
		t.Fatalf("Failed to create l2 file: %v", err)
	}

	tool := NewTreeTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"depth": 1,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestTreeTool_Execute_WithAll(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create hidden file
	err := os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("visible"), 0644)
	if err != nil {
		t.Fatalf("Failed to create visible file: %v", err)
	}
	err = os.WriteFile(filepath.Join(tmpDir, ".hidden"), []byte("hidden"), 0644)
	if err != nil {
		t.Fatalf("Failed to create hidden file: %v", err)
	}

	tool := NewTreeTool(tmpDir)

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

func TestTreeTool_Execute_SpecificDir(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create sub directory with files
	subDir := filepath.Join(tmpDir, "src")
	err := os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}
	err = os.WriteFile(filepath.Join(subDir, "main.go"), []byte("package main"), 0644)
	if err != nil {
		t.Fatalf("Failed to create main.go: %v", err)
	}

	tool := NewTreeTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"dir": "src",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestTreeTool_Execute_DirOnly(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create only directories
	subDir := filepath.Join(tmpDir, "empty_dir")
	err := os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	tool := NewTreeTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"dirs_only": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}
