package context

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestCompressContentsHandlesNilEntries(t *testing.T) {
	rc := NewResponseCompressor(16)
	contents := []*genai.Content{
		nil,
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{
					FunctionResponse: &genai.FunctionResponse{
						Name:     "read",
						Response: map[string]any{"content": "this is a long enough payload to compress"},
					},
				},
			},
		},
	}

	compressed := rc.CompressContents(contents)

	if len(compressed) != len(contents) {
		t.Fatalf("len(compressed) = %d, want %d", len(compressed), len(contents))
	}
	if compressed[0] != nil {
		t.Fatal("nil content entry should remain nil")
	}
	if compressed[1] == nil || len(compressed[1].Parts) != 1 || compressed[1].Parts[0] == nil {
		t.Fatal("non-nil content should still be compressed")
	}
}

// --- NewResponseCompressor ---

func TestNewResponseCompressor_DefaultMaxChars(t *testing.T) {
	rc := NewResponseCompressor(0)
	if rc.maxChars != 10000 {
		t.Errorf("maxChars = %d, want 10000 (default for 0)", rc.maxChars)
	}
}

func TestNewResponseCompressor_NegativeMaxChars(t *testing.T) {
	rc := NewResponseCompressor(-5)
	if rc.maxChars != 10000 {
		t.Errorf("maxChars = %d, want 10000 (default for negative)", rc.maxChars)
	}
}

func TestNewResponseCompressor_CustomMaxChars(t *testing.T) {
	rc := NewResponseCompressor(500)
	if rc.maxChars != 500 {
		t.Errorf("maxChars = %d, want 500", rc.maxChars)
	}
}

func TestNewResponseCompressor_HasKeepHeaders(t *testing.T) {
	rc := NewResponseCompressor(100)
	if len(rc.keepHeaders) == 0 {
		t.Error("keepHeaders should not be empty")
	}
}

// --- CompressFunctionResponse ---

func TestCompressFunctionResponse_NilPart(t *testing.T) {
	rc := NewResponseCompressor(100)
	if got := rc.CompressFunctionResponse(nil); got != nil {
		t.Error("nil part should return nil")
	}
}

func TestCompressFunctionResponse_NilResponse(t *testing.T) {
	rc := NewResponseCompressor(100)
	part := &genai.FunctionResponse{Name: "read", Response: nil}
	got := rc.CompressFunctionResponse(part)
	if got != part {
		t.Error("nil Response should return part as-is")
	}
}

func TestCompressFunctionResponse_PreservesMetadata(t *testing.T) {
	rc := NewResponseCompressor(100)
	part := &genai.FunctionResponse{
		ID:       "call-1",
		Name:     "bash",
		Response: map[string]any{"output": "hello"},
	}
	got := rc.CompressFunctionResponse(part)
	if got.ID != "call-1" {
		t.Errorf("ID = %q, want 'call-1'", got.ID)
	}
	if got.Name != "bash" {
		t.Errorf("Name = %q, want 'bash'", got.Name)
	}
}

func TestCompressFunctionResponse_CompressesLongString(t *testing.T) {
	rc := NewResponseCompressor(10)
	long := strings.Repeat("x", 100)
	part := &genai.FunctionResponse{
		Name:     "read",
		Response: map[string]any{"content": long},
	}
	got := rc.CompressFunctionResponse(part)
	compressed, ok := got.Response["content"].(string)
	if !ok {
		t.Fatal("content should be a string")
	}
	if !strings.Contains(compressed, "[truncated]") {
		t.Errorf("expected [truncated] in compressed output: %q", compressed)
	}
}

func TestCompressFunctionResponse_KeepsImportantField(t *testing.T) {
	rc := NewResponseCompressor(10)
	longError := strings.Repeat("x", 100)
	part := &genai.FunctionResponse{
		Name:     "bash",
		Response: map[string]any{"error": longError},
	}
	got := rc.CompressFunctionResponse(part)
	result, ok := got.Response["error"].(string)
	if !ok {
		t.Fatal("error should be a string")
	}
	if result != longError {
		t.Errorf("important field 'error' should be kept as-is, got len=%d", len(result))
	}
}

// --- compressValue ---

func TestCompressValue_String(t *testing.T) {
	rc := NewResponseCompressor(5)
	got := rc.compressValue("content", "hello world this is long")
	if str, ok := got.(string); !ok || !strings.Contains(str, "[truncated]") {
		t.Errorf("expected truncated string, got %v", got)
	}
}

