package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileChangedContractAndContextIsolation(t *testing.T) {
	for _, handler := range []string{"prompt", "agent"} {
		if _, err := parseClaudeTest(t, "FileChanged: [{hooks: [{type: "+handler+", prompt: inspect}]}]"); err == nil {
			t.Fatal("accepted model handler", handler)
		}
	}
	root, user := t.TempDir(), t.TempDir()
	path, next := filepath.Join(root, "proof.json"), filepath.Join(root, "next.json")
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	payload, _ := json.Marshal(map[string]any{"continue": false, "decision": "block", "systemMessage": "Changed notification", "hookSpecificOutput": map[string]any{"hookEventName": "FileChanged", "additionalContext": "MUST_NOT_REACH_MODEL", "watchPaths": []string{next}}})
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"FileChanged": []any{
		map[string]any{"matcher": "proof.json", "hooks": []any{map[string]any{"type": "command", "command": `cat > input.json; printf 'export FILE_PROOF=ready\n' >> "$CLAUDE_ENV_FILE"; printf '%s' ` + shellEscape(string(payload))}}},
		map[string]any{"matcher": "other.json", "hooks": []any{map[string]any{"type": "command", "command": "exit 2"}}},
	}}})
	envFile := filepath.Join(root, "env.sh")
	ctx := ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "root", EnvFile: envFile})
	results := m.RunFileChanged(ctx, root, path, "change")
	if len(results) != 1 || results[0].Error != nil || results[0].AdditionalContext != "" || results[0].StopReason != "" || results[0].BlockReason != "" || results[0].Output != "" || results[0].SystemMessage != "Changed notification" || len(results[0].WatchPaths) != 1 || results[0].WatchPaths[0] != next {
		t.Fatalf("wrong observational result: %+v", results)
	}
	data, err := os.ReadFile(filepath.Join(root, "input.json"))
	var input map[string]any
	if err != nil || json.Unmarshal(data, &input) != nil || input["file_path"] != path || input["event"] != "change" || input["session_id"] != "root" || input["tool_name"] != nil {
		t.Fatalf("wrong FileChanged input: %s %v", data, err)
	}
	data, err = os.ReadFile(envFile)
	if err != nil || !strings.Contains(string(data), "FILE_PROOF=ready") {
		t.Fatal("environment file was not available", err)
	}
}

func TestFileChangedHTTPAndMCPHandlers(t *testing.T) {
	for _, transport := range []string{"http", "mcp_tool"} {
		t.Run(transport, func(t *testing.T) {
			root, user := t.TempDir(), t.TempDir()
			m := NewManager(true, root)
			m.ConfigureClaudeSettings(root, user, true)
			response := `{"continue":false,"systemMessage":"external notification","hookSpecificOutput":{"hookEventName":"FileChanged","watchPaths":[]}}`
			handler := map[string]any{"type": transport}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input map[string]any
				_ = json.NewDecoder(r.Body).Decode(&input)
				if input["event"] != "add" {
					t.Error("HTTP event missing", input)
				}
				fmt.Fprint(w, response)
			}))
			defer server.Close()
			if transport == "http" {
				handler["url"] = server.URL
			} else {
				handler["server"], handler["tool"] = "fixture", "check"
				handler["input"] = map[string]any{"path": "${file_path}"}
				m.SetMCPHookInvoker(func(_ context.Context, _, _ string, args map[string]any) (MCPHookOutput, error) {
					if args["path"] != filepath.Join(root, "proof") {
						t.Error("MCP lost file path", args)
					}
					return MCPHookOutput{Text: response}, nil
				})
			}
			writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"FileChanged": []any{map[string]any{"hooks": []any{handler}}}}})
			results := m.RunFileChanged(t.Context(), root, filepath.Join(root, "proof"), "add")
			if len(results) != 1 || results[0].Error != nil || results[0].StopReason != "" || results[0].WatchPaths == nil || len(results[0].WatchPaths) != 0 || results[0].SystemMessage != "external notification" {
				t.Fatalf("wrong transport result: %+v", results)
			}
		})
	}
}

func TestWatchPathValidationAndExplicitEmptyList(t *testing.T) {
	hook := &Hook{Type: FileChanged, Claude: &ClaudeCommand{Event: "FileChanged"}}
	for _, encoded := range []string{`["relative"]`, `[1]`, `["\u0000"]`} {
		result := interpretClaudeResult(Result{Hook: hook}, `{"hookSpecificOutput":{"watchPaths":`+encoded+`}}`, "", 0)
		if result.Error == nil {
			t.Fatal("invalid watch list accepted", encoded)
		}
	}
	for _, encoded := range []string{`{}`, `{"hookSpecificOutput":{"watchPaths":[]}}`} {
		result := interpretClaudeResult(Result{Hook: hook}, encoded, "", 0)
		if result.Error != nil || (result.WatchPaths != nil) != strings.Contains(encoded, "[]") {
			t.Fatal("lost omitted/empty distinction", result)
		}
	}
	paths := make([]string, MaxFileWatchPaths+1)
	root := t.TempDir()
	for i := range paths {
		paths[i] = filepath.Join(root, "proof")
	}
	if ValidateWatchPaths(paths) == nil {
		t.Fatal("watch list capacity was not enforced")
	}
}

func TestAsyncFileChangedDeliversWatchControlsWithoutModelNotes(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"FileChanged": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "async": true, "command": `printf '%s' '{"continue":false,"systemMessage":"notification","hookSpecificOutput":{"additionalContext":"MUST_NOT_REACH_MODEL","watchPaths":[]}}'`}}}}}})
	scope := NewAsyncScope(t.Context(), nil)
	defer scope.Close()
	completed := make(chan Result, 1)
	ctx := ContextWithDeferredHookResults(ContextWithAsyncScope(t.Context(), scope), func(result Result) { completed <- result })
	m.RunFileChanged(ctx, root, filepath.Join(root, "proof"), "change")
	select {
	case result := <-completed:
		if result.Error != nil || result.WatchPaths == nil || result.SystemMessage != "notification" {
			t.Fatal("async controls lost", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("async file result was not delivered")
	}
	if notes := TakeScopeNotes(scope); notes != "" {
		t.Fatal("observational output leaked to the model", notes)
	}
}
