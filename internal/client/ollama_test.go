package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
	"google.golang.org/genai"
)

// ========== NewOllamaClient ==========

func TestNewOllamaClient_EmptyModel(t *testing.T) {
	_, err := NewOllamaClient(OllamaConfig{Model: ""})
	if err == nil {
		t.Fatal("expected error for empty model")
	}
	if !strings.Contains(err.Error(), "model name is required") {
		t.Errorf("error = %q, want 'model name is required'", err.Error())
	}
}

func TestNewOllamaClient_NegativeRetries(t *testing.T) {
	_, err := NewOllamaClient(OllamaConfig{Model: "llama3.2", MaxRetries: -1})
	if err == nil {
		t.Fatal("expected error for negative MaxRetries")
	}
	if !strings.Contains(err.Error(), "MaxRetries cannot be negative") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestNewOllamaClient_InvalidURL(t *testing.T) {
	_, err := NewOllamaClient(OllamaConfig{Model: "llama3.2", BaseURL: "://bad-url"})
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func TestNewOllamaClient_Defaults(t *testing.T) {
	c, err := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	if c.config.BaseURL == "" {
		t.Error("BaseURL should be set to default")
	}
	if c.config.MaxTokens != 8192 {
		t.Errorf("MaxTokens = %d, want 8192", c.config.MaxTokens)
	}
	if c.config.HTTPTimeout == 0 {
		t.Error("HTTPTimeout should be set to default")
	}
	if c.config.RetryDelay == 0 {
		t.Error("RetryDelay should be set to default")
	}
}

func TestNewOllamaClient_WithAPIKey(t *testing.T) {
	c, err := NewOllamaClient(OllamaConfig{Model: "llama3.2", APIKey: "test-key"})
	if err != nil {
		t.Fatalf("NewOllamaClient: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}

// ========== Setters ==========

func TestOllamaClient_SetSystemInstruction(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	c.SetSystemInstruction("you are a coding assistant")
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.systemInstruction != "you are a coding assistant" {
		t.Errorf("systemInstruction = %q", c.systemInstruction)
	}
}

func TestOllamaClient_SetTurnContext(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	c.SetTurnContext("working dir: /tmp")
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.turnContext != "working dir: /tmp" {
		t.Errorf("turnContext = %q", c.turnContext)
	}
}

func TestOllamaClient_SetThinkingBudget(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	// No-op for Ollama, should not panic.
	c.SetThinkingBudget(4096)
}

func TestOllamaClient_SetTools(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	tools := []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "bash"}}},
	}
	c.SetTools(tools)
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.tools) != 1 {
		t.Errorf("tools len = %d, want 1", len(c.tools))
	}
}

func TestOllamaClient_SetRateLimiter(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	// Non-matching type → ignored.
	c.SetRateLimiter("not a rate limiter")
	c.mu.RLock()
	if c.rateLimiter != nil {
		t.Error("non-matching type should be ignored")
	}
	c.mu.RUnlock()
}

func TestOllamaClient_SetStatusCallback(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	c.SetStatusCallback(&DefaultStatusCallback{})
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.statusCallback == nil {
		t.Error("statusCallback should be set")
	}
}

// ========== GetModel / SetModel ==========

func TestOllamaClient_GetSetModel(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	if got := c.GetModel(); got != "llama3.2" {
		t.Errorf("GetModel() = %q, want \"llama3.2\"", got)
	}
	c.SetModel("qwen2.5")
	if got := c.GetModel(); got != "qwen2.5" {
		t.Errorf("GetModel() after SetModel = %q, want \"qwen2.5\"", got)
	}
}

// ========== GetRawClient ==========

func TestOllamaClient_GetRawClient(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	rc := c.GetRawClient()
	if rc == nil {
		t.Error("GetRawClient should return the ollama client")
	}
}

// ========== NeedsToolCallFallback ==========

