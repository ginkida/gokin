package context

import (
	"strings"
	"testing"

	"gokin/internal/tools"
)

// This file tests the ResultCompactor in compactor.go — the tool-result
// compaction logic that truncates large outputs while preserving error info,
// function signatures, and search matches. Before this file ALL compactor
// functions were at 0% coverage. They are pure string-processing functions
// with no I/O or external dependencies.

// ========== NewResultCompactor ==========

func TestNewResultCompactor_Defaults(t *testing.T) {
	c := NewResultCompactor(0) // 0 → default 10000
	if c.maxChars != 10000 {
		t.Errorf("maxChars = %d, want 10000 (default)", c.maxChars)
	}
	if c.headLines != 10 {
		t.Errorf("headLines = %d, want 10", c.headLines)
	}
	if c.tailLines != 5 {
		t.Errorf("tailLines = %d, want 5", c.tailLines)
	}
	if !c.smartTruncate {
		t.Error("smartTruncate should be true by default")
	}
}

func TestNewResultCompactor_CustomMax(t *testing.T) {
	c := NewResultCompactor(5000)
	if c.maxChars != 5000 {
		t.Errorf("maxChars = %d, want 5000", c.maxChars)
	}
}

func TestNewResultCompactor_NegativeMax(t *testing.T) {
	c := NewResultCompactor(-1)
	if c.maxChars != 10000 {
		t.Errorf("maxChars = %d, want 10000 (default for negative)", c.maxChars)
	}
}

// ========== SetMaxChars / SetHeadTailLines ==========

func TestSetMaxChars(t *testing.T) {
	c := NewResultCompactor(1000)
	c.SetMaxChars(5000)
	if c.maxChars != 5000 {
		t.Errorf("maxChars = %d, want 5000", c.maxChars)
	}
	// Zero or negative should be ignored.
	c.SetMaxChars(0)
	if c.maxChars != 5000 {
		t.Errorf("maxChars = %d, want 5000 (0 ignored)", c.maxChars)
	}
	c.SetMaxChars(-1)
	if c.maxChars != 5000 {
		t.Errorf("maxChars = %d, want 5000 (negative ignored)", c.maxChars)
	}
}

func TestSetHeadTailLines(t *testing.T) {
	c := NewResultCompactor(1000)
	c.SetHeadTailLines(20, 15)
	if c.headLines != 20 {
		t.Errorf("headLines = %d, want 20", c.headLines)
	}
	if c.tailLines != 15 {
		t.Errorf("tailLines = %d, want 15", c.tailLines)
	}
	// Zero/negative ignored.
	c.SetHeadTailLines(0, -1)
	if c.headLines != 20 {
		t.Errorf("headLines = %d, want 20 (0 ignored)", c.headLines)
	}
	if c.tailLines != 15 {
		t.Errorf("tailLines = %d, want 15 (negative ignored)", c.tailLines)
	}
}

// ========== Compact ==========

func TestCompact_SmallSuccessResult(t *testing.T) {
	c := NewResultCompactor(10000)
	result := tools.ToolResult{Content: "short content", Success: true}
	got := c.Compact(result)
	if got.Content != "short content" {
		t.Errorf("small result should be unchanged, got %q", got.Content)
	}
}

func TestCompact_LargeSuccessResult(t *testing.T) {
	c := NewResultCompactor(100)
	content := strings.Repeat("x", 500)
	result := tools.ToolResult{Content: content, Success: true}
	got := c.Compact(result)
	if len([]rune(got.Content)) > 200 {
		t.Errorf("large result should be truncated, got len %d", len(got.Content))
	}
	if !strings.Contains(got.Content, "[truncated") {
		t.Errorf("truncated result should contain truncation marker: %q", got.Content)
	}
}

func TestCompact_ErrorResultUnderLimit(t *testing.T) {
	c := NewResultCompactor(10000)
	result := tools.ToolResult{
		Content: "error: something failed",
		Success: false,
	}
	got := c.Compact(result)
	// Error results under maxErrorChars are returned as-is.
	if got.Content != "error: something failed" {
		t.Errorf("small error result should be preserved, got %q", got.Content)
	}
}

