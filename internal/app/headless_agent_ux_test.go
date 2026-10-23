package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/permission"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// This script measures the optional review independently of provider behavior:
// read, one-line edit, final answer, then a review answer if requested.
func TestHeadlessOneLineFixRoundMeasurement(t *testing.T) {
	mock := testkit.NewMockClient()
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	application.workDir = testkit.ResolvedTempDir(t)
	path := filepath.Join(application.workDir, "value.go")
	mock.EnqueueToolCall("read", map[string]any{"file_path": path}).
		EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "return 1", "new_string": "return 2"}).
		EnqueueText("Fixed value.go.").EnqueueText("Fixed value.go; verification passed.")
	if err := os.WriteFile(filepath.Join(application.workDir, "value.go"), []byte("package fixture\nfunc Value() int { return 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	application.registry.MustRegister(tools.NewReadTool(application.workDir))
	application.registry.MustRegister(tools.NewEditTool(application.workDir))
	result, err := application.RunHeadlessWithOptions(t.Context(), "Fix Value to return 2 in value.go.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	review := 0
	for _, call := range mock.Calls() {
		if strings.Contains(call.Message, "Final completion review") {
			review++
		}
	}
	t.Logf("model rounds=%d: read/edit/final=3, completion review=%d, done-gate auto-fix=0 (disabled), verify nudge=0 (foreground), incomplete-work=0 (no todos), stop hooks=0 (none)", len(mock.Calls()), review)
	if len(mock.Calls()) != 3 || review != 0 {
		t.Fatalf("unexpected round breakdown: %+v result=%+v", mock.Calls(), result)
	}
	data, err := os.ReadFile(filepath.Join(application.workDir, "value.go"))
	if err != nil || !strings.Contains(string(data), "return 2") {
		t.Fatalf("edit not applied: %s %v", data, err)
	}
}

func TestHeadlessEditThenPermissionDenial(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			mock := testkit.NewMockClient()
			application, executor := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
			application.workDir = testkit.ResolvedTempDir(t)
			path := filepath.Join(application.workDir, "value.txt")
			if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			application.registry.MustRegister(tools.NewReadTool(application.workDir))
			application.registry.MustRegister(tools.NewEditTool(application.workDir))
			bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("must not execute")}}
			application.registry.MustRegister(bash)
			rules := permission.DefaultRules()
			rules.SetPolicy("edit", permission.LevelAllow)
			rules.SetPolicy("bash", permission.LevelAsk)
			manager := permission.NewManager(rules, true)
			manager.SetPromptHandler(func(context.Context, *permission.Request) (permission.Decision, error) {
				return permission.DecisionDeny, nil
			})
			executor.SetPermissions(manager)
			mock.EnqueueToolCall("read", map[string]any{"file_path": path}).
				EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "before", "new_string": "after"}).
				EnqueueToolCall("bash", map[string]any{"command": "echo secret-token-must-not-leak"}).
				EnqueueText("Fixed value.txt.")
			var output bytes.Buffer
			result, err := application.RunHeadlessWithOptions(t.Context(), "Fix value.txt.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &output, Stderr: io.Discard, PermissionMode: "acceptEdits", StrictPermissions: strict, Brief: true, NoSelfReview: true})
			if strict {
				if err == nil || result.Status != "policy_blocked" || len(mock.Calls()) != 3 {
					t.Fatalf("strict result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
				}
			} else if err != nil || result.Status != "success" || len(mock.Calls()) != 4 {
				t.Fatalf("normal result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
			}
			if bash.CallCount() != 0 || len(result.PermissionDenials) != 1 || result.PermissionDenials[0].Tool != "bash" || !strings.Contains(result.PermissionDenials[0].Reason, "acceptEdits") {
				t.Fatalf("denials=%+v bash=%d", result.PermissionDenials, bash.CallCount())
			}
			encoded, _ := json.Marshal(result.PermissionDenials)
			if bytes.Contains(encoded, []byte("secret-token")) {
				t.Fatal("denial leaked shell input")
			}
			if !reflect.DeepEqual(result.FilesChanged, []string{"value.txt"}) {
				t.Fatalf("files=%v", result.FilesChanged)
			}
			data, _ := os.ReadFile(path)
			if string(data) != "after\n" {
				t.Fatalf("work lost: %q", data)
			}
			decodeSingleHeadlessResult(t, output.Bytes())
		})
	}
}

