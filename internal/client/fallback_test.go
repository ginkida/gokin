package client

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"google.golang.org/genai"
)

// fakeFallbackClientStub is a minimal Client for fallback tests.
// It supports configurable SendMessage/SendMessageWithHistory/SendFunctionResponse
// behavior to exercise the failover chain.
type fakeFallbackClientStub struct {
	id          string
	sendErr     error
	sendResp    *StreamingResponse
	closed      bool
	sysInstr    string
	turnCtx     string
	model       string
	rateLimiter any
	tools       []*genai.Tool
	thinkBudget int32
	statusCB    StatusCallback
	sendCalls   int
	sendFunc    func(context.Context) (*StreamingResponse, error)
}

func (f *fakeFallbackClientStub) Close() error                  { f.closed = true; return nil }
func (f *fakeFallbackClientStub) SetTools(t []*genai.Tool)      { f.tools = t }
func (f *fakeFallbackClientStub) SetSystemInstruction(s string) { f.sysInstr = s }
func (f *fakeFallbackClientStub) SetTurnContext(s string)       { f.turnCtx = s }
func (f *fakeFallbackClientStub) SetThinkingBudget(b int32)     { f.thinkBudget = b }
func (f *fakeFallbackClientStub) SetRateLimiter(r any)          { f.rateLimiter = r }
func (f *fakeFallbackClientStub) GetModel() string              { return f.model }
func (f *fakeFallbackClientStub) SetModel(m string)             { f.model = m }
func (f *fakeFallbackClientStub) WithModel(m string) Client {
	clone := *f
	clone.model = m
	clone.sendCalls = 0
	return &clone
}
func (f *fakeFallbackClientStub) GetRawClient() any { return nil }
func (f *fakeFallbackClientStub) send(ctx context.Context) (*StreamingResponse, error) {
	f.sendCalls++
	if f.sendFunc != nil {
		return f.sendFunc(ctx)
	}
	return f.sendResp, f.sendErr
}
func (f *fakeFallbackClientStub) SendMessage(ctx context.Context, _ string) (*StreamingResponse, error) {
	return f.send(ctx)
}
func (f *fakeFallbackClientStub) SendMessageWithHistory(ctx context.Context, _ []*genai.Content, _ string) (*StreamingResponse, error) {
	return f.send(ctx)
}
func (f *fakeFallbackClientStub) SendFunctionResponse(ctx context.Context, _ []*genai.Content, _ []*genai.FunctionResponse) (*StreamingResponse, error) {
	return f.send(ctx)
}
func (f *fakeFallbackClientStub) CountTokens(_ context.Context, _ []*genai.Content) (*genai.CountTokensResponse, error) {
	return &genai.CountTokensResponse{TotalTokens: 42}, nil
}
func (f *fakeFallbackClientStub) SetStatusCallback(cb StatusCallback) { f.statusCB = cb }

// ========== NewFallbackClient ==========

func TestNewFallbackClient_EmptyClients(t *testing.T) {
	_, err := NewFallbackClient(nil, nil)
	if err == nil {
		t.Fatal("expected error for empty clients")
	}
}

func TestNewFallbackClient_RejectsNilClient(t *testing.T) {
	valid := &fakeFallbackClientStub{id: "valid"}
	_, err := NewFallbackClient([]Client{valid, nil}, []string{"glm", "kimi"})
	if err == nil {
		t.Fatal("expected error for nil fallback child")
	}
}

func TestNewFallbackClient_RejectsTypedNilClient(t *testing.T) {
	var typedNil *fakeFallbackClientStub
	_, err := NewFallbackClient([]Client{typedNil}, []string{"glm"})
	if err == nil {
		t.Fatal("expected error for typed-nil fallback child")
	}
}