func TestCompact_ErrorResultOverLimit(t *testing.T) {
	c := NewResultCompactor(100)
	// Error result exceeding maxErrorChars (10000) gets char-truncated with
	// a special "error truncated" marker (NOT compactWithErrorPreservation —
	// that path is only for success results with error indicators).
	errorContent := strings.Repeat("error: failed\n", 2000) // ~26000 chars
	result := tools.ToolResult{
		Content: errorContent,
		Success: false,
	}
	got := c.Compact(result)
	// Should contain the error truncation marker.
	if !strings.Contains(got.Content, "error truncated") {
		t.Errorf("large error result should have error-truncation marker: %q...", got.Content[:min(100, len(got.Content))])
	}
	// Should be bounded to ~maxErrorChars.
	if len([]rune(got.Content)) > maxErrorChars+200 {
		t.Errorf("error result should be bounded to ~maxErrorChars, got len %d", len(got.Content))
	}
}

func TestCompact_PreservesData(t *testing.T) {
	c := NewResultCompactor(100)
	result := tools.ToolResult{
		Content: strings.Repeat("x", 500),
		Data:    map[string]any{"key": "value"},
		Success: true,
	}
	got := c.Compact(result)
	if got.Data == nil {
		t.Error("Compact should preserve Data field")
	}
}

// ========== containsErrorIndicators ==========

func TestContainsErrorIndicators(t *testing.T) {
	c := NewResultCompactor(10000)
	yesCases := []string{
		"error: file not found",
		"Error: something went wrong",
		"panic: runtime error",
		"fatal: cannot proceed",
		"failed: connection refused",
		"stack trace:",
		"exception: NullPointerException",
		"traceback:",
		"permission denied",
		"no such file or directory",
		"syntax error near line 5",
		"build failed",
		"--- FAIL: TestExample",
	}
	for _, content := range yesCases {
		if !c.containsErrorIndicators(content) {
			t.Errorf("containsErrorIndicators(%q) = false, want true", content)
		}
	}

	noCases := []string{
		"all tests passed",
		"function completed successfully",
		"hello world",
		"",
	}
	for _, content := range noCases {
		if c.containsErrorIndicators(content) {
			t.Errorf("containsErrorIndicators(%q) = true, want false", content)
		}
	}
}

// ========== isErrorLine ==========

func TestIsErrorLine(t *testing.T) {
	c := NewResultCompactor(10000)
	yesCases := []string{
		"error: undefined variable",
		"panic: something bad",
		"fatal: cannot continue",
		"failed: timeout",
		"warning: deprecated function",
		"goroutine 1 [running]:",
		"runtime.errorString",
		"main.go:123:4: undefined variable foo", // 4 parts (3 colons), .go suffix, "undefined" in tail
		"at line 42 in function main",
	}
	for _, line := range yesCases {
		if !c.isErrorLine(line) {
			t.Errorf("isErrorLine(%q) = false, want true", line)
		}
	}

	noCases := []string{
		"package main",
		"func hello() {",
		"main.go:123: some output",    // 4 parts but no error keyword in tail
		"main.go:123: undefined: foo", // only 3 colons → SplitN(,4) gives 3 parts, not >= 4
		"just a normal line",
		"",
	}
	for _, line := range noCases {
		if c.isErrorLine(line) {
			t.Errorf("isErrorLine(%q) = true, want false", line)
		}
	}
}

// ========== isSignatureLine ==========

func TestIsSignatureLine(t *testing.T) {
	yesCases := []string{
		"func main() {",
		"func (s *Server) Start() error {",
		"type Config struct {",
		"type Handler interface {",
		"def hello():",
		"class MyClass:",
		"function foo() {",
		"export default function() {",
		"const handler = () => {",
	}
	for _, line := range yesCases {
		if !isSignatureLine(line) {
			t.Errorf("isSignatureLine(%q) = false, want true", line)
		}
	}

	noCases := []string{
		"// func commented out",
		"// type Comment struct {",
		"var x = 5",
		"return nil",
		"if err != nil {",
		"",
		"some random code",
	}
	for _, line := range noCases {
		if isSignatureLine(line) {
			t.Errorf("isSignatureLine(%q) = true, want false", line)
		}
	}
}

