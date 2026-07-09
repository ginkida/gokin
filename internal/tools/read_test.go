package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================
// ReadTool Tests
// ============================================================

func TestReadTool_Name(t *testing.T) {
	tool := NewReadTool("/tmp")
	if tool.Name() != "read" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "read")
	}
}

func TestReadTool_Description(t *testing.T) {
	tool := NewReadTool("/tmp")
	desc := tool.Description()

	if desc == "" {
		t.Error("Description() is empty")
	}
	if len(desc) < 100 {
		t.Error("Description() seems too short")
	}
}

func TestReadTool_Declaration(t *testing.T) {
	tool := NewReadTool("/tmp")
	decl := tool.Declaration()

	if decl == nil {
		t.Fatal("Declaration() is nil")
	}
	if decl.Name != "read" {
		t.Errorf("Declaration().Name = %v, want %v", decl.Name, "read")
	}
}

func TestReadTool_Validate(t *testing.T) {
	tool := NewReadTool("/tmp")

	tests := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"valid path", map[string]any{"file_path": "/tmp/test.txt"}, false},
		{"empty path", map[string]any{"file_path": ""}, true},
		{"missing path", map[string]any{}, true},
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

func TestReadTool_NewReadTool(t *testing.T) {
	tool := NewReadTool("/tmp")

	if tool == nil {
		t.Fatal("NewReadTool() returned nil")
	}
	if tool.workDir != "/tmp" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/tmp")
	}
	if tool.pathValidator == nil {
		t.Error("pathValidator is nil")
	}
}

func TestReadTool_NewReadToolEmptyWorkDir(t *testing.T) {
	tool := NewReadTool("")

	if tool == nil {
		t.Fatal("NewReadTool() returned nil")
	}
	if tool.pathValidator != nil {
		t.Error("pathValidator should be nil for empty workDir")
	}
}

func TestReadTool_SetWorkDir(t *testing.T) {
	tool := NewReadTool("")
	tool.SetWorkDir("/home")

	if tool.workDir != "/home" {
		t.Errorf("workDir = %v, want %v", tool.workDir, "/home")
	}
	if tool.pathValidator == nil {
		t.Error("pathValidator is nil after SetWorkDir")
	}
}

func TestReadTool_SetAllowedDirs(t *testing.T) {
	tool := NewReadTool("/tmp")
	tool.SetAllowedDirs([]string{"/var", "/opt"})

	if tool.pathValidator == nil {
		t.Error("pathValidator is nil after SetAllowedDirs")
	}
}

func TestReadTool_SetPredictor(t *testing.T) {
	tool := NewReadTool("/tmp")
	predictor := &mockContextPredictor{}

	tool.SetPredictor(predictor)

	if tool.predictor != predictor {
		t.Error("predictor not set correctly")
	}
}

// mockContextPredictor implements ContextPredictorInterface for testing
type mockContextPredictor struct{}

func (m *mockContextPredictor) RecordAccess(path, accessType, fromFile string) {}
func (m *mockContextPredictor) LearnImports(filePath string)                   {}

// ============================================================
// ReadTool Execute Tests (with test data)
// ============================================================

func createTestFile(t *testing.T, content string) string {
	t.Helper()

	tmpDir := resolvedTempDir(t)
	filePath := filepath.Join(tmpDir, "test.txt")

	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	return filePath
}

func TestReadTool_Execute_TextFile(t *testing.T) {
	content := "line 1\nline 2\nline 3\n"
	filePath := createTestFile(t, content)

	tool := NewReadTool(t.TempDir())
	tool.SetAllowedDirs([]string{filepath.Dir(filePath)})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false, content: %s", result.Error)
	}
}

func TestReadTool_Execute_WithOffset(t *testing.T) {
	content := "line 1\nline 2\nline 3\n"
	filePath := createTestFile(t, content)

	tool := NewReadTool(t.TempDir())
	tool.SetAllowedDirs([]string{filepath.Dir(filePath)})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"offset":    2,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestReadTool_Execute_WithLimit(t *testing.T) {
	content := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	filePath := createTestFile(t, content)

	tool := NewReadTool(t.TempDir())
	tool.SetAllowedDirs([]string{filepath.Dir(filePath)})

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": filePath,
		"limit":     2,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() result.Success = false: %s", result.Error)
	}
}

