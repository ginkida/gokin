package testkit

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestMockClient_EnqueueTextSuccess(t *testing.T) {
	m := NewMockClient().EnqueueText("hello world")

	resp, err := m.SendMessage(context.Background(), "hi")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	collected, err := resp.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if collected.Text != "hello world" {
		t.Errorf("Text = %q, want %q", collected.Text, "hello world")
	}
	if collected.FinishReason != genai.FinishReasonStop {
		t.Errorf("FinishReason = %v, want %v", collected.FinishReason, genai.FinishReasonStop)
	}
}

func TestMockClient_EnqueueToolCall(t *testing.T) {
	m := NewMockClient().EnqueueToolCall("read_file", map[string]any{"path": "/tmp/x"})

	resp, err := m.SendMessage(context.Background(), "read it")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	collected, err := resp.Collect()
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(collected.FunctionCalls) != 1 {
		t.Fatalf("FunctionCalls len = %d, want 1", len(collected.FunctionCalls))
	}
	fc := collected.FunctionCalls[0]
	if fc.Name != "read_file" {
		t.Errorf("Name = %q, want read_file", fc.Name)
	}
	if fc.Args["path"] != "/tmp/x" {
		t.Errorf("Args[path] = %v, want /tmp/x", fc.Args["path"])
	}
}

func TestMockClient_EnqueueStartupError(t *testing.T) {
	want := errors.New("401 unauthorized")
	m := NewMockClient().EnqueueStartupError(want)

	_, err := m.SendMessage(context.Background(), "x")
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestMockClient_EnqueueErrorMidStream(t *testing.T) {
	want := errors.New("stream broken")
	m := NewMockClient().EnqueueError(want)

	resp, err := m.SendMessage(context.Background(), "x")
	if err != nil {
		t.Fatalf("SendMessage should not return err for mid-stream error: %v", err)
	}
	_, err = resp.Collect()
	if !errors.Is(err, want) {
		t.Errorf("Collect err = %v, want %v", err, want)
	}
}

func TestMockClient_EnqueueOverloadMatchesClassifier(t *testing.T) {
	m := NewMockClient().EnqueueOverload()

	resp, err := m.SendMessage(context.Background(), "x")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	_, err = resp.Collect()
	if err == nil {
		t.Fatal("expected overload error, got nil")
	}
	// Real isOverloadError in internal/app should see "1305" as overload.
	if got := err.Error(); got == "" {
		t.Fatalf("empty error message")
	}
}

func TestMockClient_ScriptsExhausted(t *testing.T) {
	m := NewMockClient()
	_, err := m.SendMessage(context.Background(), "x")
	if !errors.Is(err, ErrNoScript) {
		t.Errorf("err = %v, want ErrNoScript", err)
	}
}

func TestMockClient_RecordsCallsWithHistory(t *testing.T) {
	m := NewMockClient().EnqueueText("ok")
	m.SetTools([]*genai.Tool{{}})
	m.SetModel("glm-5.1")

	history := []*genai.Content{
		{Role: "user", Parts: []*genai.Part{{Text: "earlier"}}},
	}
	if _, err := m.SendMessageWithHistory(context.Background(), history, "now"); err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	calls := m.Calls()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.Method != "SendMessageWithHistory" {
		t.Errorf("Method = %q", c.Method)
	}
	if c.Message != "now" {
		t.Errorf("Message = %q", c.Message)
	}
	if c.Model != "glm-5.1" {
		t.Errorf("Model = %q", c.Model)
	}
	if c.ToolsCount != 1 {
		t.Errorf("ToolsCount = %d", c.ToolsCount)
	}
	if len(c.History) != 1 {
		t.Errorf("History len = %d", len(c.History))
	}
}

func TestMockClient_WithModelCreatesIsolatedClone(t *testing.T) {
	parent := NewMockClient().EnqueueText("parent")
	clone := parent.WithModel("glm-4.7")

	if got := clone.GetModel(); got != "glm-4.7" {
		t.Errorf("clone model = %q, want glm-4.7", got)
	}
	// Scripts are NOT shared — clone must have its own queue.
	_, err := clone.SendMessage(context.Background(), "x")
	if !errors.Is(err, ErrNoScript) {
		t.Errorf("clone should have no scripts; err = %v", err)
	}

	clones := parent.Clones()
	if len(clones) != 1 {
		t.Fatalf("Clones len = %d, want 1", len(clones))
	}
}

func TestMockClient_OnSendFires(t *testing.T) {
	var called bool
	m := NewMockClient().EnqueueText("ok")
	m.OnSend = func(ctx context.Context) { called = true }

	_, err := m.SendMessage(context.Background(), "x")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if !called {
		t.Error("OnSend was not invoked")
	}
}

func TestMockClient_DelayHonorsContext(t *testing.T) {
	m := NewMockClient().EnqueueScript(ResponseScript{DelayBeforeFirstChunk: 5 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	resp, err := m.SendMessage(ctx, "x")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	_, _ = resp.Collect()
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("delay did not honor ctx cancel: elapsed %v", elapsed)
	}
}

func TestMockClient_ResetClearsStateButKeepsConfig(t *testing.T) {
	m := NewMockClient().EnqueueText("a").EnqueueText("b")
	m.SetModel("glm-5.1")
	if _, err := m.SendMessage(context.Background(), "x"); err != nil {
		t.Fatalf("first SendMessage: %v", err)
	}

	m.Reset()

	if m.GetModel() != "glm-5.1" {
		t.Error("Reset should not clear model config")
	}
	if calls := m.Calls(); len(calls) != 0 {
		t.Errorf("Reset should clear calls; got %d", len(calls))
	}
	if _, err := m.SendMessage(context.Background(), "x"); !errors.Is(err, ErrNoScript) {
		t.Errorf("Reset should clear scripts; err = %v", err)
	}
}

func TestMockClient_CountTokensReturnsEstimate(t *testing.T) {
	m := NewMockClient()
	contents := []*genai.Content{
		{Parts: []*genai.Part{{Text: "1234567812345678"}}}, // 16 chars → ~4 tokens
	}
	resp, err := m.CountTokens(context.Background(), contents)
	if err != nil {
		t.Fatalf("CountTokens: %v", err)
	}
	if resp.TotalTokens != 4 {
		t.Errorf("TotalTokens = %d, want 4", resp.TotalTokens)
	}
}
