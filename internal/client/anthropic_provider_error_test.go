package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"
)

// A standard Anthropic-compatible SSE error event (used by deepseek/kimi/
// minimax, not just GLM) must NOT be routed through classifyGLMErrorCode —
// that function only understands GLM's numeric "code" field and silently
// drops the standard "type" field (e.g. "overloaded_error") into its
// "unknown code" fallback, discarding the very keyword IsOverloadError and
// IsRetryableError match on. Before the fix, doStreamRequest matched on the
// mere presence of an "error" object regardless of provider, so this exact
// event on a deepseek client produced an error string that neither detector
// recognized, defeating the v0.100.46 patient-overload retry for 3 of 5
// providers. The fix gates the GLM branch on errCode != "" so a standard
// {"type":"error","error":{"type":...}} event falls through to
// processStreamEvent's own "case \"error\":" handler, which preserves the
// original errType in the resulting message.
func TestDeepSeekStandardErrorEvent_NotHijackedByGLMClassifier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Please slow down\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config:     AnthropicConfig{Model: "deepseek-v4-pro", BaseURL: srv.URL, APIKey: "test", Provider: "deepseek", StreamIdleTimeout: 5 * time.Second},
		httpClient: &http.Client{},
	}

	resp, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	_, streamErr := resp.Collect()
	if streamErr == nil {
		t.Fatalf("expected a stream error, got nil")
	}

	if !strings.Contains(streamErr.Error(), "overloaded_error") {
		t.Errorf("errType dropped: got %q, want it to retain \"overloaded_error\" (the standard error shape's type field)", streamErr.Error())
	}
	if !IsOverloadError(streamErr) {
		t.Errorf("IsOverloadError(%q) = false, want true — a standard overloaded_error event must be recognized on non-GLM providers", streamErr.Error())
	}
	if !IsRetryableError(streamErr) {
		t.Errorf("IsRetryableError(%q) = false, want true", streamErr.Error())
	}
}

func TestRequiresThinkingReplayConcurrentWithThinkingBudget(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{
		Provider: "deepseek", BaseURL: DefaultDeepSeekBaseURL, EnableThinking: true,
	}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			if i%2 == 0 {
				c.SetThinkingBudget(4096)
			} else {
				c.SetThinkingBudget(0)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			_ = c.requiresThinkingReplay()
		}
	}()
	wg.Wait()
}

// GLM's own error shape (numeric "code", no "type") must still classify
// exactly as before — this fix must not regress GLM's own error path.
func TestGLMErrorEvent_StillClassifiedByGLMPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"error\":{\"code\":\"1305\",\"message\":\"service busy\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config:     AnthropicConfig{Model: "glm-5.2", BaseURL: srv.URL, APIKey: "test", Provider: "glm", StreamIdleTimeout: 5 * time.Second},
		httpClient: &http.Client{},
	}

	resp, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	_, streamErr := resp.Collect()
	if streamErr == nil {
		t.Fatalf("expected a stream error, got nil")
	}
	if !strings.Contains(streamErr.Error(), "overloaded") {
		t.Errorf("GLM 1305 classification regressed: got %q, want it to contain \"overloaded\"", streamErr.Error())
	}
	if !IsOverloadError(streamErr) {
		t.Errorf("IsOverloadError(%q) = false, want true", streamErr.Error())
	}
}

func TestDeepSeekNumericHTTPError_NotClassifiedAsGLMTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"code":"1308","message":"provider-specific quota"}}`)
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model: "deepseek-v4-pro", BaseURL: srv.URL, APIKey: "test",
			Provider: "deepseek", StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}
	_, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if IsTerminalProviderError(err) {
		t.Fatalf("non-GLM numeric code became a GLM terminal error: %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "1308") || !strings.Contains(err.Error(), "provider-specific quota") {
		t.Fatalf("provider error details were lost: %v", err)
	}
}

func TestDeepSeekBareNumericStreamError_NotClassifiedAsGLM(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"error\":{\"code\":\"1308\",\"message\":\"provider-specific quota\"}}\n\n")
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model: "deepseek-v4-pro", BaseURL: srv.URL, APIKey: "test",
			Provider: "deepseek", StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}
	resp, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	_, streamErr := resp.Collect()
	if streamErr == nil {
		t.Fatal("expected streamed provider error")
	}
	if IsTerminalProviderError(streamErr) {
		t.Fatalf("non-GLM stream code became a GLM terminal error: %T %v", streamErr, streamErr)
	}
	if !strings.Contains(streamErr.Error(), "deepseek") || !strings.Contains(streamErr.Error(), "1308") {
		t.Fatalf("provider/code context was lost: %v", streamErr)
	}
}
