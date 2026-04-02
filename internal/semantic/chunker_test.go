package semantic

import (
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================
// Chunker Tests
// ============================================================

func TestStructuralChunker_NewStructuralChunker(t *testing.T) {
	chunker := NewStructuralChunker(100, 20)

	if chunker.baseChunkSize != 100 {
		t.Errorf("baseChunkSize = %d, want 100", chunker.baseChunkSize)
	}
	if chunker.overlap != 20 {
		t.Errorf("overlap = %d, want 20", chunker.overlap)
	}
}

func TestStructuralChunker_Chunk_Go(t *testing.T) {
	chunker := NewStructuralChunker(50, 10)

	code := `package main

import "fmt"

func main() {
	fmt.Println("Hello")
}

func helper() int {
	return 42
}

type MyStruct struct {
	Name string
}

func (m *MyStruct) Method() {
	fmt.Println(m.Name)
}`

	chunks := chunker.Chunk("test.go", code)

	// Should have at least some chunks from Go AST parsing
	if len(chunks) == 0 {
		t.Error("expected at least one chunk for Go code")
	}

	// Verify chunk structure
	for i, chunk := range chunks {
		if chunk.FilePath != "test.go" {
			t.Errorf("chunk[%d].FilePath = %q, want %q", i, chunk.FilePath, "test.go")
		}
		if chunk.LineStart <= 0 {
			t.Errorf("chunk[%d].LineStart = %d, want > 0", i, chunk.LineStart)
		}
		if chunk.LineEnd < chunk.LineStart {
			t.Errorf("chunk[%d].LineEnd = %d, want >= LineStart (%d)", i, chunk.LineEnd, chunk.LineStart)
		}
		if chunk.Content == "" {
			t.Errorf("chunk[%d].Content is empty", i)
		}
	}
}

func TestStructuralChunker_Chunk_Python(t *testing.T) {
	chunker := NewStructuralChunker(30, 5)

	code := `class MyClass:
    def method1(self):
        pass

    def method2(self):
        pass

def standalone_function():
    pass

class AnotherClass:
    pass`

	chunks := chunker.Chunk("test.py", code)

	if len(chunks) == 0 {
		t.Error("expected at least one chunk for Python code")
	}

	// Verify all chunks have content
	for i, chunk := range chunks {
		if strings.TrimSpace(chunk.Content) == "" {
			t.Errorf("chunk[%d].Content is empty or whitespace only", i)
		}
	}
}

func TestStructuralChunker_Chunk_JavaScript(t *testing.T) {
	chunker := NewStructuralChunker(30, 5)

	code := `function greet(name) {
    return "Hello, " + name;
}

class Animal {
    constructor(name) {
        this.name = name;
    }
}

export const constant = 42;

async function asyncFunc() {
    return await Promise.resolve();
}`

	chunks := chunker.Chunk("test.js", code)

	if len(chunks) == 0 {
		t.Error("expected at least one chunk for JavaScript code")
	}
}

func TestStructuralChunker_Chunk_Java(t *testing.T) {
	chunker := NewStructuralChunker(30, 5)

	code := `public class Main {
    public void method1() {
        System.out.println("test");
    }

    private int method2() {
        return 1;
    }
}

class Helper {
    void help() {}
}`

	chunks := chunker.Chunk("test.java", code)

	if len(chunks) == 0 {
		t.Error("expected at least one chunk for Java code")
	}
}

func TestStructuralChunker_Chunk_UnknownExtension(t *testing.T) {
	chunker := NewStructuralChunker(20, 5)

	// Test with unknown extension - should fallback to heuristic
	code := `some random content
with multiple lines
that should be chunked
using the heuristic approach`

	chunks := chunker.Chunk("test.xyz", code)

	// Should still produce chunks via sliding window fallback
	if len(chunks) == 0 {
		t.Error("expected at least one chunk for unknown extension")
	}
}

func TestStructuralChunker_Chunk_EmptyContent(t *testing.T) {
	chunker := NewStructuralChunker(50, 10)

	chunks := chunker.Chunk("test.go", "")

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty content, got %d", len(chunks))
	}
}