func TestOllamaClient_NeedsToolCallFallback(t *testing.T) {
	// Model with SupportsTools=true → false
	c1, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	if c1.NeedsToolCallFallback() {
		t.Error("llama3.2 should not need fallback (supports tools)")
	}

	// Model with SupportsTools=false → true
	c2, _ := NewOllamaClient(OllamaConfig{Model: "llama2"})
	if !c2.NeedsToolCallFallback() {
		t.Error("llama2 should need fallback (no native tools)")
	}
}

// ========== Close ==========

func TestOllamaClient_Close(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	if err := c.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

// ========== CountTokens ==========

func TestOllamaClient_CountTokens(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	contents := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello world"}}},
	}
	resp, err := c.CountTokens(context.Background(), contents)
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if resp.TotalTokens <= 0 {
		t.Errorf("TotalTokens = %d, want > 0", resp.TotalTokens)
	}
}

func TestOllamaClient_CountTokens_WithFunctionCall(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	contents := []*genai.Content{
		{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				Name: "bash",
				Args: map[string]any{"command": "ls"},
			},
		}}},
	}
	resp, err := c.CountTokens(context.Background(), contents)
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if resp.TotalTokens <= 0 {
		t.Errorf("TotalTokens = %d, want > 0", resp.TotalTokens)
	}
}

func TestOllamaClient_CountTokens_WithFunctionResponse(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	contents := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				Name:     "bash",
				Response: map[string]any{"content": "file.txt"},
			},
		}}},
	}
	resp, err := c.CountTokens(context.Background(), contents)
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if resp.TotalTokens <= 0 {
		t.Errorf("TotalTokens = %d, want > 0", resp.TotalTokens)
	}
}

// ========== appendOllamaTurnContext ==========

func TestAppendOllamaTurnContext_EmptyContext(t *testing.T) {
	msgs := []api.Message{{Role: "user", Content: "hello"}}
	result := appendOllamaTurnContext(msgs, "")
	if len(result) != 1 || result[0].Content != "hello" {
		t.Error("empty context should return messages unchanged")
	}
}

func TestAppendOllamaTurnContext_EmptyMessages(t *testing.T) {
	msgs := []api.Message{}
	result := appendOllamaTurnContext(msgs, "context")
	if len(result) != 0 {
		t.Error("empty messages should return empty")
	}
}

func TestAppendOllamaTurnContext_NonUserRole(t *testing.T) {
	msgs := []api.Message{{Role: "assistant", Content: "hi"}}
	result := appendOllamaTurnContext(msgs, "context")
	if result[0].Content != "hi" {
		t.Error("non-user role should not be modified")
	}
}

func TestAppendOllamaTurnContext_AppendsToUserMessage(t *testing.T) {
	msgs := []api.Message{{Role: "user", Content: "hello"}}
	result := appendOllamaTurnContext(msgs, "task state")
	if !strings.Contains(result[0].Content, "hello") {
		t.Error("original content should be preserved")
	}
	if !strings.Contains(result[0].Content, "task state") {
		t.Error("turn context should be appended")
	}
	if !strings.Contains(result[0].Content, "<turn-context>") {
		t.Error("should contain turn-context tag")
	}
}

// ========== convertContentToMessage ==========

func TestConvertContentToMessage_UserText(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	content := &genai.Content{
		Role:  genai.RoleUser,
		Parts: []*genai.Part{{Text: "hello"}},
	}
	msg := c.convertContentToMessage(content)
	if msg.Role != "user" {
		t.Errorf("Role = %q, want \"user\"", msg.Role)
	}
	if msg.Content != "hello" {
		t.Errorf("Content = %q, want \"hello\"", msg.Content)
	}
}

func TestConvertContentToMessage_AssistantText(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	content := &genai.Content{
		Role:  genai.RoleModel,
		Parts: []*genai.Part{{Text: "world"}},
	}
	msg := c.convertContentToMessage(content)
	if msg.Role != "assistant" {
		t.Errorf("Role = %q, want \"assistant\"", msg.Role)
	}
}

