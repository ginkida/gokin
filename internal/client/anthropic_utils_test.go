package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// This file tests pure utility functions in the client package that were at
// 0% coverage: error classification (isRetryableError, isEOFError,
// classifyGLMErrorCode), backoff calculation, thinking budget clamping, JSON
// schema conversion, tool-ID generation/sanitization, model registry lookups,
// typed timeout error constructors, and simple setters.

// ========== isRetryableError ==========

func TestIsRetryableError_HTTPStatusCodes(t *testing.T) {
	c := &AnthropicClient{}
	retryableCodes := []int{429, 500, 502, 503, 504, 529}
	for _, code := range retryableCodes {
		if !c.isRetryableError(nil, code) {
			t.Errorf("isRetryableError(nil, %d) = false, want true", code)
		}
	}

	nonRetryableCodes := []int{200, 400, 401, 403, 404}
	for _, code := range nonRetryableCodes {
		if c.isRetryableError(nil, code) {
			t.Errorf("isRetryableError(nil, %d) = true, want false", code)
		}
	}
}

func TestIsRetryableError_400ModelNotFound(t *testing.T) {
	c := &AnthropicClient{}
	// 400 with model_not_found → transient retry.
	err := errors.New("model_not_found: foo not available")
	if !c.isRetryableError(err, 400) {
		t.Error("400 with model_not_found should be retryable")
	}
	// 400 with other error → not retryable.
	err = errors.New("bad request")
	if c.isRetryableError(err, 400) {
		t.Error("400 with generic bad request should not be retryable")
	}
}

func TestIsRetryableError_NetworkErrors(t *testing.T) {
	c := &AnthropicClient{}
	networkErrs := []error{
		context.DeadlineExceeded,
		errors.New("connection refused"),
		errors.New("no such host"),
		errors.New("connection reset by peer"),
		errors.New("server overloaded"),
		errors.New("service temporarily unavailable"),
		errors.New("unexpected EOF"),
	}
	for _, err := range networkErrs {
		if !c.isRetryableError(err, 0) {
			t.Errorf("isRetryableError(%q, 0) = false, want true", err.Error())
		}
	}

	nonRetryable := []error{
		errors.New("invalid api key"),
		errors.New("permission denied"),
		nil,
	}
	for _, err := range nonRetryable {
		if c.isRetryableError(err, 0) {
			t.Errorf("isRetryableError(%v, 0) = true, want false", err)
		}
	}
}

// ========== isEOFError ==========

func TestIsEOFError(t *testing.T) {
	yesCases := []error{
		io.EOF,
		io.ErrUnexpectedEOF,
		errors.New("read tcp: unexpected EOF"),
		errors.New("connection closed with EOF"),
	}
	for _, err := range yesCases {
		if !isEOFError(err) {
			t.Errorf("isEOFError(%q) = false, want true", err.Error())
		}
	}

	noCases := []error{
		errors.New("connection refused"),
		errors.New("timeout"),
		errors.New("invalid api key"),
	}
	for _, err := range noCases {
		if isEOFError(err) {
			t.Errorf("isEOFError(%v) = true, want false", err)
		}
	}
}

// ========== classifyGLMErrorCode ==========

