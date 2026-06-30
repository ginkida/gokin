package client

import (
	"context"
	"errors"
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
	return &fakeFallbackClientStub{id: f.id, model: m}
}
func (f *fakeFallbackClientStub) GetRawClient() any { return nil }
func (f *fakeFallbackClientStub) SendMessage(_ context.Context, _ string) (*StreamingResponse, error) {
	return f.sendResp, f.sendErr
}
func (f *fakeFallbackClientStub) SendMessageWithHistory(_ context.Context, _ []*genai.Content, _ string) (*StreamingResponse, error) {
	return f.sendResp, f.sendErr
}
func (f *fakeFallbackClientStub) SendFunctionResponse(_ context.Context, _ []*genai.Content, _ []*genai.FunctionResponse) (*StreamingResponse, error) {
	return f.sendResp, f.sendErr
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
	c1 := &fakeFallbackClientStub{id: "a", sendResp: &StreamingResponse{}}
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
	c2 := &fakeFallbackClientStub{id: "b", sendResp: &StreamingResponse{}}
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

// ========== SendMessageWithHistory failover ==========

func TestFallbackClient_SendMessageWithHistory_FirstSucceeds(t *testing.T) {
	c1 := &fakeFallbackClientStub{id: "a", sendResp: &StreamingResponse{}}
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
	c2 := &fakeFallbackClientStub{id: "b", sendResp: &StreamingResponse{}}
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
	c1 := &fakeFallbackClientStub{id: "a", sendResp: &StreamingResponse{}}
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
	c2 := &fakeFallbackClientStub{id: "b", sendResp: &StreamingResponse{}}
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
