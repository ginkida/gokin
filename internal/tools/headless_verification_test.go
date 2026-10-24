package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/testkit"

	"google.golang.org/genai"
)

func TestHeadlessVerificationTracksOutcomesAndInvalidatesAfterEdits(t *testing.T) {
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	call := &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "go test ./... -args secret-value"}}
	ObserveHeadlessToolResult(ctx, call, NewPolicyBlockedResult(PolicyBlockPermission, "denied"))
	if state.Verification().Status != "denied" {
		t.Fatal("permission refusal lost")
	}
	ObserveHeadlessToolResult(ctx, call, NewErrorResult("test failed"))
	if state.Verification().Status != "failed" {
		t.Fatal("failed retry lost")
	}
	ObserveHeadlessToolResult(ctx, call, NewSuccessResult("passed"))
	if state.Verification().Status != "passed" {
		t.Fatal("successful retry did not replace earlier failure")
	}
	data, err := json.Marshal(state.Verification())
	if err != nil || strings.Contains(string(data), "secret-value") {
		t.Fatalf("raw arguments leaked: %s %v", data, err)
	}
	ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "printf changed > m.go"}}, NewSuccessResult("changed"))
	if state.Verification().Status != "not_run" {
		t.Fatal("a shell write retained verification of older code")
	}
	ObserveHeadlessToolResult(ctx, call, NewSuccessResult("passed"))
	ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "edit", Args: map[string]any{"file_path": "m.go"}}, NewSuccessResult("edited"))
	if state.Verification().Status != "not_run" {
		t.Fatal("a check before the last edit claimed to verify the new code")
	}
	skipped := NewSuccessResult("skipped")
	skipped.VerificationSkipped = true
	ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "verify_code"}, skipped)
	if state.Verification().Status != "not_run" {
		t.Fatal("skipped check claimed verification")
	}
	ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "go test ./...", "run_in_background": true}}, NewSuccessResult("task started"))
	if state.Verification().Status != "not_run" {
		t.Fatal("a background shell launch claimed completed verification")
	}
}

func TestHeadlessVerificationCommandRecognitionIsConservative(t *testing.T) {
	for _, command := range []string{"go test ./...", "cd pkg && go vet .", "npm run lint", "pytest", "python3 -m unittest", "cargo check"} {
		if !IsVerificationCommand(command) {
			t.Errorf("unrecognized check %q", command)
		}
	}
	for _, command := range []string{`echo 'go test passed'`, "git diff --check", "go test ./... || true", "go test ./... | cat", "false && go test ./...", "echo $(go test)", "go test; true", "cat test-results", "go test ./... &", "go test -n ./...", "go test -help", "pytest --collect-only"} {
		if IsVerificationCommand(command) {
			t.Errorf("unproven check accepted: %q", command)
		}
	}
}

func TestHeadlessVerificationRecordsRealSkippedVerifier(t *testing.T) {
	work := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(work, "package.json"), []byte(`{"name":"no-check-scripts"}`), 0600); err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	reg.MustRegister(NewVerifyCodeTool(work))
	executor := NewExecutor(reg, nil, 0)
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
	result, err := executor.InvokeTool(ctx, "verify_code", map[string]any{"path": work})
	if err != nil || !result.Success || !result.VerificationSkipped || state.Verification().Status != "not_run" {
		t.Fatalf("skipped verifier=%+v receipt=%+v err=%v", result, state.Verification(), err)
	}
}