func TestNewFallbackClient_CopiesOwnedClientSlice(t *testing.T) {
	first := &fakeFallbackClientStub{id: "first"}
	second := &fakeFallbackClientStub{id: "second"}
	replacement := &fakeFallbackClientStub{id: "replacement"}
	input := []Client{first, second}

	fc, err := NewFallbackClient(input, []string{"glm", "kimi"})
	if err != nil {
		t.Fatalf("NewFallbackClient: %v", err)
	}
	input[0] = replacement
	input[1] = replacement

	if err := fc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !first.closed || !second.closed {
		t.Fatalf("owned children were not closed: first=%v second=%v", first.closed, second.closed)
	}
	if replacement.closed {
		t.Error("mutating the caller's slice changed fallback ownership")
	}
}

func TestNewFallbackClient_ProviderCountMismatch(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", model: "m1"}
	// providers slice shorter than clients → should pad with empty strings
	fc, err := NewFallbackClient([]Client{c1}, nil)
	if err != nil {
		t.Fatalf("NewFallbackClient: %v", err)
	}
	if len(fc.providers) != 1 {
		t.Errorf("providers len = %d, want 1 (padded)", len(fc.providers))
	}
}

func TestNewFallbackClient_Success(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", model: "m1"}
	c2 := &fakeFallbackClientStub{id: "b", model: "m2"}
	fc, err := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})
	if err != nil {
		t.Fatalf("NewFallbackClient: %v", err)
	}
	if len(fc.clients) != 2 {
		t.Errorf("clients len = %d, want 2", len(fc.clients))
	}
	if fc.current != 0 {
		t.Errorf("current = %d, want 0", fc.current)
	}
}

func TestNewFallbackClient_NormalizesProviderNames(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	c2 := &fakeFallbackClientStub{id: "b"}
	fc, err := NewFallbackClient([]Client{c1, c2}, []string{" GLM ", "KIMI"})
	if err != nil {
		t.Fatalf("NewFallbackClient: %v", err)
	}
	if got := fc.providers; len(got) != 2 || got[0] != "glm" || got[1] != "kimi" {
		t.Fatalf("providers = %v, want [glm kimi]", got)
	}
}

// ========== providerAt ==========

func TestFallbackClient_ProviderAt(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	if fc.providerAt(0) != "test-fb-0" {
		t.Errorf("providerAt(0) = %q, want \"test-fb-0\"", fc.providerAt(0))
	}
	if fc.providerAt(-1) != "" {
		t.Error("providerAt(-1) should return empty")
	}
	if fc.providerAt(99) != "" {
		t.Error("providerAt(99) should return empty")
	}
}

// ========== getCurrent / resetCurrent ==========

func TestFallbackClient_GetCurrent(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	if fc.getCurrent() != 0 {
		t.Errorf("getCurrent() = %d, want 0", fc.getCurrent())
	}
}

func TestFallbackClient_ResetCurrent(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	fc.mu.Lock()
	fc.current = 5
	fc.mu.Unlock()
	fc.resetCurrent()
	if fc.getCurrent() != 0 {
		t.Errorf("after reset, getCurrent() = %d, want 0", fc.getCurrent())
	}
}

func TestFallbackClient_ResetFallbackPosition(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	fc.mu.Lock()
	fc.current = 3
	fc.mu.Unlock()
	fc.ResetFallbackPosition()
	if fc.getCurrent() != 0 {
		t.Errorf("after ResetFallbackPosition, getCurrent() = %d, want 0", fc.getCurrent())
	}
}

// ========== SetSystemInstruction ==========

func TestFallbackClient_SetSystemInstruction(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	c2 := &fakeFallbackClientStub{id: "b"}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})
	fc.SetSystemInstruction("test instruction")
	if c1.sysInstr != "test instruction" {
		t.Errorf("c1 sysInstr = %q", c1.sysInstr)
	}
	if c2.sysInstr != "test instruction" {
		t.Errorf("c2 sysInstr = %q", c2.sysInstr)
	}
}

