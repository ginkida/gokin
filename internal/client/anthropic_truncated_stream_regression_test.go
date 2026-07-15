package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"google.golang.org/genai"
)

// A clean HTTP EOF is not a successful SSE completion. In particular, GLM's
// Anthropic-compatible endpoint can have delivered a complete tool_use block
// before the connection is truncated; silently accepting that EOF used to drop
// the call and turn a retryable transport failure into an empty model response.
func TestAnthropicStream_TruncatedAfterCompleteToolUseReturnsPartialAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"read\",\"input\":{}}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\\\"main.go\\\"}\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		// Deliberately omit message_delta/message_stop/[DONE] and close the body.
	}))
	defer srv.Close()

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

	stream, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("inspect main.go", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	resp, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ProcessStream error = %v, want io.ErrUnexpectedEOF", err)
	}
	if resp == nil || len(resp.FunctionCalls) != 1 {
		t.Fatalf("partial function calls = %#v, want one preserved call", resp)
	}
	call := resp.FunctionCalls[0]
	if call.ID != "call_1" || call.Name != "read" || call.Args["path"] != "main.go" {
		t.Fatalf("partial function call = %#v", call)
	}
}

func TestAnthropicStream_MalformedToolDeltaFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_bad\",\"name\":\"edit\",\"input\":{}}}\n\n")
		// A malformed envelope can represent a lost argument fragment. Continuing
		// and executing whatever JSON remains would be a fail-open mutation.
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",BROKEN}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

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
	stream, err := c.SendMessageWithHistory(context.Background(),
		[]*genai.Content{genai.NewContentFromText("edit main.go", genai.RoleUser)}, "")
	if err != nil {
		t.Fatalf("SendMessageWithHistory: %v", err)
	}
	resp, err := ProcessStream(context.Background(), stream, &StreamHandler{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("ProcessStream error = %v, want retryable malformed-stream error", err)
	}
	if resp == nil {
		t.Fatal("partial response is nil")
	}
	if len(resp.FunctionCalls) != 0 {
		t.Fatalf("malformed tool call escaped as executable output: %#v", resp.FunctionCalls)
	}
}

// Both ctx.Done and a closed chunks channel can be ready in ProcessStream's
// select. Whichever case wins, cancellation is terminal and must never become a
// successful empty response.
func TestProcessStream_ClosedChannelCannotMaskCancellation(t *testing.T) {
	cause := errors.New("user cancelled turn")
	for i := 0; i < 128; i++ {
		ctx, cancel := context.WithCancelCause(context.Background())
		chunks := make(chan ResponseChunk)
		close(chunks)
		cancel(cause)

		resp, err := ProcessStream(ctx, &StreamingResponse{Chunks: chunks}, &StreamHandler{})
		if !errors.Is(err, cause) {
			t.Fatalf("iteration %d: response=%#v error=%v, want cancellation cause", i, resp, err)
		}
	}
}