func TestConvertContentToMessage_MultipleTextParts(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	content := &genai.Content{
		Role: genai.RoleUser,
		Parts: []*genai.Part{
			{Text: "line1"},
			{Text: "line2"},
		},
	}
	msg := c.convertContentToMessage(content)
	if !strings.Contains(msg.Content, "line1") || !strings.Contains(msg.Content, "line2") {
		t.Errorf("Content = %q, should contain both lines", msg.Content)
	}
}

func TestConvertContentToMessage_WithFunctionCall(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	content := &genai.Content{
		Role: genai.RoleModel,
		Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				ID:   "call_1",
				Name: "bash",
				Args: map[string]any{"command": "ls"},
			},
		}},
	}
	msg := c.convertContentToMessage(content)
	if msg.Role != "assistant" {
		t.Errorf("Role = %q, want \"assistant\"", msg.Role)
	}
	if len(msg.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(msg.ToolCalls))
	}
	if msg.ToolCalls[0].Function.Name != "bash" {
		t.Errorf("tool call name = %q, want \"bash\"", msg.ToolCalls[0].Function.Name)
	}
}

// ========== convertHistoryToMessages ==========

func TestConvertHistoryToMessages_BasicFlow(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	c.SetSystemInstruction("system prompt")

	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "hi there"}}},
	}

	msgs := c.convertHistoryToMessages(history, "next question")
	// Expect: system + 2 history + 1 new = 4
	if len(msgs) != 4 {
		t.Fatalf("messages len = %d, want 4", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want \"system\"", msgs[0].Role)
	}
	if msgs[3].Role != "user" {
		t.Errorf("last message role = %q, want \"user\"", msgs[3].Role)
	}
	if msgs[3].Content != "next question" {
		t.Errorf("last message content = %q, want \"next question\"", msgs[3].Content)
	}
}

func TestConvertHistoryToMessages_NoSystemInstruction(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}},
	}
	msgs := c.convertHistoryToMessages(history, "")
	// No system, 1 history, no new message = 1
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
}

// ========== convertToolsToOllamaFrom ==========

func TestConvertToolsToOllamaFrom_Basic(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	tools := []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name:        "bash",
			Description: "run a command",
		}}},
	}
	result := c.convertToolsToOllamaFrom(tools)
	if len(result) != 1 {
		t.Fatalf("tools len = %d, want 1", len(result))
	}
	if result[0].Function.Name != "bash" {
		t.Errorf("name = %q, want \"bash\"", result[0].Function.Name)
	}
}

func TestConvertToolsToOllamaFrom_WithParameters(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	strType := genai.TypeString
	tools := []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "edit",
			Parameters: &genai.Schema{
				Required: []string{"file_path"},
				Properties: map[string]*genai.Schema{
					"file_path": {Type: strType, Description: "path to file"},
				},
			},
		}}},
	}
	result := c.convertToolsToOllamaFrom(tools)
	if len(result) != 1 {
		t.Fatalf("tools len = %d, want 1", len(result))
	}
	if len(result[0].Function.Parameters.Required) != 1 {
		t.Errorf("required len = %d, want 1", len(result[0].Function.Parameters.Required))
	}
}

func TestConvertToolsToOllamaFrom_WithEnum(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	strType := genai.TypeString
	tools := []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name: "set_mode",
			Parameters: &genai.Schema{
				Properties: map[string]*genai.Schema{
					"mode": {Type: strType, Enum: []string{"auto", "manual"}},
				},
			},
		}}},
	}
	result := c.convertToolsToOllamaFrom(tools)
	if len(result) != 1 {
		t.Fatalf("tools len = %d, want 1", len(result))
	}
}

func TestConvertToolsToOllamaFrom_Empty(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	result := c.convertToolsToOllamaFrom(nil)
	if len(result) != 0 {
		t.Errorf("tools len = %d, want 0", len(result))
	}
}

// ========== convertGenaiToolCallToOllama ==========