func TestClassifyGLMErrorCode(t *testing.T) {
	retryableCases := []struct {
		code     string
		keyword  string
		descPart string
	}{
		{"1210", "rate limit", "rate limit"},
		{"1301", "overloaded", "concurrency"},
		{"1302", "overloaded", "throughput"},
		{"1303", "overloaded", "throughput"},
		{"1305", "overloaded", "overloaded"},
	}
	for _, tc := range retryableCases {
		retryable, keyword, desc := classifyGLMErrorCode(tc.code, "")
		if !retryable {
			t.Errorf("code %q should be retryable", tc.code)
		}
		if keyword == "" {
			t.Errorf("code %q should have non-empty keyword", tc.code)
		}
		if !strings.Contains(strings.ToLower(desc), tc.descPart) {
			t.Errorf("code %q desc = %q, want substring %q", tc.code, desc, tc.descPart)
		}
	}

	nonRetryableCases := []struct {
		code     string
		descPart string
	}{
		{"1211", "balance"},
		{"1213", "balance"},
		{"1212", "quota"},
		{"1308", "quota"},
		{"1214", "authentication"},
		{"1215", "authentication"},
	}
	for _, tc := range nonRetryableCases {
		retryable, _, desc := classifyGLMErrorCode(tc.code, "")
		if retryable {
			t.Errorf("code %q should NOT be retryable", tc.code)
		}
		if !strings.Contains(strings.ToLower(desc), tc.descPart) {
			t.Errorf("code %q desc = %q, want substring %q", tc.code, desc, tc.descPart)
		}
	}

	// Unknown code with message → returns the message verbatim.
	retryable, _, desc := classifyGLMErrorCode("9999", "something went wrong")
	if retryable {
		t.Error("unknown code should not be retryable")
	}
	if desc != "something went wrong" {
		t.Errorf("unknown code desc = %q, want original message", desc)
	}

	// Unknown code without message → returns "GLM error <code>".
	_, _, desc = classifyGLMErrorCode("9999", "")
	if !strings.Contains(desc, "9999") {
		t.Errorf("unknown code without message should contain code: %q", desc)
	}
}

// ========== calculateBackoffWithJitter ==========

func TestCalculateBackoffWithJitter(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := 10 * time.Second

	// Attempt 0 → base delay.
	d0 := calculateBackoffWithJitter(base, 0, maxDelay)
	if d0 < base/2 || d0 > base*2 {
		t.Errorf("attempt 0 backoff = %v, want ~%v (with jitter)", d0, base)
	}

	// Higher attempts → larger backoff (exponential growth).
	d5 := calculateBackoffWithJitter(base, 5, maxDelay)
	if d5 <= base {
		t.Errorf("attempt 5 backoff = %v, should be > base %v", d5, base)
	}

	// Backoff is capped at maxDelay.
	dHigh := calculateBackoffWithJitter(base, 20, maxDelay)
	if dHigh > maxDelay*2 {
		t.Errorf("attempt 20 backoff = %v, should be capped near maxDelay %v", dHigh, maxDelay)
	}
}

// ========== clampThinkingBudgetBelowMax ==========

func TestClampThinkingBudgetBelowMax(t *testing.T) {
	// Budget >= max with sufficient max → clamped to max - 1024.
	got := clampThinkingBudgetBelowMax(8000, 4096)
	if got != 3072 {
		t.Errorf("clamp(8000, 4096) = %d, want 3072", got)
	}

	// Budget < max → unchanged.
	got = clampThinkingBudgetBelowMax(2000, 8000)
	if got != 2000 {
		t.Errorf("clamp(2000, 8000) = %d, want 2000", got)
	}

	// maxTokens <= 1024 → no clamping (can't subtract safely).
	got = clampThinkingBudgetBelowMax(500, 1024)
	if got != 500 {
		t.Errorf("clamp(500, 1024) = %d, want 500", got)
	}

	// Budget == max → clamped.
	got = clampThinkingBudgetBelowMax(4096, 4096)
	if got != 3072 {
		t.Errorf("clamp(4096, 4096) = %d, want 3072", got)
	}
}

// ========== convertSchemaToJSON ==========

func TestConvertSchemaToJSON_Nil(t *testing.T) {
	if got := convertSchemaToJSON(nil); got != nil {
		t.Errorf("convertSchemaToJSON(nil) = %v, want nil", got)
	}
}

func TestConvertSchemaToJSON_Simple(t *testing.T) {
	schema := &genai.Schema{
		Type:        "STRING",
		Description: "a file path",
	}
	got := convertSchemaToJSON(schema)
	if got["type"] != "string" {
		t.Errorf("type = %v, want \"string\"", got["type"])
	}
	if got["description"] != "a file path" {
		t.Errorf("description = %v, want \"a file path\"", got["description"])
	}
}

