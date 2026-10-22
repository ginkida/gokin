package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionStartContract(t *testing.T) {
	for _, handler := range []string{"http", "prompt", "agent"} {
		if _, err := parseClaudeTest(t, "SessionStart: [{hooks: [{type: "+handler+", command: true}]}]"); err == nil {
			t.Fatalf("accepted %s for SessionStart", handler)
		}
	}
	definitions, err := parseClaudeTest(t, "SessionStart: [{matcher: 'startup|resume', hooks: [{type: command, command: echo context}]}]")
	if err != nil || len(definitions) != 1 {
		t.Fatal(definitions, err)
	}
	hook := &Hook{Type: SessionStart, Claude: &definitions[0]}
	hctx := NewContext("startup", nil, "/workspace")
	hctx.Source = "startup"
	data, _, err := claudeInput(ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionID: "root", Model: "model", SessionTitle: "Review"}), hook, hctx)
	var payload map[string]any
	if err != nil || json.Unmarshal(data, &payload) != nil || payload["source"] != "startup" || payload["model"] != "model" || payload["tool_name"] != nil || payload["session_title"] != "Review" {
		t.Fatalf("invalid startup payload: %s %v", data, err)
	}
	stdout := `{"continue":false,"decision":"block","systemMessage":"starting","hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"proof","initialUserMessage":"first turn","sessionTitle":"Title","reloadSkills":true,"watchPaths":["/workspace/proof"]}}`
	for _, code := range []int{0, 2} {
		result := interpretClaudeResult(Result{Hook: hook}, stdout, "", code)
		if result.BlockReason != "" || result.StopReason != "" || (result.AdditionalContext == "proof") != (code == 0) || (result.InitialUserMessage == "first turn") != (code == 0) || result.ReloadSkills != (code == 0) {
			t.Fatalf("startup output: %+v", result)
		}
	}
	if got := interpretClaudeResult(Result{Hook: hook}, "plain context", "", 0).AdditionalContext; got != "plain context" {
		t.Fatal("plain stdout was lost", got)
	}
}

func TestSessionStartMatchersAndEnvironmentFile(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"SessionStart": []any{
		map[string]any{"matcher": "startup|fork", "hooks": []any{map[string]any{"type": "command", "command": `printf 'export START_PROOF=ready\n' >> "$CLAUDE_ENV_FILE"; printf 'matched'`}}},
		map[string]any{"matcher": "clear", "hooks": []any{map[string]any{"type": "command", "command": "exit 2"}}},
	}}})
	envFile := filepath.Join(root, "with spaces.sh")
	ctx := ContextWithRuntimeInfo(t.Context(), RuntimeInfo{EnvFile: envFile})
	results := m.RunSessionStart(ctx, root, "fork")
	data, err := os.ReadFile(envFile)
	if err != nil || !strings.Contains(string(data), "START_PROOF=ready") || len(results) != 1 || results[0].AdditionalContext != "matched" {
		t.Fatalf("source/environment wrong: %+v %q %v", results, data, err)
	}
	if got := m.RunSessionStart(ctx, root, "resume"); len(got) != 0 {
		t.Fatal("unmatched handler ran", got)
	}
}

func TestChildInboxCannotConsumeSessionStartGateOrNotes(t *testing.T) {
	joined := false
	ctx := ContextWithSessionStartGate(t.Context(), func(_ctx context.Context) error { joined = true; return nil }, func() string { return "parent proof" })
	ctx = ContextWithSessionEnvironment(ctx, func() []string { return []string{"/env.sh"} })
	scope := NewAsyncScope(t.Context(), nil)
	defer scope.Close()
	child := ContextWithAsyncScope(ctx, scope)
	if err := AwaitSessionStart(child); err != nil || joined || TakeAsyncNotes(child) != "" || len(SessionEnvironmentFiles(child)) != 1 {
		t.Fatal("child inherited root gate/notes or lost its session environment")
	}
	if err := AwaitSessionStart(ctx); err != nil || !joined || TakeAsyncNotes(ctx) != "parent proof" {
		t.Fatal("root startup boundary lost")
	}
}

func TestSessionStartMCPSkipsLaunchAndRunsAfterClear(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"SessionStart": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "mcp_tool", "server": "fixture", "tool": "check", "input": map[string]any{"source": "${source}"}}}},
	}}})
	calls := 0
	m.SetMCPHookInvoker(func(ctx context.Context, server, tool string, args map[string]any) (MCPHookOutput, error) {
		calls++
		if args["source"] != "clear" {
			t.Errorf("wrong MCP startup source: %+v", args)
		}
		return MCPHookOutput{Text: "MCP_START_PROOF"}, nil
	})
	results := m.RunSessionStart(ContextWithRuntimeInfo(t.Context(), RuntimeInfo{SessionStartAtLaunch: true}), root, "resume")
	if calls != 0 || len(results) != 1 || !results[0].skipped {
		t.Fatal("launch reached unavailable MCP", results, calls)
	}
	results = m.RunSessionStart(t.Context(), root, "clear")
	if calls != 1 || len(results) != 1 || results[0].AdditionalContext != "MCP_START_PROOF" {
		t.Fatal("in-session startup skipped MCP", results, calls)
	}
}

func TestSessionStartAsyncPlainStdoutReachesOwner(t *testing.T) {
	root, user := t.TempDir(), t.TempDir()
	m := NewManager(true, root)
	m.ConfigureClaudeSettings(root, user, true)
	writeClaudeSettingsTestFile(t, filepath.Join(user, "settings.json"), map[string]any{"hooks": map[string]any{"SessionStart": []any{
		map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "printf ASYNC_START_PROOF", "async": true}}},
	}}})
	scope := NewAsyncScope(t.Context(), nil)
	defer scope.Close()
	m.RunSessionStart(ContextWithAsyncScope(t.Context(), scope), root, "startup")
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(TakeScopeNotes(scope), "ASYNC_START_PROOF") {
			return
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("async startup lost plain stdout")
		}
	}
}