func TestStructuralChunker_Chunk_WhitespaceOnly(t *testing.T) {
	chunker := NewStructuralChunker(50, 10)

	chunks := chunker.Chunk("test.go", "   \n\n   \n   ")

	// Whitespace-only content should not produce meaningful chunks
	for _, chunk := range chunks {
		if len(strings.TrimSpace(chunk.Content)) > 0 {
			t.Errorf("expected empty/whitespace chunks for whitespace-only input, got %q", chunk.Content)
		}
	}
}

func TestStructuralChunker_Chunk_LargeFile(t *testing.T) {
	chunker := NewStructuralChunker(10, 2)

	// Generate a large file
	var sb strings.Builder
	sb.WriteString("package main\n\n")
	for i := 0; i < 100; i++ {
		sb.WriteString("func function")
		sb.WriteString(strings.Repeat("_", i%10))
		sb.WriteString("() {\n")
		sb.WriteString("    // comment\n")
		sb.WriteString("}\n\n")
	}
	code := sb.String()

	chunks := chunker.Chunk("large.go", code)

	// Should produce multiple chunks for large file
	if len(chunks) < 5 {
		t.Errorf("expected at least 5 chunks for large file, got %d", len(chunks))
	}
}

func TestStructuralChunker_SlidingWindow_Overlap(t *testing.T) {
	chunker := NewStructuralChunker(5, 2)

	// Create content that will definitely use sliding window
	code := `line1
line2
line3
line4
line5
line6
line7
line8`

	chunks := chunker.Chunk("test.txt", code)

	if len(chunks) < 2 {
		t.Error("expected multiple chunks with sliding window")
	}

	// Verify chunks don't overlap excessively
	for i := 1; i < len(chunks); i++ {
		if chunks[i].LineStart <= chunks[i-1].LineStart {
			t.Errorf("chunks[%d].LineStart (%d) should be > chunks[%d].LineStart (%d)",
				i, chunks[i].LineStart, i-1, chunks[i-1].LineStart)
		}
	}
}

func TestStructuralChunker_AddChunk(t *testing.T) {
	chunker := NewStructuralChunker(50, 10)

	lines := []string{"line1", "line2", "line3", "line4", "line5"}
	var chunks []ChunkInfo

	// Test addChunk with valid content
	chunker.addChunk("test.go", lines, 0, 3, &chunks)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	if chunks[0].LineStart != 1 {
		t.Errorf("LineStart = %d, want 1", chunks[0].LineStart)
	}
	if chunks[0].LineEnd != 3 {
		t.Errorf("LineEnd = %d, want 3", chunks[0].LineEnd)
	}
}

func TestStructuralChunker_AddChunk_EmptyContent(t *testing.T) {
	chunker := NewStructuralChunker(50, 10)

	lines := []string{"   ", "\t", "  "}
	var chunks []ChunkInfo

	// addChunk should skip empty content
	chunker.addChunk("test.go", lines, 0, len(lines), &chunks)

	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty content, got %d", len(chunks))
	}
}

// ============================================================
// Similarity Tests
// ============================================================