func TestCompressValue_Map(t *testing.T) {
	rc := NewResponseCompressor(5)
	m := map[string]any{"content": "hello world this is long"}
	got := rc.compressValue("data", m)
	result, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", got)
	}
	compressed, ok := result["content"].(string)
	if !ok || !strings.Contains(compressed, "[truncated]") {
		t.Errorf("expected truncated string in map, got %v", result["content"])
	}
}

func TestCompressValue_Array(t *testing.T) {
	rc := NewResponseCompressor(100)
	arr := make([]any, 15)
	for i := range arr {
		arr[i] = i
	}
	got := rc.compressValue("items", arr)
	result, ok := got.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", got)
	}
	if len(result) != 7 {
		t.Errorf("compressed array len = %d, want 7", len(result))
	}
}

func TestCompressValue_SmallArray(t *testing.T) {
	rc := NewResponseCompressor(100)
	arr := []any{1, 2, 3}
	got := rc.compressValue("items", arr)
	if result, ok := got.([]any); !ok || len(result) != 3 {
		t.Errorf("small array should be kept as-is, got %v", got)
	}
}

func TestCompressValue_DefaultType(t *testing.T) {
	rc := NewResponseCompressor(100)
	got := rc.compressValue("count", 42)
	if got != 42 {
		t.Errorf("default type = %v, want 42", got)
	}
}

func TestCompressValue_ImportantField(t *testing.T) {
	rc := NewResponseCompressor(5)
	got := rc.compressValue("exit_code", 1)
	if got != 1 {
		t.Errorf("important field value = %v, want 1", got)
	}
}

// --- compressString ---

func TestCompressString_ShortString(t *testing.T) {
	rc := NewResponseCompressor(100)
	got := rc.compressString("short")
	if got != "short" {
		t.Errorf("short string = %q, want 'short'", got)
	}
}

func TestCompressString_LongString(t *testing.T) {
	rc := NewResponseCompressor(10)
	long := strings.Repeat("a", 50)
	got := rc.compressString(long)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated]: %q", got)
	}
}

func TestCompressString_ImportantKeywordUnder2x(t *testing.T) {
	rc := NewResponseCompressor(20)
	important := "error: " + strings.Repeat("x", 15) // len=22, maxChars*2=40 → kept
	got := rc.compressString(important)
	if got != important {
		t.Errorf("important keyword string under 2x limit should be kept: len(got)=%d", len(got))
	}
}

func TestCompressString_ImportantKeywordOver2x(t *testing.T) {
	rc := NewResponseCompressor(10)
	important := "error: " + strings.Repeat("x", 50)
	got := rc.compressString(important)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("important keyword over 2x should be truncated: %q", got)
	}
}

func TestCompressString_BreakAtNewline(t *testing.T) {
	rc := NewResponseCompressor(20)
	content := "line one\nline two is very long and continues"
	got := rc.compressString(content)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated]: %q", got)
	}
}

// --- compressMap ---

func TestCompressMap(t *testing.T) {
	rc := NewResponseCompressor(5)
	m := map[string]any{
		"content": "this is a long string that should be compressed",
		"count":   5,
	}
	got := rc.compressMap(m)
	if _, ok := got["content"]; !ok {
		t.Error("compressed map should have content key")
	}
	if got["count"] != 5 {
		t.Errorf("count = %v, want 5", got["count"])
	}
}

func TestCompressMap_Empty(t *testing.T) {
	rc := NewResponseCompressor(100)
	got := rc.compressMap(map[string]any{})
	if len(got) != 0 {
		t.Errorf("empty map should stay empty, got %d entries", len(got))
	}
}

// --- compressArray ---

func TestCompressArray_SmallKeptAsIs(t *testing.T) {
	rc := NewResponseCompressor(100)
	arr := []any{1, 2, 3, 4, 5}
	got := rc.compressArray(arr)
	if len(got) != 5 {
		t.Errorf("small array len = %d, want 5", len(got))
	}
}

func TestCompressArray_LargeCompressed(t *testing.T) {
	rc := NewResponseCompressor(100)
	arr := make([]any, 20)
	for i := range arr {
		arr[i] = i
	}
	got := rc.compressArray(arr)
	if len(got) != 7 {
		t.Errorf("large array len = %d, want 7", len(got))
	}
}

