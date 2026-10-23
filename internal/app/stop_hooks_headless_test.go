package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessStopHookContinuationSharesInvocation(t *testing.T) {
	for _, scenario := range []struct {
		name, command, kind string
		maxTurns            int
		budget              float64
		calls, hooks        int
	}{
		{"accepted", `case "$payload" in *'"stop_hook_active":true'*) exit 0;; *) printf '%s' '{"decision":"block","reason":"run verification"}';; esac`, "", 0, 0, 3, 2},
		{"repeated_block", `printf '%s' '{"decision":"block","reason":"HTTP 429 timeout is just the gate explanation"}'`, "hook_stopped", 0, 0, 3, 2},
		{"explicit_stop", `printf '%s' '{"continue":false,"stopReason":"stop requested","decision":"block","reason":"must not retry"}'`, "hook_stopped", 0, 0, 2, 1},
		{"turn_limit", `printf '%s' '{"decision":"block","reason":"verify"}'`, "max_turns", 2, 0, 2, 1},
		{"cost_limit", `printf '%s' '{"decision":"block","reason":"verify"}'`, "budget_exceeded", 0, 1, 3, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("Initial answer.").EnqueueText("Verified answer.")
			application, executor := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "write"})
			logPath := filepath.Join(application.workDir, "stop.jsonl")
			command := "payload=$(cat); printf '%s\\n' \"$payload\" >> '" + logPath + "'; " + scenario.command
			installHeadlessStopSkill(t, application, executor, command)
			if scenario.budget > 0 {
				executor.SetCostCalculator(func(_, _ string, _, _, _ int) (float64, bool) { return 0.4, true })
			}
			var stdout bytes.Buffer
			result, err := application.RunHeadlessWithOptions(t.Context(), "Review using the guard skill.", HeadlessOptions{
				OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard,
				MaxTurns: scenario.maxTurns, MaxBudgetUSD: scenario.budget,
			})
			if scenario.kind == "" {
				if err != nil || result.Error != nil || result.Result != "Verified answer." {
					t.Fatalf("successful continuation: result=%+v err=%v", result, err)
				}
			} else if err == nil || result.Error == nil || result.Error.Kind != scenario.kind {
				t.Fatalf("terminal verdict: result=%+v err=%v, want %s", result, err, scenario.kind)
			}
			if scenario.kind == "hook_stopped" && result.Status != "policy_blocked" {
				t.Fatalf("terminal hook stop status=%s", result.Status)
			}
			if scenario.kind == "max_turns" && !errors.Is(err, tools.ErrMaxTurnsExceeded) {
				t.Fatalf("turn cap lost typed error: %v", err)
			}
			if len(mock.Calls()) != scenario.calls || result.Usage.ModelRounds != scenario.calls || result.Usage.InputTokens != 10*scenario.calls {
				t.Fatalf("lost/repeated provider work: calls=%d usage=%+v", len(mock.Calls()), result.Usage)
			}
			decodeSingleHeadlessResult(t, stdout.Bytes())
			data, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != scenario.hooks {
				t.Fatalf("Stop runs=%d want=%d: %s", len(lines), scenario.hooks, data)
			}
			for i, line := range lines {
				var input struct {
					Active  bool   `json:"stop_hook_active"`
					Message string `json:"last_assistant_message"`
				}
				if err := json.Unmarshal([]byte(line), &input); err != nil || input.Active != (i > 0) || input.Message == "" {
					t.Fatalf("wrong Stop payload %d: %s (%v)", i, line, err)
				}
			}
			if scenario.calls == 3 {
				if !strings.Contains(mock.Calls()[2].Message, "Stop hook") {
					t.Fatalf("missing continuation instructions: %+v", mock.Calls()[2])
				}
			}
			if _, _, queued := application.dequeuePending(); queued || len(application.rateLimitRetryCount) != 0 {
				t.Fatal("Stop verdict escaped the headless invocation into a queue/retry")
			}
		})
	}
}

