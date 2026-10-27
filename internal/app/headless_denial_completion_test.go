package app

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessUnavailableVerificationContinuesImplementationTodos(t *testing.T) {
	testHeadlessImplementationTodo(t, "Update the second file")
}

func TestHeadlessMixedVerificationTodoContinuesImplementation(t *testing.T) {
	testHeadlessImplementationTodo(t, "Run tests (also update second.go)")
}

func testHeadlessImplementationTodo(t *testing.T, pendingTodo string) {
	t.Helper()
	work := testkit.ResolvedTempDir(t)
	first, second := filepath.Join(work, "first.go"), filepath.Join(work, "second.go")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	todos := func(status string) map[string]any {
		return map[string]any{"todos": []any{
			map[string]any{"content": "Update the first file", "status": "completed", "activeForm": "Updating first"},
			map[string]any{"content": pendingTodo, "status": status, "activeForm": "Updating second"},
			map[string]any{"content": "Run tests", "status": "pending", "activeForm": "Running tests"},
		}}
	}
	mock := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": first}).
		EnqueueToolCall("edit", map[string]any{"file_path": first, "old_string": "before", "new_string": "after"}).
		EnqueueToolCall("todo", todos("pending")).EnqueueText("I will continue with the second file.").
		EnqueueToolCall("read", map[string]any{"file_path": second}).
		EnqueueToolCall("edit", map[string]any{"file_path": second, "old_string": "before", "new_string": "after"}).
		EnqueueToolCall("todo", todos("completed")).EnqueueText("Updated both files; verification is unavailable.")
	a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewReadTool(work))
	a.workDir = work
	a.registry.MustRegister(tools.NewEditTool(work))
	a.registry.MustRegister(tools.NewTodoTool())
	a.registry.MustRegister(tools.NewBashTool(work))
	rules := permission.DefaultRules()
	rules.SetPolicy("edit", permission.LevelAllow)
	rules.SetPolicy("bash", permission.LevelDeny)
	executor.SetPermissions(permission.NewManager(rules, true))
	result, err := a.RunHeadlessWithOptions(t.Context(), "Update both files", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || len(mock.Calls()) != 8 || len(result.FilesChanged) != 2 || len(result.PermissionDenials) != 0 {
		t.Fatalf("implementation was abandoned: result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
	if data, err := os.ReadFile(second); err != nil || string(data) != "after" {
		t.Fatalf("second edit=%q err=%v", data, err)
	}
	for _, call := range mock.Calls()[4:] {
		for _, content := range call.History {
			for _, part := range content.Parts {
				if strings.Contains(part.Text, "task list still has") && (strings.Contains(part.Text, "• Run tests\n") || !strings.Contains(part.Text, pendingTodo)) {
					t.Fatalf("continuation must request only permitted work: %s", part.Text)
				}
			}
		}
	}
}

func TestHeadlessStopSkillGrantReplacesDeniedGitChecks(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"})
	bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("vet passed"), tools.NewSuccessResult("diff clean"), tools.NewSuccessResult("no conflicts")}}
	a, executor := newHeadlessPolicyTestApp(t, mock, bash)
	work := testkit.ResolvedTempDir(t)
	a.workDir = work
	sdkTestFile(t, filepath.Join(work, "go.mod"), "module example.com/recheck\n\ngo 1.23\n")
	path := filepath.Join(work, "m.go")
	a.registry.MustRegister(tools.NewWriteTool(work))
	verify := &appHeadlessScriptedTool{name: "verify_code", results: []tools.ToolResult{tools.NewSuccessResult("build passed")}}
	tests := &appHeadlessScriptedTool{name: "run_tests", results: []tools.ToolResult{tools.NewSuccessResult("tests passed")}}
	a.registry.MustRegister(verify)
	a.registry.MustRegister(tests)
	a.config.DoneGate.Enabled = true
	installHeadlessStopSkill(t, a, executor, `case "$(cat)" in *'"stop_hook_active":true'*) exit 0;; *) printf '%s' '{"decision":"block","reason":"Load the checks skill and recheck"}';; esac`)
	sdkTestFile(t, filepath.Join(work, "checks", "SKILL.md"), "---\ndescription: Check\nallowed-tools: Bash, verify_code, run_tests\n---\nRun the checks.")
	// Refresh the catalog installed by installHeadlessStopSkill after adding checks.
	skill, _ := a.registry.Get("skill")
	skill.(*tools.SkillTool).ReloadCatalog()
	rules := permission.DefaultRules()
	rules.SetPolicy("write", permission.LevelAllow)
	executor.SetPermissions(permission.NewManager(rules, true))
	mock.EnqueueToolCall("write", map[string]any{"file_path": path, "content": "package main\n"}).EnqueueText("Edited m.go.").
		EnqueueToolCall("skill", map[string]any{"name": "checks"}).EnqueueText("Rechecked without further edits.")
	result, err := a.RunHeadlessWithOptions(t.Context(), "Use guard and write m.go", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || result.Verification.Status != "passed" || len(mock.Calls()) != 5 || bash.CallCount() != 3 || verify.CallCount() != 1 || tests.CallCount() != 1 || len(result.PermissionDenials) != 0 || len(result.FilesChanged) != 1 {
		t.Fatalf("recheck kept stale denial: result=%+v err=%v calls=%d bash=%d verify=%d tests=%d", result, err, len(mock.Calls()), bash.CallCount(), verify.CallCount(), tests.CallCount())
	}
	for _, check := range result.Verification.Checks {
		if check.Status != "passed" {
			t.Fatalf("stale check after successful recheck: %+v", check)
		}
	}
}

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

func TestHeadlessUnavailableVerificationDoesNotContinuePendingTodos(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	path := filepath.Join(work, "m.go")
	if err := os.WriteFile(path, []byte("package main\n// before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().
		EnqueueToolCall("todo", map[string]any{"todos": []any{map[string]any{"content": "Run tests", "status": "pending", "activeForm": "Running tests"}}}).
		EnqueueToolCall("read", map[string]any{"file_path": path}).
		EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "// before", "new_string": "// after\n// documented"}).
		EnqueueText("Changed m.go; verification is unavailable.").
		EnqueueScript(testkit.ResponseScript{DelayBeforeFirstChunk: 10 * time.Second, Chunks: []client.ResponseChunk{{Text: "unexpected continuation"}, {Done: true}}})
	a, executor := newHeadlessPolicyTestApp(t, mock, tools.NewReadTool(work))
	a.workDir = work
	a.registry.MustRegister(tools.NewTodoTool())
	a.registry.MustRegister(tools.NewEditTool(work))
	a.registry.MustRegister(tools.NewBashTool(work))
	rules := permission.DefaultRules()
	rules.SetPolicy("edit", permission.LevelAllow)
	rules.SetPolicy("bash", permission.LevelDeny)
	executor.SetPermissions(permission.NewManager(rules, true))
	result, err := a.RunHeadlessWithOptions(t.Context(), "Update m.go", HeadlessOptions{
		PermissionMode: "acceptEdits", Stdout: io.Discard, Stderr: io.Discard, Timeout: time.Second,
	})
	if err != nil || result.Status != "success" || len(mock.Calls()) != 4 || !result.Incomplete || len(result.FilesChanged) != 1 || len(result.PermissionDenials) != 0 {
		t.Fatalf("result=%+v err=%v rounds=%d", result, err, len(mock.Calls()))
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
	if bash.CallCount() != 0 || len(state.Denials()) != 0 || state.Verification().Status != "denied" {
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
