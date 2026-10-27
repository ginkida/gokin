package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDegenerateOutputStreaming(t *testing.T) {
	for _, tool := range []bool{true, false} {
		t.Run(fmt.Sprint("tool=", tool), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event any) {
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "data: %s\n\n", data)
					w.(http.Flusher).Flush()
				}
				if tool {
					emit(map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "id": "bad", "name": "repl_exec", "input": map[string]any{}}})
					emit(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"code":"`}})
				}
				for range 1024 {
					delta := map[string]any{"type": "text_delta", "text": "data = context.artifact_get('result'); "}
					if tool {
						delta = map[string]any{"type": "input_json_delta", "partial_json": "data = context.artifact_get('result'); "}
					}
					emit(map[string]any{"type": "content_block_delta", "delta": delta})
				}
				if tool {
					emit(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": `"}`}})
					emit(map[string]any{"type": "content_block_stop"})
				}
				emit(map[string]any{"type": "message_stop"})
			}))
			defer server.Close()
			c, err := NewAnthropicClient(AnthropicConfig{APIKey: "test", Model: "glm-5", Provider: "glm", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := c.SendMessage(ctx, "inspect")
			if err != nil {
				t.Fatal(err)
			}
			response, err := stream.CollectContext(ctx)
			if err == nil || !strings.Contains(err.Error(), "degenerate") {
				t.Fatalf("active repeating stream was accepted: err=%v", err)
			}
			if response != nil && len(response.FunctionCalls) != 0 {
				t.Fatal("partial tool call escaped")
			}
		})
	}
}
