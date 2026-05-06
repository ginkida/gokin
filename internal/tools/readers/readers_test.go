package readers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNotebookReader_Read(t *testing.T) {
	// Create a minimal notebook JSON
	nb := `{
		"cells": [
			{
				"cell_type": "markdown",
				"source": ["# Test Notebook\n", "Hello world"]
			},
			{
				"cell_type": "code",
				"source": "print('hello')\n",
				"execution_count": 1,
				"outputs": [
					{
						"output_type": "stream",
						"name": "stdout",
						"text": "hello\n"
					}
				]
			},
			{
				"cell_type": "code",
				"source": ["x = 1/0\n"],
				"execution_count": 2,
				"outputs": [
					{
						"output_type": "error",
						"ename": "ZeroDivisionError",
						"evalue": "division by zero"
					}
				]
			},
			{
				"cell_type": "raw",
				"source": "raw content"
			}
		],
		"metadata": {},
		"nbformat": 4
	}`

	dir := t.TempDir()
	path := filepath.Join(dir, "test.ipynb")
	if err := os.WriteFile(path, []byte(nb), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewNotebookReader()
	result, err := reader.Read(path)
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}

	// Check that all cell types are represented
	checks := []string{
		"Jupyter Notebook (nbformat 4)",
		"Cell 1 [markdown]",
		"# Test Notebook",
		"Cell 2 [code]",
		"In[1]",
		"print('hello')",
		"hello",
		"Cell 3 [code]",
		"ZeroDivisionError",
		"division by zero",
		"Cell 4 [raw]",
		"raw content",
	}
	for _, check := range checks {
		if !containsStr(result, check) {
			t.Errorf("Read() result missing %q", check)
		}
	}
}

func TestNotebookReader_ReadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.ipynb")
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	reader := NewNotebookReader()
	_, err := reader.Read(path)
	if err == nil {
		t.Error("Read() should error on invalid JSON")
	}
}

func TestNotebookReader_ReadMissing(t *testing.T) {
	reader := NewNotebookReader()
	_, err := reader.Read("/nonexistent/path.ipynb")
	if err == nil {
		t.Error("Read() should error on missing file")
	}
}

func TestNotebookReader_ExtractSource(t *testing.T) {
	reader := NewNotebookReader()

	// String source
	if got := reader.extractSource("hello"); got != "hello" {
		t.Errorf("extractSource(string) = %q, want %q", got, "hello")
	}

	// []any source (JSON arrays are decoded as []any)
	arr := []any{"line1\n", "line2\n"}
	if got := reader.extractSource(arr); got != "line1\nline2\n" {
		t.Errorf("extractSource([]any) = %q", got)
	}

	// nil source
	if got := reader.extractSource(nil); got != "<nil>" {
		t.Errorf("extractSource(nil) = %q", got)
	}
}

func TestImageReader_Read(t *testing.T) {
	reader := NewImageReader()

	// Create a tiny PNG (1x1 pixel, minimal valid PNG)
	// PNG header + IHDR + IDAT + IEND
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, // 8-bit RGB
		0xDE, // CRC
		0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, 0x54, // IDAT
		0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00, 0x00, // data
		0x00, 0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC, 0x33, // CRC
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, // IEND
		0xAE, 0x42, 0x60, 0x82, // CRC
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.png")
	if err := os.WriteFile(path, png, 0644); err != nil {
		t.Fatal(err)
	}

	result, err := reader.Read(path)
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}

	if result == nil {
		t.Fatal("Read() returned nil")
	}

	if result.MimeType == "" {
		t.Error("MIME type should not be empty")
	}
	if len(result.Data) == 0 {
		t.Error("Data should not be empty")
	}
}

func TestImageReader_ReadMissing(t *testing.T) {
	reader := NewImageReader()
	_, err := reader.Read("/nonexistent/path.png")
	if err == nil {
		t.Error("Read() should error on missing file")
	}
}

func TestImageReader_ReadBase64(t *testing.T) {
	reader := NewImageReader()

	// Minimal valid PNG bytes
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE,
		0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, 0x54,
		0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00, 0x00,
		0x00, 0x02, 0x00, 0x01, 0xE2, 0x21, 0xBC, 0x33,
		0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
	}

	dir := t.TempDir()
	path := dir + "/test.png"
	if err := os.WriteFile(path, png, 0644); err != nil {
		t.Fatal(err)
	}

	encoded, mimeType, err := reader.ReadBase64(path)
	if err != nil {
		t.Fatalf("ReadBase64() error: %v", err)
	}
	if encoded == "" {
		t.Error("encoded should not be empty")
	}
	if mimeType != "image/png" {
		t.Errorf("mimeType = %q, want image/png", mimeType)
	}

	// Error case
	_, _, err = reader.ReadBase64("/nonexistent/path.jpg")
	if err == nil {
		t.Error("ReadBase64 on missing file should error")
	}
}

func TestImageReader_GetMimeType(t *testing.T) {
	reader := NewImageReader()
	cases := []struct {
		path string
		want string
	}{
		{"file.png", "image/png"},
		{"file.jpg", "image/jpeg"},
		{"file.jpeg", "image/jpeg"},
		{"file.gif", "image/gif"},
		{"file.webp", "image/webp"},
		{"file.svg", "image/svg+xml"},
		{"file.bmp", "image/bmp"},
		{"file.ico", "image/x-icon"},
		{"file.tiff", "image/tiff"},
		{"file.tif", "image/tiff"},
		{"file.unknown", "application/octet-stream"},
		{"FILE.PNG", "image/png"}, // case-insensitive
	}
	for _, tc := range cases {
		if got := reader.getMimeType(tc.path); got != tc.want {
			t.Errorf("getMimeType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestIsSupportedImage(t *testing.T) {
	supported := []string{"a.png", "b.jpg", "c.jpeg", "d.gif", "e.webp", "f.svg", "g.bmp", "h.ico", "i.tiff", "j.tif"}
	for _, path := range supported {
		if !IsSupportedImage(path) {
			t.Errorf("IsSupportedImage(%q) should be true", path)
		}
	}
	unsupported := []string{"a.go", "b.txt", "c.pdf", "d.mp4", ""}
	for _, path := range unsupported {
		if IsSupportedImage(path) {
			t.Errorf("IsSupportedImage(%q) should be false", path)
		}
	}
}

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
