package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gokin/internal/config"

	"google.golang.org/genai"
)

func TestDeepSeekThinkingWireSettings(t *testing.T) {
	for _, tc := range []struct {
		name         string
		budget       int32
		effort, want string
	}{
		{"disabled", 0, "", ""},
		{"adaptive", 4096, "", "high"},
		{"explicit", 16384, "low", "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan map[string]any, 2)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test" {
					t.Errorf("unexpected endpoint/auth: %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
			}))
			defer srv.Close()
			c := &AnthropicClient{config: AnthropicConfig{APIKey: "test", BaseURL: srv.URL, Provider: "deepseek", Model: "deepseek-flash", StreamIdleTimeout: time.Second, MaxTokens: 4096, ReasoningEffort: tc.effort}, httpClient: srv.Client()}
			c.SetThinkingBudget(tc.budget)
			stream, err := c.SendMessage(context.Background(), "inspect")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = stream.Collect(); err != nil {
				t.Fatal(err)
			}
			history := []*genai.Content{
				genai.NewContentFromText("inspect", genai.RoleUser),
				{Role: genai.RoleModel, Parts: []*genai.Part{
					{Thought: true, Text: "preserved reasoning", ThoughtSignature: []byte("signed")},
					{FunctionCall: &genai.FunctionCall{ID: "call-1", Name: "read"}},
				}},
			}
			stream, err = c.SendFunctionResponse(context.Background(), history, []*genai.FunctionResponse{{ID: "call-1", Name: "read", Response: map[string]any{"content": "contents"}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = stream.Collect(); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				body := <-requests
				thinking, _ := body["thinking"].(map[string]any)
				wantType := "enabled"
				if tc.budget == 0 {
					wantType = "disabled"
				}
				if thinking["type"] != wantType {
					t.Errorf("request %d thinking=%#v, want %s", i, thinking, wantType)
				}
				if _, ok := thinking["budget_tokens"]; ok {
					t.Error("DeepSeek ignores numeric budgets; use output_config.effort")
				}
				if tc.want != "" {
					output, _ := body["output_config"].(map[string]any)
					if output["effort"] != tc.want {
						t.Errorf("request %d effort=%#v, want %s", i, output, tc.want)
					}
				}
				if i == 1 {
					messages := body["messages"].([]any)
					blocks := messages[1].(map[string]any)["content"].([]any)
					thinking := blocks[0].(map[string]any)
					if thinking["signature"] != "signed" || thinking["thinking"] != "preserved reasoning" {
						t.Errorf("thinking/signature not replayed: %#v", blocks)
					}
				}
			}
		})
	}
}

func TestDeepSeekErrorsHTTPAndSSE(t *testing.T) {
	for _, tc := range []struct {
		status                    int
		typ, message              string
		terminal, retry, overload bool
	}{
		{401, "authentication_error", "invalid key", true, false, false},
		{402, "insufficient_balance", "Insufficient Balance", true, false, false},
		{429, "rate_limit_error", "Too many requests", false, true, true},
		{429, "rate_limit_error", "Insufficient Balance; rate limit reached", true, false, false},
		{503, "overloaded_error", "Server overloaded", false, true, true},
		{500, "api_error", "Server error", false, true, false},
	} {
		for _, sse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/sse=%t", tc.status, sse), func(t *testing.T) {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					payload := map[string]any{"type": "error", "error": map[string]any{"type": tc.typ, "message": tc.message}}
					if sse {
						w.Header().Set("Content-Type", "text/event-stream")
						raw, _ := json.Marshal(payload)
						fmt.Fprintf(w, "data: %s\n\n", raw)
					} else {
						w.WriteHeader(tc.status)
						_ = json.NewEncoder(w).Encode(payload)
					}
				}))
				defer srv.Close()
				c := &AnthropicClient{config: AnthropicConfig{APIKey: "test", BaseURL: srv.URL, Provider: "deepseek", Model: "deepseek-v4-pro", StreamIdleTimeout: time.Second, MaxTokens: 4096}, httpClient: srv.Client()}
				stream, err := c.SendMessage(context.Background(), "hi")
				if err == nil {
					_, err = stream.Collect()
				}
				if err == nil {
					t.Fatal("expected provider error")
				}
				var terminal *TerminalProviderError
				if errors.As(err, &terminal) != tc.terminal {
					t.Errorf("terminal=%v, want %v: %v", terminal, tc.terminal, err)
				}
				if IsRetryableError(err) != tc.retry {
					t.Errorf("retryable=%v, want %v: %v", IsRetryableError(err), tc.retry, err)
				}
				if IsOverloadError(err) != tc.overload {
					t.Errorf("overload=%v, want %v: %v", IsOverloadError(err), tc.overload, err)
				}
			})
		}
	}
}

func TestDeepSeekFactoryHonorsThinkingOff(t *testing.T) {
	for _, staleFlag := range []bool{false, true} {
		cfg := newDeepSeekTestConfig()
		cfg.Model.ThinkingMode = config.ThinkingModeOff
		cfg.Model.EnableThinking = staleFlag
		if staleFlag {
			cfg.Model.ThinkingBudget = 8192
		}
		c, err := newDeepSeekClient(cfg, "deepseek-v4-pro")
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if c.(*AnthropicClient).config.EnableThinking {
			t.Fatal("explicit thinking off was overridden")
		}
	}
}

func TestDeepSeekInvalidRequestDoesNotFallbackOrDegradeHealth(t *testing.T) {
	for _, status := range []int{400, 422} {
		for _, sse := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/sse=%t", status, sse), func(t *testing.T) {
				requestErr := newDeepSeekProviderError(status, 0, "invalid_request_error", "invalid tool schema")
				provider := fmt.Sprintf("test-fb-deepseek-invalid-%d-%t", status, sse)
				before := getProviderHealth(provider)
				primary := &fakeFallbackClientStub{sendErr: requestErr}
				if sse {
					primary.sendErr = nil
					primary.sendResp = fallbackTestStream(ResponseChunk{Error: requestErr, Done: true})
				}
				secondary := &fakeFallbackClientStub{sendResp: fallbackTestStream(ResponseChunk{Text: "must not run", Done: true})}
				fc, err := NewFallbackClient([]Client{primary, secondary}, []string{provider, provider + "-fallback"})
				if err != nil {
					t.Fatal(err)
				}
				defer fc.Close()
				stream, err := fc.SendMessage(t.Context(), "hello")
				if err == nil {
					_, err = stream.Collect()
				}
				if !errors.Is(err, requestErr) {
					t.Errorf("error = %v, want original validation error", err)
				}
				if secondary.sendCalls != 0 {
					t.Errorf("validation error reached fallback: %d calls", secondary.sendCalls)
				}
				if after := getProviderHealth(provider); after != before {
					t.Errorf("fallback health changed: before=%+v after=%+v", before, after)
				}

				// Single-provider requests use separate HTTP and stream observers.
				directProvider := provider + "-direct"
				before = getProviderHealth(directProvider)
				c := &AnthropicClient{config: AnthropicConfig{Provider: directProvider}, directHealthTracking: true}
				if sse {
					stream := c.observeDirectHealth(t.Context(), fallbackTestStream(ResponseChunk{Error: requestErr, Done: true}))
					if _, err := stream.Collect(); !errors.Is(err, requestErr) {
						t.Errorf("observed error = %v, want original validation error", err)
					}
				} else {
					c.recordDirectHealthFailure(true, requestErr)
				}
				if after := getProviderHealth(directProvider); after != before {
					t.Errorf("direct health changed: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}
