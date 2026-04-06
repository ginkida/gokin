package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/undo"
)

// ============================================================
// WriteTool Tests
// ============================================================

func TestWriteTool_Name(t *testing.T) {
	tool := NewWriteTool("/tmp")
	if tool.Name() != "write" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "write")
	}
}

func TestWriteTool_Description(t *testing.T) {
	tool := NewWriteTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
}

func TestWriteTool_Declaration(t *testing.T) {
	tool := NewWriteTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Error("Declaration() is nil")
	}
	if decl.Name != "write" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "write")
	}
}

func TestWriteTool_Validate(t *testing.T) {
	tool := NewWriteTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"valid args", map[string]any{"file_path": "/tmp/test.txt", "content": "hello"}, false},
		{"missing file_path", map[string]any{"content": "hello"}, true},
		{"missing content", map[string]any{"file_path": "/tmp/test.txt"}, true},
		{"empty file_path", map[string]any{"file_path": "", "content": "hello"}, true},
		{"empty content", map[string]any{"file_path": "/tmp/test.txt", "content": ""}, false}, // Empty content is allowed
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

func TestWriteTool_NewWriteTool(t *testing.T) {
	tool := NewWriteTool("/tmp")

	if tool == nil {
		t.Fatal("NewWriteTool() returned nil")
	}
	if tool.workDir != "/tmp" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/tmp")
	}
	if tool.pathValidator == nil {
		t.Error("pathValidator is nil")
	}
}

func TestWriteTool_SetUndoManager(t *testing.T) {
	tool := NewWriteTool("/tmp")
	manager := undo.NewManager()

	tool.SetUndoManager(manager)

	if tool.undoManager == nil {
		t.Error("undoManager is nil after SetUndoManager")
	}
}

func TestWriteTool_SetDiffHandler(t *testing.T) {
	tool := NewWriteTool("/tmp")
	handler := &mockDiffHandler{approve: true}

	tool.SetDiffHandler(handler)

	if tool.diffHandler != handler {
		t.Error("diffHandler not set correctly")
	}
}

func TestWriteTool_SetDiffEnabled(t *testing.T) {
	tool := NewWriteTool("/tmp")

	tool.SetDiffEnabled(true)
	if !tool.diffEnabled {
		t.Error("diffEnabled = false, want true")
	}

	tool.SetDiffEnabled(false)
	if tool.diffEnabled {
		t.Error("diffEnabled = true, want false")
	}
}

func TestWriteTool_SetAllowedDirs(t *testing.T) {
	tool := NewWriteTool("/tmp")
	tool.SetAllowedDirs([]string{"/var", "/opt"})

	if tool.pathValidator == nil {
		t.Error("pathValidator is nil after SetAllowedDirs")
	}
}

// mockUndoManager implements undo.Manager interface for testing
type mockUndoManager struct{}

func (m *mockUndoManager) Record(filePath string, before, after []byte) error {
	return nil
}

// mockDiffHandler implements DiffHandler for testing
type mockDiffHandler struct {
	approve bool
}

func (m *mockDiffHandler) PromptDiff(ctx context.Context, path, oldContent, newContent, action string, isNew bool) (bool, error) {
	return m.approve, nil
}

// ============================================================
// WriteTool Execute Tests
// ============================================================

func TestWriteTool_Execute_CreateNewFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "new_file.txt")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "hello world",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}

	// Verify file was created
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}
	if string(content) != "hello world" {
		t.Errorf("File content = %v, want %v", string(content), "hello world")
	}
}

func TestWriteTool_Execute_OverwriteFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "existing_file.txt")

	// Create existing file
	err := os.WriteFile(filePath, []byte("old content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "new content",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}

	// Verify file was overwritten
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if string(content) != "new content" {
		t.Errorf("File content = %v, want %v", string(content), "new content")
	}
}

func TestWriteTool_Execute_AppendMode(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "append_file.txt")

	// Create existing file
	err := os.WriteFile(filePath, []byte("line 1\n"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "line 2\n",
		"append":    true,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}

	// Verify content was appended
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if string(content) != "line 1\nline 2\n" {
		t.Errorf("File content = %v, want %v", string(content), "line 1\nline 2\n")
	}
}

func TestWriteTool_Execute_CreateParentDirs(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	// Try to write to a file with non-existent parent directories
	filePath := filepath.Join(tmpDir, "subdir1", "subdir2", "nested_file.txt")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "nested content",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}

	// Verify file was created in nested directory
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file in nested directory: %v", err)
	}
	if string(content) != "nested content" {
		t.Errorf("File content = %v, want %v", string(content), "nested content")
	}
}