func TestReadTool_Execute_FileNotFound(t *testing.T) {
	tool := NewReadTool("/tmp")

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": "/tmp/nonexistent_file_12345.txt",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should return error for non-existent file")
	}
	if result.Error == "" {
		t.Error("Execute() should set Error field for non-existent file")
	}
}

func TestReadTool_Execute_Directory(t *testing.T) {
	tmpDir := resolvedTempDir(t)

	tool := NewReadTool(tmpDir)

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": tmpDir,
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if result.Success {
		t.Error("Execute() should return error for directory")
	}
}

// ============================================================
// ChunkedReader Tests
// ============================================================

func TestChunkedReader_TotalLines(t *testing.T) {
	content := "line 1\nline 2\nline 3\n"
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, 100)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}

	if reader.TotalLines() != 3 {
		t.Errorf("TotalLines() = %v, want %v", reader.TotalLines(), 3)
	}
}

func TestChunkedReader_DefaultChunkSize(t *testing.T) {
	content := "line 1\nline 2\n"
	filePath := createTestFile(t, content)

	// Negative chunk size should use default
	reader, err := NewChunkedReader(filePath, -5)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}
	if reader.chunkSize != DefaultChunkSize {
		t.Errorf("chunkSize = %v, want %v", reader.chunkSize, DefaultChunkSize)
	}
}

func TestChunkedReader_NextChunk(t *testing.T) {
	content := "line 1\nline 2\nline 3\nline 4\n"
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, 2)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}

	lines, startLine, hasMore, err := reader.NextChunk()
	if err != nil {
		t.Fatalf("NextChunk() error: %v", err)
	}

	if len(lines) != 2 {
		t.Errorf("len(lines) = %v, want %v", len(lines), 2)
	}
	if startLine != 1 {
		t.Errorf("startLine = %v, want %v", startLine, 1)
	}
	if !hasMore {
		t.Error("hasMore = false, want true")
	}

	// Read second chunk
	lines2, startLine2, hasMore2, err := reader.NextChunk()
	if err != nil {
		t.Fatalf("NextChunk() error: %v", err)
	}
	if len(lines2) != 2 {
		t.Errorf("len(lines2) = %v, want %v", len(lines2), 2)
	}
	if startLine2 != 3 {
		t.Errorf("startLine2 = %v, want %v", startLine2, 3)
	}
	if hasMore2 {
		t.Error("hasMore2 = true, want false (all lines read)")
	}
}

func TestChunkedReader_NextChunk_Empty(t *testing.T) {
	content := ""
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, 10)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}

	lines, _, hasMore, err := reader.NextChunk()
	if err != nil {
		t.Fatalf("NextChunk() error: %v", err)
	}

	// Empty file should return no lines (but not error)
	if len(lines) != 0 {
		t.Errorf("len(lines) = %v, want %v", len(lines), 0)
	}
	if hasMore {
		t.Error("hasMore = true for empty file, want false")
	}
}

func TestChunkedReader_SeekToLine(t *testing.T) {
	content := "line 1\nline 2\nline 3\n"
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, 10)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}

	err = reader.SeekToLine(2)
	if err != nil {
		t.Fatalf("SeekToLine() error: %v", err)
	}

	lines, startLine, _, err := reader.NextChunk()
	if err != nil {
		t.Fatalf("NextChunk() error: %v", err)
	}

	if startLine != 2 {
		t.Errorf("startLine = %v, want %v", startLine, 2)
	}
	if len(lines) < 1 || lines[0] != "line 2" {
		t.Errorf("first line = %v, want %v", lines[0], "line 2")
	}
}

func TestChunkedReader_SeekToLine_Invalid(t *testing.T) {
	content := "line 1\nline 2\n"
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, 10)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}

	// Line 0 should be normalized to 1
	err = reader.SeekToLine(0)
	if err != nil {
		t.Fatalf("SeekToLine(0) error: %v", err)
	}
}

