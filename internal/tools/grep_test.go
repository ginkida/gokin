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
// GrepTool Tests
// ============================================================

func TestGrepTool_Name(t *testing.T) {
	tool := NewGrepTool("/tmp")
	if tool.Name() != "grep" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "grep")
	}
}

func TestGrepTool_Description(t *testing.T) {
	tool := NewGrepTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestGrepTool_Declaration(t *testing.T) {
	tool := NewGrepTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "grep" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "grep")
	}
}

func TestGrepTool_Validate(t *testing.T) {
	tool := NewGrepTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"with pattern", map[string]any{"pattern": "func"}, false},
		{"with file", map[string]any{"pattern": "func", "file": "test.go"}, false},
		{"with case_insensitive", map[string]any{"pattern": "func", "case_insensitive": true}, false},
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

func TestGrepTool_NewGrepTool(t *testing.T) {
	tool := NewGrepTool("/home/user/project")

	if tool == nil {
		t.Fatal("NewGrepTool() returned nil")
	}
	if tool.workDir != "/home/user/project" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home/user/project")
	}
}

func TestGrepTool_SetGitIgnore(t *testing.T) {
	tool := NewGrepTool("/tmp")
	gi := git.NewGitIgnore("/tmp")

	tool.SetGitIgnore(gi)

	if tool.gitIgnore != gi {
		t.Error("SetGitIgnore() did not set gitIgnore")
	}
}

func TestGrepTool_SetCache(t *testing.T) {
	tool := NewGrepTool("/tmp")
	c := cache.NewSearchCache(100, 5*time.Minute)

	tool.SetCache(c)

	if tool.cache != c {
		t.Error("SetCache() did not set cache")
	}
}

func TestGrepTool_SetAllowedDirs(t *testing.T) {
	tool := NewGrepTool("/tmp")

	tool.SetAllowedDirs([]string{"/another/dir"})

	if tool.pathValidator == nil {
		t.Error("SetAllowedDirs() did not set pathValidator")
	}
}

func TestGrepTool_SetPredictor(t *testing.T) {
	tool := NewGrepTool("/tmp")
	p := &mockPredictor{}

	tool.SetPredictor(p)

	if tool.predictor != p {
		t.Error("SetPredictor() did not set predictor")
	}
}

// mockPredictor implements GrepPredictorInterface for testing
type mockPredictor struct{}

func (m *mockPredictor) RecordAccess(path, accessType, fromFile string) {}

func TestGrepTool_Execute_SimplePattern(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create a file with content
	filePath := filepath.Join(tmpDir, "test.go")
	content := "package main\n\nfunc main() {}\n"
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "func",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_SpecificFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create files
	file1 := filepath.Join(tmpDir, "match.go")
	file2 := filepath.Join(tmpDir, "no_match.txt")
	err := os.WriteFile(file1, []byte("package main\nfunc test() {}\n"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	err = os.WriteFile(file2, []byte("no match here"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "func",
		"file":    "match.go",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_CaseInsensitive(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	filePath := filepath.Join(tmpDir, "test.go")
	content := "package main\n\nfunc Test() {}\n"
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern":          "TEST",
		"case_insensitive": true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_NoMatches(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	filePath := filepath.Join(tmpDir, "test.go")
	content := "package main\n\nfunc test() {}\n"
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "nonexistent",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// No matches is still success with empty result
	_ = result.Success
}

func TestGrepTool_Execute_MultipleFiles(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	// Create multiple files
	for i := 0; i < 3; i++ {
		filePath := filepath.Join(tmpDir, "file"+string(rune('0'+i))+".go")
		err := os.WriteFile(filePath, []byte("package main\nfunc test() {}\n"), 0644)
		if err != nil {
			t.Fatalf("Failed to create file: %v", err)
		}
	}

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "func",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_WithContext(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	filePath := filepath.Join(tmpDir, "test.go")
	content := "line1\nline2\nline3\nline4\nline5\n"
	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern":       "line3",
		"context_lines": 1,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestGrepTool_Execute_InvalidRegex(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	filePath := filepath.Join(tmpDir, "test.go")
	err := os.WriteFile(filePath, []byte("content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "[invalid(",
	})

	// Invalid regex might return error
	if err != nil {
		t.Logf("Execute() returned expected error for invalid regex: %v", err)
	}
	_ = result.Success
}

func TestGrepTool_Execute_NonexistentFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	tool := NewGrepTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "test",
		"file":    "nonexistent.go",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should handle gracefully
	_ = result.Success
}