// ========== SetTurnContext ==========

func TestFallbackClient_SetTurnContext(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	fc.SetTurnContext("working dir")
	if c1.turnCtx != "working dir" {
		t.Errorf("turnCtx = %q", c1.turnCtx)
	}
}

// ========== SetThinkingBudget ==========

func TestFallbackClient_SetThinkingBudget(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	fc.SetThinkingBudget(8192)
	if c1.thinkBudget != 8192 {
		t.Errorf("thinkBudget = %d, want 8192", c1.thinkBudget)
	}
}

// ========== SetTools ==========

func TestFallbackClient_SetTools(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	tools := []*genai.Tool{
		{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "bash"}}},
	}
	fc.SetTools(tools)
	if len(c1.tools) != 1 {
		t.Errorf("c1 tools len = %d, want 1", len(c1.tools))
	}
}

// ========== SetRateLimiter ==========

func TestFallbackClient_SetRateLimiter(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	fc.SetRateLimiter("some-limiter")
	if c1.rateLimiter != "some-limiter" {
		t.Errorf("rateLimiter = %v", c1.rateLimiter)
	}
}

// ========== SetStatusCallback ==========

func TestFallbackClient_SetStatusCallback_Nil(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	// nil callback should be a no-op.
	fc.SetStatusCallback(nil)
	if c1.statusCB != nil {
		t.Error("nil callback should not set anything")
	}
}

func TestFallbackClient_SetStatusCallback_Set(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	cb := &DefaultStatusCallback{}
	fc.SetStatusCallback(cb)
	if c1.statusCB == nil {
		t.Error("statusCallback should be set")
	}
}

// ========== GetModel ==========

func TestFallbackClient_GetModel(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", model: "glm-4"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	if got := fc.GetModel(); got != "glm-4" {
		t.Errorf("GetModel() = %q, want \"glm-4\"", got)
	}
}

// ========== SetModel ==========

func TestFallbackClient_SetModel(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", model: "glm-4"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	fc.SetModel("glm-4-plus")
	if c1.model != "glm-4-plus" {
		t.Errorf("model = %q, want \"glm-4-plus\"", c1.model)
	}
}

// ========== WithModel ==========

func TestFallbackClient_WithModel(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", model: "glm-4"}
	c2 := &fakeFallbackClientStub{id: "b", model: "kimi-k2"}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})

	newFC := fc.WithModel("new-model")
	if newFC == nil {
		t.Fatal("WithModel returned nil")
	}
	// Should return a FallbackClient
	if _, ok := newFC.(*FallbackClient); !ok {
		t.Errorf("WithModel should return *FallbackClient, got %T", newFC)
	}
	if got := newFC.GetModel(); got != "new-model" {
		t.Errorf("GetModel() = %q, want \"new-model\"", got)
	}
}

func TestFallbackClient_WithModel_PreservesFallbackProviderModel(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", model: "glm-5.2"}
	c2 := &fakeFallbackClientStub{id: "b", model: "kimi-for-coding"}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"glm", "kimi"})

	got := fc.WithModel("glm-5.2").(*FallbackClient)
	if got.clients[0].GetModel() != "glm-5.2" {
		t.Fatalf("primary model = %q, want glm-5.2", got.clients[0].GetModel())
	}
	if got.clients[1].GetModel() != "kimi-for-coding" {
		t.Fatalf("fallback model = %q, want provider-native model", got.clients[1].GetModel())
	}
}

func TestFallbackClient_WithModel_SameCustomModelKeepsExplicitProvider(t *testing.T) {
	primary := &fakeFallbackClientStub{id: "gateway", model: "qwen-custom"}
	ollama := &fakeFallbackClientStub{id: "ollama", model: "llama3.2"}
	fc, _ := NewFallbackClient([]Client{primary, ollama}, []string{"glm", "ollama"})

	got := fc.WithModel(fc.GetModel()).(*FallbackClient)
	if got.getCurrent() != 0 || got.GetProvider() != "glm" || got.GetModel() != "qwen-custom" {
		t.Fatalf("clone switched explicit provider: current/provider/model = %d/%q/%q",
			got.getCurrent(), got.GetProvider(), got.GetModel())
	}
}