func TestChunkedReader_Close(t *testing.T) {
	content := "line 1\nline 2\n"
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, 10)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}

	err = reader.Close()
	if err != nil {
		t.Errorf("Close() error: %v", err)
	}

	// Should be able to seek and read after close
	err = reader.SeekToLine(1)
	if err != nil {
		t.Errorf("SeekToLine() after Close() error: %v", err)
	}
}

// clampChunkLimit must bound `limit` at BOTH ends. Only the lower bound
// (limit<=0) was clamped before; a huge model-supplied limit passed straight
// through as ChunkedReader's chunkSize, which preallocates/scans that many
// lines in one NextChunk() call regardless of the file's actual size —
// defeating the >10MB "large file" chunking safeguard this path exists for.
func TestClampChunkLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero falls back to default", 0, DefaultChunkSize},
		{"negative falls back to default", -5, DefaultChunkSize},
		{"within bounds passes through", 500, 500},
		{"at the cap passes through", MaxChunkSize, MaxChunkSize},
		{"oversized clamps to cap", 50_000_000, MaxChunkSize},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampChunkLimit(tt.limit); got != tt.want {
				t.Errorf("clampChunkLimit(%d) = %d, want %d", tt.limit, got, tt.want)
			}
		})
	}
}

// End-to-end: readLargeFile must not choke on an oversized model-supplied
// limit (it should clamp and proceed normally, not panic/hang/OOM).
func TestReadLargeFile_ClampsOversizedLimit(t *testing.T) {
	content := "line 1\nline 2\nline 3\n"
	filePath := createTestFile(t, content)

	tool := NewReadTool(t.TempDir())
	tool.SetAllowedDirs([]string{filepath.Dir(filePath)})

	result, err := tool.readLargeFile(context.Background(), filePath, map[string]any{
		"limit": float64(50_000_000),
	})
	if err != nil {
		t.Fatalf("readLargeFile() error: %v", err)
	}
	if !result.Success {
		t.Fatalf("readLargeFile() failed: %s", result.Error)
	}
}

func TestNewChunkedReader_InvalidPath(t *testing.T) {
	_, err := NewChunkedReader("/tmp/nonexistent_file_12345.txt", 10)
	if err == nil {
		t.Error("NewChunkedReader() expected error for non-existent file")
	}
}

func TestNewChunkedReader_NegativeChunkSize(t *testing.T) {
	content := "test\n"
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, -5)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}
	if reader.chunkSize != DefaultChunkSize {
		t.Errorf("chunkSize = %v, want %v", reader.chunkSize, DefaultChunkSize)
	}
}

func TestNewChunkedReader_ZeroChunkSize(t *testing.T) {
	content := "test\n"
	filePath := createTestFile(t, content)

	reader, err := NewChunkedReader(filePath, 0)
	if err != nil {
		t.Fatalf("NewChunkedReader() error: %v", err)
	}
	if reader.chunkSize != DefaultChunkSize {
		t.Errorf("chunkSize = %v, want %v", reader.chunkSize, DefaultChunkSize)
	}
}

// ============================================================
// Helper Functions Tests
// ============================================================

func TestGetString(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]any
		key    string
		want   string
		wantOk bool
	}{
		{"valid string", map[string]any{"key": "value"}, "key", "value", true},
		{"missing key", map[string]any{}, "key", "", false},
		{"nil value", map[string]any{"key": nil}, "key", "", false},
		{"non-string value", map[string]any{"key": 123}, "key", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetString(tt.args, tt.key)
			if got != tt.want || ok != tt.wantOk {
				t.Errorf("GetString() = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOk)
			}
		})
	}
}

func TestGetInt(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]any
		key    string
		want   int
		wantOk bool
	}{
		{"valid int", map[string]any{"key": 42}, "key", 42, true},
		{"float to int", map[string]any{"key": 42.9}, "key", 42, true},
		{"string int", map[string]any{"key": "42"}, "key", 0, false},
		{"missing key", map[string]any{}, "key", 0, false},
		{"nil value", map[string]any{"key": nil}, "key", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetInt(tt.args, tt.key)
			if got != tt.want || ok != tt.wantOk {
				t.Errorf("GetInt() = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOk)
			}
		})
	}
}

