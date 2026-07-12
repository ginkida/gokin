package readers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// NOTE: ImageReader, IsSupportedImage, NotebookReader basic Read/extractSource
// are already covered in readers_test.go. This file adds coverage for:
//   - PDFReader (all functions)
//   - NotebookReader edge cases (raw cells, unknown types, display_data, stderr)

// ---------------------------------------------------------------------------
// NotebookReader — additional edge cases
// ---------------------------------------------------------------------------

func TestNotebookReader_Read_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.ipynb")
	os.WriteFile(path, []byte("{invalid json}"), 0644)

	r := NewNotebookReader()
	_, err := r.Read(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestNotebookReader_Read_Empty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.ipynb")
	os.WriteFile(path, []byte(`{"cells": [], "nbformat": 4}`), 0644)

	r := NewNotebookReader()
	text, err := r.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "nbformat 4") {
		t.Errorf("expected nbformat header, got: %s", text)
	}
}

func TestNotebookReader_Read_RawCell(t *testing.T) {
	nb := `{"cells": [{"cell_type": "raw", "source": "raw content"}], "nbformat": 4}`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ipynb")
	os.WriteFile(path, []byte(nb), 0644)

	r := NewNotebookReader()
	text, err := r.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "[raw]") {
		t.Errorf("expected raw cell marker: %s", text)
	}
	if !strings.Contains(text, "raw content") {
		t.Errorf("expected raw content: %s", text)
	}
}

func TestNotebookReader_Read_UnknownCellType(t *testing.T) {
	nb := `{"cells": [{"cell_type": "custom", "source": "custom data"}], "nbformat": 4}`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ipynb")
	os.WriteFile(path, []byte(nb), 0644)

	r := NewNotebookReader()
	text, err := r.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "[custom]") {
		t.Errorf("expected custom cell type marker: %s", text)
	}
}

