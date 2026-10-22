package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestDirectoryAddedContractAndTransports(t *testing.T) {
	for _, kind := range []string{"prompt", "agent"} {
		if _, err := parseClaudeTest(t, "DirectoryAdded: [{hooks: [{type: "+kind+", prompt: inspect}]}]"); err == nil {
			t.Fatal("accepted unsupported handler", kind)
		}
	}
	for _, kind := range []string{"command", "http", "mcp_tool"} {
		t.Run(kind, func(t *testing.T) {
			root, added := t.TempDir(), t.TempDir()
			m := NewManager(true, root)
			m.ConfigureClaudeSettings(root, t.TempDir(), true)
			response := `{"continue":false,"stopReason":"no stop","decision":"block","systemMessage":"directory ready","hookSpecificOutput":{"additionalContext":"discard","watchPaths":["relative ignored"],"reloadSkills":true,"sessionTitle":"no rename"}}`
			checkInput := func(input map[string]any) {
				if input["directory"] != added || input["source"] != "slash_command" || input["cwd"] != root || input["session_id"] != "root-session" || input["tool_name"] != nil {
					t.Error("wrong directory identity", input)
				}
			}
			handler := map[string]any{"type": kind}
			switch kind {
			case "command":
				handler["command"] = "printf '%s' " + shellEscape(response)
			case "http":
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var input map[string]any
					_ = json.NewDecoder(r.Body).Decode(&input)
					checkInput(input)
					fmt.Fprint(w, response)
				}))
				defer server.Close()
				handler["url"] = server.URL
			case "mcp_tool":
				handler["server"], handler["tool"] = "fixture", "check"
				handler["input"] = map[string]any{"directory": "${directory}", "source": "${source}"}
				m.SetMCPHookInvoker(func(_ context.Context, _, _ string, input map[string]any) (MCPHookOutput, error) {
					if input["directory"] != added || input["source"] != "slash_command" {
						t.Error("MCP source substitution failed", input)
					}
					return MCPHookOutput{Text: response}, nil
				})
			}
			user, _ := m.ClaudeSettingsDirectories()
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"DirectoryAdded": []any{map[string]any{"matcher": "^slash_command$", "hooks": []any{handler}}}}})
			ctx := ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "root-session"})
			results := m.RunDirectoryAdded(ctx, root, added, "slash_command")
			if len(results) != 1 {
				t.Fatal("directory hook did not run", results)
			}
			result := results[0]
			if result.Error != nil || result.SystemMessage != "directory ready" || result.StopReason != "" || result.BlockReason != "" || result.AdditionalContext != "" || result.Output != "" || result.WatchPaths != nil || result.ReloadSkills || result.SessionTitle != "" {
				t.Fatal("directory handler changed execution", result)
			}
			if result.Hook.Claude.Timeout != 600*time.Second {
				t.Fatal("wrong setup timeout", result.Hook.Claude.Timeout)
			}
			if got := m.RunDirectoryAdded(ctx, root, added, "register_repo_root"); len(got) != 0 {
				t.Fatal("source matcher ignored", got)
			}
		})
	}
}

func TestDirectoryAddedInputAndFailedOutput(t *testing.T) {
	hook := &Hook{Type: DirectoryAdded, Claude: &ClaudeCommand{Event: "DirectoryAdded"}}
	hctx := NewContext("slash_command", nil, "/working")
	hctx.Directory, hctx.Source = "/additional", "slash_command"
	data, _, err := claudeInput(ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "root-session"}), hook, hctx)
	var input map[string]any
	if err != nil || json.Unmarshal(data, &input) != nil || input["directory"] != "/additional" || input["source"] != "slash_command" || input["cwd"] != "/working" || input["tool_name"] != nil {
		t.Fatalf("invalid directory input: %s %v", data, err)
	}
	result := interpretClaudeResult(Result{Hook: hook}, `{"continue":false,"systemMessage":"failed output"}`, "private stderr", 2)
	if result.SystemMessage != "" || result.Output != "" || result.StopReason != "" {
		t.Fatal("failed directory hook affected execution", result)
	}
}