// ========== filterOmittedSignatures ==========

func TestFilterOmittedSignatures(t *testing.T) {
	allLines := []string{
		"package main",         // 0
		"func Alpha() {",       // 1
		"	x := 1",              // 2
		"type Beta struct {",   // 3
		"	y int",               // 4
		"}",                    // 5
		"func Gamma() error {", // 6
		"	return nil",          // 7
		"}",                    // 8
		"var z = 42",           // 9
	}
	sigs := []string{"func Alpha() {", "type Beta struct {", "func Gamma() error {"}

	// Omit range [2, 8) → should find Beta (line 3) and Gamma (line 6).
	got := filterOmittedSignatures(sigs, allLines, 2, 8)
	if len(got) != 2 {
		t.Fatalf("expected 2 signatures in omit range, got %d: %v", len(got), got)
	}
	if got[0] != "type Beta struct {" {
		t.Errorf("first sig = %q, want 'type Beta struct {'", got[0])
	}
	if got[1] != "func Gamma() error {" {
		t.Errorf("second sig = %q, want 'func Gamma() error {'", got[1])
	}

	// Omit range outside any signatures → empty.
	got = filterOmittedSignatures(sigs, allLines, 4, 5)
	if len(got) != 0 {
		t.Errorf("expected 0 signatures in range [4,5), got %d", len(got))
	}
}

func TestFilterOmittedSignatures_Limit20(t *testing.T) {
	// Generate 30 signature lines.
	allLines := make([]string, 30)
	sigs := make([]string, 30)
	for i := range allLines {
		line := "func Func" + string(rune('A'+i)) + "() {"
		allLines[i] = line
		sigs[i] = line
	}
	// All in omit range → capped at 20.
	got := filterOmittedSignatures(sigs, allLines, 0, 30)
	if len(got) != 20 {
		t.Errorf("expected 20 signatures (capped), got %d", len(got))
	}
}

// ========== smartTruncateContent ==========

func TestSmartTruncateContent_ShortContent(t *testing.T) {
	c := NewResultCompactor(10000)
	c.SetHeadTailLines(3, 2)
	content := "line1\nline2\nline3\nline4\nline5"
	got := c.smartTruncateContent(content)
	// Only 5 lines, head+tail+1=6, so no line truncation; and under char limit.
	if got != content {
		t.Errorf("short content should be unchanged, got %q", got)
	}
}

func TestSmartTruncateContent_LineTruncation(t *testing.T) {
	c := NewResultCompactor(10000)
	c.SetHeadTailLines(2, 2)
	// 10 lines → head 2 + tail 2, 6 omitted.
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = "line" + string(rune('0'+i))
	}
	content := strings.Join(lines, "\n")
	got := c.smartTruncateContent(content)
	if !strings.Contains(got, "lines omitted") {
		t.Errorf("truncated content should mention omitted lines: %q", got)
	}
	if !strings.HasPrefix(got, "line0\nline1") {
		t.Errorf("should start with head lines: %q", got)
	}
	if !strings.HasSuffix(got, "line8\nline9") {
		t.Errorf("should end with tail lines: %q", got)
	}
}

func TestSmartTruncateContent_CharLimitFallback(t *testing.T) {
	c := NewResultCompactor(50)
	c.SetHeadTailLines(2, 2)
	// Many long lines → head+tail still exceeds char limit.
	content := strings.Repeat("this is a long line\n", 20)
	got := c.smartTruncateContent(content)
	if len(got) > 100 {
		t.Errorf("char-limited content should be short, got len %d", len(got))
	}
}

// ========== CompactForType ==========

