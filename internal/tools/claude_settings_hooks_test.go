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
	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func settingsExecutorFixture(t *testing.T, command string) (*Executor, *hooks.Manager, string, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user := t.TempDir()
	settingsPath := filepath.Join(user, "settings.json")
	settings := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Write", "hooks": []any{map[string]any{"type": "command", "command": strings.ReplaceAll(command, "TEST_ROOT", root)}}}}}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry()
	registry.MustRegister(NewWriteTool(root))
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetWorkDir(root)
	executor.SetSessionID("settings-session")
	manager := hooks.NewManager(true, root)
	manager.ConfigureClaudeSettings(root, user, false)
	executor.SetHooks(manager)
	return executor, manager, root, settingsPath
}

func TestClaudeSettingsGLMWireRewritesWithoutSkillAndPreservesProviderHistory(t *testing.T) {
	for _, phase := range []string{"rewrite", "stop", "managed-rewrite", "managed-stop", "plugin-rewrite", "plugin-stop"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("GOKIN_PROVIDER_HEALTH_FILE", filepath.Join(t.TempDir(), "health.json"))
			output := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"file_path":"TEST_ROOT/actual.txt","content":"actual"},"additionalContext":"Use the actual target"}}`
			if strings.HasSuffix(phase, "stop") {
				output = `{"continue":false,"stopReason":"Stop settings request"}`
			}
			executor, manager, root, source := settingsExecutorFixture(t, "cat > settings-input.json; printf '%s' '"+output+"'")
			if strings.HasPrefix(phase, "managed-") {
				managed := t.TempDir()
				if err := os.Rename(source, filepath.Join(managed, "managed-settings.json")); err != nil {
					t.Fatal(err)
				}
				manager.ConfigureClaudeManagedSettings(managed)
				manager.SetEnabled(false)
				// Disabled optional settings cannot interfere with mandatory enforcement.
				if err := os.WriteFile(source, []byte("ignored optional file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.HasPrefix(phase, "plugin-") {
				plugin := t.TempDir()
				hookPath := filepath.Join(plugin, "hooks", "hooks.json")
				if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(source, hookPath); err != nil {
					t.Fatal(err)
				}
				manager.ConfigureClaudePlugins([]string{plugin})
				manager.SetEnabled(false)
			}
			original := filepath.Join(root, "original.txt")
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				mu.Lock()
				requests = append(requests, request)
				index := len(requests) - 1
				mu.Unlock()
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10}}}\n\n")
				stop := "end_turn"
				if index == 0 {
					event, _ := json.Marshal(map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "settings-call", "name": "write", "input": map[string]any{"file_path": original, "content": "original"}}})
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
			history, response, err := executor.Execute(t.Context(), nil, "Write a file.")
			want := 2
			if strings.HasSuffix(phase, "stop") {
				want = 1
				if !IsHookStopError(err) {
					t.Fatalf("stop lost: %v", err)
				}
			} else if err != nil || response != "Reviewed." {
				t.Fatalf("response=%q error=%v", response, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != want {
				t.Fatalf("requests=%d want=%d", len(requests), want)
			}
			paired := false
			for _, message := range history {
				for _, part := range message.Parts {
					if call := part.FunctionCall; call != nil && call.Args["file_path"] != original {
						t.Fatal("provider history was rewritten")
					}
					if result := part.FunctionResponse; result != nil && result.ID == "settings-call" {
						paired = true
					}
				}
			}
			if !paired {
				t.Fatal("tool-use result pair lost")
			}
			if _, err := os.Stat(original); !os.IsNotExist(err) {
				t.Fatal("original target was written")
			}
			if strings.HasSuffix(phase, "rewrite") {
				if data, err := os.ReadFile(filepath.Join(root, "actual.txt")); err != nil || string(data) != "actual" {
					t.Fatalf("actual=%q %v", data, err)
				}
				data, _ := json.Marshal(requests[1]["messages"])
				if !strings.Contains(string(data), "Use the actual target") {
					t.Fatal("hook context missing from GLM request")
				}
			}
			data, err := os.ReadFile(filepath.Join(root, "settings-input.json"))
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]any
			if err := json.Unmarshal(data, &input); err != nil || input["session_id"] != "settings-session" || input["tool_use_id"] != "settings-call" || input["effort"].(map[string]any)["level"] != "high" {
				t.Fatalf("hook input=%v %v", input, err)
			}
		})
	}
}

func TestClaudeSettingsChangeAfterPreHookStopsToolBeforeExecution(t *testing.T) {
	executor, _, root, path := settingsExecutorFixture(t, "exit 0")
	executor.SetHandler(&ExecutionHandler{OnToolStart: func(string, map[string]any) {
		if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"exit 2"}]}]}}`), 0o600); err != nil {
			t.Error(err)
		}
	}})
	call := &genai.FunctionCall{ID: "waiting-call", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"}}
	result := executor.doExecuteTool(t.Context(), call)
	if result.HookStopReason == "" || !strings.Contains(result.HookStopReason, "configuration changed") {
		t.Fatalf("changed settings did not stop pending call: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatal("tool ran under stale checks")
	}
}

func TestClaudeSettingsInvalidStateBlocksExecutorBeforeProviderWithoutSkillTool(t *testing.T) {
	executor, _, _, path := settingsExecutorFixture(t, "exit 0")
	if err := os.WriteFile(path, []byte(`{"hooks":{"StopFailure":[]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().EnqueueText("must not request")
	executor.SetClient(mock)
	_, _, err := executor.Execute(t.Context(), nil, "Continue.")
	if !IsHookStopError(err) || len(mock.Calls()) != 0 {
		t.Fatalf("invalid settings reached provider: %v calls=%d", err, len(mock.Calls()))
	}
}

func TestHookRewrittenWriteCheckpointUsesProviderInput(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained=%t", retained), func(t *testing.T) {
			executor, _, root, _ := settingsExecutorFixture(t, `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"file_path":"TEST_ROOT/actual.txt","content":"actual"}}}'`)
			call := &genai.FunctionCall{ID: "original", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "original.txt"), "content": "original"}}
			result := executor.executeTool(t.Context(), call)
			if !result.Success {
				t.Fatalf("initial write failed: %+v", result)
			}
			if data, err := os.ReadFile(filepath.Join(root, "actual.txt")); err != nil || string(data) != "actual" {
				t.Fatalf("rewrite=%q %v", data, err)
			}
			if call.Args["content"] != "original" {
				t.Fatal("provider input mutated")
			}
			if retained {
				executor.AcknowledgeToolResults([]*genai.Content{
					{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: call}}},
					{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: call.ID, Name: call.Name, Response: result.ToMap()}}}},
				})
				if n := executor.GetCheckpointJournal().Len(); n != 0 {
					t.Fatalf("acknowledged rewritten write left %d checkpoints", n)
				}
			} else {
				// A retry must replay by the original signature, even with a new ID,
				// without rerunning hooks or changing an externally edited target.
				if err := os.WriteFile(filepath.Join(root, "actual.txt"), []byte("external"), 0600); err != nil {
					t.Fatal(err)
				}
				executor.SetSideEffectDedup(true)
				replay := *call
				replay.ID = "retry"
				got := executor.executeTool(t.Context(), &replay)
				if !got.Success || got.Content != result.Content {
					t.Fatalf("original call could not replay: %+v", got)
				}
				if data, err := os.ReadFile(filepath.Join(root, "actual.txt")); err != nil || string(data) != "external" {
					t.Fatalf("retry repeated write: %q %v", data, err)
				}
			}
		})
	}
}