func TestFallbackClient_WithModel_SelectedProviderCanWrapToEarlierFallback(t *testing.T) {
	glm := &fakeFallbackClientStub{id: "glm", model: "glm-5.2", sendResp: fallbackTestStream(
		ResponseChunk{Text: "glm recovered", Done: true},
	)}
	kimi := &fakeFallbackClientStub{id: "kimi", model: "kimi-for-coding", sendErr: errors.New("kimi unavailable")}
	fc, _ := NewFallbackClient([]Client{glm, kimi}, []string{"glm", "kimi"})
	selected := fc.WithModel("kimi-for-coding").(*FallbackClient)
	// Health persistence is process-global; keep this failure isolated from
	// adaptive-retry tests for the real provider names.
	selected.providers = []string{"test-fb-wrap-glm", "test-fb-wrap-kimi"}

	stream, err := selected.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	resp, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if err != nil || resp.Text != "glm recovered" {
		t.Fatalf("wrapped fallback response/error = %#v/%v", resp, err)
	}
	if selected.getCurrent() != 0 {
		t.Fatalf("current = %d, want wrapped earlier fallback index 0", selected.getCurrent())
	}
}

// ========== GetRawClient ==========

func TestFallbackClient_GetRawClient(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	// fakeClient returns nil for GetRawClient — just verify it doesn't panic.
	rc := fc.GetRawClient()
	_ = rc
}

// ========== Close ==========

func TestFallbackClient_Close(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	c2 := &fakeFallbackClientStub{id: "b"}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})
	if err := fc.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
	if !c1.closed || !c2.closed {
		t.Error("all clients should be closed")
	}
}

// ========== CountTokens ==========

func TestFallbackClient_CountTokens(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})
	resp, err := fc.CountTokens(context.Background(), nil)
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if resp.TotalTokens != 42 {
		t.Errorf("TotalTokens = %d, want 42", resp.TotalTokens)
	}
}

// ========== SendMessage failover ==========

func TestFallbackClient_SendMessage_FirstSucceeds(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true})}
	c2 := &fakeFallbackClientStub{id: "b"}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})

	resp, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp == nil {
		t.Fatal("resp should not be nil")
	}
	if fc.getCurrent() != 0 {
		t.Errorf("current = %d, want 0 (first succeeded)", fc.getCurrent())
	}
}

func TestFallbackClient_SendMessage_FailoverToSecond(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendErr: errors.New("connection refused")}
	c2 := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true})}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})

	resp, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if resp == nil {
		t.Fatal("resp should not be nil")
	}
	if fc.getCurrent() != 1 {
		t.Errorf("current = %d, want 1 (failed over to second)", fc.getCurrent())
	}
}

