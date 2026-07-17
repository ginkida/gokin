package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestKimiErrorTaxonomy(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		errType     string
		message     string
		terminal    bool
		retryable   bool
		overload    bool
		contextLong bool
		contains    string
	}{
		{
			name: "five hour quota is terminal", status: 429, errType: "rate_limit_error",
			message:  "You've reached your usage limit for this period. Your quota will be refreshed in the next period.",
			terminal: true, contains: "wait for the usage window",
		},
		{
			name: "monthly quota is terminal", status: 429, errType: "rate_limit_error",
			message:  "You've reached kimi monthly usage limit for this billing cycle.",
			terminal: true, contains: "/provider",
		},
		{
			name: "billing cycle quota on 403 is terminal", status: 403, errType: "permission_error",
			message:  "You've reached your usage limit for this billing cycle. Your quota will be refreshed in the next cycle.",
			terminal: true, contains: "wait for the usage window",
		},
		{
			name: "engine overload stays patient", status: 429, errType: "overloaded_error",
			message:   "The engine is currently overloaded, please try again later",
			retryable: true, overload: true,
		},
		{
			name: "concurrency limit stays patient", status: 429, errType: "rate_limit_error",
			message:   "We're receiving too many requests at the moment. Please wait a moment and try again.",
			retryable: true, overload: true,
		},
		{
			name: "membership lookup is transient", status: 402,
			message:   "We're unable to verify your membership benefits at this time.",
			retryable: true, contains: "temporarily unavailable",
		},
		{
			name: "moderato 256k denial is actionable", status: 401,
			message:  "Your current plan supports only kimi-k3 up to 256K context. 1M context is available on higher-tier plans.",
			terminal: true, contains: "context.max_input_tokens",
		},
		{
			name: "serialized message ceiling compacts", status: 400, errType: "invalid_request_error",
			message:     "total message size 5943865 exceeds limit 2097152",
			contextLong: true, contains: "context limit exceeded",
		},
		{
			name: "token window compacts", status: 400, errType: "invalid_request_error",
			message:     "Your request exceeded model token limit: 262144 (requested: 558009)",
			contextLong: true,
		},
		{
			name: "missing reasoning never retries", status: 400, errType: "invalid_request_error",
			message:  "thinking is enabled but reasoning_content is missing in assistant tool call message at index 2",
			terminal: true, contains: "preserved thinking",
		},
		{
			name: "highspeed entitlement is actionable", status: 401,
			message:  "Your current subscription does not have access to kimi-for-coding-highspeed.",
			terminal: true, contains: "/model kimi-for-coding",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := newKimiProviderError(tc.status, 0, tc.errType, tc.message)
			if got := IsTerminalProviderError(err); got != tc.terminal {
				t.Errorf("terminal = %v, want %v (%T: %v)", got, tc.terminal, err, err)
			}
			if got := IsRetryableError(err); got != tc.retryable {
				t.Errorf("retryable = %v, want %v (%T: %v)", got, tc.retryable, err, err)
			}
			if got := IsOverloadError(err); got != tc.overload {
				t.Errorf("overload = %v, want %v (%T: %v)", got, tc.overload, err, err)
			}
			if got := IsContextTooLongError(err); got != tc.contextLong {
				t.Errorf("context too long = %v, want %v (%T: %v)", got, tc.contextLong, err, err)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("error %q does not contain %q", err, tc.contains)
			}
		})
	}
}

func TestKimiHTTPQuotaErrorBecomesTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"You've reached your usage limit for this period. Your quota will be refreshed in the next period."}}`)
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model: "k3", BaseURL: srv.URL, APIKey: "test", Provider: "kimi",
			StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}
	_, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err == nil || !IsTerminalProviderError(err) {
		t.Fatalf("error = %T %v, want terminal Kimi quota error", err, err)
	}
	if IsRetryableError(err) || IsOverloadError(err) {
		t.Fatalf("terminal Kimi quota entered retry path: %v", err)
	}
}

func TestKimiStreamQuotaErrorBecomesTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"You've reached kimi monthly usage limit for this billing cycle.\"}}\n\n")
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model: "k3", BaseURL: srv.URL, APIKey: "test", Provider: "kimi",
			StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}
	stream, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	_, streamErr := stream.Collect()
	if streamErr == nil || !IsTerminalProviderError(streamErr) {
		t.Fatalf("stream error = %T %v, want terminal Kimi quota error", streamErr, streamErr)
	}
	if IsRetryableError(streamErr) || IsOverloadError(streamErr) {
		t.Fatalf("terminal streamed quota entered retry path: %v", streamErr)
	}
}

func TestKimiStreamOverloadRemainsRetryable(t *testing.T) {
	c := &AnthropicClient{config: AnthropicConfig{Provider: "kimi", BaseURL: DefaultKimiBaseURL}}
	chunk := c.processStreamEvent(map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "overloaded_error",
			"message": "The engine is currently overloaded, please try again later",
		},
	}, &toolCallAccumulator{})
	if chunk.Error == nil || !IsRetryableError(chunk.Error) || !IsOverloadError(chunk.Error) {
		t.Fatalf("overload classification = %T %v", chunk.Error, chunk.Error)
	}
	if IsTerminalProviderError(chunk.Error) {
		t.Fatalf("transient overload became terminal: %v", chunk.Error)
	}
}
