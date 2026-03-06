package highlight

import (
	"testing"
)

func TestNew(t *testing.T) {
	h := New("")
	if h.style != "monokai" {
		t.Errorf("default style = %q, want monokai", h.style)
	}

	h = New("dracula")
	if h.style != "dracula" {
		t.Errorf("style = %q, want dracula", h.style)
	}
}

func TestHighlight(t *testing.T) {
	h := New("monokai")

	// Should return non-empty for valid code
	result := h.Highlight("func main() {}", "go")
	if result == "" {
		t.Error("Highlight should return non-empty for Go code")
	}

	// Unknown language should not crash, use fallback
	result = h.Highlight("some text", "nonexistent-lang")
	if result == "" {
		t.Error("unknown language should still return text")
	}

	// Empty code
	result = h.Highlight("", "go")
	// Should not panic
}

func TestHighlightWithLineNumbers(t *testing.T) {
	h := New("monokai")
	result := h.HighlightWithLineNumbers("line1\nline2\nline3", "text", 1)
	if result == "" {
		t.Error("should return non-empty")
	}
	// Should contain line separator
	if !containsStr(result, "│") {
		t.Error("should contain line number separator │")
	}
}

func TestHighlightDiff(t *testing.T) {
	h := New("monokai")
	diff := "--- a/file.go\n+++ b/file.go\n@@ -1,3 +1,3 @@\n-old line\n+new line\n context"
	result := h.HighlightDiff(diff)
	if result == "" {
		t.Error("diff highlight should return non-empty")
	}
}

func TestHighlightInlineDiff(t *testing.T) {
	h := New("monokai")

	// Both empty
	old, new_ := h.HighlightInlineDiff("", "")
	if old != "" || new_ != "" {
		t.Error("both empty should return empty")
	}

	// Only old
	old, new_ = h.HighlightInlineDiff("old text", "")
	if old == "" {
		t.Error("old should be styled")
	}
	if new_ != "" {
		t.Error("new should be empty")
	}

	// Only new
	old, new_ = h.HighlightInlineDiff("", "new text")
	if old != "" {
		t.Error("old should be empty")
	}
	if new_ == "" {
		t.Error("new should be styled")
	}

	// Both present with difference
	old, new_ = h.HighlightInlineDiff("hello world", "hello earth")
	if old == "" || new_ == "" {
		t.Error("both should have content")
	}
}

func TestDetectLanguage(t *testing.T) {
	h := New("")

	tests := []struct {
		filename string
		want     string
	}{
		{"main.go", "go"},
		{"script.py", "python"},
		{"app.js", "javascript"},
		{"style.css", "css"},
		{"config.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"data.json", "json"},
		{"notes.md", "markdown"},
		{"Dockerfile", "docker"},
		{"Makefile", "makefile"},
		{"go.mod", "gomod"},
		{"run.sh", "bash"},
		{"lib.rs", "rust"},
		{"App.tsx", "tsx"},
		{"query.sql", "sql"},
		{"main.tf", "terraform"},
		{"unknown.xyz", "text"},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := h.DetectLanguage(tt.filename)
			if got != tt.want {
				t.Errorf("DetectLanguage(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestDetectLanguageCaseInsensitive(t *testing.T) {
	h := New("")
	// Extension should be case-insensitive
	got := h.DetectLanguage("Main.GO")
	if got != "go" {
		t.Errorf("DetectLanguage(Main.GO) = %q, want go", got)
	}
}

// --- Helper tests ---

func TestPadLeft(t *testing.T) {
	tests := []struct {
		num   int
		width int
		want  string
	}{
		{1, 4, "   1"},
		{42, 4, "  42"},
		{1000, 4, "1000"},
		{12345, 4, "12345"}, // wider than width
		{0, 4, "   0"},
	}

	for _, tt := range tests {
		got := padLeft(tt.num, tt.width)
		if got != tt.want {
			t.Errorf("padLeft(%d, %d) = %q, want %q", tt.num, tt.width, got, tt.want)
		}
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{42, "42"},
		{12345, "12345"},
	}

	for _, tt := range tests {
		got := itoa(tt.n)
		if got != tt.want {
			t.Errorf("itoa(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestCommonPrefix(t *testing.T) {
	tests := []struct {
		a, b string
		want string
	}{
		{"hello", "hello", "hello"},
		{"hello", "help", "hel"},
		{"abc", "xyz", ""},
		{"", "hello", ""},
		{"hello", "", ""},
	}

	for _, tt := range tests {
		got := commonPrefix(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("commonPrefix(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestCommonSuffix(t *testing.T) {
	tests := []struct {
		a, b string
		want string
	}{
		{"hello", "hello", "hello"},
		{"world", "bold", "ld"},
		{"abc", "xyz", ""},
		{"", "hello", ""},
		{"hello", "", ""},
	}

	for _, tt := range tests {
		got := commonSuffix(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("commonSuffix(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