func TestConvertGenaiToolCallToOllama(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	fc := &genai.FunctionCall{
		ID:   "call_abc",
		Name: "bash",
		Args: map[string]any{"command": "ls -la"},
	}
	tc := c.convertGenaiToolCallToOllama(fc)
	if tc.ID != "call_abc" {
		t.Errorf("ID = %q, want \"call_abc\"", tc.ID)
	}
	if tc.Function.Name != "bash" {
		t.Errorf("name = %q, want \"bash\"", tc.Function.Name)
	}
}

// ========== convertOllamaToolCallToGenai ==========

func TestConvertOllamaToolCallToGenai_WithID(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	args := api.NewToolCallFunctionArguments()
	args.Set("command", "ls")
	tc := api.ToolCall{
		ID:       "call_xyz",
		Function: api.ToolCallFunction{Name: "bash", Arguments: args},
	}
	fc := c.convertOllamaToolCallToGenai(tc, 0)
	if fc.ID != "call_xyz" {
		t.Errorf("ID = %q, want \"call_xyz\"", fc.ID)
	}
	if fc.Name != "bash" {
		t.Errorf("Name = %q, want \"bash\"", fc.Name)
	}
}

func TestConvertOllamaToolCallToGenai_WithoutID(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	args := api.NewToolCallFunctionArguments()
	tc := api.ToolCall{
		Function: api.ToolCallFunction{Name: "edit", Arguments: args},
	}
	fc := c.convertOllamaToolCallToGenai(tc, 5)
	if fc.ID != "call_5" {
		t.Errorf("ID = %q, want \"call_5\"", fc.ID)
	}
}

// ========== isRetryableError ==========

func TestOllamaIsRetryableError_Nil(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	if c.isRetryableError(nil) {
		t.Error("nil error should not be retryable")
	}
}

func TestOllamaIsRetryableError_ConnectionRefused(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := errors.New("dial tcp: connection refused")
	if !c.isRetryableError(err) {
		t.Error("connection refused should be retryable")
	}
}

func TestOllamaIsRetryableError_Timeout(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := errors.New("request timeout")
	if !c.isRetryableError(err) {
		t.Error("timeout should be retryable")
	}
}

func TestOllamaIsRetryableError_EOF(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := errors.New("unexpected EOF")
	if !c.isRetryableError(err) {
		t.Error("EOF should be retryable")
	}
}

func TestOllamaIsRetryableError_NoSuchHost(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := errors.New("dial tcp: no such host")
	if !c.isRetryableError(err) {
		t.Error("no such host should be retryable")
	}
}

func TestOllamaIsRetryableError_NonRetryable(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := errors.New("invalid model name")
	if c.isRetryableError(err) {
		t.Error("generic error should not be retryable")
	}
}

func TestOllamaIsRetryableError_StatusError429(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := &api.StatusError{Status: "429 Too Many Requests", StatusCode: 429}
	if !c.isRetryableError(err) {
		t.Error("429 should be retryable")
	}
}

func TestOllamaIsRetryableError_StatusError500(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := &api.StatusError{Status: "500", StatusCode: 500}
	if !c.isRetryableError(err) {
		t.Error("500 should be retryable")
	}
}

func TestOllamaIsRetryableError_StatusError400(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := &api.StatusError{Status: "400", StatusCode: 400}
	if c.isRetryableError(err) {
		t.Error("400 should not be retryable")
	}
}

