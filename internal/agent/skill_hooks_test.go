package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"google.golang.org/genai"
)

func agentHookFixture(t *testing.T, event, command string, once bool) (*Agent, *tools.SkillTool, *testkit.MockClient, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command = strings.ReplaceAll(command, "TEST_ROOT", root)
	path := filepath.Join(root, "guard", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	matcher := ""
	if event != "Stop" {
		matcher = "      matcher: Write\n"
	}
	document := fmt.Sprintf("---\ndescription: Guard workflow\nhooks:\n  %s:\n    - hooks:\n        - type: command\n          command: %q\n          timeout: 2\n          once: %t\n%s---\nReview code.", event, command, once, matcher)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	parent := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	registry := tools.NewRegistry()
	registry.MustRegister(parent)
	registry.MustRegister(tools.NewWriteTool(root))
	mock := testkit.NewMockClient()
	agent := NewAgent(AgentTypeGeneral, mock, registry, root, 5, "", nil, nil)
	mock = mock.Clones()[0]
	agent.reflector = nil
	agent.SetHooks(hooks.NewManager(true, root))
	return agent, parent, mock, root
}

func TestAgentSkillStopHookRequestsContinuationAndDoesNotLeakToParentOrNextRun(t *testing.T) {
	command := `cat > stop-input.json; printf '%s' '{"decision":"block","reason":"Review once before finishing"}'`
	agent, parent, mock, root := agentHookFixture(t, "Stop", command, true)
	mock.EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("Initial answer.").EnqueueText("Reviewed answer.")
	result, err := agent.Run(t.Context(), "Review this task.")
	if err != nil || result.Status != AgentStatusCompleted || !strings.Contains(result.Output, "Reviewed answer") || len(mock.Calls()) != 3 {
		encoded, _ := json.Marshal(mock.Calls())
		t.Fatalf("result=%+v err=%v calls=%s", result, err, encoded)
	}
	data, err := os.ReadFile(filepath.Join(root, "stop-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(data, &input); err != nil {
		t.Fatal(err)
	}
	if input["hook_event_name"] != "SubagentStop" || input["agent_id"] != agent.ID || input["stop_hook_active"] != false {
		t.Fatalf("input=%v", input)
	}
	if len(parent.HookScope().Hooks()) != 0 || len(tools.SkillHooks(agent.registry)) != 0 {
		t.Fatal("child registration outlived its run or reached parent")
	}
	mock.EnqueueText("Independent answer.")
	if _, err := agent.Run(t.Context(), "Independent task."); err != nil || len(mock.Calls()) != 4 {
		t.Fatalf("next run err=%v calls=%d", err, len(mock.Calls()))
	}
}

func TestAgentSkillRepeatedStopDenialCannotReportSuccessfulCompletion(t *testing.T) {
	agent, _, mock, _ := agentHookFixture(t, "Stop", `printf '%s' '{"decision":"block","reason":"Required work is unfinished"}'`, false)
	mock.EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("First answer.").EnqueueText("Second answer.")
	result, err := agent.Run(t.Context(), "Review this task.")
	if err == nil || result.Status != AgentStatusFailed || !strings.Contains(result.Error, "Stop hook still requires continuation") || len(mock.Calls()) != 3 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestAgentSkillStopContinueFalseDoesNotRequestAnotherPass(t *testing.T) {
	agent, _, mock, _ := agentHookFixture(t, "Stop", `printf '%s' '{"continue":false,"stopReason":"Stop after this answer","decision":"block","reason":"Must not start another pass"}'`, false)
	mock.EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("First answer.").EnqueueText("Must not request this.")
	result, err := agent.Run(t.Context(), "Review this task.")
	if !tools.IsHookStopError(err) || !strings.Contains(err.Error(), "Stop after this answer") || result.Status != AgentStatusFailed || len(mock.Calls()) != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestAgentSkillUpdatedInputTracksActualMutationAndPreservesOriginalCall(t *testing.T) {
	agent, _, _, root := agentHookFixture(t, "PreToolUse", `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"file_path":"TEST_ROOT/actual.txt","content":"actual"}}}'`, false)
	loaded := agent.executeToolResultSafely(t.Context(), &genai.FunctionCall{ID: "load", Name: "skill", Args: map[string]any{"name": "guard"}})
	if !loaded.Success {
		t.Fatal(loaded)
	}
	call := &genai.FunctionCall{ID: "write", Name: "write", Args: map[string]any{"file_path": "original.txt", "content": "original"}}
	result := agent.executeToolWithReflection(t.Context(), call)
	if success, _ := result.Response.Response["success"].(bool); !success {
		t.Fatalf("result=%+v", result.Response.Response)
	}
	if call.Args["file_path"] != "original.txt" {
		t.Fatal("hook rewrote model history")
	}
	if got := agent.GetTouchedPaths(); len(got) != 1 || got[0] != "actual.txt" {
		t.Fatalf("touched=%v", got)
	}
	if data, err := os.ReadFile(filepath.Join(root, "actual.txt")); err != nil || string(data) != "actual" {
		t.Fatalf("actual=%q err=%v", data, err)
	}
}

func TestAgentParentSkillHookBlocksDelegatedWriteWithoutChildSkillTool(t *testing.T) {
	for _, omitSkill := range []bool{false, true} {
		t.Run(fmt.Sprintf("omit_skill_%t", omitSkill), func(t *testing.T) {
			agent, parent, mock, root := agentHookFixture(t, "PreToolUse", "exit 0", false)
			registration, err := parent.HookScope().Prepare("parent", []hooks.ClaudeCommand{{Event: "PreToolUse", Matcher: "^Write$", Command: "printf parent-denial >&2; exit 2", Timeout: time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			registration.Commit()
			if omitSkill {
				agent.registry.Unregister("skill")
			}
			mock.EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"}).EnqueueText("Write was blocked.")
			_, _ = agent.Run(t.Context(), "Write this file.")
			calls := mock.Calls()
			encoded, _ := json.Marshal(calls)
			if len(calls) < 2 || !strings.Contains(string(encoded), "parent-denial") {
				t.Fatalf("calls=%d missing parent denial", len(calls))
			}
			if _, err := os.Stat(filepath.Join(root, "forbidden.txt")); !os.IsNotExist(err) {
				t.Fatal("delegation bypassed parent hook")
			}
			if len(parent.HookScope().Hooks()) != 1 {
				t.Fatal("child teardown cleared parent registration")
			}
		})
	}
}

func TestAgentSkillContinueFalseOverridesSimultaneousDenial(t *testing.T) {
	agent, _, mock, root := agentHookFixture(t, "PreToolUse", `printf '%s' '{"continue":false,"stopReason":"Stop this run","hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"Deny write"}}'`, false)
	mock.EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"}).EnqueueText("Must not request this.")
	result, err := agent.Run(t.Context(), "Write this file.")
	if err == nil || !strings.Contains(err.Error(), "Stop this run") || result.Status != AgentStatusFailed || len(mock.Calls()) != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestAgentSkillHookStopCannotOverrideCallerCancellation(t *testing.T) {
	agent, _, mock, root := agentHookFixture(t, "PreToolUse", `printf '%s' '{"continue":false,"stopReason":"Stop this run"}'`, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	agent.hooks.SetHandler(func(*hooks.Hook, string, error) { cancel() })
	mock.EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(root, "forbidden.txt"), "content": "forbidden"})
	result, err := agent.Run(ctx, "Write this file.")
	if !errors.Is(err, context.Canceled) || !tools.IsHookStopError(err) || result.Status != AgentStatusCancelled || len(mock.Calls()) != 2 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestAgentSkillHookStopsRemainingMutationsInSameBatch(t *testing.T) {
	agent, _, _, root := agentHookFixture(t, "PreToolUse", `printf x >> hook-runs; printf '%s' '{"continue":false,"stopReason":"Stop this batch"}'`, false)
	loaded := agent.executeToolResultSafely(t.Context(), &genai.FunctionCall{Name: "skill", Args: map[string]any{"name": "guard"}})
	if !loaded.Success {
		t.Fatal(loaded)
	}
	calls := []*genai.FunctionCall{
		{ID: "first", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "first.txt"), "content": "first"}},
		{ID: "second", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "second.txt"), "content": "second"}},
	}
	results := agent.executeTools(t.Context(), calls)
	if len(results) != 2 || results[0].Response.ID != "first" || results[1].Response.ID != "second" || !strings.Contains(results[1].Response.Response["error"].(string), "not executed") {
		t.Fatalf("results=%+v", results)
	}
	data, err := os.ReadFile(filepath.Join(root, "hook-runs"))
	if err != nil || string(data) != "x" {
		t.Fatalf("subsequent hook ran: %q err=%v", data, err)
	}
}
