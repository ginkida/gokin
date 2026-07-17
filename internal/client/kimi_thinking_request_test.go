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

func TestKimiThinkingOffIsExplicitOnBothRequestPaths(t *testing.T) {
	requests := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := &AnthropicClient{
		config: AnthropicConfig{
			Model: "k3", BaseURL: srv.URL, APIKey: "test", Provider: "kimi",
			EnableThinking: false, ThinkingBudget: 8192,
			StreamIdleTimeout: 5 * time.Second,
		},
		httpClient: srv.Client(),
	}

	history := []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}
	stream, err := c.SendMessageWithHistory(context.Background(), history, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("collect message response: %v", err)
	}

	stream, err = c.SendFunctionResponse(context.Background(), history, []*genai.FunctionResponse{{
		ID: "tool-1", Name: "read", Response: map[string]any{"result": "ok"},
	}})
	if err != nil {
		t.Fatalf("SendFunctionResponse: %v", err)
	}
	if _, err := stream.Collect(); err != nil {
		t.Fatalf("collect function response: %v", err)
	}

	for i := 0; i < 2; i++ {
		body := <-requests
		thinking, ok := body["thinking"].(map[string]any)
		if !ok {
			t.Fatalf("request %d omitted explicit Kimi thinking config: %#v", i+1, body["thinking"])
		}
		if got := thinking["type"]; got != "disabled" {
			t.Fatalf("request %d thinking.type = %#v, want disabled", i+1, got)
		}
		if _, exists := thinking["budget_tokens"]; exists {
			t.Fatalf("request %d disabled thinking retained budget_tokens", i+1)
		}
	}
}