// The Ollama SDK returns api.StatusError as a value in its production request
// path, although wrapped callers and older tests commonly use a pointer. Keep
// both forms typed all the way into the shared fallback/health policy: 400/422
// describe this request and must stop the chain without poisoning provider
// health, while auth/rate-limit/server failures remain provider-local.
func TestOllamaStatusErrorFallbackAndHealthTaxonomy(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	tests := []struct {
		name          string
		provider      string
		raw           error
		wantStatus    int
		wantRetryable bool
		wantFallback  bool
	}{
		{
			name:       "value 400 with retry wording",
			provider:   "test-ollama-status-value-400",
			raw:        api.StatusError{StatusCode: 400, Status: "400 Bad Request", ErrorMessage: "invalid request: unexpected EOF"},
			wantStatus: 400,
		},
		{
			name:       "pointer 400 with timeout wording",
			provider:   "test-ollama-status-pointer-400",
			raw:        &api.StatusError{StatusCode: 400, Status: "400 Bad Request", ErrorMessage: "invalid request timeout"},
			wantStatus: 400,
		},
		{
			name:       "value 422 safety rejection",
			provider:   "test-ollama-status-value-422",
			raw:        api.StatusError{StatusCode: 422, Status: "422 Unprocessable Entity", ErrorMessage: "safety policy rejected"},
			wantStatus: 422,
		},
		{
			name:       "pointer 422 invalid schema",
			provider:   "test-ollama-status-pointer-422",
			raw:        &api.StatusError{StatusCode: 422, Status: "422 Unprocessable Entity", ErrorMessage: "invalid tool schema"},
			wantStatus: 422,
		},
		{
			name:         "value 401 is provider local",
			provider:     "test-ollama-status-value-401",
			raw:          api.StatusError{StatusCode: 401, Status: "401 Unauthorized", ErrorMessage: "bad provider key"},
			wantStatus:   401,
			wantFallback: true,
		},
		{
			name:          "value 429 is transient provider local",
			provider:      "test-ollama-status-value-429",
			raw:           api.StatusError{StatusCode: 429, Status: "429 Too Many Requests", ErrorMessage: "rate limited"},
			wantStatus:    429,
			wantRetryable: true,
			wantFallback:  true,
		},
		{
			name:          "pointer 500 is transient provider local",
			provider:      "test-ollama-status-pointer-500",
			raw:           &api.StatusError{StatusCode: 500, Status: "500 Internal Server Error", ErrorMessage: "backend unavailable"},
			wantStatus:    500,
			wantRetryable: true,
			wantFallback:  true,
		},
		{
			name:         "value authorization error is provider local",
			provider:     "test-ollama-auth-value-401",
			raw:          api.AuthorizationError{StatusCode: 401, Status: "401 Unauthorized"},
			wantStatus:   401,
			wantFallback: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.isRetryableError(tt.raw); got != tt.wantRetryable {
				t.Fatalf("isRetryableError(%T) = %v, want %v", tt.raw, got, tt.wantRetryable)
			}

			normalized := c.wrapOllamaError(tt.raw)
			var httpErr *HTTPError
			if !errors.As(normalized, &httpErr) || httpErr == nil {
				t.Fatalf("wrapOllamaError(%T) = %T, want *HTTPError", tt.raw, normalized)
			}
			if httpErr.StatusCode != tt.wantStatus {
				t.Fatalf("normalized status = %d, want %d", httpErr.StatusCode, tt.wantStatus)
			}
			if !errors.Is(normalized, tt.raw) {
				t.Fatalf("normalized error no longer unwraps to original %T", tt.raw)
			}
			if got := shouldFallbackToNextProvider(normalized); got != tt.wantFallback {
				t.Fatalf("shouldFallbackToNextProvider(%T status %d) = %v, want %v",
					tt.raw, tt.wantStatus, got, tt.wantFallback)
			}

			fc, err := NewFallbackClient(
				[]Client{&fakeFallbackClientStub{id: "ollama"}}, []string{tt.provider})
			if err != nil {
				t.Fatalf("NewFallbackClient: %v", err)
			}
			before := getProviderHealth(tt.provider)
			fc.recordFailure("Ollama status taxonomy test", 0, normalized)
			after := getProviderHealth(tt.provider)
			changed := after.Score < before.Score && after.FailureStreak == before.FailureStreak+1
			if changed != tt.wantFallback {
				t.Fatalf("health change = %v, want %v; before=%+v after=%+v",
					changed, tt.wantFallback, before, after)
			}
			if !tt.wantFallback && (after.Score != before.Score || after.FailureStreak != before.FailureStreak) {
				t.Fatalf("request-scoped failure changed health: before=%+v after=%+v", before, after)
			}
		})
	}
}