func TestShouldFallbackToNextProvider_TypedPolicy(t *testing.T) {
	for _, code := range []string{"1210", "1211", "1212", "1213", "1214", "1215", "1301"} {
		t.Run("GLM request or safety "+code, func(t *testing.T) {
			if shouldFallbackToNextProvider(&TerminalProviderError{Code: code, Message: "terminal"}) {
				t.Fatalf("GLM terminal code %s was eligible for cross-provider fallback", code)
			}
		})
	}

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"GLM auth is provider local", &TerminalProviderError{Code: "1000", Message: "auth"}, true},
		{"GLM quota is provider local", &TerminalProviderError{Code: "1308", Message: "quota"}, true},
		{"GLM plan is provider local", &TerminalProviderError{Code: "1311", Message: "plan"}, true},
		{"typed HTTP bad request", &HTTPError{StatusCode: 400, Message: "invalid parameter"}, false},
		{"typed API bad request", &APIError{StatusCode: 400, Message: "invalid parameter"}, false},
		{"typed unprocessable request", &HTTPError{StatusCode: 422, Message: "invalid tool schema"}, false},
		{"bad request cannot escape through retry wording", &HTTPError{StatusCode: 400, Message: "invalid parameter: unexpected EOF"}, false},
		{"known transient 400 remains eligible", &HTTPError{StatusCode: 400, Message: "model_not_found"}, true},
		{"typed auth status is provider local", &HTTPError{StatusCode: 401, Message: "bad provider key"}, true},
		{"typed rate limit is provider local", &APIError{StatusCode: 429, Message: "rate limited"}, true},
		{"transient untyped error", errors.New("connection reset"), true},
		{"cancellation", context.Canceled, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFallbackToNextProvider(tt.err); got != tt.want {
				t.Fatalf("shouldFallbackToNextProvider(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}

	var typedNilTerminal *TerminalProviderError
	if shouldFallbackToNextProvider(typedNilTerminal) {
		t.Fatal("typed-nil terminal error was eligible for fallback")
	}
}

func TestFallbackClient_RecordFailure_HealthPolicy(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantChanged bool
	}{
		{"request shape", &HTTPError{StatusCode: 400, Message: "invalid parameter"}, false},
		{"safety", &TerminalProviderError{Code: "1301", Message: "safety refusal"}, false},
		{"cancellation", context.Canceled, false},
		{"auth", &TerminalProviderError{Code: "1000", Message: "bad key"}, true},
		{"quota", &TerminalProviderError{Code: "1308", Message: "quota exhausted"}, true},
		{"transient", errors.New("connection reset"), true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := fmt.Sprintf("test-fb-health-policy-%d", i)
			fc, err := NewFallbackClient(
				[]Client{&fakeFallbackClientStub{id: "primary"}}, []string{provider})
			if err != nil {
				t.Fatalf("NewFallbackClient: %v", err)
			}
			before := getProviderHealth(provider)
			fc.recordFailure("policy test", 0, tt.err)
			after := getProviderHealth(provider)
			changed := after.Score < before.Score && after.FailureStreak == before.FailureStreak+1
			if changed != tt.wantChanged {
				t.Fatalf("health change = %v, want %v; before=%+v after=%+v",
					changed, tt.wantChanged, before, after)
			}
			if !tt.wantChanged && (after.Score != before.Score || after.FailureStreak != before.FailureStreak) {
				t.Fatalf("health changed for request-scoped failure: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestFallbackClient_SendMessage_RequestShapeErrorStopsChain(t *testing.T) {
	provider := "test-fb-request-shape-primary"
	before := getProviderHealth(provider)
	requestErr := &HTTPError{StatusCode: 400, Message: "invalid tool schema"}
	primary := &fakeFallbackClientStub{id: "primary", sendErr: requestErr}
	secondary := &fakeFallbackClientStub{id: "secondary", sendResp: fallbackTestStream(
		ResponseChunk{Text: "must not run", Done: true},
	)}
	fc, _ := NewFallbackClient(
		[]Client{primary, secondary},
		[]string{provider, "test-fb-request-shape-secondary"},
	)

	stream, err := fc.SendMessage(context.Background(), "hello")
	if stream != nil || !errors.Is(err, requestErr) {
		t.Fatalf("response/error = %#v/%v, want original request error", stream, err)
	}
	if secondary.sendCalls != 0 || fc.getCurrent() != 0 {
		t.Fatalf("request-shape error reached secondary: calls/current = %d/%d", secondary.sendCalls, fc.getCurrent())
	}
	after := getProviderHealth(provider)
	if after.Score != before.Score || after.FailureStreak != before.FailureStreak {
		t.Fatalf("request-scoped failure changed provider health: before=%+v after=%+v", before, after)
	}
}

func TestFallbackClient_SendMessage_ProviderLocalTerminalErrorFailsOver(t *testing.T) {
	provider := "test-fb-quota-primary"
	before := getProviderHealth(provider)
	primary := &fakeFallbackClientStub{id: "primary", sendErr: &TerminalProviderError{
		Code: "1308", Message: "GLM quota exhausted",
	}}
	secondary := &fakeFallbackClientStub{id: "secondary", sendResp: fallbackTestStream(
		ResponseChunk{Text: "fallback ok", Done: true},
	)}
	fc, _ := NewFallbackClient(
		[]Client{primary, secondary},
		[]string{provider, "test-fb-quota-secondary"},
	)

	stream, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	response, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if err != nil || response.Text != "fallback ok" {
		t.Fatalf("response/error = %#v/%v, want provider-local fallback", response, err)
	}
	if secondary.sendCalls != 1 || fc.getCurrent() != 1 {
		t.Fatalf("secondary calls/current = %d/%d, want 1/1", secondary.sendCalls, fc.getCurrent())
	}
	after := getProviderHealth(provider)
	if after.Score >= before.Score || after.FailureStreak != before.FailureStreak+1 {
		t.Fatalf("provider-local quota failure did not change health: before=%+v after=%+v", before, after)
	}
}

func TestFallbackClient_SendMessage_AllFail(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendErr: errors.New("err1")}
	c2 := &fakeFallbackClientStub{id: "b", sendErr: errors.New("err2")}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})

	_, err := fc.SendMessage(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error when all clients fail")
	}
}

func TestFallbackClient_SendMessage_CancelledContext(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fc.SendMessage(ctx, "hello")
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
}

func TestFallbackClient_SendMessage_FailsOverOnEarlyStreamError(t *testing.T) {
	primary := &fakeFallbackClientStub{id: "a", model: "glm-5.2", sendResp: fallbackTestStream(
		ResponseChunk{RateLimit: &RateLimitMetadata{RequestsRemaining: 0}},
		ResponseChunk{Error: errors.New("GLM 1305 overloaded"), Done: true},
	)}
	secondary := &fakeFallbackClientStub{id: "b", model: "kimi-for-coding", sendResp: fallbackTestStream(
		ResponseChunk{Text: "fallback ok", Done: true, FinishReason: genai.FinishReasonStop},
	)}
	fc, _ := NewFallbackClient([]Client{primary, secondary}, []string{"test-fb-stream-0", "test-fb-stream-1"})

	stream, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	resp, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if resp.Text != "fallback ok" {
		t.Fatalf("Text = %q, want fallback response", resp.Text)
	}
	if secondary.sendCalls != 1 || fc.getCurrent() != 1 {
		t.Fatalf("secondary calls/current = %d/%d, want 1/1", secondary.sendCalls, fc.getCurrent())
	}
}

func TestFallbackClient_SendMessage_EarlySafetyErrorStopsChain(t *testing.T) {
	provider := "test-fb-safety-primary"
	before := getProviderHealth(provider)
	safetyErr := &TerminalProviderError{Code: "1301", Message: "sensitive content rejected"}
	primary := &fakeFallbackClientStub{id: "primary", sendResp: fallbackTestStream(
		ResponseChunk{Error: safetyErr, Done: true},
	)}
	secondary := &fakeFallbackClientStub{id: "secondary", sendResp: fallbackTestStream(
		ResponseChunk{Text: "must not bypass safety", Done: true},
	)}
	fc, _ := NewFallbackClient(
		[]Client{primary, secondary},
		[]string{provider, "test-fb-safety-secondary"},
	)

	stream, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	response, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if response == nil || !errors.Is(err, safetyErr) {
		t.Fatalf("response/error = %#v/%v, want original safety error", response, err)
	}
	if secondary.sendCalls != 0 || fc.getCurrent() != 0 {
		t.Fatalf("safety error reached secondary: calls/current = %d/%d", secondary.sendCalls, fc.getCurrent())
	}
	after := getProviderHealth(provider)
	if after.Score != before.Score || after.FailureStreak != before.FailureStreak {
		t.Fatalf("request-scoped safety failure changed provider health: before=%+v after=%+v", before, after)
	}
}

func TestFallbackClient_SendMessage_DoesNotReplayAfterContentCommitted(t *testing.T) {
	primaryErr := errors.New("stream broke after content")
	primary := &fakeFallbackClientStub{id: "a", sendResp: fallbackTestStream(
		ResponseChunk{Text: "partial"},
		ResponseChunk{Error: primaryErr, Done: true},
	)}
	secondary := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(
		ResponseChunk{Text: "would duplicate", Done: true},
	)}
	fc, _ := NewFallbackClient([]Client{primary, secondary}, []string{"test-fb-commit-0", "test-fb-commit-1"})

	stream, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	resp, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if !errors.Is(err, primaryErr) {
		t.Fatalf("ProcessStream error = %v, want %v", err, primaryErr)
	}
	if resp == nil || resp.Text != "partial" {
		t.Fatalf("partial response = %#v", resp)
	}
	if secondary.sendCalls != 0 {
		t.Fatalf("secondary was called %d times after content was committed", secondary.sendCalls)
	}
}

func TestFallbackClient_SendMessage_NilChunksFailsOver(t *testing.T) {
	primary := &fakeFallbackClientStub{id: "a", sendResp: &StreamingResponse{}}
	secondary := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(
		ResponseChunk{Text: "valid stream", Done: true},
	)}
	fc, _ := NewFallbackClient([]Client{primary, secondary}, []string{"test-fb-nil-0", "test-fb-nil-1"})

	stream, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	resp, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if err != nil || resp.Text != "valid stream" {
		t.Fatalf("response/error = %#v/%v, want valid fallback stream", resp, err)
	}
}