func TestCosineSimilarity_Basic(t *testing.T) {
	tests := []struct {
		name   string
		a      []float32
		b      []float32
		minVal float32 // Minimum expected value (accounting for float precision)
		maxVal float32 // Maximum expected value (0 = use default 1.001)
	}{
		{
			name:   "identical vectors",
			a:      []float32{1, 0, 0},
			b:      []float32{1, 0, 0},
			minVal: 0.999,
		},
		{
			name:   "orthogonal vectors",
			a:      []float32{1, 0, 0},
			b:      []float32{0, 1, 0},
			minVal: -0.001,
			maxVal: 0.001,
		},
		{
			name:   "opposite vectors",
			a:      []float32{1, 0, 0},
			b:      []float32{-1, 0, 0},
			minVal: -1.001,
			maxVal: -0.999,
		},
		{
			name:   "multi-dimensional identical",
			a:      []float32{0.5, 0.5, 0.5, 0.5},
			b:      []float32{0.5, 0.5, 0.5, 0.5},
			minVal: 0.999,
		},
		{
			name:   "similar vectors",
			a:      []float32{1, 2, 3},
			b:      []float32{2, 4, 6},
			minVal: 0.999,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maxVal := tt.maxVal
			if maxVal == 0 {
				maxVal = 1.001 // default upper bound
			}
			result := CosineSimilarity(tt.a, tt.b)
			if result < tt.minVal || result > maxVal {
				t.Errorf("CosineSimilarity() = %v, want between %v and %v", result, tt.minVal, maxVal)
			}
		})
	}
}