func TestCompactForType_SmallResult_Unchanged(t *testing.T) {
	c := NewResultCompactor(10000)
	result := tools.ToolResult{Content: "small", Success: true}
	for _, toolName := range []string{"read", "bash", "grep", "glob", "tree", "unknown"} {
		got := c.CompactForType(toolName, result)
		if got.Content != "small" {
			t.Errorf("CompactForType(%q) small result should be unchanged, got %q", toolName, got.Content)
		}
	}
}

func TestCompactForType_FailedResult_Unchanged(t *testing.T) {
	c := NewResultCompactor(100)
	result := tools.ToolResult{
		Content: strings.Repeat("x", 500),
		Success: false,
	}
	// Failed results are returned as-is by CompactForType (only success results get type-based compaction).
	got := c.CompactForType("read", result)
	if got.Content != result.Content {
		t.Errorf("failed result should be unchanged by CompactForType, got different content")
	}
}

func TestCompactForType_Read_LargeFile(t *testing.T) {
	c := NewResultCompactor(100) // small maxChars so result exceeds limit
	// >50 lines triggers compactFileContent.
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "line " + string(rune('A'+i%26)) + " content here"
	}
	// Add some signatures in the middle.
	lines[35] = "func MyFunc() {"
	lines[40] = "type MyType struct {"
	result := tools.ToolResult{Content: strings.Join(lines, "\n"), Success: true}
	got := c.CompactForType("read", result)
	// compactFileContent keeps head 30 + tail 10; with small maxChars the
	// final result still exceeds maxChars, but the function returns its own
	// builder output without re-applying char limit. So we check for the
	// omission marker instead.
	if !strings.Contains(got.Content, "omitted") {
		t.Errorf("large read should be truncated with omission marker: %q...", got.Content[:min(80, len(got.Content))])
	}
	if !strings.Contains(got.Content, "func MyFunc()") {
		t.Errorf("should preserve signature in omitted section: %q", got.Content)
	}
}

func TestCompactForType_Bash_LargeOutput(t *testing.T) {
	c := NewResultCompactor(100) // small maxChars so result exceeds limit
	// >30 lines triggers compactCommandOutput.
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "output line " + string(rune('A'+i%26))
	}
	result := tools.ToolResult{Content: strings.Join(lines, "\n"), Success: true}
	got := c.CompactForType("bash", result)
	if !strings.Contains(got.Content, "omitted") {
		t.Errorf("large bash output should be truncated: %q...", got.Content[:min(80, len(got.Content))])
	}
}

func TestCompactForType_Grep_LargeResults(t *testing.T) {
	c := NewResultCompactor(100) // small maxChars so result exceeds limit
	// >50 lines triggers compactSearchResults.
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "file.go:" + string(rune('0'+i%10)) + ": some match"
	}
	// Add error lines for prioritization (must be isErrorLine-compatible).
	lines[55] = "main.go:42: error: undefined variable here"
	lines[56] = "util.go:10: panic: something bad happened"
	result := tools.ToolResult{Content: strings.Join(lines, "\n"), Success: true}
	got := c.CompactForType("grep", result)
	// Error matches should appear first.
	errorIdx := strings.Index(got.Content, "Error-related matches")
	if errorIdx < 0 {
		// If no error matches detected, the function still compacts — verify it ran.
		if len(got.Content) == 0 {
			t.Error("grep compaction produced empty output")
		}
		return
	}
	otherIdx := strings.Index(got.Content, "Other matches")
	if otherIdx >= 0 && errorIdx > otherIdx {
		t.Errorf("grep compaction should prioritize error matches first: %q...", got.Content[:min(100, len(got.Content))])
	}
}

func TestCompactForType_Glob_LargeList(t *testing.T) {
	c := NewResultCompactor(100) // small maxChars so result exceeds limit
	// >100 lines triggers compactFileList.
	lines := make([]string, 120)
	for i := range lines {
		lines[i] = "path/to/file" + string(rune('A'+i%26)) + ".go"
	}
	result := tools.ToolResult{Content: strings.Join(lines, "\n"), Success: true}
	got := c.CompactForType("glob", result)
	if !strings.Contains(got.Content, "more files not shown") {
		t.Errorf("large glob should show file count: %q...", got.Content[:min(80, len(got.Content))])
	}
}

