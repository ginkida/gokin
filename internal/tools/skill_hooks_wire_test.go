package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gokin/internal/client"
)

type freshHookResultCompactor struct{}

func (freshHookResultCompactor) CompactForType(_ string, result ToolResult) ToolResult {
	return NewSuccessResult(result.Content)
}

func TestSkillHooksGLMWirePreservesOriginalToolInputAndHaltsAfterPairedResults(t *testing.T) {
	for _, phase := range []string{"rewrite", "pre_stop", "post_stop"} {
		t.Run(phase, func(t *testing.T) {
			halt := phase != "rewrite"
			post := phase == "post_stop"
			t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(t.TempDir(), "health.json"))
			output := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"file_path":"TEST_ROOT/actual.txt","content":"actual"},"additionalContext":"Review the actual target"}}`
			if halt {
				output = `{"continue":false,"stopReason":"Stop this execution"}`
			}
			command := "cat > hook-input.json; printf '%s' '" + output + "'"
			executor, _, root := hookSkillFixture(t, "Write", command, "", "Review code.")
			if post {
				path := filepath.Join(root, "guard", "SKILL.md")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), "PreToolUse", "PostToolUse")), 0o600); err != nil {
					t.Fatal(err)
				}

				executor.SetCompactor(freshHookResultCompactor{})
			}
			original := filepath.Join(root, "original.txt")
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				requests = append(requests, body)
				index := len(requests) - 1
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
				stop := "end_turn"
				if index < 2 {
					name, input := "skill", map[string]any{"name": "guard"}
					if index == 1 {
						name, input = "write", map[string]any{"file_path": original, "content": "original"}
					}
					event, _ := json.Marshal(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": fmt.Sprintf("call-%d", index), "name": name, "input": input}})
					fmt.Fprintf(w, "data: %s\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n", event)
					stop = "tool_use"
				} else {
					io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Reviewed.\"}}\n\n")
				}
				fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":1}}\n\ndata: {\"type\":\"message_stop\"}\n\n", stop)
			}))
			defer server.Close()
			c, err := client.NewAnthropicClient(client.AnthropicConfig{Provider: "glm", Model: "glm-5.3", APIKey: "test", BaseURL: server.URL, MaxTokens: 4096, ReasoningEffort: "high", StreamIdleTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetTools(executor.registry.GeminiTools())
			executor.SetClient(c)
			executor.SetSessionID("hook-session")
			history, text, err := executor.Execute(t.Context(), nil, "Review using the guard skill.")
			if halt {
				if err == nil || !strings.Contains(err.Error(), "Stop this execution") {
					t.Fatalf("halt error=%v", err)
				}
			} else if err != nil || text != "Reviewed." {
				t.Fatalf("text=%q err=%v", text, err)
			}
			mu.Lock()
			calls := len(requests)
			mu.Unlock()
			want := 3
			if halt {
				want = 2
			}
			if calls != want {
				t.Fatalf("requests=%d want=%d", calls, want)
			}
			paired := map[string]bool{}
			for _, message := range history {
				for _, part := range message.Parts {
					if call := part.FunctionCall; call != nil && call.Name == "write" && call.Args["file_path"] != original {
						t.Fatal("hook rewrote provider tool_use history")
					}
					if response := part.FunctionResponse; response != nil {
						paired[response.ID] = true
					}
				}
			}
			if !paired["call-0"] || !paired["call-1"] {
				t.Fatalf("unpaired history: %v", paired)
			}
			if _, err := os.Stat(original); !post && !os.IsNotExist(err) {
				t.Fatal("original target was written")
			}
			if post {
				if data, err := os.ReadFile(original); err != nil || string(data) != "original" {
					t.Fatalf("post-stop lost completed write: data=%q err=%v", data, err)
				}
			}
			if !halt {
				if data, err := os.ReadFile(filepath.Join(root, "actual.txt")); err != nil || string(data) != "actual" {
					t.Fatalf("actual=%q err=%v", data, err)
				}
				encoded, _ := json.Marshal(requests[2]["messages"])
				if !strings.Contains(string(encoded), "Review the actual target") {
					t.Fatal("hook context missing from next provider request")
				}
			}
			data, err := os.ReadFile(filepath.Join(root, "hook-input.json"))
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]any
			if err := json.Unmarshal(data, &input); err != nil {
				t.Fatal(err)
			}
			if input["session_id"] != "hook-session" || input["tool_use_id"] != "call-1" || input["effort"].(map[string]any)["level"] != "high" {
				t.Fatalf("hook input=%v", input)
			}
		})
	}
}