// ========== IsModelNotFoundError ==========

func TestIsModelNotFoundError_Nil(t *testing.T) {
	if IsModelNotFoundError(nil) {
		t.Error("nil should not be model not found")
	}
}

func TestIsModelNotFoundError_NotInstalled(t *testing.T) {
	err := errors.New("model llama3.2 is not installed")
	if !IsModelNotFoundError(err) {
		t.Error("'is not installed' should be model not found")
	}
}

func TestIsModelNotFoundError_NotFoundInMessage(t *testing.T) {
	err := errors.New("model not found in registry")
	if !IsModelNotFoundError(err) {
		t.Error("'model not found' should be model not found")
	}
}

func TestIsModelNotFoundError_GenericError(t *testing.T) {
	err := errors.New("some other error")
	if IsModelNotFoundError(err) {
		t.Error("generic error should not be model not found")
	}
}

func TestIsModelNotFoundError_StatusError404(t *testing.T) {
	err := &api.StatusError{Status: "404", StatusCode: 404}
	if !IsModelNotFoundError(err) {
		t.Error("404 status should be model not found")
	}
}

func TestIsModelNotFoundError_StatusError500(t *testing.T) {
	err := &api.StatusError{Status: "500", StatusCode: 500}
	if IsModelNotFoundError(err) {
		t.Error("500 status should not be model not found")
	}
}

// ========== wrapOllamaError ==========

func TestWrapOllamaError_Nil(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	if err := c.wrapOllamaError(nil); err != nil {
		t.Errorf("nil should return nil, got %v", err)
	}
}

func TestWrapOllamaError_ConnectionRefused(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := c.wrapOllamaError(errors.New("connection refused"))
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("should mention 'not running', got %q", err.Error())
	}
}

func TestWrapOllamaError_Timeout(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := c.wrapOllamaError(errors.New("request timeout"))
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("should mention 'timed out', got %q", err.Error())
	}
}

func TestWrapOllamaError_DeadlineExceeded(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := c.wrapOllamaError(errors.New("context deadline exceeded"))
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("should mention 'timed out', got %q", err.Error())
	}
}

func TestWrapOllamaError_Status404(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	origErr := &api.StatusError{Status: "404", StatusCode: 404}
	err := c.wrapOllamaError(origErr)
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("should mention 'not installed', got %q", err.Error())
	}
}

func TestWrapOllamaError_ModelNotFoundText(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	err := c.wrapOllamaError(errors.New("model xyz not found"))
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("should mention 'not installed', got %q", err.Error())
	}
}

func TestWrapOllamaError_Generic(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	orig := errors.New("some random error")
	err := c.wrapOllamaError(orig)
	if err.Error() != "some random error" {
		t.Errorf("generic error should pass through, got %q", err.Error())
	}
}

// ========== convertHistoryForFallback ==========

func TestConvertHistoryForFallback_Basic(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "deepseek-r1"})
	c.SetSystemInstruction("system")

	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}},
		{Role: genai.RoleModel, Parts: []*genai.Part{{
			FunctionCall: &genai.FunctionCall{
				Name: "bash",
				Args: map[string]any{"command": "ls"},
			},
		}}},
	}
	results := []*genai.FunctionResponse{
		{Name: "bash", Response: map[string]any{"content": "output"}},
	}

	msgs := c.convertHistoryForFallback(history, results)
	// system + user + assistant(with tool call as text) + tool result
	if len(msgs) < 3 {
		t.Fatalf("messages len = %d, want >= 3", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("first role = %q, want \"system\"", msgs[0].Role)
	}
}