func TestWriteTool_Execute_EmptyContent(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "empty_file.txt")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Empty content should succeed (creates/overwrites with empty string)
	if !result.Success {
		t.Errorf("Execute() should succeed with empty content: %s", result.Error)
	}

	// Verify file is empty
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if len(content) != 0 {
		t.Errorf("File content length = %v, want 0", len(content))
	}
}

func TestWriteTool_Execute_BinaryContent(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "binary_file.bin")

	binaryContent := []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   string(binaryContent),
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}

	// Verify binary content
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read binary file: %v", err)
	}
	if string(content) != string(binaryContent) {
		t.Errorf("Binary content mismatch")
	}
}

func TestWriteTool_Execute_NoPathValidator(t *testing.T) {
	tool := NewWriteTool("") // Empty workDir means no path validator

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": "/tmp/test.txt",
		"content":   "test",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// With empty workDir, path validation might work differently
	// Just verify the function completes without panic
	_ = result
}

// ============================================================
// WriteTool Diff Handler Tests
// ============================================================

func TestWriteTool_Execute_DiffEnabled_Approved(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)
	tool.SetDiffEnabled(true)
	tool.SetDiffHandler(&mockDiffHandler{approve: true})

	filePath := filepath.Join(tmpDir, "diff_file.txt")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "approved content",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() should succeed when diff is approved: %s", result.Error)
	}
}

func TestWriteTool_Execute_DiffEnabled_Rejected(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)
	tool.SetDiffEnabled(true)
	tool.SetDiffHandler(&mockDiffHandler{approve: false})

	filePath := filepath.Join(tmpDir, "diff_file.txt")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "rejected content",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when diff is rejected")
	}
	if result.Error != "changes rejected by user" {
		t.Errorf("Error = %v, want %v", result.Error, "changes rejected by user")
	}
}

func TestWriteTool_Execute_DiffEnabled_FileCreated(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)
	tool.SetDiffEnabled(true)
	tool.SetDiffHandler(&mockDiffHandler{approve: true})

	filePath := filepath.Join(tmpDir, "new_file.txt")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "new file content",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() should succeed for new file: %s", result.Error)
	}
}

// ============================================================
// WriteTool Edge Cases
// ============================================================

func TestWriteTool_Execute_AppendToNewFile(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	filePath := filepath.Join(tmpDir, "append_to_new.txt")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"content":   "content",
		"append":    true, // append but file doesn't exist
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should work - just writes the content
	if !result.Success {
		t.Errorf("Execute() should succeed: %s", result.Error)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}
	if string(content) != "content" {
		t.Errorf("Content = %v, want %v", string(content), "content")
	}
}

func TestWriteTool_Execute_PathValidation(t *testing.T) {
	tmpDir := resolvedTempDir(t)
	tool := NewWriteTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": "/etc/passwd", // Outside allowed directory
		"content":   "hacking",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should fail path validation
	if result.Success {
		t.Error("Execute() should fail for path outside workDir")
	}
}

// ============================================================
// GetBoolDefault Tests
// ============================================================

func TestGetBoolDefault(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		key      string
		defValue bool
		want     bool
	}{
		{"true value", map[string]any{"key": true}, "key", false, true},
		{"false value", map[string]any{"key": false}, "key", true, false},
		{"missing key", map[string]any{}, "key", true, true},
		{"missing key default false", map[string]any{}, "key", false, false},
		{"nil value", map[string]any{"key": nil}, "key", true, true},
		{"non-bool value", map[string]any{"key": "true"}, "key", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetBoolDefault(tt.args, tt.key, tt.defValue)
			if got != tt.want {
				t.Errorf("GetBoolDefault() = %v, want %v", got, tt.want)
			}
		})
	}
}