func TestConvertSchemaToJSON_WithEnum(t *testing.T) {
	schema := &genai.Schema{
		Type: "STRING",
		Enum: []string{"a", "b", "c"},
	}
	got := convertSchemaToJSON(schema)
	enum, ok := got["enum"].([]string)
	if !ok {
		t.Fatalf("enum is not []string: %T", got["enum"])
	}
	if len(enum) != 3 {
		t.Errorf("enum len = %d, want 3", len(enum))
	}
}

func TestConvertSchemaToJSON_WithProperties(t *testing.T) {
	schema := &genai.Schema{
		Type: "OBJECT",
		Properties: map[string]*genai.Schema{
			"name": {Type: "STRING", Description: "name field"},
			"age":  {Type: "INTEGER"},
		},
		Required: []string{"name"},
	}
	got := convertSchemaToJSON(schema)
	props, ok := got["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties is not map[string]any: %T", got["properties"])
	}
	if len(props) != 2 {
		t.Errorf("properties len = %d, want 2", len(props))
	}
	nameProp, ok := props["name"].(map[string]any)
	if !ok {
		t.Fatalf("name property is not map[string]any: %T", props["name"])
	}
	if nameProp["type"] != "string" {
		t.Errorf("name type = %v, want \"string\"", nameProp["type"])
	}
	req, ok := got["required"].([]string)
	if !ok {
		t.Fatalf("required is not []string: %T", got["required"])
	}
	if len(req) != 1 || req[0] != "name" {
		t.Errorf("required = %v, want [\"name\"]", req)
	}
}

func TestConvertSchemaToJSON_WithItems(t *testing.T) {
	schema := &genai.Schema{
		Type:  "ARRAY",
		Items: &genai.Schema{Type: "STRING"},
	}
	got := convertSchemaToJSON(schema)
	items, ok := got["items"].(map[string]any)
	if !ok {
		t.Fatalf("items is not map[string]any: %T", got["items"])
	}
	if items["type"] != "string" {
		t.Errorf("items type = %v, want \"string\"", items["type"])
	}
}

// ========== convertToolsToAnthropicFrom ==========

func TestConvertToolsToAnthropicFrom(t *testing.T) {
	c := &AnthropicClient{}
	tools := []*genai.Tool{
		{
			FunctionDeclarations: []*genai.FunctionDeclaration{
				{
					Name:        "read_file",
					Description: "Read a file",
					Parameters: &genai.Schema{
						Type: "OBJECT",
						Properties: map[string]*genai.Schema{
							"path": {Type: "STRING", Description: "file path"},
						},
						Required: []string{"path"},
					},
				},
			},
		},
	}
	got := c.convertToolsToAnthropicFrom(tools)
	if len(got) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(got))
	}
	if got[0]["name"] != "read_file" {
		t.Errorf("name = %v, want \"read_file\"", got[0]["name"])
	}
	if got[0]["description"] != "Read a file" {
		t.Errorf("description = %v, want \"Read a file\"", got[0]["description"])
	}
	inputSchema, ok := got[0]["input_schema"].(map[string]any)
	if !ok {
		t.Fatalf("input_schema is not map[string]any: %T", got[0]["input_schema"])
	}
	if inputSchema["type"] != "object" {
		t.Errorf("input_schema type = %v, want \"object\"", inputSchema["type"])
	}
}

func TestConvertToolsToAnthropicFrom_Empty(t *testing.T) {
	c := &AnthropicClient{}
	got := c.convertToolsToAnthropicFrom(nil)
	if len(got) != 0 {
		t.Errorf("nil tools should return empty slice, got %d", len(got))
	}
}

// ========== randomID ==========

func TestRandomID(t *testing.T) {
	id1 := randomID()
	id2 := randomID()
	if !strings.HasPrefix(id1, "toolu_") {
		t.Errorf("randomID = %q, want prefix \"toolu_\"", id1)
	}
	if id1 == id2 {
		t.Error("two randomID calls should produce different IDs")
	}
}

