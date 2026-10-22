package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestAnthropicToolResultPayloadSurvivesContinuationAndHistoryReplay(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response map[string]any
		want     string
	}{
		{"structured", map[string]any{"value": "gokin-glm-probe-ok", "items": []any{"one", true}}, `{"items":["one",true],"value":"gokin-glm-probe-ok"}`},
		{"structured-content", map[string]any{"content": map[string]any{"count": 3}}, `{"content":{"count":3}}`},
		{"text-envelope", map[string]any{"content": "plain result", "success": true}, "plain result"},
		{"data-envelope", map[string]any{"content": "", "data": map[string]any{"value": "nested"}}, `{"value":"nested"}`},
		{"error-envelope", map[string]any{"content": "unused", "error": "probe failed"}, "Error: probe failed"},
	} {
		for _, replay := range []bool{false, true} {
			name := "continuation"
			if replay {
				name = "history"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				requests := make(chan map[string]any, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests <- body
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n")
				}))
				defer server.Close()
				c, err := NewAnthropicClient(AnthropicConfig{APIKey: "test-key", Provider: "glm", Model: "glm-5.3", BaseURL: server.URL})
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				c.setDirectHealthTracking(false)
				c.SetSystemInstruction("Summarize the tool result.")
				history := []*genai.Content{
					genai.NewContentFromText("Call probe.", genai.RoleUser),
					genai.NewContentFromParts([]*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "probe-1", Name: "probe", Args: map[string]any{}}}}, genai.RoleModel),
				}
				result := &genai.FunctionResponse{ID: "probe-1", Name: "probe", Response: tc.response}
				var stream *StreamingResponse
				if replay {
					history = append(history, genai.NewContentFromParts([]*genai.Part{{FunctionResponse: result}}, genai.RoleUser))
					stream, err = c.SendMessageWithHistory(context.Background(), history, "Summarize the previous result.")
				} else {
					stream, err = c.SendFunctionResponse(context.Background(), history, []*genai.FunctionResponse{result})
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := stream.Collect(); err != nil {
					t.Fatal(err)
				}
				body := <-requests
				found := 0
				for _, message := range body["messages"].([]any) {
					blocks, _ := message.(map[string]any)["content"].([]any)
					for _, rawBlock := range blocks {
						block := rawBlock.(map[string]any)
						if block["type"] == "tool_result" {
							found++
							if block["tool_use_id"] != "probe-1" || block["content"] != tc.want {
								t.Fatalf("tool result payload was lost: %#v, want %q", block, tc.want)
							}
						}
					}
				}
				if found != 1 {
					t.Fatalf("sent %d tool results, want exactly one", found)
				}
			})
		}
	}
}

func TestAnthropicUnserializableToolResultDoesNotClaimSuccess(t *testing.T) {
	for _, response := range []map[string]any{{"value": make(chan int)}, {"data": make(chan int)}} {
		if got := anthropicToolResultContent(response); !strings.HasPrefix(got, "Error:") {
			t.Fatalf("unserializable result claimed success: %q", got)
		}
	}
}
