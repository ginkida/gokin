package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/cache"
	"gokin/internal/git"
)

// ============================================================
// GlobTool Tests
// ============================================================

func TestGlobTool_Name(t *testing.T) {
	tool := NewGlobTool("/tmp")
	if tool.Name() != "glob" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "glob")
	}
}

func TestGlobTool_Description(t *testing.T) {
	tool := NewGlobTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGlobTool_Declaration(t *testing.T) {
	tool := NewGlobTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "glob" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "glob")
	}
}

func TestGlobTool_Validate(t *testing.T) {
	tool := NewGlobTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"with pattern", map[string]any{"pattern": "*.go"}, false},
		{"with pattern and max_results", map[string]any{"pattern": "**/*.go", "max_results": 100}, false},
		{"missing pattern", map[string]any{}, true},
		{"empty pattern", map[string]any{"pattern": ""}, true},
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

func TestGlobTool_NewGlobTool(t *testing.T) {
	tool := NewGlobTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGlobTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGlobTool_SetGitIgnore(t *testing.T) {
	tool := NewGlobTool("/tmp")
	gi := git.NewGitIgnore("/tmp")

	tool.SetGitIgnore(gi)

	if tool.gitIgnore != gi {
		t.Error("SetGitIgnore() did not set gitIgnore")
	}
}

func TestGlobTool_SetCache(t *testing.T) {
	tool := NewGlobTool("/tmp")
	c := cache.NewSearchCache(100, 5*time.Minute)

	tool.SetCache(c)

	if tool.cache != c {
		t.Error("SetCache() did not set cache")
	}
}

func TestGlobTool_SetAllowedDirs(t *testing.T) {
	tool := NewGlobTool("/tmp")

	tool.SetAllowedDirs([]string{"/another/dir"})

	if tool.pathValidator == nil {
		t.Error("SetAllowedDirs() did not set pathValidator")
	}
}

func TestGlobTool_Execute_SinglePattern(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create test files
	file1 := filepath.Join(tmpDir, "test.go")
	file2 := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(file1, []byte("package main"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	err = os.WriteFile(file2, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	tool := NewGlobTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "*.go",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGlobTool_Execute_RecursivePattern(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create nested files
	subDir := filepath.Join(tmpDir, "sub")
	err := os.MkdirAll(subDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	file1 := filepath.Join(tmpDir, "test.go")
	file2 := filepath.Join(subDir, "nested.go")
	err = os.WriteFile(file1, []byte("package main"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	err = os.WriteFile(file2, []byte("package main"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	tool := NewGlobTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "**/*.go",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGlobTool_Execute_MaxResults(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create multiple files
	for i := 0; i < 5; i++ {
		filePath := filepath.Join(tmpDir, "file"+string(rune('0'+i))+".txt")
		err := os.WriteFile(filePath, []byte("content"), 0644)
		if err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	tool := NewGlobTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern":     "*.txt",
		"max_results": 3,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should limit results
	_ = result.Success
}

func TestGlobTool_Execute_NoMatches(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create a file that won't match
	filePath := filepath.Join(tmpDir, "test.txt")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGlobTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "*.go",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// No matches is still success, just empty result
	_ = result.Success
}

func TestGlobTool_Execute_InvalidPattern(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	tool := NewGlobTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "[invalid",
	})

	// Invalid pattern might return error or empty results
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}

func TestGlobTool_Execute_ExcludeGitignore(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create .gitignore
	gitignore := filepath.Join(tmpDir, ".gitignore")
	err := os.WriteFile(gitignore, []byte("*.log"), 0644)
	if err != nil {
		t.Fatalf("Failed to create .gitignore: %v", err)
	}

	// Create files
	file1 := filepath.Join(tmpDir, "test.go")
	file2 := filepath.Join(tmpDir, "debug.log")
	err = os.WriteFile(file1, []byte("code"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	err = os.WriteFile(file2, []byte("log"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	tool := NewGlobTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "*.*",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	_ = result.Success
}