func TestFallbackClient_SendMessage_EmptyStreamFailsOver(t *testing.T) {
	primary := &fakeFallbackClientStub{id: "a", sendResp: fallbackTestStream(
		ResponseChunk{RateLimit: &RateLimitMetadata{RequestsRemaining: 1}},
		ResponseChunk{Done: true},
	)}
	secondary := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(
		ResponseChunk{Text: "non-empty fallback", Done: true},
	)}
	fc, _ := NewFallbackClient([]Client{primary, secondary}, []string{"test-fb-empty-0", "test-fb-empty-1"})

	stream, err := fc.SendMessage(context.Background(), "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	response, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if err != nil || response.Text != "non-empty fallback" {
		t.Fatalf("response/error = %#v/%v, want non-empty fallback", response, err)
	}
	if secondary.sendCalls != 1 || fc.getCurrent() != 1 {
		t.Fatalf("secondary calls/current = %d/%d, want 1/1", secondary.sendCalls, fc.getCurrent())
	}
}

func TestFallbackClient_UserCancellationDoesNotPenalizeProviderHealth(t *testing.T) {
	provider := "test-fb-user-cancel-health"
	before := getProviderHealth(provider)
	blocked := make(chan ResponseChunk)
	primary := &fakeFallbackClientStub{id: "a", sendResp: &StreamingResponse{Chunks: blocked}}
	fc, _ := NewFallbackClient([]Client{primary}, []string{provider})
	ctx, cancel := context.WithCancel(context.Background())

	stream, err := fc.SendMessage(ctx, "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	cancel()
	_, _ = ProcessStream(ctx, stream, &StreamHandler{})
	<-stream.Done
	after := getProviderHealth(provider)
	if after.Score != before.Score || after.FailureStreak != before.FailureStreak {
		t.Fatalf("health changed on user cancellation: before=%+v after=%+v", before, after)
	}
}

func TestFallbackClient_CancellationDuringFallbackSendDoesNotPenalizeProviderHealth(t *testing.T) {
	provider := "test-fb-cancel-during-send"
	before := getProviderHealth(provider)
	primary := &fakeFallbackClientStub{id: "primary", sendResp: fallbackTestStream(
		ResponseChunk{Error: errors.New("primary stream failed"), Done: true},
	)}
	started := make(chan struct{})
	secondary := &fakeFallbackClientStub{id: "secondary", sendFunc: func(ctx context.Context) (*StreamingResponse, error) {
		close(started)
		<-ctx.Done()
		return nil, ContextErr(ctx)
	}}
	fc, _ := NewFallbackClient(
		[]Client{primary, secondary},
		[]string{"test-fb-cancel-primary", provider},
	)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := fc.SendMessage(ctx, "hello")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	collectDone := make(chan struct{})
	go func() {
		_, _ = ProcessStream(ctx, stream, &StreamHandler{})
		close(collectDone)
	}()
	<-started
	cancel()
	<-collectDone
	<-stream.Done

	after := getProviderHealth(provider)
	if after.Score != before.Score || after.FailureStreak != before.FailureStreak {
		t.Fatalf("fallback health changed on cancellation during send: before=%+v after=%+v", before, after)
	}
}

func fallbackTestStream(chunks ...ResponseChunk) *StreamingResponse {
	ch := make(chan ResponseChunk, len(chunks))
	for _, chunk := range chunks {
		ch <- chunk
	}
	close(ch)
	return &StreamingResponse{Chunks: ch}
}

// ========== SendMessageWithHistory failover ==========

func TestFallbackClient_SendMessageWithHistory_FirstSucceeds(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true})}
	c2 := &fakeFallbackClientStub{id: "b"}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})

	resp, err := fc.SendMessageWithHistory(context.Background(), nil, "hello")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	if resp == nil {
		t.Fatal("resp should not be nil")
	}
}