// ========== sanitizeToolIDComponent ==========

func TestSanitizeToolIDComponent(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"read_file", "read_file"},
		{"read-file", "read-file"},
		{"ReadFile123", "ReadFile123"},
		{"read file!", "read_file"},
		{"a@b#c$d", "a_b_c_d"},
		{"", "tool"},
		{"___", "tool"}, // all underscores → trimmed to empty → "tool"
		{"  spaces  ", "spaces"},
	}
	for _, tc := range cases {
		got := sanitizeToolIDComponent(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeToolIDComponent(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ========== fallbackToolID ==========

func TestFallbackToolID(t *testing.T) {
	got := fallbackToolID("read_file", 3)
	if !strings.Contains(got, "read_file") {
		t.Errorf("fallbackToolID should contain name: %q", got)
	}
	if !strings.Contains(got, "3") {
		t.Errorf("fallbackToolID should contain ordinal: %q", got)
	}
	if !strings.HasPrefix(got, "fallback_") {
		t.Errorf("fallbackToolID should have prefix: %q", got)
	}

	// Name with special chars gets sanitized.
	got = fallbackToolID("read file!", 0)
	if strings.Contains(got, "!") || strings.Contains(got, " ") {
		t.Errorf("fallbackToolID should sanitize name: %q", got)
	}
}

// ========== truncateString ==========

func TestTruncateString(t *testing.T) {
	// Short string → unchanged.
	got := truncateString("hello", 10)
	if got != "hello" {
		t.Errorf("truncateString(\"hello\", 10) = %q, want \"hello\"", got)
	}

	// Long string → truncated with ellipsis.
	got = truncateString("abcdefghij", 5)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("truncated string should end with \"...\": %q", got)
	}

	// Exact length → unchanged.
	got = truncateString("hello", 5)
	if got != "hello" {
		t.Errorf("exact-length string should be unchanged: %q", got)
	}

	// Unicode-safe: truncates by rune count.
	got = truncateString("世界你好世界", 3)
	if !strings.HasSuffix(got, "...") {
		t.Errorf("unicode truncated should end with \"...\": %q", got)
	}
}

// ========== GetModelsForProvider / IsValidModel / GetModelInfo ==========

func TestGetModelsForProvider(t *testing.T) {
	// Pick any provider that exists in AvailableModels.
	if len(AvailableModels) == 0 {
		t.Fatal("AvailableModels is empty")
	}
	provider := AvailableModels[0].Provider
	models := GetModelsForProvider(provider)
	if len(models) == 0 {
		t.Errorf("GetModelsForProvider(%q) returned empty", provider)
	}
	for _, m := range models {
		if m.Provider != provider {
			t.Errorf("model %q has provider %q, want %q", m.ID, m.Provider, provider)
		}
	}

	// Non-existent provider → empty.
	models = GetModelsForProvider("nonexistent_provider_xyz")
	if len(models) != 0 {
		t.Errorf("nonexistent provider should return empty, got %d", len(models))
	}
}

func TestIsValidModel(t *testing.T) {
	if len(AvailableModels) == 0 {
		t.Fatal("AvailableModels is empty")
	}
	validID := AvailableModels[0].ID
	if !IsValidModel(validID) {
		t.Errorf("IsValidModel(%q) = false, want true", validID)
	}
	if IsValidModel("nonexistent_model_xyz") {
		t.Error("IsValidModel(nonexistent) = true, want false")
	}
}

func TestGetModelInfo(t *testing.T) {
	if len(AvailableModels) == 0 {
		t.Fatal("AvailableModels is empty")
	}
	validID := AvailableModels[0].ID
	info, ok := GetModelInfo(validID)
	if !ok {
		t.Fatalf("GetModelInfo(%q) not found", validID)
	}
	if info.ID != validID {
		t.Errorf("info.ID = %q, want %q", info.ID, validID)
	}

	_, ok = GetModelInfo("nonexistent")
	if ok {
		t.Error("GetModelInfo(nonexistent) should return false")
	}
}

// ========== NewModelRoundTimeoutError ==========

func TestNewModelRoundTimeoutError(t *testing.T) {
	timeout := 14 * time.Minute
	err := NewModelRoundTimeoutError(timeout)
	if err == nil {
		t.Fatal("NewModelRoundTimeoutError returned nil")
	}
	if !errors.Is(err, ErrModelRoundTimeout) {
		t.Errorf("error should wrap ErrModelRoundTimeout: %v", err)
	}
	// DetectFailureTelemetry should classify it.
	tel := DetectFailureTelemetry(err)
	if tel.Reason != string(FailureReasonModelRoundTimeout) {
		t.Errorf("telemetry reason = %q, want %q", tel.Reason, FailureReasonModelRoundTimeout)
	}
}

// ========== WrapProviderHTTPTimeout ==========

func TestWrapProviderHTTPTimeout_NilError(t *testing.T) {
	if err := WrapProviderHTTPTimeout(nil, "glm", 30*time.Second); err != nil {
		t.Errorf("WrapProviderHTTPTimeout(nil, ...) = %v, want nil", err)
	}
}

func TestWrapProviderHTTPTimeout_NonTimeoutError(t *testing.T) {
	original := errors.New("some random error")
	wrapped := WrapProviderHTTPTimeout(original, "glm", 30*time.Second)
	if wrapped != original {
		t.Error("non-timeout error should be returned unchanged")
	}
}

func TestWrapProviderHTTPTimeout_DeadlineExceeded(t *testing.T) {
	original := context.DeadlineExceeded
	wrapped := WrapProviderHTTPTimeout(original, "glm", 30*time.Second)
	if wrapped == original {
		t.Error("DeadlineExceeded should be wrapped into TimeoutError")
	}
	tel := DetectFailureTelemetry(wrapped)
	if tel.Reason != string(FailureReasonHTTPTimeout) {
		t.Errorf("telemetry reason = %q, want %q", tel.Reason, FailureReasonHTTPTimeout)
	}
	if tel.Provider != "glm" {
		t.Errorf("telemetry provider = %q, want \"glm\"", tel.Provider)
	}
}

func TestWrapProviderHTTPTimeout_StringTimeout(t *testing.T) {
	original := errors.New("client.timeout exceeded awaiting response headers")
	wrapped := WrapProviderHTTPTimeout(original, "deepseek", 60*time.Second)
	if wrapped == original {
		t.Error("string timeout error should be wrapped")
	}
	if !IsHTTPTimeout(wrapped) {
		t.Error("wrapped error should be detected as HTTP timeout")
	}
}

func TestWrapProviderHTTPTimeout_NetTimeout(t *testing.T) {
	// Simulate a net.Error timeout without real network I/O.
	// We use a fake net.Error implementation to avoid network calls.
	timeoutErr := fakeNetTimeout{msg: "read tcp: i/o timeout"}
	wrapped := WrapProviderHTTPTimeout(timeoutErr, "kimi", 10*time.Second)
	tel := DetectFailureTelemetry(wrapped)
	if tel.Reason != string(FailureReasonHTTPTimeout) {
		t.Errorf("net timeout telemetry reason = %q, want %q", tel.Reason, FailureReasonHTTPTimeout)
	}
}

// fakeNetTimeout implements net.Error for testing timeout classification
// without making real network calls.
type fakeNetTimeout struct{ msg string }

func (e fakeNetTimeout) Error() string   { return e.msg }
func (e fakeNetTimeout) Timeout() bool   { return true }
func (e fakeNetTimeout) Temporary() bool { return true }

// ========== IsHTTPTimeout ==========

func TestIsHTTPTimeout(t *testing.T) {
	// Direct TimeoutError.
	timeoutErr := &TimeoutError{Reason: FailureReasonHTTPTimeout}
	if !IsHTTPTimeout(timeoutErr) {
		t.Error("TimeoutError with HTTPTimeout reason should be detected")
	}

	// Plain DeadlineExceeded.
	if !IsHTTPTimeout(context.DeadlineExceeded) {
		t.Error("DeadlineExceeded should be detected as HTTP timeout")
	}

	// Non-timeout error.
	if IsHTTPTimeout(errors.New("random error")) {
		t.Error("random error should not be detected as HTTP timeout")
	}

	// Nil.
	if IsHTTPTimeout(nil) {
		t.Error("nil should not be detected as HTTP timeout")
	}
}

// ========== AnthropicClient setters ==========

func TestAnthropicClient_SetSystemInstruction(t *testing.T) {
	c := &AnthropicClient{}
	c.SetSystemInstruction("you are a helpful assistant")
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.systemInstruction != "you are a helpful assistant" {
		t.Errorf("systemInstruction = %q", c.systemInstruction)
	}
}

func TestAnthropicClient_SetTurnContext(t *testing.T) {
	c := &AnthropicClient{}
	c.SetTurnContext("ephemeral context")
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.turnContext != "ephemeral context" {
		t.Errorf("turnContext = %q", c.turnContext)
	}
}

func TestAnthropicClient_SetThinkingBudget(t *testing.T) {
	c := &AnthropicClient{}
	c.SetThinkingBudget(4096)
	c.mu.RLock()
	if !c.config.EnableThinking {
		t.Error("EnableThinking should be true for budget > 0")
	}
	if c.config.ThinkingBudget != 4096 {
		t.Errorf("ThinkingBudget = %d, want 4096", c.config.ThinkingBudget)
	}
	c.mu.RUnlock()

	// Budget 0 → EnableThinking false.
	c.SetThinkingBudget(0)
	c.mu.RLock()
	if c.config.EnableThinking {
		t.Error("EnableThinking should be false for budget 0")
	}
	c.mu.RUnlock()
}

func TestAnthropicClient_SetTools(t *testing.T) {
	c := &AnthropicClient{}
	tools := []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "test_tool"}}},
	}
	c.SetTools(tools)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.tools) != 1 {
		t.Errorf("tools len = %d, want 1", len(c.tools))
	}
}

