package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/genai"
)

func TestGLMProviderIdentitySurvivesCustomLoopbackBaseURL(t *testing.T) {
	var authorization, apiKey, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		apiKey = r.Header.Get("x-api-key")
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := &AnthropicClient{
		config:     AnthropicConfig{Model: "glm-5.2", BaseURL: server.URL, APIKey: "secret", Provider: "glm"},
		httpClient: server.Client(),
	}
	response, err := client.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	for range response.Chunks {
	}
	if authorization != "Bearer secret" || apiKey != "secret" {
		t.Fatalf("GLM auth headers through custom endpoint = Authorization %q, x-api-key %q", authorization, apiKey)
	}
	if path != "/v1/messages" {
		t.Fatalf("custom endpoint path = %q, want /v1/messages", path)
	}
	if client.supportsPromptCaching() {
		t.Fatal("custom loopback URL changed GLM prompt-caching semantics")
	}
}
