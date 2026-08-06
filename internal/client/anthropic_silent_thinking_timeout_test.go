package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestSilentThinkingDefersToModelRoundDeadline(t *testing.T) {
	tests := []struct {
		name           string
		provider       string
		model          string
		enableThinking bool
	}{
		{name: "explicit GLM thinking", provider: "glm", model: "glm-5.2", enableThinking: true},
		{name: "always-on K3 reasoning", provider: "kimi", model: "k3", enableThinking: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newSilentSSEServer(t)
			client := &AnthropicClient{
				config: AnthropicConfig{
					Model:             tt.model,
					BaseURL:           srv.URL,
					APIKey:            "test",
					Provider:          tt.provider,
					EnableThinking:    tt.enableThinking,
					StreamIdleTimeout: 20 * time.Millisecond,
				},
				httpClient: srv.Client(),
			}
			cause := NewModelRoundTimeoutError(120 * time.Millisecond)
			ctx, cancel := context.WithTimeoutCause(context.Background(), 120*time.Millisecond, cause)
			defer cancel()

			stream, err := client.SendMessageWithHistory(ctx,
				[]*genai.Content{genai.NewContentFromText("think deeply", genai.RoleUser)}, "")
			if err != nil {
				t.Fatalf("SendMessageWithHistory: %v", err)
			}
			_, err = ProcessStream(ctx, stream, &StreamHandler{})
			if !errors.Is(err, ErrModelRoundTimeout) {
				t.Fatalf("silent thinking error = %v, want model round timeout", err)
			}
			if IsStreamIdleTimeout(err) {
				t.Fatalf("stream-idle watchdog beat the authoritative round deadline: %v", err)
			}
		})
	}
}

func TestSilentNonThinkingStreamKeepsIdleWatchdog(t *testing.T) {
	srv := newSilentSSEServer(t)
	client := &AnthropicClient{
		config: AnthropicConfig{
			Model:             "deepseek-chat",
			BaseURL:           srv.URL,
			APIKey:            "test",
			Provider:          "deepseek",
			EnableThinking:    false,
			StreamIdleTimeout: 20 * time.Millisecond,
		},
		httpClient: srv.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stream, err := client.SendMessageWithHistory(ctx,
		[]*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	_, err = ProcessStream(ctx, stream, &StreamHandler{})
	if !IsStreamIdleTimeout(err) {
		t.Fatalf("silent non-thinking error = %v, want stream idle timeout", err)
	}
}

func TestThinkingStreamUsesStrictIdleWatchdogAfterFirstContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"partial thought\"}}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	client := &AnthropicClient{
		config: AnthropicConfig{
			Model:             "glm-5.2",
			BaseURL:           srv.URL,
			APIKey:            "test",
			Provider:          "glm",
			EnableThinking:    true,
			StreamIdleTimeout: 20 * time.Millisecond,
		},
		httpClient: srv.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	stream, err := client.SendMessageWithHistory(ctx,
		[]*genai.Content{genai.NewContentFromText("think", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	response, err := ProcessStream(ctx, stream, &StreamHandler{})
	if !IsStreamIdleTimeout(err) {
		t.Fatalf("post-content thinking error = %v, want stream idle timeout", err)
	}
	telemetry := DetectFailureTelemetry(err)
	if !telemetry.Partial {
		t.Fatalf("post-content idle telemetry = %#v, want partial=true", telemetry)
	}
	if response == nil || response.Thinking != "partial thought" {
		t.Fatalf("partial thinking was not preserved: %#v", response)
	}
}

func newSilentSSEServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}
