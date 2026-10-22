package app

import (
	"context"
	"io"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestMCPHookHeadlessControlsAndModelUsage(t *testing.T) {
	for _, test := range []struct {
		name, text      string
		toolError, stop bool
		reads           int
	}{
		{"allow", `{"continue":true}`, false, false, 1},
		{"deny", `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"review required"}}`, false, false, 0},
		{"stop", `{"continue":false,"stopReason":"stop"}`, false, true, 0},
		{"tool-error", `{"continue":false}`, true, false, 1},
		{"malformed", `{"continue":true,"continue":false}`, false, false, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			worker := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "proof.txt"}).EnqueueText("Proof inspected")
			a := newGoalTestApp(t, worker, testkit.NewMockClient())
			skill := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
			a.registry.MustRegister(skill)
			registration, err := skill.HookScope().Prepare("mcp-review", []hooks.ClaudeCommand{{Event: "PreToolUse", Matcher: "Read", Handler: "mcp_tool", MCPServer: "fixture", MCPTool: "check", MCPInput: map[string]any{"path": "${tool_input.file_path}"}, Timeout: time.Second}})
			if err != nil {
				t.Fatal(err)
			}
			registration.Commit()
			calls := 0
			a.hooksManager.SetMCPHookInvoker(func(ctx context.Context, server, tool string, input map[string]any) (hooks.MCPHookOutput, error) {
				calls++
				if input["path"] != "proof.txt" {
					t.Errorf("lost event input: %+v", input)
				}
				return hooks.MCPHookOutput{Text: test.text, IsError: test.toolError}, nil
			})
			result, err := a.RunHeadlessWithOptions(t.Context(), "inspect proof", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 4})
			if test.stop {
				if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" {
					t.Fatalf("lost MCP stop: %+v %v", result, err)
				}
			} else if test.reads == 1 && err != nil {
				t.Fatalf("nonblocking MCP hook failed turn: %+v %v", result, err)
			}
			read, _ := a.registry.Get("read")
			if calls != 1 || read.(*appHeadlessScriptedTool).CallCount() != test.reads {
				t.Fatalf("MCP result crossed wrong tool boundary: calls=%d reads=%d", calls, read.(*appHeadlessScriptedTool).CallCount())
			}
			if result.Usage.ModelRounds != len(worker.Calls()) || result.Usage.InputTokens != 10*len(worker.Calls()) {
				t.Fatalf("MCP hook charged nonexistent model requests: %+v", result.Usage)
			}
		})
	}
}

func TestMCPHookStopContinuesWithinHeadlessRoundLimit(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Initial result").EnqueueText("Acceptance complete")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	skill := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
	a.registry.MustRegister(skill)
	registration, err := skill.HookScope().Prepare("mcp-stop", []hooks.ClaudeCommand{{Event: "Stop", Handler: "mcp_tool", MCPServer: "fixture", MCPTool: "check", Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	calls := 0
	a.hooksManager.SetMCPHookInvoker(func(context.Context, string, string, map[string]any) (hooks.MCPHookOutput, error) {
		calls++
		if calls == 1 {
			return hooks.MCPHookOutput{Text: `{"decision":"block","reason":"finish acceptance"}`}, nil
		}
		return hooks.MCPHookOutput{}, nil
	})
	result, err := a.RunHeadlessWithOptions(t.Context(), "finish acceptance", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, MaxTurns: 2})
	if err != nil || calls != 2 || result.Usage.ModelRounds != 2 || result.Usage.InputTokens != 20 {
		t.Fatalf("MCP Stop continuation lost bounds or usage: %+v %v calls=%d", result, err, calls)
	}
}