func TestHeadlessNoSelfReviewRoundCount(t *testing.T) {
	for _, mode := range []string{"default", "flag", "env"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GOKIN_NO_SELF_REVIEW", map[bool]string{true: "1", false: ""}[mode == "env"])
			mock := testkit.NewMockClient()
			application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
			application.workDir = testkit.ResolvedTempDir(t)
			application.registry.MustRegister(tools.NewWriteTool(application.workDir))
			first, second := filepath.Join(application.workDir, "first.go"), filepath.Join(application.workDir, "second.go")
			mock.EnqueueToolCall("write", map[string]any{"file_path": first, "content": "package fixture\n"}).
				EnqueueToolCall("write", map[string]any{"file_path": second, "content": "package fixture\n"}).EnqueueText("Fixed.").EnqueueText("Reviewed.")
			result, err := application.RunHeadlessWithOptions(t.Context(), "Fix both files.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: mode == "flag"})
			if err != nil {
				t.Fatal(err)
			}
			want := 3
			if mode == "default" {
				want = 4
			}
			if len(mock.Calls()) != want {
				t.Fatalf("rounds=%d want=%d result=%+v", len(mock.Calls()), want, result)
			}
			if !reflect.DeepEqual(result.FilesChanged, []string{"first.go", "second.go"}) {
				t.Fatalf("files=%v", result.FilesChanged)
			}
			t.Logf("%s: %d model rounds (2 writes + final%s)", mode, len(mock.Calls()), map[bool]string{true: " + completion review", false: ""}[mode == "default"])
		})
	}
}

func TestBriefTextPrintsFinalSummaryAndInvocationFiles(t *testing.T) {
	mock := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{}).EnqueueText("Updated.\n")
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	application.workDir = testkit.ResolvedTempDir(t)
	path := filepath.Join(application.workDir, "notes.txt")
	mock = testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": path, "content": "updated\n"}).EnqueueText("Updated.\n")
	application.client = mock
	application.executor.SetClient(mock)
	application.registry.MustRegister(tools.NewWriteTool(application.workDir))
	mock.OnSend = func(context.Context) {
		if !strings.Contains(mock.SystemInstruction(), "single-line summary") {
			t.Error("brief instruction did not reach provider")
		}
	}
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(t.Context(), "Update notes.txt.", HeadlessOptions{OutputFormat: HeadlessOutputText, Stdout: &stdout, Stderr: io.Discard, Brief: true})
	if err != nil || stdout.String() != "Updated.\nnotes.txt\n" || result.Result != "Updated." {
		t.Fatalf("brief result=%+v output=%q err=%v", result, stdout.String(), err)
	}
	if strings.Contains(application.session.GetSystemInstruction(), "single-line summary") || strings.Contains(mock.SystemInstruction(), "single-line summary") {
		t.Fatal("brief instruction leaked past invocation")
	}
	mock.OnSend = nil
	mock.EnqueueText("Next answer.")
	next, err := application.RunHeadlessWithOptions(t.Context(), "Read-only next request.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || len(next.FilesChanged) != 0 {
		t.Fatalf("changed files leaked into next invocation: %+v %v", next, err)
	}
}

