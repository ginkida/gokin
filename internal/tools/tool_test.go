package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gokin/internal/config"
)

// ============================================================
// ToolResult Tests
// ============================================================

func TestNewSuccessResult(t *testing.T) {
	result := NewSuccessResult("test content")

	if result.Content != "test content" {
		t.Errorf("Content = %v, want %v", result.Content, "test content")
	}
	if result.Success != true {
		t.Error("Success = false, want true")
	}
	if result.Error != "" {
		t.Errorf("Error = %v, want empty", result.Error)
	}
}

func TestNewSuccessResultWithData(t *testing.T) {
	data := map[string]string{"key": "value"}
	result := NewSuccessResultWithData("test content", data)

	if result.Content != "test content" {
		t.Errorf("Content = %v, want %v", result.Content, "test content")
	}
	if result.Success != true {
		t.Error("Success = false, want true")
	}
	if result.Data == nil {
		t.Error("Data = nil, want map")
	}
}

func TestNewErrorResult(t *testing.T) {
	result := NewErrorResult("error message")

	if result.Error != "error message" {
		t.Errorf("Error = %v, want %v", result.Error, "error message")
	}
	if result.Success != false {
		t.Error("Success = true, want false")
	}
	if result.Content != "" {
		t.Errorf("Content = %v, want empty", result.Content)
	}
}

func TestNewErrorResultWithContext(t *testing.T) {
	result := NewErrorResultWithContext("error message", "file context here")

	if result.Error != "error message" {
		t.Errorf("Error = %v, want %v", result.Error, "error message")
	}
	if result.Success != false {
		t.Error("Success = true, want false")
	}
}

// ============================================================
// SafetyLevel Tests
// ============================================================

func TestSafetyLevel_Constants(t *testing.T) {
	levels := []SafetyLevel{
		SafetyLevelSafe,
		SafetyLevelCaution,
		SafetyLevelDangerous,
		SafetyLevelCritical,
	}

	if len(levels) != 4 {
		t.Errorf("expected 4 safety levels, got %d", len(levels))
	}

	for _, level := range levels {
		if string(level) == "" {
			t.Errorf("SafetyLevel is empty string")
		}
	}
}

func TestSafetyLevel_Values(t *testing.T) {
	if SafetyLevelSafe != "safe" {
		t.Errorf("SafetyLevelSafe = %v, want %v", SafetyLevelSafe, "safe")
	}
	if SafetyLevelCaution != "caution" {
		t.Errorf("SafetyLevelCaution = %v, want %v", SafetyLevelCaution, "caution")
	}
	if SafetyLevelDangerous != "dangerous" {
		t.Errorf("SafetyLevelDangerous = %v, want %v", SafetyLevelDangerous, "dangerous")
	}
	if SafetyLevelCritical != "critical" {
		t.Errorf("SafetyLevelCritical = %v, want %v", SafetyLevelCritical, "critical")
	}
}

func TestSafetyLevel_String(t *testing.T) {
	level := SafetyLevelSafe
	if string(level) != "safe" {
		t.Errorf("SafetyLevel.String() = %v, want safe", string(level))
	}
}

// ============================================================
// ValidationError Tests
// ============================================================

func TestNewValidationError(t *testing.T) {
	err := NewValidationError("field_name", "is required")

	if err.Field != "field_name" {
		t.Errorf("Field = %v, want %v", err.Field, "field_name")
	}
	if err.Message != "is required" {
		t.Errorf("Message = %v, want %v", err.Message, "is required")
	}
}

func TestValidationError_Error(t *testing.T) {
	err := NewValidationError("field", "cannot be empty")

	expected := "field: cannot be empty"
	if got := err.Error(); got != expected {
		t.Errorf("Error() = %v, want %v", got, expected)
	}
}

// ============================================================
// Tool Interface Tests (Mock Tool)
// ============================================================

type mockTool struct {
	nameVal       string
	descVal       string
	validateErr   error
	executeErr    error
	executeResult ToolResult
}

func (m *mockTool) Name() string        { return m.nameVal }
func (m *mockTool) Description() string { return m.descVal }
func (m *mockTool) Validate(args map[string]any) error {
	return m.validateErr
}
func (m *mockTool) Execute(ctx context.Context, args map[string]any) (ToolResult, error) {
	return m.executeResult, m.executeErr
}