func TestNotebookReader_Read_DisplayData(t *testing.T) {
	nb := `{
		"cells": [{
			"cell_type": "code",
			"source": "x = 1\n",
			"execution_count": 1,
			"outputs": [
				{"output_type": "execute_result", "data": {"text/plain": "1"}},
				{"output_type": "display_data", "data": {"image/png": "base64data"}}
			]
		}],
		"nbformat": 4
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ipynb")
	os.WriteFile(path, []byte(nb), 0644)

	r := NewNotebookReader()
	text, err := r.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "1") {
		t.Errorf("expected execute_result output: %s", text)
	}
	if !strings.Contains(text, "[Image output: PNG]") {
		t.Errorf("expected image output marker: %s", text)
	}
}

func TestNotebookReader_Read_StderrStream(t *testing.T) {
	nb := `{
		"cells": [{
			"cell_type": "code",
			"source": "import sys\n",
			"execution_count": 1,
			"outputs": [
				{"output_type": "stream", "name": "stderr", "text": "warning msg"}
			]
		}],
		"nbformat": 4
	}`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ipynb")
	os.WriteFile(path, []byte(nb), 0644)

	r := NewNotebookReader()
	text, err := r.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "stderr") {
		t.Errorf("expected stderr marker: %s", text)
	}
	if !strings.Contains(text, "warning msg") {
		t.Errorf("expected warning text: %s", text)
	}
}

func TestNotebookReader_extractSource_IntAndStringSlice(t *testing.T) {
	r := NewNotebookReader()
	// int source → fmt.Sprintf("%v", source)
	if got := r.extractSource(42); got != "42" {
		t.Errorf("extractSource(int) = %q, want 42", got)
	}
	// []string source (Go-typed, not []any)
	if got := r.extractSource([]string{"a", "b"}); got != "ab" {
		t.Errorf("extractSource([]string) = %q, want ab", got)
	}
	// []any with non-string element → skipped
	mixed := []any{"a", 42, "b"}
	if got := r.extractSource(mixed); got != "ab" {
		t.Errorf("extractSource(mixed) = %q, want ab", got)
	}
}

func TestNotebookReader_formatOutput_UnknownType(t *testing.T) {
	r := NewNotebookReader()
	result := r.formatOutput(Output{OutputType: "unknown"})
	if result != "" {
		t.Errorf("expected empty for unknown output type, got %q", result)
	}
}

// ---------------------------------------------------------------------------
// PDFReader
// ---------------------------------------------------------------------------

func TestPDFReader_Read_NotExist(t *testing.T) {
	r := NewPDFReader()
	_, err := r.Read("/nonexistent.pdf")
	if err == nil {
		t.Error("expected error for non-existent file")
	}
}

func TestPDFReader_Read_InvalidHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fake.pdf")
	os.WriteFile(path, []byte("NOT A PDF"), 0644)

	r := NewPDFReader()
	_, err := r.Read(path)
	if err == nil {
		t.Error("expected error for invalid PDF header")
	}
}

func TestPDFReader_Read_NoText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.pdf")
	// Valid PDF header but ONLY binary noise after it — no printable ASCII
	// runs ≥4 chars, so extractStreams and extractPlainText both return empty.
	// We append enough non-printable bytes to ensure no 4-char word survives.
	header := []byte("%PDF-1.4")
	noise := make([]byte, 100)
	for i := range noise {
		noise[i] = byte(i % 31) // 0..30, all non-printable or short runs
	}
	data := append(header, noise...)
	os.WriteFile(path, data, 0644)

	r := NewPDFReader()
	text, err := r.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// The header word "%PDF-1.4" is filtered by isReadableWord (starts with no
	// letter prefix issue, but has <50% letters: P,F = 2 letters / 8 chars),
	// so extractPlainText returns empty → "No extractable text" message.
	if !strings.Contains(text, "No extractable text") {
		t.Errorf("expected 'No extractable text' message: %s", text)
	}
}

func TestPDFReader_Read_WithStream(t *testing.T) {
	pdfContent := `%PDF-1.4
1 0 obj
<< /Type /Page >>
endobj
stream
(Hello World) Tj
endstream
%%EOF
`
	dir := t.TempDir()
	path := filepath.Join(dir, "test.pdf")
	os.WriteFile(path, []byte(pdfContent), 0644)

	r := NewPDFReader()
	text, err := r.Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(text, "Hello World") {
		t.Errorf("expected 'Hello World' in extracted text: %s", text)
	}
}

func TestPDFReader_extractTextOperators_ParenStrings(t *testing.T) {
	r := NewPDFReader()
	content := "(Hello) Tj (World) Tj"
	result := r.extractTextOperators(content)
	if !strings.Contains(result, "Hello") || !strings.Contains(result, "World") {
		t.Errorf("expected Hello and World, got: %q", result)
	}
}

func TestPDFReader_extractTextOperators_ArrayTJ(t *testing.T) {
	r := NewPDFReader()
	content := "[(Hello) -10 (World)] TJ"
	result := r.extractTextOperators(content)
	if !strings.Contains(result, "Hello") || !strings.Contains(result, "World") {
		t.Errorf("expected Hello and World from array, got: %q", result)
	}
}

func TestPDFReader_extractTextOperators_HexString(t *testing.T) {
	r := NewPDFReader()
	// <48656c6c6f> = "Hello" in hex
	content := "<48656c6c6f> Tj"
	result := r.extractTextOperators(content)
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected Hello from hex string, got: %q", result)
	}
}

func TestPDFReader_decodeString_Escapes(t *testing.T) {
	r := NewPDFReader()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", "hello"},
		{"newline", `hello\nworld`, "hello\nworld"},
		{"carriage_return", `a\rb`, "a\rb"},
		{"tab", `a\tb`, "a\tb"},
		{"backspace", `a\bb`, "a\bb"},
		{"formfeed", `a\fb`, "a\fb"},
		{"escaped_paren", `\(text\)`, "(text)"},
		{"escaped_backslash", `a\\b`, "a\\b"},
		{"octal_A", `\101`, "A"},   // 0o101 = 65 = 'A'
		{"octal_bang", `\41`, "!"}, // 0o41 = 33 = '!'
		{"unknown_escape", `\x`, "x"},
		{"trailing_backslash", `abc\`, "abc\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.decodeString(tt.in)
			if got != tt.want {
				t.Errorf("decodeString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPDFReader_decodeHexString(t *testing.T) {
	r := NewPDFReader()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"basic", "48656c6c6f", "Hello"},
		{"with_spaces", "48 65 6c 6c 6f", "Hello"},
		{"with_newlines", "48\n65\n6c\n6c\n6f", "Hello"},
		{"odd_length", "486", "H`"}, // padded with 0 → "48"+"60" → 'H'+'`'(0x60=96)
		{"non_printable", "00", ""}, // 0x00 < 32, filtered
		{"invalid_hex", "zz", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.decodeHexString(tt.in)
			if got != tt.want {
				t.Errorf("decodeHexString(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestPDFReader_extractFromArray(t *testing.T) {
	r := NewPDFReader()
	// Array with paren strings and hex strings
	content := "(Hello) <576f726c64>"
	result := r.extractFromArray(content)
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected Hello from array parens: %q", result)
	}
	if !strings.Contains(result, "World") {
		t.Errorf("expected World from array hex: %q", result)
	}
}

func TestPDFReader_extractPlainText(t *testing.T) {
	r := NewPDFReader()
	// Mix of readable words and binary noise
	data := []byte("Hello World\x00\x01binary_noise_here_obj")
	result := r.extractPlainText(data)
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected Hello in plain text: %q", result)
	}
}

func TestPDFReader_extractPlainText_LastWord(t *testing.T) {
	r := NewPDFReader()
	// Data ending with a readable word (no trailing non-printable to flush)
	data := []byte("Hello World")
	result := r.extractPlainText(data)
	if !strings.Contains(result, "Hello") {
		t.Errorf("expected Hello: %q", result)
	}
}

func TestPDFReader_isReadableWord(t *testing.T) {
	r := NewPDFReader()
	tests := []struct {
		word string
		want bool
	}{
		{"hello", true},
		{"Hello123", true},
		{"obj", false}, // PDF keyword
		{"endobj", false},
		{"stream", false},
		{"endstream", false},
		{"123456", false}, // no letters
		{"abcdef", true},
		// "a1b2c3" is 50% letters — exactly the threshold, not >50%, so filtered out
	}
	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			got := r.isReadableWord(tt.word)
			if got != tt.want {
				t.Errorf("isReadableWord(%q) = %v, want %v", tt.word, got, tt.want)
			}
		})
	}
}

func TestPDFReader_extractStreams_NoStreams(t *testing.T) {
	r := NewPDFReader()
	data := []byte("%PDF-1.4\nsome content without streams\n")
	result := r.extractStreams(data)
	if result != "" {
		t.Errorf("expected empty for no streams, got %q", result)
	}
}

func TestPDFReader_extractStreams_UnterminatedStream(t *testing.T) {
	r := NewPDFReader()
	// Stream without endstream → no match, returns empty
	data := []byte("%PDF-1.4\nstream\n(Hello) Tj\n")
	result := r.extractStreams(data)
	if result != "" {
		t.Errorf("expected empty for unterminated stream, got %q", result)
	}
}

func TestPDFReader_extractTextFromStream_NoText(t *testing.T) {
	r := NewPDFReader()
	data := []byte("binary data without text operators")
	result := r.extractTextFromStream(data)
	if result != "" {
		t.Errorf("expected empty for no text operators, got %q", result)
	}
}
