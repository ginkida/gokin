package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/genai"
)

func TestHeadlessVerificationUsesHookUpdatedCommands(t *testing.T) {
	for _, writing := range []bool{false, true} {
		t.Run(map[bool]string{false: "skip check", true: "write instead of read"}[writing], func(t *testing.T) {
			original, actual := "go test ./...", "echo skipped"
			if writing {
				original, actual = "echo inspect", "printf changed > m.go"
			}
			exec, manager, work, source := settingsExecutorFixture(t, "exit 0")
			if err := exec.registry.Register(NewBashTool(work)); err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PreToolUse", "updatedInput": map[string]any{"command": actual}}})
			settings, _ := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "printf '%s' '" + string(payload) + "'"}}}}}})
			if err := os.WriteFile(source, settings, 0600); err != nil {
				t.Fatal(err)
			}
			exec.SetHooks(manager)
			ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
			if writing {
				RecordHeadlessVerification(ctx, "earlier tests", "passed")
			}
			args := map[string]any{"command": original}
			result, err := exec.InvokeTool(ctx, "bash", args)
			if err != nil || !result.Success || state.Verification().Status != "not_run" || args["command"] != original {
				t.Fatalf("original args claimed execution: result=%+v err=%v receipt=%+v", result, err, state.Verification())
			}
			if writing {
				if data, err := os.ReadFile(filepath.Join(work, "m.go")); err != nil || string(data) != "changed" {
					t.Fatalf("rewritten shell did not write: %s %v", data, err)
				}
			}
			exec.SetSideEffectDedup(true)
			ctx, replayState := ContextWithHeadlessPermissions(t.Context(), false, "default")
			if writing {
				RecordHeadlessVerification(ctx, "earlier tests", "passed")
			}
			replayed, err := exec.InvokeTool(ctx, "bash", args)
			if err != nil || !replayed.Success || replayed.HookUpdatedInput["command"] != actual || replayState.Verification().Status != "not_run" {
				t.Fatalf("retry lost executed input: result=%+v err=%v receipt=%+v", replayed, err, replayState.Verification())
			}
		})
	}
}

func TestHeadlessVerificationRejectsInformationalCommands(t *testing.T) {
	for _, command := range []string{"ruff rule F401", "ruff config", "ruff format main.py", "ruff check --show-settings", "tsc --init", "tsc --showConfig", "tsc --showConfig=true", "tsc --listFilesOnly", "tsc -v", "tsc --all", "tsc --build --clean", "eslint --print-config main.js"} {
		if IsVerificationCommand(command) {
			t.Errorf("informational/configuration operation counted as verification: %s", command)
		}
	}
	for _, command := range []string{"ruff check .", "ruff format --check .", "tsc", "tsc --noEmit", "eslint .", "mypy .", "go test -v ./..."} {
		if !IsVerificationCommand(command) {
			t.Errorf("real validation rejected: %s", command)
		}
	}
}

func TestHeadlessVerificationLabelsAreReadableBoundedAndRedacted(t *testing.T) {
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
	command := "go test ./... -args --password long-secret-password"
	call := &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": command}}
	ObserveHeadlessToolResult(ctx, call, NewSuccessResult("passed"))
	encoded, _ := json.Marshal(state.Verification())
	var receipt struct {
		Checks []map[string]any `json:"checks"`
	}
	if err := json.Unmarshal(encoded, &receipt); err != nil || len(receipt.Checks) != 1 {
		t.Fatalf("receipt=%s err=%v", encoded, err)
	}
	name, _ := receipt.Checks[0]["name"].(string)
	id, _ := receipt.Checks[0]["id"].(string)
	if !strings.HasPrefix(name, "bash: go test ./...") || id == "" || strings.Contains(string(encoded), "long-secret-password") {
		t.Fatalf("unusable or unredacted label: %s", encoded)
	}
	ctx, state = ContextWithHeadlessPermissions(t.Context(), false, "default")
	call.Args["command"] = "go test ./" + strings.Repeat("界", 100)
	ObserveHeadlessToolResult(ctx, call, NewSuccessResult("passed"))
	if name := state.Verification().Checks[0].Name; !utf8.ValidString(name) || utf8.RuneCountInString(name) > 96 {
		t.Fatalf("unbounded label: %q", name)
	}
}

func TestHeadlessChildStatusTracksSuccessfulResume(t *testing.T) {
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
	for _, status := range []string{"running", "failed", "completed"} {
		ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "task_output"}, NewSuccessResultWithData("child output", map[string]any{"agent_id": "child", "status": status}))
		if state.Incomplete() != (status != "completed") {
			t.Fatalf("status=%s incomplete=%v", status, state.Incomplete())
		}
	}
}

func TestHeadlessVerificationLabelRedactsFlagsAndKeepsDistinctIDs(t *testing.T) {
	for _, command := range []string{"go test ./... --token opaque-secret-value", "go test ./... --password='quoted secret value'", "go test ./... api_key=opaque-secret-value"} {
		ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
		ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": command}}, NewSuccessResult("passed"))
		name := state.Verification().Checks[0].Name
		if strings.Contains(name, "opaque-secret-value") || strings.Contains(name, "quoted secret value") || !strings.Contains(name, "[REDACTED]") {
			t.Fatalf("secret leaked: %q", name)
		}
	}
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
	prefix := "go test ./" + strings.Repeat("a", 100)
	call := &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": prefix + "first"}}
	ObserveHeadlessToolResult(ctx, call, NewErrorResult("failed"))
	first := state.Verification().Checks[0]
	ObserveHeadlessToolResult(ctx, call, NewSuccessResult("passed"))
	if checks := state.Verification().Checks; len(checks) != 1 || checks[0].ID != first.ID || checks[0].Status != "passed" {
		t.Fatalf("retry lost stable identity: %+v", checks)
	}
	call.Args["command"] = prefix + "second"
	ObserveHeadlessToolResult(ctx, call, NewSuccessResult("passed"))
	if checks := state.Verification().Checks; len(checks) != 2 || checks[0].Name != checks[1].Name || checks[0].ID == checks[1].ID {
		t.Fatalf("truncation merged distinct checks: %+v", checks)
	}
}
