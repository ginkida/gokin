package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/donegate"
	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestAgentDoneGateHookStopsTerminateRun(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		t.Run(event, func(t *testing.T) {
			work := testkit.ResolvedTempDir(t)
			if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/gate\n\ngo 1.23\n"), 0600); err != nil {
				t.Fatal(err)
			}
			reg := tools.NewRegistry()
			reg.MustRegister(tools.NewWriteTool(work))
			verify := &agentHookProbeTool{name: "verify_code", result: tools.NewSuccessResult("checked")}
			later := &agentHookProbeTool{name: "bash", result: tools.NewSuccessResult("checked")}
			reg.MustRegister(verify)
			reg.MustRegister(later)
			mock := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(work, "m.go"), "content": "package main\n"}).EnqueueText("Done.")
			a := NewAgent(AgentTypeGeneral, mock, reg, work, 5, "", nil, nil)
			a.client = mock
			a.SetDoneGatePolicy(donegate.Policy{Enabled: true, AutoFixAttempts: 2})
			user := t.TempDir()
			settings, err := json.Marshal(map[string]any{"hooks": map[string]any{event: []any{map[string]any{
				"matcher": hooks.ClaudeToolName("verify_code"), "hooks": []any{map[string]any{"type": "command", "command": `printf '%s' '{"continue":false,"stopReason":"stop child gate"}'`}},
			}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(user, "settings.json"), settings, 0600); err != nil {
				t.Fatal(err)
			}
			manager := hooks.NewManager(true, work)
			manager.ConfigureClaudeSettings(work, user, false)
			a.SetHooks(manager)
			ctx, _ := tools.ContextWithHeadlessPermissions(t.Context(), false, "default")
			result, err := a.Run(tools.ContextWithoutSelfReview(ctx), "Write m.go")
			if !tools.IsHookStopError(err) || result == nil || result.Status != AgentStatusFailed || len(mock.Calls()) != 2 || later.calls != 0 {
				t.Fatalf("child gate lost hook stop: result=%+v err=%v model=%d later=%d", result, err, len(mock.Calls()), later.calls)
			}
			if data, err := os.ReadFile(filepath.Join(work, "m.go")); err != nil || string(data) != "package main\n" {
				t.Fatalf("completed work was lost: %s %v", data, err)
			}
		})
	}
}