func TestHeadlessStopHookContinuationRetainsSkillDenials(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("Initial answer.").EnqueueToolCall("write", map[string]any{"file_path": "denied.txt"}).EnqueueText("Write refused.")
	write := &appHeadlessScriptedTool{name: "write", results: []tools.ToolResult{tools.NewSuccessResult("must not execute")}}
	application, executor := newHeadlessPolicyTestApp(t, mock, write)
	installHeadlessStopSkill(t, application, executor, `case "$(cat)" in *'"stop_hook_active":true'*) exit 0;; *) printf '%s' '{"decision":"block","reason":"try writing"}';; esac`)
	path := filepath.Join(application.workDir, "guard", "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.Replace(string(body), "description: Review", "description: Review\ndisallowed-tools: Write", 1))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(t.Context(), "Load guard and review.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
	if err != nil || result.Status != "success" || len(result.PermissionDenials) != 1 || write.CallCount() != 0 {
		t.Fatalf("continuation dropped skill denial: result=%+v err=%v writes=%d", result, err, write.CallCount())
	}
	paired := false
	for _, content := range application.session.GetHistory() {
		for _, part := range content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.Name == "write" {
				paired = true
			}
		}
	}
	if !paired {
		t.Fatal("continuation lost refused tool's paired response")
	}
	decodeSingleHeadlessResult(t, stdout.Bytes())
}

func TestHeadlessStopHookCancellationOwnsTerminalOutcome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("Completed work.").EnqueueText("Must not continue.")
	application, executor := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "write"})
	installHeadlessStopSkill(t, application, executor, `printf '%s' '{"continue":false,"stopReason":"hook verdict","decision":"block"}'`)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	application.hooksManager.SetHandler(func(*hooks.Hook, string, error) { cancel() })
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(ctx, "Load guard and review.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
	if !errors.Is(err, context.Canceled) || result.Error == nil || result.Error.Kind != "cancelled" || len(mock.Calls()) != 2 || result.Usage.ModelRounds != 2 {
		t.Fatalf("Stop hook outranked cancellation: result=%+v err=%v calls=%d", result, err, len(mock.Calls()))
	}
	if result.Result != "Completed work." || len(application.session.GetHistory()) < 4 {
		t.Fatalf("cancellation lost completed work: %+v", result)
	}
	decodeSingleHeadlessResult(t, stdout.Bytes())
}

func TestHeadlessReviewCannotSwallowToolHookStop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).
		EnqueueToolCall("write", map[string]any{"file_path": "artifact.go"}).EnqueueText("Draft answer.").
		EnqueueToolCall("bash", map[string]any{"command": "go test ./..."}).EnqueueText("Must not request this.")
	bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("must not execute")}}
	write := &appHeadlessScriptedTool{name: "write", results: []tools.ToolResult{tools.NewSuccessResult("write completed")}}
	application, executor := newHeadlessPolicyTestApp(t, mock, bash)
	application.registry.MustRegister(write)
	installHeadlessStopSkill(t, application, executor, "exit 0")
	path := filepath.Join(application.workDir, "guard", "SKILL.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	extra := "  PreToolUse:\n    - matcher: Bash\n      hooks:\n        - type: command\n          command: \"printf '%s' '{\\\"continue\\\":false,\\\"stopReason\\\":\\\"review must stop\\\"}'\"\n"
	body = []byte(strings.Replace(string(body), "---\nReview.", extra+"---\nReview.", 1))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(t.Context(), "Use guard to fix code.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
	if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" || len(mock.Calls()) != 4 || write.CallCount() != 1 || bash.CallCount() != 0 {
		t.Fatalf("review ignored terminal hook: result=%+v err=%v calls=%d write=%d bash=%d", result, err, len(mock.Calls()), write.CallCount(), bash.CallCount())
	}
	paired := map[string]bool{}
	for _, content := range application.session.GetHistory() {
		for _, part := range content.Parts {
			if response := part.FunctionResponse; response != nil {
				paired[response.Name] = true
			}
		}
	}
	if !paired["write"] || !paired["bash"] || result.Usage.ModelRounds != 4 {
		t.Fatalf("review lost completed protocol or usage: paired=%v usage=%+v", paired, result.Usage)
	}
	decodeSingleHeadlessResult(t, stdout.Bytes())
}

func installHeadlessStopSkill(t *testing.T, application *App, executor *tools.Executor, command string) {
	t.Helper()
	root := application.workDir
	path := filepath.Join(root, "guard", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf("---\ndescription: Review\nhooks:\n  Stop:\n    - hooks:\n        - type: command\n          command: %q\n---\nReview.", command)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	application.registry.MustRegister(skill)
	application.hooksManager = hooks.NewManager(true, root)
	executor.SetHooks(application.hooksManager)
	executor.SetWorkDir(root)
	manager, err := chat.NewSessionManager(application.session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatal(err)
	}
	application.sessionManager = manager
}
