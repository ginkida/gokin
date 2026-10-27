package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
	"google.golang.org/genai"
)

type outputRecoveryProbe struct{ calls int }

func (*outputRecoveryProbe) Name() string        { return "read" }
func (*outputRecoveryProbe) Description() string { return "probe" }
func (*outputRecoveryProbe) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{Name: "read"}
}
func (*outputRecoveryProbe) Validate(map[string]any) error { return nil }
func (p *outputRecoveryProbe) Execute(context.Context, map[string]any) (ToolResult, error) {
	p.calls++
	return NewSuccessResult("already inspected"), nil
}

func TestExecutorDegenerateRecoveryAfterToolResults(t *testing.T) {
	for _, recover := range []bool{true, false} {
		t.Run(fmt.Sprint(recover), func(t *testing.T) {
			var requests []map[string]any
			var requestMessages [][]json.RawMessage
			var mu sync.Mutex
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var raw json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					t.Error(err)
					return
				}
				var request map[string]any
				if err := json.Unmarshal(raw, &request); err != nil {
					t.Error(err)
					return
				}
				var wire struct{ Messages []json.RawMessage }
				if err := json.Unmarshal(raw, &wire); err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				requests = append(requests, request)
				requestMessages = append(requestMessages, wire.Messages)
				index := len(requests)
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				emit := func(event any) { data, _ := json.Marshal(event); fmt.Fprintf(w, "data: %s\n\n", data) }
				if index == 1 {
					emit(map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "id": "read-1", "name": "read", "input": map[string]any{}}})
					emit(map[string]any{"type": "content_block_stop"})
				} else if index == 3 && recover {
					emit(map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "id": "read-2", "name": "read", "input": map[string]any{"file_path": "second.txt"}}})
					emit(map[string]any{"type": "content_block_stop"})
				} else if index == 4 && recover {
					emit(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "Finished inspecting."}})
				} else {
					emit(map[string]any{"type": "content_block_start", "content_block": map[string]any{"type": "tool_use", "id": "bad", "name": "read", "input": map[string]any{}}})
					emit(map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "input_json_delta", "partial_json": strings.Repeat("data = context.artifact_get('x'); ", 2000)}})
				}
				emit(map[string]any{"type": "message_stop"})
			}))
			defer server.Close()
			c, err := client.NewAnthropicClient(client.AnthropicConfig{Model: "glm-5", Provider: "glm", APIKey: "test", BaseURL: server.URL, Temperature: 1})
			if err != nil {
				t.Fatal(err)
			}
			c.SetSystemInstruction("Stable system instruction.")
			probe := &outputRecoveryProbe{}
			registry := NewRegistry()
			registry.MustRegister(probe)
			c.SetTools(registry.GeminiTools())
			executor := NewExecutor(registry, c, time.Second)
			history, text, err := executor.Execute(t.Context(), nil, "inspect")
			if recover && (err != nil || text != "Finished inspecting.") {
				t.Fatalf("recovery failed: %q %v", text, err)
			}
			if !recover {
				failure, ok := client.OutputRecoveryError(err)
				if !ok || !failure.Exhausted || client.IsRetryableError(err) {
					t.Fatalf("second trip escaped: %v", err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			wantTools, wantRequests := 1, 3
			if recover {
				wantTools, wantRequests = 2, 4
			}
			if probe.calls != wantTools || len(requests) != wantRequests {
				t.Fatalf("tools=%d requests=%d; want %d and %d", probe.calls, len(requests), wantTools, wantRequests)
			}
			before, after := requests[1], requests[2]
			if !reflect.DeepEqual(before["system"], after["system"]) || !reflect.DeepEqual(before["tools"], after["tools"]) {
				t.Fatal("retry changed system/tools prefix")
			}
			prior := before["messages"].([]any)
			retried := after["messages"].([]any)
			if len(retried) != len(prior)+2 || !reflect.DeepEqual(prior, retried[:len(prior)]) {
				t.Fatal("retry rewrote prior messages instead of appending guidance")
			}
			if before["temperature"] != float64(1) || after["temperature"] != float64(0.2) {
				t.Fatalf("temperatures=%v/%v", before["temperature"], after["temperature"])
			}
			if recover {
				for i, message := range requestMessages[2] {
					if string(message) != string(requestMessages[3][i]) {
						t.Fatalf("following tool round changed earlier message %d: before=%s after=%s", i, message, requestMessages[3][i])
					}
				}
			}
			calls, results := 0, 0
			for _, msg := range history {
				for _, part := range msg.Parts {
					if part.FunctionCall != nil {
						calls++
					}
					if part.FunctionResponse != nil {
						results++
					}
				}
			}
			if calls != wantTools || results != wantTools {
				t.Fatalf("retained call/result pairs=%d/%d", calls, results)
			}
		})
	}
}