func TestTool_NameAndDescription(t *testing.T) {
	tool := &mockTool{
		nameVal: "test_tool",
		descVal: "A test tool",
	}

	if tool.Name() != "test_tool" {
		t.Errorf("Name() = %v, want %v", tool.Name(), "test_tool")
	}
	if tool.Description() != "A test tool" {
		t.Errorf("Description() = %v, want %v", tool.Description(), "A test tool")
	}
}

func TestTool_ValidateSuccess(t *testing.T) {
	tool := &mockTool{
		nameVal:     "test",
		descVal:     "test",
		validateErr: nil,
	}

	err := tool.Validate(map[string]any{})
	if err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestTool_ValidateError(t *testing.T) {
	expectedErr := NewValidationError("arg", "invalid")
	tool := &mockTool{
		nameVal:     "test",
		descVal:     "test",
		validateErr: expectedErr,
	}

	err := tool.Validate(map[string]any{})
	if err == nil {
		t.Error("Validate() expected error, got nil")
	}
	if err != expectedErr {
		t.Errorf("Validate() error = %v, want %v", err, expectedErr)
	}
}

func TestTool_ExecuteSuccess(t *testing.T) {
	tool := &mockTool{
		nameVal:       "test",
		descVal:       "test",
		executeResult: NewSuccessResult("success"),
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})
	if err != nil {
		t.Errorf("Execute() unexpected error: %v", err)
	}
	if !result.Success {
		t.Error("Execute() result.Success = false, want true")
	}
}

func TestTool_ExecuteError(t *testing.T) {
	expectedErr := errors.New("execute failed")
	tool := &mockTool{
		nameVal:    "test",
		descVal:    "test",
		executeErr: expectedErr,
	}

	ctx := context.Background()
	_, err := tool.Execute(ctx, map[string]any{})
	if err == nil {
		t.Error("Execute() expected error, got nil")
	}
}

func TestTool_ValidateNilArgs(t *testing.T) {
	tool := &mockTool{
		nameVal:     "test",
		descVal:     "test",
		validateErr: nil,
	}

	err := tool.Validate(nil)
	if err != nil {
		t.Errorf("Validate(nil) unexpected error: %v", err)
	}
}

func TestTool_ValidateEmptyArgs(t *testing.T) {
	tool := &mockTool{
		nameVal:     "test",
		descVal:     "test",
		validateErr: nil,
	}

	err := tool.Validate(map[string]any{})
	if err != nil {
		t.Errorf("Validate({}) unexpected error: %v", err)
	}
}

func TestMockTool_ErrorInExecute(t *testing.T) {
	tool := &mockTool{
		nameVal:    "failing_tool",
		descVal:    "A failing tool",
		executeErr: errors.New("execution failed"),
	}

	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{})

	if err == nil {
		t.Error("Expected error from Execute")
	}
	if result.Success {
		t.Error("Result.Success should be false when error occurs")
	}
}

func TestMockTool_ErrorInValidate(t *testing.T) {
	validationErr := NewValidationError("arg", "is required")
	tool := &mockTool{
		nameVal:     "validation_tool",
		descVal:     "A validation tool",
		validateErr: validationErr,
	}

	err := tool.Validate(map[string]any{})

	if err == nil {
		t.Error("Expected validation error")
	}

	// Check error is the validation error we set
	if err != validationErr {
		t.Errorf("Validate() error = %v, want %v", err, validationErr)
	}
}

// ============================================================
// ToolRequester Interface Tests
// ============================================================

type mockToolRequester struct {
	requestedTool string
	requestErr    error
}

func (m *mockToolRequester) RequestTool(name string) error {
	m.requestedTool = name
	return m.requestErr
}

func TestToolRequester_Interface(t *testing.T) {
	var requester ToolRequester = &mockToolRequester{}

	err := requester.RequestTool("test_tool")
	if err != nil {
		t.Errorf("RequestTool() unexpected error: %v", err)
	}
}

// ============================================================
// Messenger Interface Tests
// ============================================================

type mockMessenger struct {
	sentMessages []string
	responses    map[string]string
}

func (m *mockMessenger) SendMessage(msgType string, toRole string, content string, data map[string]any) (string, error) {
	m.sentMessages = append(m.sentMessages, content)
	return "msg-id-1", nil
}

