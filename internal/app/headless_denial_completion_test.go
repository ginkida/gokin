package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// A final answer after a refused verification must not start an optional
// completion exchange (or an unfinished-todo nudge) against a slow provider.
func TestHeadlessDenialFinishesWithoutAutomaticContinuation(t *testing.T) {
	for _, pendingTodo := range []bool{false, true} {
		for _, format := range []HeadlessOutputFormat{HeadlessOutputJSON, HeadlessOutputStreamJSON} {
			t.Run(string(format)+map[bool]string{false: "/review", true: "/todo"}[pendingTodo], func(t *testing.T) {
				work := testkit.ResolvedTempDir(t)
				path := filepath.Join(work, "m.go")
				if err := os.WriteFile(path, []byte("package main\n// before\n"), 0600); err != nil {
					t.Fatal(err)
				}
				mock := testkit.NewMockClient()
				if pendingTodo {
					mock.EnqueueToolCall("todo", map[string]any{"todos": []any{map[string]any{"content": "Run tests", "status": "pending", "activeForm": "Running tests"}}})
				}
				mock.EnqueueToolCall("read", map[string]any{"file_path": path}).
					EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "// before", "new_string": "// after\n// documented"}).
					EnqueueToolCall("bash", map[string]any{"command": "go test ./..."}).
					EnqueueText("Changed m.go; verification was denied.").
					EnqueueScript(testkit.ResponseScript{DelayBeforeFirstChunk: 10 * time.Second, Chunks: []client.ResponseChunk{{Text: "unexpected continuation"}, {Done: true}}})
				a, exec := newHeadlessPolicyTestApp(t, mock, tools.NewReadTool(work))
				a.workDir = work
				a.registry.MustRegister(tools.NewEditTool(work))
				a.registry.MustRegister(tools.NewBashTool(work))
				a.registry.MustRegister(tools.NewTodoTool())
				rules := permission.DefaultRules()
				rules.SetPolicy("edit", permission.LevelAllow)
				rules.SetPolicy("bash", permission.LevelDeny)
				exec.SetPermissions(permission.NewManager(rules, true))
				result, err := a.RunHeadlessWithOptions(t.Context(), "Update the comment in m.go", HeadlessOptions{
					PermissionMode: "acceptEdits", OutputFormat: format, Stdout: io.Discard, Stderr: io.Discard,
					Timeout: 500 * time.Millisecond, InlineExternalSteers: format == HeadlessOutputStreamJSON,
				})
				wantCalls := 4
				if pendingTodo {
					wantCalls++
				}
				if err != nil || result.Status != "success" || len(mock.Calls()) != wantCalls {
					t.Fatalf("status=%s err=%v rounds=%d want=%d", result.Status, err, len(mock.Calls()), wantCalls)
				}
				if len(result.PermissionDenials) != 1 || len(result.FilesChanged) != 1 {
					t.Fatalf("lost edits or denial: %+v", result)
				}
				if result.Verification.Status != "denied" || result.Incomplete != pendingTodo {
					t.Fatalf("verification or unfinished-work receipt=%+v", result)
				}
			})
		}
	}
}

func TestHeadlessDoneGateRespectsVerificationDenial(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/denial\n\ngo 1.23\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("must not run")}}
	a, exec := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), bash)
	a.workDir = work
	a.config.DoneGate.Enabled = true
	a.config.DoneGate.AutoFixAttempts = 0
	a.responseToolsUsed = []string{"edit"}
	a.responseTouchedPaths = []string{"m.go"}
	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelDeny)
	exec.SetPermissions(permission.NewManager(rules, true))
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	if !a.enforceDoneGate(ctx, "Update m.go") {
		t.Fatal("denied verification discarded completed work")
	}
	if bash.CallCount() != 0 || len(state.Denials()) == 0 {
		t.Fatalf("gate bypassed permissions: executions=%d denials=%v", bash.CallCount(), state.Denials())
	}
}

func TestHeadlessDoneGateRecordsSuccessfulChecks(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/checks\n\ngo 1.23\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{
		tools.NewSuccessResult("vet passed"), tools.NewSuccessResult("diff clean"), tools.NewSuccessResult("no conflicts"),
	}}
	a, _ := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), bash)
	a.workDir = work
	a.config.DoneGate.Enabled = true
	a.config.DoneGate.AutoFixAttempts = 0
	a.responseToolsUsed = []string{"edit"}
	a.responseTouchedPaths = []string{"m.go"}
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "default")
	if !a.enforceDoneGate(ctx, "Update m.go") || state.Verification().Status != "passed" {
		t.Fatalf("successful checks lost their receipt: %+v", state.Verification())
	}
}

func TestHeadlessDoneGateDoesNotHideFailureBehindDenial(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/checks\n\ngo 1.23\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, exec := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), &appHeadlessScriptedTool{name: "verify_code", results: []tools.ToolResult{tools.NewErrorResult("build failed")}})
	a.registry.MustRegister(tools.NewBashTool(work))
	a.workDir = work
	a.config.DoneGate.Enabled = true
	a.config.DoneGate.AutoFixAttempts = 0
	a.responseToolsUsed = []string{"edit"}
	a.responseTouchedPaths = []string{"m.go"}
	rules := permission.DefaultRules()
	rules.SetPolicy("verify_code", permission.LevelAllow)
	rules.SetPolicy("bash", permission.LevelDeny)
	exec.SetPermissions(permission.NewManager(rules, true))
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	if a.enforceDoneGate(ctx, "Update m.go") || state.Verification().Status != "failed" {
		t.Fatalf("a real check failure was masked: %+v", state.Verification())
	}
}