func TestConvertHistoryForFallback_FunctionResponseInHistory(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "deepseek-r1"})
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				Name:     "bash",
				Response: map[string]any{"content": "result text"},
			},
		}}},
	}
	msgs := c.convertHistoryForFallback(history, nil)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "result text") {
		t.Errorf("should contain response content, got %q", msgs[0].Content)
	}
}

func TestConvertHistoryForFallback_FunctionResponseJSON(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "deepseek-r1"})
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				Name:     "read",
				Response: map[string]any{"data": map[string]any{"lines": 42}},
			},
		}}},
	}
	msgs := c.convertHistoryForFallback(history, nil)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
}

func TestConvertHistoryForFallback_FunctionResponseError(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "deepseek-r1"})
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{
			FunctionResponse: &genai.FunctionResponse{
				Name:     "bash",
				Response: map[string]any{"error": "command failed"},
			},
		}}},
	}
	msgs := c.convertHistoryForFallback(history, nil)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "command failed") {
		t.Errorf("should contain error text, got %q", msgs[0].Content)
	}
}

func TestConvertHistoryForFallback_ResultWithContent(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "deepseek-r1"})
	results := []*genai.FunctionResponse{
		{Name: "bash", Response: map[string]any{"content": "file1.txt\nfile2.txt"}},
	}
	msgs := c.convertHistoryForFallback(nil, results)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "file1.txt") {
		t.Errorf("should contain result content, got %q", msgs[0].Content)
	}
}

func TestConvertHistoryForFallback_ResultEmpty(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "deepseek-r1"})
	results := []*genai.FunctionResponse{
		{Name: "noop", Response: nil},
	}
	msgs := c.convertHistoryForFallback(nil, results)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Operation completed") {
		t.Errorf("should contain default text, got %q", msgs[0].Content)
	}
}

// ========== convertHistoryWithResults ==========

func TestConvertHistoryWithResults_Basic(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	history := []*genai.Content{
		{Role: genai.RoleUser, Parts: []*genai.Part{{Text: "hello"}}},
	}
	results := []*genai.FunctionResponse{
		{Name: "bash", Response: map[string]any{"content": "output"}},
	}
	msgs := c.convertHistoryWithResults(history, results)
	// 1 history + 1 result = 2
	if len(msgs) != 2 {
		t.Fatalf("messages len = %d, want 2", len(msgs))
	}
}

func TestConvertHistoryWithResults_ResultEmpty(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	results := []*genai.FunctionResponse{
		{Name: "noop", Response: nil},
	}
	msgs := c.convertHistoryWithResults(nil, results)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Operation completed") {
		t.Errorf("should contain default text, got %q", msgs[0].Content)
	}
}

func TestConvertHistoryWithResults_ResultWithData(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	results := []*genai.FunctionResponse{
		{Name: "read", Response: map[string]any{"data": map[string]any{"n": 1}}},
	}
	msgs := c.convertHistoryWithResults(nil, results)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
}

func TestConvertHistoryWithResults_ResultWithError(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	results := []*genai.FunctionResponse{
		{Name: "bash", Response: map[string]any{"error": "failed"}},
	}
	msgs := c.convertHistoryWithResults(nil, results)
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want 1", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Error:") {
		t.Errorf("should contain error, got %q", msgs[0].Content)
	}
}

// ========== WithModel ==========

func TestOllamaClient_WithModel(t *testing.T) {
	c, _ := NewOllamaClient(OllamaConfig{Model: "llama3.2"})
	c.SetSystemInstruction("sys")
	c.SetTurnContext("ctx")

	newClient := c.WithModel("qwen2.5")
	if newClient == nil {
		t.Fatal("WithModel returned nil")
	}
	if got := newClient.GetModel(); got != "qwen2.5" {
		t.Errorf("GetModel() = %q, want \"qwen2.5\"", got)
	}
	// Original should be unchanged.
	if got := c.GetModel(); got != "llama3.2" {
		t.Errorf("original GetModel() = %q, want \"llama3.2\"", got)
	}
}