func (m *mockMessenger) ReceiveResponse(ctx context.Context, messageID string) (string, error) {
	if resp, ok := m.responses[messageID]; ok {
		return resp, nil
	}
	return "", nil
}

func TestMessenger_Interface(t *testing.T) {
	var messenger Messenger = &mockMessenger{
		responses: map[string]string{
			"msg-1": "response 1",
		},
	}

	id, err := messenger.SendMessage("request", "agent", "hello", nil)
	if err != nil {
		t.Errorf("SendMessage() unexpected error: %v", err)
	}
	if id == "" {
		t.Error("SendMessage() returned empty message ID")
	}

	ctx := context.Background()
	resp, err := messenger.ReceiveResponse(ctx, "msg-1")
	if err != nil {
		t.Errorf("ReceiveResponse() unexpected error: %v", err)
	}
	if resp != "response 1" {
		t.Errorf("ReceiveResponse() = %v, want %v", resp, "response 1")
	}
}

// ============================================================
// MultimodalPart Tests
// ============================================================

func TestMultimodalPart(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02}
	part := &MultimodalPart{
		MimeType: "image/png",
		Data:     data,
	}

	if part.MimeType != "image/png" {
		t.Errorf("MimeType = %v, want %v", part.MimeType, "image/png")
	}
	if len(part.Data) != 3 {
		t.Errorf("len(Data) = %v, want %v", len(part.Data), 3)
	}
}

// ============================================================
// ExecutionSummary Tests
// ============================================================

func TestExecutionSummary(t *testing.T) {
	summary := &ExecutionSummary{
		ToolName:    "read",
		DisplayName: "Read File",
		Action:      "Read file",
		Target:      "/path/to/file",
	}

	if summary.Action != "Read file" {
		t.Errorf("Action = %v, want %v", summary.Action, "Read file")
	}
	if summary.Target != "/path/to/file" {
		t.Errorf("Target = %v, want %v", summary.Target, "/path/to/file")
	}
}

func TestExecutionSummary_String(t *testing.T) {
	summary := &ExecutionSummary{
		Action: "Read file",
		Target: "/path/to/file",
	}

	expected := "Read file /path/to/file"
	if got := summary.String(); got != expected {
		t.Errorf("String() = %v, want %v", got, expected)
	}
}

func TestExecutionSummary_StringNoTarget(t *testing.T) {
	summary := &ExecutionSummary{
		Action: "List files",
	}

	expected := "List files"
	if got := summary.String(); got != expected {
		t.Errorf("String() = %v, want %v", got, expected)
	}
}

// ============================================================
// ToolMetadata Tests
// ============================================================

func TestToolMetadata(t *testing.T) {
	meta := ToolMetadata{
		Name:        "read",
		SafetyLevel: SafetyLevelSafe,
		Category:    "file",
		Impact:      "Reads file contents",
	}

	if meta.Name != "read" {
		t.Errorf("Name = %v, want %v", meta.Name, "read")
	}
	if meta.SafetyLevel != SafetyLevelSafe {
		t.Errorf("SafetyLevel = %v, want %v", meta.SafetyLevel, SafetyLevelSafe)
	}
	if meta.Category != "file" {
		t.Errorf("Category = %v, want %v", meta.Category, "file")
	}
}

// ============================================================
// PreFlightCheck Tests
// ============================================================

func TestPreFlightCheck(t *testing.T) {
	check := PreFlightCheck{
		IsValid:      true,
		Warnings:     []string{"file is large"},
		Errors:       []string{},
		Requirements: []string{"file must exist"},
		Suggestions:  []string{"consider using glob first"},
	}

	if !check.IsValid {
		t.Error("IsValid = false, want true")
	}
	if len(check.Warnings) != 1 {
		t.Errorf("len(Warnings) = %v, want 1", len(check.Warnings))
	}
	if len(check.Requirements) != 1 {
		t.Errorf("len(Requirements) = %v, want 1", len(check.Requirements))
	}
}

// ============================================================
// Edge Cases
// ============================================================

func TestNewSuccessResult_EmptyContent(t *testing.T) {
	result := NewSuccessResult("")

	if result.Content != "" {
		t.Errorf("Content = %v, want empty", result.Content)
	}
	if !result.Success {
		t.Error("Success = false for empty content, want true")
	}
}