func TestHeadlessFilesChangedIncludesRealDelegatedWrite(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("no-self-review=%v", disabled), func(t *testing.T) {
			model := &sharedBudgetTestClient{MockClient: testkit.NewMockClient()}
			model.SetModel("glm-5.3-flash")
			task := tools.NewTaskTool()
			application, executor := newHeadlessPolicyTestApp(t, model.MockClient, task)
			application.client = model
			executor.SetClient(model)
			application.workDir = testkit.ResolvedTempDir(t)
			path := filepath.Join(application.workDir, "child.txt")
			application.registry.MustRegister(tools.NewWriteTool(application.workDir))
			runner := agent.NewRunner(application.ctx, model, application.registry, application.workDir)
			types := agent.NewAgentTypeRegistry()
			runner.SetTypeRegistry(types)
			runner.SetOnAgentUsage(func(_ string, result *agent.AgentResult) { application.accumulateScopedAgentResultUsage(result) })
			task.SetRunner(&agentRunnerAdapter{runner: runner})
			task.SetAgentTypeProvider(types)
			model.EnqueueToolCall("task", map[string]any{"prompt": "Write child.txt.", "subagent_type": "general", "max_turns": 5}).
				EnqueueToolCall("write", map[string]any{"file_path": path, "content": "from child\n"}).EnqueueText("Updated child.txt.")
			if !disabled {
				model.EnqueueText("Updated child.txt.")
			}
			model.EnqueueText("Updated.")
			if !disabled {
				model.EnqueueText("Reviewed child change.")
			}
			result, err := application.RunHeadlessWithOptions(t.Context(), "Delegate the write.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: disabled})
			if err != nil || !reflect.DeepEqual(result.FilesChanged, []string{"child.txt"}) {
				t.Fatalf("delegated files=%+v err=%v", result, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "from child\n" {
				t.Fatalf("delegated write not applied: %q %v", data, err)
			}
			want := 6
			if disabled {
				want = 4
			}
			if len(model.Calls()) != want {
				t.Fatalf("delegated model rounds=%d want=%d", len(model.Calls()), want)
			}
			t.Logf("%d model rounds: parent task/final=2, child write/final=2, verification nudge=%d, completion review=%d", want, (want-4)/2, (want-4)/2)
		})
	}
}

func TestNoSelfReviewKeepsHardDoneGate(t *testing.T) {
	mock := testkit.NewMockClient()
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	application.workDir = testkit.ResolvedTempDir(t)
	path := filepath.Join(application.workDir, "value.go")
	writeHeadlessFixture(t, path, "package fixture\nfunc Value() int { return 1 }\n")
	writeHeadlessFixture(t, filepath.Join(application.workDir, "go.mod"), "module example.com/fixture\n\ngo 1.23\n")
	application.registry.MustRegister(tools.NewReadTool(application.workDir))
	application.registry.MustRegister(tools.NewEditTool(application.workDir))
	// Gate commands use the same registry, directly, without a model round.
	application.registry.MustRegister(tools.NewBashTool(application.workDir))
	application.registry.MustRegister(tools.NewVerifyCodeTool(application.workDir))
	application.registry.MustRegister(tools.NewRunTestsTool(application.workDir))
	application.config.DoneGate.Enabled = true
	application.config.DoneGate.AutoFixAttempts = 0
	mock.EnqueueToolCall("read", map[string]any{"file_path": path}).EnqueueToolCall("edit", map[string]any{"file_path": path, "old_string": "return 1", "new_string": "return 2"}).EnqueueText("Fixed value.go.")
	result, err := application.RunHeadlessWithOptions(t.Context(), "Fix Value to return 2.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
	if err != nil || len(mock.Calls()) != 3 {
		t.Fatalf("hard gate result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
	if !strings.Contains(strings.Join(application.snapshotResponseCommands(), " "), "go vet") {
		t.Fatal("hard verification gate did not run")
	}
	t.Log("model rounds=3: read/edit/final=3; passing hard done-gate=0 additional model rounds")
}

func TestHeadlessDelegatedCapabilityCeilingRemainsTerminal(t *testing.T) {
	model := &sharedBudgetTestClient{MockClient: testkit.NewMockClient()}
	model.SetModel("glm-5.3-flash")
	task := tools.NewTaskTool()
	application, executor := newHeadlessPolicyTestApp(t, model.MockClient, task)
	application.client = model
	executor.SetClient(model)
	application.workDir = testkit.ResolvedTempDir(t)
	path := filepath.Join(application.workDir, "forbidden.txt")
	application.registry.MustRegister(tools.NewWriteTool(application.workDir))
	runner := agent.NewRunner(application.ctx, model, application.registry, application.workDir)
	types := agent.NewAgentTypeRegistry()
	runner.SetTypeRegistry(types)
	task.SetRunner(&agentRunnerAdapter{runner: runner})
	task.SetAgentTypeProvider(types)
	model.EnqueueToolCall("task", map[string]any{"prompt": "Write forbidden.txt.", "subagent_type": "general", "max_turns": 5}).
		EnqueueToolCall("write", map[string]any{"file_path": path, "content": "forbidden"}).EnqueueText("Denied.").EnqueueText("Finished.")
	ctx := tools.ContextWithToolCapabilityCeiling(t.Context(), []string{"task", "read"})
	result, err := application.RunHeadlessWithOptions(ctx, "Delegate work.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
	if err == nil || result.Status != "policy_blocked" || len(result.PermissionDenials) != 0 {
		t.Fatalf("ceiling was demoted to ordinary denial: %+v %v", result, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("forbidden file was written: %v", err)
	}
}

func TestSDKResultCarriesInvocationMetadata(t *testing.T) {
	result := HeadlessResult{Status: "success", FilesChanged: []string{"file.go"}, PermissionDenials: []tools.PermissionDenial{{Tool: "bash", ToolUseID: "call-1", Reason: "denied"}}}
	frame := SDKResult(result, 0, 0)
	if !reflect.DeepEqual(frame["files_changed"], result.FilesChanged) || !reflect.DeepEqual(frame["permission_denials"], result.PermissionDenials) {
		t.Fatalf("SDK result dropped invocation metadata: %+v", frame)
	}
}