func TestAnthropicClient_SetRateLimiter(t *testing.T) {
	c := &AnthropicClient{}
	// Non-matching type → ignored.
	c.SetRateLimiter("not a rate limiter")
	c.mu.RLock()
	if c.rateLimiter != nil {
		t.Error("non-matching type should be ignored")
	}
	c.mu.RUnlock()
}

func TestAnthropicClient_SetStatusCallback(t *testing.T) {
	c := &AnthropicClient{}
	var cb StatusCallback = &DefaultStatusCallback{}
	c.SetStatusCallback(cb)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.statusCallback == nil {
		t.Error("statusCallback should be set")
	}
}

// ========== GetModel / SetModel ==========

func TestAnthropicClient_GetSetModel(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{Model: "original-model"}}
	if got := c.GetModel(); got != "original-model" {
		t.Errorf("GetModel() = %q, want \"original-model\"", got)
	}
	c.SetModel("new-model")
	if got := c.GetModel(); got != "new-model" {
		t.Errorf("GetModel() after SetModel = %q, want \"new-model\"", got)
	}
}

// ========== GetRawClient ==========

func TestAnthropicClient_GetRawClient(t *testing.T) {
	c := &AnthropicClient{httpClient: &http.Client{}}
	rc := c.GetRawClient()
	if rc == nil {
		t.Error("GetRawClient should return the http client")
	}
}

// ========== ContextErr ==========

func TestContextErr_WithCause(t *testing.T) {
	cause := errors.New("custom cancellation cause")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(cause)
	if err := ContextErr(ctx); !errors.Is(err, cause) {
		t.Errorf("ContextErr should return cause: %v", err)
	}
}

func TestContextErr_NoCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ContextErr(ctx); err != context.Canceled {
		t.Errorf("ContextErr without cause should return ctx.Err(): %v", err)
	}
}
