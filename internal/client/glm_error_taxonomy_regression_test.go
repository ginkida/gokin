package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
)

// A provider business code must win over a retryable outer HTTP status. Codes
// 1316-1321 were missing from the terminal set, so a 429 could previously enter
// the generic retry budget instead of surfacing the account limit immediately.
func TestGLM1316_HTTP429_BusinessCodeWinsOverStatus(t *testing.T) {
	const body = `{"error":{"code":1316,"message":"Usage limit reached. Resets at 2026-07-16 09:30:00"}}`

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model:             "glm-5.2",
			BaseURL:           srv.URL,
			APIKey:            "test",
			Provider:          "glm",
			MaxRetries:        3,
			RetryDelay:        time.Millisecond,
			StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}

	_, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, "")
	if err == nil {
		t.Fatal("expected terminal GLM error")
	}
	var terminal *TerminalProviderError
	if !errors.As(err, &terminal) {
		t.Fatalf("error type = %T, want *TerminalProviderError: %v", err, err)
	}
	if terminal.Code != "1316" || terminal.Status != http.StatusTooManyRequests {
		t.Errorf("terminal metadata = code %q status %d", terminal.Code, terminal.Status)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("request count = %d, want 1; terminal business errors must not retry", got)
	}
	if IsRetryableError(err) || IsOverloadError(err) || IsTransientProviderError(err) {
		t.Errorf("terminal 1316 entered a retry/transient path: %v", err)
	}
	if !strings.Contains(err.Error(), "Resets at 2026-07-16 09:30:00") {
		t.Errorf("reset time was not preserved: %q", err.Error())
	}
}

func TestGLMStreamErrors_KeepTypedRetrySemantics(t *testing.T) {
	t.Run("sensitive content is terminal", func(t *testing.T) {
		err := collectGLMErrorEvent(t, `{"error":{"code":1301,"message":"request rejected"}}`)
		if !IsTerminalProviderError(err) {
			t.Fatalf("error type = %T, want terminal: %v", err, err)
		}
		if IsRetryableError(err) || IsOverloadError(err) || IsTransientProviderError(err) {
			t.Errorf("1301 entered a retry/transient path: %v", err)
		}
	})

	t.Run("network error is transient", func(t *testing.T) {
		err := collectGLMErrorEvent(t, `{"error":{"code":"1234","message":"upstream connection failed"}}`)
		if IsTerminalProviderError(err) {
			t.Fatalf("1234 incorrectly terminal: %v", err)
		}
		if !IsRetryableError(err) || !IsTransientProviderError(err) {
			t.Errorf("1234 must be retryable and transient: %v", err)
		}
		if IsOverloadError(err) {
			t.Errorf("1234 is a network fault, not capacity overload: %v", err)
		}
	})

	t.Run("prompt too long remains compactable", func(t *testing.T) {
		err := collectGLMErrorEvent(t, `{"error":{"code":"1261","message":"prompt too long"}}`)
		if !IsContextTooLongError(err) {
			t.Fatalf("1261 was not recognized as context overflow: %T %v", err, err)
		}
		if IsTerminalProviderError(err) || IsRetryableError(err) {
			t.Errorf("1261 must compact context, not retry unchanged or become terminal: %v", err)
		}
	})
}

func collectGLMErrorEvent(t *testing.T, event string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+event+"\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model:             "glm-5.2",
			BaseURL:           srv.URL,
			APIKey:            "test",
			Provider:          "glm",
			StreamIdleTimeout: 5 * time.Second,
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
		t.Fatal("expected a streamed GLM error")
	}
	return streamErr
}