func TestCompressArray_NoteFields(t *testing.T) {
	rc := NewResponseCompressor(100)
	arr := make([]any, 20)
	for i := range arr {
		arr[i] = i
	}
	got := rc.compressArray(arr)
	note, ok := got[3].(map[string]any)
	if !ok {
		t.Fatalf("expected note map at index 3, got %T", got[3])
	}
	if note["_skipped"] != 14 {
		t.Errorf("_skipped = %v, want 14", note["_skipped"])
	}
	if note["_total"] != 20 {
		t.Errorf("_total = %v, want 20", note["_total"])
	}
}

func TestCompressArray_Exactly10(t *testing.T) {
	rc := NewResponseCompressor(100)
	arr := make([]any, 10)
	for i := range arr {
		arr[i] = i
	}
	got := rc.compressArray(arr)
	if len(got) != 10 {
		t.Errorf("exactly 10 items should be kept, got len=%d", len(got))
	}
}

func TestCompressArray_FirstAndLastPreserved(t *testing.T) {
	rc := NewResponseCompressor(100)
	arr := make([]any, 20)
	for i := range arr {
		arr[i] = i
	}
	got := rc.compressArray(arr)
	if got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Errorf("first 3 items = %v, want 0,1,2", got[:3])
	}
	if got[4] != 17 || got[5] != 18 || got[6] != 19 {
		t.Errorf("last 3 items = %v, want 17,18,19", got[4:])
	}
}

// --- isImportantField ---

func TestIsImportantField(t *testing.T) {
	rc := NewResponseCompressor(100)
	important := []string{"error", "Error", "success", "failed", "status", "exit_code", "file_path", "command", "tool", "return_code", "path"}
	for _, key := range important {
		if !rc.isImportantField(key) {
			t.Errorf("isImportantField(%q) = false, want true", key)
		}
	}
}

func TestIsImportantField_NotImportant(t *testing.T) {
	rc := NewResponseCompressor(100)
	notImportant := []string{"content", "output", "data", "result", "body", "text", ""}
	for _, key := range notImportant {
		if rc.isImportantField(key) {
			t.Errorf("isImportantField(%q) = true, want false", key)
		}
	}
}

// --- CompressContent ---

func TestCompressContent_NilPart(t *testing.T) {
	rc := NewResponseCompressor(100)
	if got := rc.CompressContent(nil); got != nil {
		t.Error("nil part should return nil")
	}
}

func TestCompressContent_NoFunctionResponse(t *testing.T) {
	rc := NewResponseCompressor(100)
	part := &genai.Part{Text: "hello"}
	got := rc.CompressContent(part)
	if got != part {
		t.Error("part without FunctionResponse should be returned as-is")
	}
}

func TestCompressContent_WithFunctionResponse(t *testing.T) {
	rc := NewResponseCompressor(5)
	part := &genai.Part{
		FunctionResponse: &genai.FunctionResponse{
			Name:     "read",
			Response: map[string]any{"content": strings.Repeat("x", 50)},
		},
	}
	got := rc.CompressContent(part)
	if got == nil || got.FunctionResponse == nil {
		t.Fatal("expected non-nil compressed FunctionResponse")
	}
}

// --- CompressContents ---

func TestCompressContents_Empty(t *testing.T) {
	rc := NewResponseCompressor(100)
	got := rc.CompressContents(nil)
	if got != nil {
		t.Errorf("nil input should return nil/empty, got %v", got)
	}
}

func TestCompressContents_MultipleContents(t *testing.T) {
	rc := NewResponseCompressor(5)
	contents := []*genai.Content{
		{
			Role: genai.RoleUser,
			Parts: []*genai.Part{
				{FunctionResponse: &genai.FunctionResponse{
					Name:     "read",
					Response: map[string]any{"content": strings.Repeat("x", 50)},
				}},
				{Text: "plain text"},
			},
		},
		{
			Role: genai.RoleModel,
			Parts: []*genai.Part{
				{Text: "model response"},
			},
		},
	}
	got := rc.CompressContents(contents)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if len(got[0].Parts) != 2 {
		t.Errorf("first content parts = %d, want 2", len(got[0].Parts))
	}
}

// --- SmartTruncate ---

func TestSmartTruncate_ShortText(t *testing.T) {
	got := SmartTruncate("short", 100)
	if got != "short" {
		t.Errorf("short text = %q, want 'short'", got)
	}
}

func TestSmartTruncate_LongText(t *testing.T) {
	long := strings.Repeat("a", 100)
	got := SmartTruncate(long, 10)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated]: %q", got)
	}
}