func TestGetBool(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]any
		key    string
		want   bool
		wantOk bool
	}{
		{"true bool", map[string]any{"key": true}, "key", true, true},
		{"false bool", map[string]any{"key": false}, "key", false, true},
		{"missing key", map[string]any{}, "key", false, false},
		{"non-bool value", map[string]any{"key": "true"}, "key", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := GetBool(tt.args, tt.key)
			if got != tt.want || ok != tt.wantOk {
				t.Errorf("GetBool() = (%v, %v), want (%v, %v)", got, ok, tt.want, tt.wantOk)
			}
		})
	}
}

// ============================================================
// PathValidator (security) edge cases
// ============================================================

func TestReadTool_Execute_NoPathValidator(t *testing.T) {
	tool := NewReadTool("") // Empty workDir means no path validator

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"file_path": "/tmp/test.txt",
	})

	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	// Should fail with security error when no path validator
	if result.Success {
		t.Error("Execute() should fail when path validator is nil")
	}
	if result.Error == "" {
		t.Error("Error field should be set for security error")
	}
}

// --- Round 10 fixes ---

// TestReadTool_ZeroLimitReturnsContent pins the lower clamp: an explicit
// limit:0 (or negative) used to satisfy `linesRead >= limit` on the very
// first line — Success=true with ZERO content and a pagination hint pointing
// at itself.
func TestReadTool_ZeroLimitReturnsContent(t *testing.T) {
	dir := resolvedTempDir(t)
	path := filepath.Join(dir, "z.txt")
	os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0644)
	tool := NewReadTool(dir)

	for _, lim := range []any{float64(0), float64(-5)} {
		res, err := tool.Execute(context.Background(), map[string]any{"file_path": path, "limit": lim})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Success {
			t.Fatalf("limit=%v: expected success, got error: %s", lim, res.Error)
		}
		if !strings.Contains(res.Content, "alpha") {
			t.Fatalf("limit=%v: content missing (empty-success bug): %q", lim, res.Content)
		}
	}
}

// TestReadTool_SVGReadAsText pins the .svg routing fix: SVG is XML text; the
// binary image reader left the model blind to the source (and serialized it
// as an unsupported image/svg+xml block).
func TestReadTool_SVGReadAsText(t *testing.T) {
	dir := resolvedTempDir(t)
	path := filepath.Join(dir, "icon.svg")
	os.WriteFile(path, []byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><rect/></svg>\n"), 0644)
	tool := NewReadTool(dir)

	res, err := tool.Execute(context.Background(), map[string]any{"file_path": path})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("expected success, got: %s", res.Error)
	}
	if !strings.Contains(res.Content, "<svg") {
		t.Fatalf("SVG source not readable as text: %q", res.Content)
	}
	if len(res.MultimodalParts) != 0 {
		t.Fatal("SVG must not be attached as an image block")
	}
}

// TestReadTool_OversizedSpecialFilesRejected pins the size gate: .pdf/.ipynb/
// image files routed BEFORE the >10MB isLarge gate and were read whole with
// no bound.
func TestReadTool_OversizedSpecialFilesRejected(t *testing.T) {
	dir := resolvedTempDir(t)
	tool := NewReadTool(dir)

	big := make([]byte, LargeFileSizeMB*1024*1024+1)
	for _, name := range []string{"huge.pdf", "huge.png", "huge.ipynb"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, big, 0644); err != nil {
			t.Fatal(err)
		}
		res, err := tool.Execute(context.Background(), map[string]any{"file_path": path})
		if err != nil {
			t.Fatal(err)
		}
		if res.Success {
			t.Fatalf("%s: oversized special file must be rejected, got success", name)
		}
		if !strings.Contains(res.Error, "too large") {
			t.Fatalf("%s: error should say too large, got: %s", name, res.Error)
		}
		os.Remove(path)
	}
}