func TestFallbackClient_SendMessageWithHistory_Failover(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendErr: errors.New("timeout")}
	c2 := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true})}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})

	resp, err := fc.SendMessageWithHistory(context.Background(), nil, "hello")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	if resp == nil {
		t.Fatal("resp should not be nil")
	}
	if fc.getCurrent() != 1 {
		t.Errorf("current = %d, want 1", fc.getCurrent())
	}
}

func TestFallbackClient_SendMessageWithHistory_AllFail(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendErr: errors.New("err")}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})

	_, err := fc.SendMessageWithHistory(context.Background(), nil, "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ========== SendFunctionResponse failover ==========

func TestFallbackClient_SendFunctionResponse_FirstSucceeds(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true})}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})

	resp, err := fc.SendFunctionResponse(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("SendFunctionResponse: %v", err)
	}
	if resp == nil {
		t.Fatal("resp should not be nil")
	}
}

func TestFallbackClient_SendFunctionResponse_Failover(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendErr: errors.New("500")}
	c2 := &fakeFallbackClientStub{id: "b", sendResp: fallbackTestStream(ResponseChunk{Text: "ok", Done: true})}
	fc, _ := NewFallbackClient([]Client{c1, c2}, []string{"test-fb-0", "test-fb-1"})

	resp, err := fc.SendFunctionResponse(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("SendFunctionResponse: %v", err)
	}
	if resp == nil {
		t.Fatal("resp should not be nil")
	}
	if fc.getCurrent() != 1 {
		t.Errorf("current = %d, want 1", fc.getCurrent())
	}
}

func TestFallbackClient_SendFunctionResponse_AllFail(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendErr: errors.New("err")}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})

	_, err := fc.SendFunctionResponse(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFallbackClient_SendFunctionResponse_CancelledContext(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a"}
	fc, _ := NewFallbackClient([]Client{c1}, []string{"test-fb-0"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := fc.SendFunctionResponse(ctx, nil, nil)
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
}