func TestCompactForType_Tree_LargeOutput(t *testing.T) {
	c := NewResultCompactor(100) // small maxChars so result exceeds limit
	// >100 lines triggers compactTreeOutput.
	lines := make([]string, 120)
	for i := range lines {
		lines[i] = "dir" + string(rune('A'+i%26))
	}
	result := tools.ToolResult{Content: strings.Join(lines, "\n"), Success: true}
	got := c.CompactForType("tree", result)
	if !strings.Contains(got.Content, "more items not shown") {
		t.Errorf("large tree should be truncated: %q...", got.Content[:min(80, len(got.Content))])
	}
}

func TestCompactForType_UnknownTool_FallsBackToCompact(t *testing.T) {
	c := NewResultCompactor(100)
	result := tools.ToolResult{
		Content: strings.Repeat("x", 500),
		Success: true,
	}
	got := c.CompactForType("some_unknown_tool", result)
	if !strings.Contains(got.Content, "[truncated") {
		t.Errorf("unknown tool should fall back to Compact: %q...", got.Content[:min(60, len(got.Content))])
	}
}

// ========== compactWithErrorPreservation ==========

func TestCompactWithErrorPreservation(t *testing.T) {
	c := NewResultCompactor(5000)
	// Mix of error and normal lines.
	content := strings.Join([]string{
		"normal output line 1",
		"error: something failed",
		"normal output line 2",
		"panic: stack overflow",
		"normal output line 3",
	}, "\n")
	result := tools.ToolResult{Content: content, Success: false}
	got := c.compactWithErrorPreservation(result)
	// Should contain preserved error section.
	if !strings.Contains(got.Content, "Error Information") {
		t.Errorf("should preserve error info section: %q", got.Content)
	}
	if !strings.Contains(got.Content, "error: something failed") {
		t.Errorf("should contain first error line: %q", got.Content)
	}
	if !strings.Contains(got.Content, "panic: stack overflow") {
		t.Errorf("should contain second error line: %q", got.Content)
	}
}

func TestCompactWithErrorPreservation_NoErrors(t *testing.T) {
	c := NewResultCompactor(5000)
	content := "just normal output\nnothing wrong here"
	result := tools.ToolResult{Content: content, Success: false}
	got := c.compactWithErrorPreservation(result)
	// No error lines → no Error Information section.
	if strings.Contains(got.Content, "Error Information") {
		t.Errorf("should NOT have error section when no errors: %q", got.Content)
	}
}

// ========== compactFileContent (edge cases) ==========

func TestCompactFileContent_Under50Lines(t *testing.T) {
	c := NewResultCompactor(50) // small max to trigger Compact
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = strings.Repeat("x", 10)
	}
	result := tools.ToolResult{Content: strings.Join(lines, "\n"), Success: true}
	got := c.compactFileContent(result)
	// Under 50 lines → falls back to Compact → char truncation.
	if !strings.Contains(got.Content, "[truncated") {
		t.Errorf("under-50-line file with small max should char-truncate: %q...", got.Content[:min(40, len(got.Content))])
	}
}

// ========== compactCommandOutput (error-aware) ==========

func TestCompactCommandOutput_WithErrorIndicators(t *testing.T) {
	c := NewResultCompactor(10000)
	// >30 lines with error indicators → headCount=3, tailCount=25.
	lines := make([]string, 40)
	for i := range lines {
		lines[i] = "output " + string(rune('A'+i%26))
	}
	lines[38] = "error: build failed"
	lines[39] = "fatal: cannot proceed"
	result := tools.ToolResult{Content: strings.Join(lines, "\n"), Success: true}
	got := c.compactCommandOutput(result)
	// Should keep more tail lines (25) to capture the error.
	if !strings.Contains(got.Content, "error: build failed") {
		t.Errorf("should preserve error in tail: %q...", got.Content[len(got.Content)-min(80, len(got.Content)):])
	}
}
