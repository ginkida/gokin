package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func callerReviewGateApp(t *testing.T) (*App, *tools.Executor, *testkit.MockClient, *appHeadlessScriptedTool) {
	t.Helper()
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/checks\n\ngo 1.23\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient()
	bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("ok"), tools.NewSuccessResult("ok"), tools.NewSuccessResult("ok")}}
	a, exec := newHeadlessPolicyTestApp(t, mock, bash)
	a.workDir = work
	a.config.DoneGate.Enabled = true
	a.config.DoneGate.AutoFixAttempts = 2
	a.responseToolsUsed = []string{"edit"}
	a.responseTouchedPaths = []string{"m.go"}
	return a, exec, mock, bash
}

func TestInteractiveDoneGateDontAskAllowsWrappedReadChecks(t *testing.T) {
	a, exec, mock, bash := callerReviewGateApp(t)
	manager := permission.NewManager(permission.DefaultRules(), true)
	manager.SetDontAsk(true)
	manager.SetRunToolRules([]string{"edit", "verify_code", "run_tests"}, nil)
	exec.SetPermissions(manager)
	if !a.enforceDoneGate(t.Context(), "Update m.go") || bash.CallCount() < 1 || len(mock.Calls()) != 0 {
		t.Fatalf("trusted wrapped vet was denied or auto-fixed: bash=%d model=%d", bash.CallCount(), len(mock.Calls()))
	}
}

func TestInteractiveDoneGatePermissionDenialDoesNotAutoFix(t *testing.T) {
	a, exec, mock, bash := callerReviewGateApp(t)
	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelDeny)
	exec.SetPermissions(permission.NewManager(rules, true))
	if !a.enforceDoneGate(t.Context(), "Update m.go") || bash.CallCount() != 0 || len(mock.Calls()) != 0 {
		t.Fatalf("interactive denial drove auto-fix: bash=%d model=%d", bash.CallCount(), len(mock.Calls()))
	}
}

func TestHeadlessDoneGateHookStopsAreTerminal(t *testing.T) {
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		t.Run(event, func(t *testing.T) {
			a, exec, mock, bash := callerReviewGateApp(t)
			a.registry.MustRegister(&appHeadlessScriptedTool{name: "write", results: []tools.ToolResult{tools.NewSuccessResult("edited")}})
			verify := &appHeadlessScriptedTool{name: "verify_code", results: []tools.ToolResult{tools.NewSuccessResult("checked")}}
			a.registry.MustRegister(verify)
			mock.EnqueueToolCall("write", map[string]any{"file_path": "m.go", "content": "package main"}).EnqueueText("Done.")
			user := t.TempDir()
			settings, err := json.Marshal(map[string]any{"hooks": map[string]any{event: []any{map[string]any{
				"matcher": hooks.ClaudeToolName("verify_code"), "hooks": []any{map[string]any{"type": "command", "command": `printf '%s' '{"continue":false,"stopReason":"stop gate"}'`}},
			}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(user, "settings.json"), settings, 0600); err != nil {
				t.Fatal(err)
			}
			manager := hooks.NewManager(true, a.workDir)
			manager.ConfigureClaudeSettings(a.workDir, user, false)
			exec.SetHooks(manager)
			result, err := a.RunHeadlessWithOptions(t.Context(), "Write m.go", HeadlessOptions{NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
			if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" || len(mock.Calls()) != 2 || bash.CallCount() != 0 {
				t.Fatalf("gate swallowed hook stop: result=%+v err=%v model=%d bash=%d", result, err, len(mock.Calls()), bash.CallCount())
			}
		})
	}
}

func TestHeadlessSingleLineInsertionSkipsCompletionReview(t *testing.T) {
	for _, insertion := range []bool{true, false} {
		t.Run(map[bool]string{true: "insert", false: "delete"}[insertion], func(t *testing.T) {
			work := testkit.ResolvedTempDir(t)
			old, next := "func Add(a, b int) int { return a + b }", "// Add adds two ints.\nfunc Add(a, b int) int { return a + b }"
			if !insertion {
				old, next = next, old
			}
			path := filepath.Join(work, "m.go")
			if err := os.WriteFile(path, []byte("package main\n"+old+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			mock := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": path}).
				EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": old, "new_string": next}).EnqueueText("Done.")
			a, _ := newHeadlessPolicyTestApp(t, mock, tools.NewReadTool(work))
			a.workDir = work
			a.registry.MustRegister(tools.NewEditTool(work))
			result, err := a.RunHeadlessWithOptions(t.Context(), "Change the one-line comment above Add in m.go", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
			if err != nil || result.Status != "success" || len(mock.Calls()) != 3 {
				t.Fatalf("one-line edit triggered review: result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
			}
		})
	}
}

func TestHeadlessFailedDelegatedChildIsIncomplete(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueToolCall("task", map[string]any{"prompt": "Fix code", "subagent_type": "general"}).EnqueueText("Finished.")
	task := tools.NewTaskTool()
	task.SetRunner(appPolicyAgentRunner{result: tools.AgentResult{AgentID: "failed-child", Type: "general", Status: "failed", Completed: true, Error: "reached maximum turn limit", Output: "Partial edits retained."}})
	a, _ := newHeadlessPolicyTestApp(t, mock, task)
	result, err := a.RunHeadlessWithOptions(t.Context(), "Delegate this fix", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || !result.Incomplete {
		t.Fatalf("failed child disappeared from completeness: result=%+v err=%v", result, err)
	}
}