func TestSmartTruncate_JSON(t *testing.T) {
	jsonText := `{"key": "` + strings.Repeat("x", 100) + `"}`
	got := SmartTruncate(jsonText, 20)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated] for JSON: %q", got)
	}
}

func TestSmartTruncate_JSONArray(t *testing.T) {
	jsonText := `[` + strings.Repeat("\"item\",", 100) + `]`
	got := SmartTruncate(jsonText, 20)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated] for JSON array: %q", got)
	}
}

func TestSmartTruncate_CodeBlock(t *testing.T) {
	code := "```go\n" + strings.Repeat("x", 100) + "\n```"
	got := SmartTruncate(code, 20)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated] for code block: %q", got)
	}
}

func TestSmartTruncate_CodeBlockUnclosed(t *testing.T) {
	code := "```go\n" + strings.Repeat("x", 100)
	got := SmartTruncate(code, 20)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated] for unclosed code block: %q", got)
	}
}

func TestSmartTruncate_List(t *testing.T) {
	var lines []string
	for i := 0; i < 20; i++ {
		lines = append(lines, "item "+string(rune('a'+i)))
	}
	text := strings.Join(lines, "\n")
	// maxChars must be < len(text) (~140) so SmartTruncate enters the list path
	got := SmartTruncate(text, 50)
	if !strings.Contains(got, "skipped") {
		t.Errorf("expected 'skipped' note in list truncation: %q", got)
	}
}

// --- runesTruncate ---

func TestRunesTruncate_ShortString(t *testing.T) {
	got := runesTruncate("hello", 10)
	if got != "hello" {
		t.Errorf("short string = %q, want 'hello'", got)
	}
}

func TestRunesTruncate_LongString(t *testing.T) {
	got := runesTruncate("hello world", 5)
	if got != "hello" {
		t.Errorf("truncated = %q, want 'hello'", got)
	}
}

func TestRunesTruncate_Unicode(t *testing.T) {
	got := runesTruncate("привет мир", 3)
	if len([]rune(got)) != 3 {
		t.Errorf("unicode truncate rune count = %d, want 3", len([]rune(got)))
	}
}

func TestRunesTruncate_ExactLength(t *testing.T) {
	got := runesTruncate("hello", 5)
	if got != "hello" {
		t.Errorf("exact length = %q, want 'hello'", got)
	}
}

// --- truncateStructured ---

func TestTruncateStructured(t *testing.T) {
	jsonText := `{"key": "` + strings.Repeat("x", 100) + `"}`
	got := truncateStructured(jsonText, 20)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated]: %q", got)
	}
}

// --- truncateCodeBlock ---

func TestTruncateCodeBlock_Basic(t *testing.T) {
	code := "```go\nfunc main() {}\n```"
	got := truncateCodeBlock(code, 100)
	if !strings.Contains(got, "```") {
		t.Errorf("expected code block preserved: %q", got)
	}
}

func TestTruncateCodeBlock_NoMarker(t *testing.T) {
	got := truncateCodeBlock("no markers here but long enough text", 10)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated]: %q", got)
	}
}

func TestTruncateCodeBlock_UnclosedBlock(t *testing.T) {
	code := "```go\n" + strings.Repeat("x", 100)
	got := truncateCodeBlock(code, 10)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated] for unclosed: %q", got)
	}
}

func TestTruncateCodeBlock_LargeBlock(t *testing.T) {
	code := "```go\n" + strings.Repeat("x", 200) + "\n```"
	got := truncateCodeBlock(code, 10)
	if !strings.Contains(got, "[truncated]") {
		t.Errorf("expected [truncated] for large block: %q", got)
	}
}

// --- truncateList ---

func TestTruncateList(t *testing.T) {
	lines := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	got := truncateList(lines, 100)
	if !strings.Contains(got, "skipped") {
		t.Errorf("expected 'skipped' note: %q", got)
	}
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") || !strings.Contains(got, "c") {
		t.Errorf("expected first 3 items in output: %q", got)
	}
}

func TestTruncateList_SmallMaxChars(t *testing.T) {
	lines := []string{"aaaa", "bbbb", "cccc", "dddd", "eeee", "ffff", "gggg", "hhhh"}
	got := truncateList(lines, 20)
	if !strings.Contains(got, "skipped") {
		t.Errorf("expected 'skipped' note: %q", got)
	}
}