func TestNewErrorResult_EmptyMessage(t *testing.T) {
	result := NewErrorResult("")

	if result.Error != "" {
		t.Errorf("Error = %v, want empty", result.Error)
	}
	if result.Success {
		t.Error("Success = true for empty error, want false")
	}
}

func TestToolResult_AllFields(t *testing.T) {
	parts := []*MultimodalPart{{MimeType: "image/png", Data: []byte("data")}}

	result := ToolResult{
		Content:          "content",
		Data:             map[string]int{"key": 1},
		Error:            "",
		Success:          true,
		ExecutionSummary: &ExecutionSummary{Action: "test"},
		SafetyLevel:      SafetyLevelSafe,
		Duration:         "50ms",
		MultimodalParts:  parts,
	}

	if result.Content != "content" {
		t.Errorf("Content = %v, want %v", result.Content, "content")
	}
	if result.Data == nil {
		t.Error("Data is nil")
	}
	if result.SafetyLevel != SafetyLevelSafe {
		t.Errorf("SafetyLevel = %v, want %v", result.SafetyLevel, SafetyLevelSafe)
	}
	if result.Duration != "50ms" {
		t.Errorf("Duration = %v, want %v", result.Duration, "50ms")
	}
	if len(result.MultimodalParts) != 1 {
		t.Errorf("len(MultimodalParts) = %v, want 1", len(result.MultimodalParts))
	}
}

func TestToolExecute_WithCancelledContext(t *testing.T) {
	tool := &mockTool{
		nameVal:       "slow_tool",
		descVal:       "A slow tool",
		executeResult: NewSuccessResult("done"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Execute with cancelled context — should not panic
	result, err := tool.Execute(ctx, map[string]any{})
	// mockTool ignores context, so it returns success.
	// Real tools should check ctx.Err().
	if err != nil {
		t.Logf("Execute returned error with cancelled context: %v", err)
	}
	if !result.Success {
		t.Logf("Execute returned failure with cancelled context: %s", result.Error)
	}
}

func TestToolExecute_WithTimeout(t *testing.T) {
	tool := &mockTool{
		nameVal:       "slow_tool",
		descVal:       "A slow tool",
		executeResult: NewSuccessResult("done"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := tool.Execute(ctx, map[string]any{})
	if err != nil {
		t.Errorf("Execute() unexpected error with timeout: %v", err)
	}
}

// ── truncateToolResultContent ────────────────────────────────────────────────

// TestTruncateToolResultContent_PassThrough — content under the limit is
// returned verbatim with no truncation suffix.
func TestTruncateToolResultContent_PassThrough(t *testing.T) {
	content := "short content"
	got := truncateToolResultContent(content, "")
	if got != content {
		t.Errorf("short content was modified: %q", got)
	}
}

// TestTruncateToolResultContent_AppendsSuffix — content over the limit gets
// truncated and a notice appended; the visible character count must match.
func TestTruncateToolResultContent_AppendsSuffix(t *testing.T) {
	content := strings.Repeat("a", config.DefaultToolResultMaxChars+100)
	got := truncateToolResultContent(content, "(grep)")
	if !strings.Contains(got, "OUTPUT TRUNCATED") {
		t.Error("missing truncation notice")
	}
	if !strings.Contains(got, "(grep)") {
		t.Error("hint not propagated")
	}
}

// TestTruncateToolResultContent_MultibyteSafe — regression for v0.79.3:
// truncating CJK / Cyrillic / emoji content used to byte-slice mid-codepoint,
// producing invalid UTF-8 in the API payload and triggering replacement chars
// downstream. Now rune-aware.
func TestTruncateToolResultContent_MultibyteSafe(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"cyrillic", strings.Repeat("я", config.DefaultToolResultMaxChars+50)},
		{"cjk", strings.Repeat("世界", config.DefaultToolResultMaxChars+50)},
		{"emoji", strings.Repeat("🚀", config.DefaultToolResultMaxChars+50)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateToolResultContent(tc.in, "")
			if !utf8.ValidString(got) {
				t.Fatalf("invalid UTF-8 in truncated %s output", tc.name)
			}
			if strings.Contains(got, string(utf8.RuneError)) {
				t.Fatalf("replacement char in truncated %s output", tc.name)
			}
		})
	}
}
