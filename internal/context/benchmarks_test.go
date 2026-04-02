package context

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// BenchmarkTokenCounting benchmarks token counting for various message types
func BenchmarkTokenCounting_SimpleText(b *testing.B) {
	text := "This is a simple test message with some words."

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateTokens(text)
	}
}

func BenchmarkTokenCounting_LongText(b *testing.B) {
	text := strings.Repeat("Lorem ipsum dolor sit amet consectetur adipiscing elit. ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateTokens(text)
	}
}

func BenchmarkTokenCounting_Code(b *testing.B) {
	code := `func main() {
    fmt.Println("Hello, World!")
    for i := 0; i < 10; i++ {
        fmt.Println(i)
    }
}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateTokens(code)
	}
}

func BenchmarkTokenCounting_WithType(b *testing.B) {
	text := strings.Repeat("Lorem ipsum dolor sit amet. ", 50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = EstimateTokensWithType(text, ContentTypeProse)
	}
}

// BenchmarkSHA256Hashing benchmarks SHA256 hashing (used in deduplication)
func BenchmarkSHA256Hashing(b *testing.B) {
	text := "This is a longer text that will be hashed for deduplication purposes"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hash := sha256.Sum256([]byte(text))
		_ = hex.EncodeToString(hash[:])
	}
}

// BenchmarkStringOperations benchmarks common string operations
func BenchmarkStringOperations_Split(b *testing.B) {
	text := strings.Repeat("word1 word2 word3 word4 word5 ", 20)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.Split(text, " ")
	}
}

func BenchmarkStringOperations_Contains(b *testing.B) {
	text := strings.Repeat("Lorem ipsum dolor sit amet. ", 50)
	search := "dolor"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.Contains(text, search)
	}
}

func BenchmarkStringOperations_TrimSpace(b *testing.B) {
	text := "   " + strings.Repeat("word ", 100) + "   "

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.TrimSpace(text)
	}
}

func BenchmarkStringOperations_Fields(b *testing.B) {
	text := strings.Repeat("word1 word2 word3 word4 word5 ", 20)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.Fields(text)
	}
}

func BenchmarkStringOperations_Join(b *testing.B) {
	parts := make([]string, 100)
	for i := range parts {
		parts[i] = "word"
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.Join(parts, " ")
	}
}

func BenchmarkStringOperations_ReplaceAll(b *testing.B) {
	text := strings.Repeat("old text old text old text ", 50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.ReplaceAll(text, "old", "new")
	}
}

// BenchmarkMemoryAllocation benchmarks memory allocation patterns
func BenchmarkMemoryAllocation_MapCreation(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m := make(map[string]int)
		for j := 0; j < 100; j++ {
			m[string(rune('a'+j%26))+string(rune('0'+j%10))] = j
		}
		_ = m
	}
}

func BenchmarkMemoryAllocation_SliceAppend(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var s []string
		for j := 0; j < 100; j++ {
			s = append(s, "item")
		}
		_ = s
	}
}

func BenchmarkMemoryAllocation_SlicePrealloc(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := make([]string, 0, 100)
		for j := 0; j < 100; j++ {
			s = append(s, "item")
		}
		_ = s
	}
}

// BenchmarkJSONMarshaling benchmarks JSON operations
func BenchmarkJSONMarshaling_SimpleMap(b *testing.B) {
	data := map[string]interface{}{
		"name":    "test",
		"count":   42,
		"enabled": true,
		"tags":    []string{"a", "b", "c"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(data)
	}
}

func BenchmarkJSONMarshaling_ComplexMap(b *testing.B) {
	data := map[string]interface{}{
		"messages": []map[string]interface{}{
			{"role": "user", "content": "Hello"},
			{"role": "model", "content": "Hi there!"},
			{"role": "user", "content": "How are you?"},
		},
		"metadata": map[string]interface{}{
			"tokens":    1234,
			"model":     "gemini-2.0-flash",
			"truncated": false,
		},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = json.Marshal(data)
	}
}

func BenchmarkJSONUnmarshaling(b *testing.B) {
	jsonData := `{"name":"test","count":42,"enabled":true,"tags":["a","b","c"]}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var result map[string]interface{}
		_ = json.Unmarshal([]byte(jsonData), &result)
	}
}

// BenchmarkLargeContentProcessing benchmarks processing of large content
func BenchmarkLargeContentProcessing(b *testing.B) {
	// Simulate a large file content
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = "This is line number " + strings.Repeat("x", 10) + " with some content"
	}
	text := strings.Join(lines, "\n")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Simulate common operations on large text
		fields := strings.Fields(text)
		_ = len(fields)
		_ = EstimateTokens(text)
	}
}
