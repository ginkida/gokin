package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/genai"
)

func TestGLMThinkingRequestExplicitlyPreservesReasoning(t *testing.T) {
	var gotThinking map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		gotThinking, _ = body["thinking"].(map[string]any)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model:             "glm-5.2",
			BaseURL:           srv.URL,
			Provider:          "glm",
			APIKey:            "test",
			MaxTokens:         4096,
			EnableThinking:    true,
			ThinkingBudget:    1024,
			StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}
	stream, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("inspect", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	if _, err := ProcessStream(context.Background(), stream, &StreamHandler{}); err != nil {
		t.Fatalf("ProcessStream: %v", err)
	}
	if gotThinking == nil {
		t.Fatal("thinking config missing")
	}
	if clear, ok := gotThinking["clear_thinking"].(bool); !ok || clear {
		t.Fatalf("clear_thinking = %#v, want false", gotThinking["clear_thinking"])
	}
}