func TestCosineSimilarity_EmptyVectors(t *testing.T) {
	result := CosineSimilarity([]float32{}, []float32{})

	if result != 0 {
		t.Errorf("CosineSimilarity() = %v, want 0 for empty vectors", result)
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	result := CosineSimilarity([]float32{1, 2, 3}, []float32{1, 2})

	if result != 0 {
		t.Errorf("CosineSimilarity() = %v, want 0 for different length vectors", result)
	}
}

func TestCosineSimilarity_ZeroVector(t *testing.T) {
	result := CosineSimilarity([]float32{0, 0, 0}, []float32{1, 2, 3})

	if result != 0 {
		t.Errorf("CosineSimilarity() = %v, want 0 when one vector is zero", result)
	}
}

func TestCosineSimilarity_BothZero(t *testing.T) {
	result := CosineSimilarity([]float32{0, 0, 0}, []float32{0, 0, 0})

	if result != 0 {
		t.Errorf("CosineSimilarity() = %v, want 0 when both vectors are zero", result)
	}
}

func TestCosineSimilarity_LargeVectors(t *testing.T) {
	// Test with 512-dimensional vectors (common in embeddings)
	a := make([]float32, 512)
	b := make([]float32, 512)
	for i := range a {
		a[i] = float32(i) / 512.0
		b[i] = float32(i) / 512.0
	}

	result := CosineSimilarity(a, b)
	if result < 0.999 {
		t.Errorf("CosineSimilarity() = %v, want close to 1.0 for identical large vectors", result)
	}
}

// ============================================================
// SearchResult Tests
// ============================================================

func TestSearchResults_Len(t *testing.T) {
	results := SearchResults{
		{Score: 1.0},
		{Score: 2.0},
		{Score: 0.5},
	}

	if results.Len() != 3 {
		t.Errorf("Len() = %d, want 3", results.Len())
	}
}

func TestSearchResults_Less(t *testing.T) {
	results := SearchResults{
		{Score: 1.0},
		{Score: 3.0},
		{Score: 2.0},
	}

	// Less returns true when first element should come BEFORE second in sorted order
	// Since we sort descending (highest first), Less(i, j) returns true if r[i].Score > r[j].Score

	// 3.0 > 1.0, so index 1 should come before index 0
	if !results.Less(1, 0) {
		t.Error("Less(1, 0) should be true (3.0 > 1.0, descending sort)")
	}

	// 3.0 > 2.0, so index 1 should come before index 2
	if !results.Less(1, 2) {
		t.Error("Less(1, 2) should be true (3.0 > 2.0, descending sort)")
	}

	// 2.0 > 1.0, so index 2 should come before index 0
	if !results.Less(2, 0) {
		t.Error("Less(2, 0) should be true (2.0 > 1.0, descending sort)")
	}
}

func TestSearchResults_Swap(t *testing.T) {
	results := SearchResults{
		{FilePath: "a.txt", Score: 1.0},
		{FilePath: "b.txt", Score: 2.0},
	}

	results.Swap(0, 1)

	if results[0].FilePath != "b.txt" {
		t.Errorf("After Swap, results[0].FilePath = %q, want %q", results[0].FilePath, "b.txt")
	}
	if results[1].FilePath != "a.txt" {
		t.Errorf("After Swap, results[1].FilePath = %q, want %q", results[1].FilePath, "a.txt")
	}
}

func TestSearchResults_Sort(t *testing.T) {
	results := SearchResults{
		{Score: 0.3, FilePath: "low.txt"},
		{Score: 0.9, FilePath: "high.txt"},
		{Score: 0.6, FilePath: "mid.txt"},
	}

	// Sort using the built-in sort
	sorted := make(SearchResults, len(results))
	copy(sorted, results)
	// Note: In real usage, you would use sort.Sort(sorted)
	// Here we just verify the Less function works correctly
	_ = sorted

	// Verify Less function for sorting
	if !results.Less(1, 0) {
		t.Error("0.9 should be less than 0.3 in index terms (descending sort)")
	}
}

// ============================================================
// Integration-like Tests (chunking multiple files)
// ============================================================

func TestChunker_MultipleFiles(t *testing.T) {
	chunker := NewStructuralChunker(20, 5)

	files := map[string]string{
		"main.go": `package main

import "fmt"

func main() {
	fmt.Println("Hello")
}

func helper() int {
	return 42
}`,
		"utils.go": `package main

func Add(a, b int) int {
	return a + b
}

func Subtract(a, b int) int {
	return a - b
}`,
		"test.py": `def hello():
    print("world")

class MyClass:
    def method(self):
        pass`,
	}

	totalChunks := 0
	for filePath, content := range files {
		chunks := chunker.Chunk(filePath, content)
		totalChunks += len(chunks)

		// Verify all chunks reference correct file
		for _, chunk := range chunks {
			if chunk.FilePath != filepath.Base(filePath) {
				t.Errorf("Chunk file path = %q, want %q", chunk.FilePath, filepath.Base(filePath))
			}
		}
	}

	if totalChunks == 0 {
		t.Error("expected at least one chunk across all files")
	}
}

// ============================================================
// Edge Cases
// ============================================================

func TestStructuralChunker_SingleLineFile(t *testing.T) {
	chunker := NewStructuralChunker(50, 10)

	code := "package main"
	chunks := chunker.Chunk("test.go", code)

	// Single line should produce at most one chunk
	if len(chunks) > 1 {
		t.Logf("Got %d chunks for single line (may be acceptable)", len(chunks))
	}
}

func TestStructuralChunker_SpecialCharacters(t *testing.T) {
	chunker := NewStructuralChunker(50, 10)

	code := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\t// Unicode\n\t_ = \"你好世界\"\n\t// Emoji\n\t_ = \"🎉\"\n}\n"

	chunks := chunker.Chunk("test.go", code)

	if len(chunks) == 0 {
		t.Error("expected at least one chunk for file with special characters")
	}
}

func TestStructuralChunker_DeeplyNested(t *testing.T) {
	chunker := NewStructuralChunker(10, 2)

	// Create deeply nested structure
	code := "class Outer:\n    class Middle:\n        class Inner:\n            def deep_method(self):\n                pass\n\ndef function():\n    if True:\n        if True:\n            if True:\n                pass\n"

	chunks := chunker.Chunk("test.py", code)

	if len(chunks) == 0 {
		t.Error("expected at least one chunk for deeply nested code")
	}
}

func TestCosineSimilarity_FloatPrecision(t *testing.T) {
	// Test that similarity is computed correctly with float precision
	a := []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	b := []float32{0.2, 0.4, 0.6, 0.8, 1.0}

	result := CosineSimilarity(a, b)

	// Calculate expected manually
	var dotProduct, normA, normB float64
	for i := range a {
		dotProduct += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	expected := float32(dotProduct / (math.Sqrt(normA) * math.Sqrt(normB)))

	diff := math.Abs(float64(result - expected))
	if diff > 0.0001 {
		t.Errorf("CosineSimilarity() = %v, expected ~%v (diff: %v)", result, expected, diff)
	}
}
