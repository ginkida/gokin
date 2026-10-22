package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCwdChangedContractAndNonblockingControls(t *testing.T) {
	for _, kind := range []string{"prompt", "agent"} {
		if _, err := parseClaudeTest(t, "CwdChanged: [{hooks: [{type: "+kind+", prompt: inspect}]}]"); err == nil {
			t.Fatal("accepted model hook", kind)
		}
	}
	defs, err := parseClaudeTest(t, "CwdChanged: [{matcher: '[', hooks: [{type: command, command: echo context}]}]")
	if err != nil || len(defs) != 1 || defs[0].Matcher != "" {
		t.Fatal("cwd matcher was not ignored", defs, err)
	}
	hook := &Hook{Type: CwdChanged, Claude: &defs[0]}
	hctx := NewContext("", nil, "/new")
	hctx.OldCwd, hctx.NewCwd = "/old", "/new"
	data, _, err := claudeInput(ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "root", PermissionMode: "acceptEdits"}), hook, hctx)
	var input map[string]any
	if err != nil || json.Unmarshal(data, &input) != nil || input["cwd"] != "/new" || input["old_cwd"] != "/old" || input["new_cwd"] != "/new" || input["tool_name"] != nil || input["permission_mode"] != "acceptEdits" {
		t.Fatalf("wrong cwd input: %s %v", data, err)
	}
	for _, code := range []int{0, 2} {
		result := interpretClaudeResult(Result{Hook: hook}, `{"continue":false,"decision":"block","systemMessage":"notification","hookSpecificOutput":{"watchPaths":[],"additionalContext":"NO_MODEL_CONTEXT","sessionTitle":"no rename"}}`, "stderr", code)
		if result.StopReason != "" || result.BlockReason != "" || result.AdditionalContext != "" || result.Output != "" || result.SessionTitle != "" || (result.WatchPaths != nil) != (code == 0) {
			t.Fatal("cwd output changed execution", result)
		}
	}
}

func TestCwdChangedCommandHTTPAndMCP(t *testing.T) {
	for _, kind := range []string{"command", "http", "mcp_tool"} {
		t.Run(kind, func(t *testing.T) {
			root, user, newDir := t.TempDir(), t.TempDir(), t.TempDir()
			m := NewManager(true, root)
			m.ConfigureClaudeSettings(root, user, true)
			response := `{"continue":false,"systemMessage":"cwd notification","hookSpecificOutput":{"watchPaths":[]}}`
			handler := map[string]any{"type": kind}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input["old_cwd"] != root || input["new_cwd"] != newDir {
					t.Error("HTTP cwd missing", input)
				}
				fmt.Fprint(w, response)
			}))
			defer server.Close()
			switch kind {
			case "command":
				handler["command"] = `printf 'export CWD_TEST=ready\n' >> "$CLAUDE_ENV_FILE"; printf '%s' ` + shellEscape(response)
			case "http":
				handler["url"] = server.URL
			case "mcp_tool":
				handler["server"], handler["tool"] = "fixture", "check"
				handler["input"] = map[string]any{"from": "${old_cwd}", "to": "${new_cwd}"}
				m.SetMCPHookInvoker(func(_ context.Context, _, _ string, args map[string]any) (MCPHookOutput, error) {
					if args["from"] != root || args["to"] != newDir {
						t.Error("MCP cwd missing", args)
					}
					return MCPHookOutput{Text: response}, nil
				})
			}
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"CwdChanged": []any{map[string]any{"matcher": "unused", "hooks": []any{handler}}}}})
			env := filepath.Join(root, "environment.sh")
			results := m.RunCwdChanged(ContextWithRuntimeInfo(t.Context(), RuntimeInfo{EnvFile: env}), root, newDir)
			if len(results) != 1 || results[0].Error != nil || results[0].StopReason != "" || results[0].SystemMessage != "cwd notification" || results[0].WatchPaths == nil {
				t.Fatalf("wrong cwd result: %+v", results)
			}
			if kind == "command" {
				if _, err := os.Stat(env); err != nil {
					t.Fatal("cwd env file unavailable", err)
				}
			}
		})
	}
}

func TestChildScopeMasksRootCwdPublication(t *testing.T) {
	notifications, transactions := 0, 0
	ctx := ContextWithCwdChanges(t.Context(), func(context.Context, string, string) { notifications++ }, func(ctx context.Context, update func() (string, string)) {
		transactions++
		old, new := update()
		NotifyCwdChanged(ctx, old, new)
	})
	CommitShellState(ctx, func() (string, string) { return "/old", "/new" })
	child := ContextWithAsyncScope(ctx, nil)
	CommitShellState(child, func() (string, string) { return "/child", "/child/new" })
	if notifications != 1 || transactions != 1 {
		t.Fatal("child altered root cwd", notifications, transactions)
	}
}
