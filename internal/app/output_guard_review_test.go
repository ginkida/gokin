package app

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/client"
	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func reviewGuardFailure() error {
	return &client.DegenerateOutputError{Model: "test-model", Bytes: 32768, Period: 32}
}

func TestHeadlessInternalDegenerateOutputTerminal(t *testing.T) {
	for _, phase := range []string{"completion_review", "done_gate_auto_fix", "stop_continuation"} {
		t.Run(phase, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			mock := testkit.NewMockClient()
			var tool tools.Tool = &appHeadlessScriptedTool{name: "unused"}
			if phase == "stop_continuation" {
				mock.EnqueueText("Initial answer.")
			} else {
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.25\n"), 0600); err != nil {
					t.Fatal(err)
				}
				tool = tools.NewWriteTool(root)
				mock.EnqueueToolCall("write", map[string]any{"file_path": filepath.Join(root, "controller.go"), "content": "package fixture\n\nfunc Controller() int { return 42 }\n"}).EnqueueText("Implemented the controller.")
			}
			mock.EnqueueError(reviewGuardFailure()).EnqueueError(reviewGuardFailure())
			application, executor := newHeadlessPolicyTestApp(t, mock, tool)
			application.workDir = root
			if phase == "stop_continuation" {
				user := t.TempDir()
				settings := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"printf '%s' '{\"decision\":\"block\",\"reason\":\"Review required checks\"}'"}]}]}}`
				if err := os.WriteFile(filepath.Join(user, "settings.json"), []byte(settings), 0600); err != nil {
					t.Fatal(err)
				}
				application.hooksManager = hooks.NewManager(true, root)
				application.hooksManager.ConfigureClaudeSettings(root, user, false)
				executor.SetHooks(application.hooksManager)
			} else {
				application.config.DoneGate.Enabled = true
				application.config.DoneGate.AutoFixAttempts = 1
				result := tools.NewSuccessResult("verified")
				if phase == "done_gate_auto_fix" {
					result = tools.NewErrorResult("synthetic compiler failure")
				}
				application.registry.MustRegister(&appHeadlessScriptedTool{name: "verify_code", results: []tools.ToolResult{result}})
			}
			result, err := application.RunHeadlessWithOptions(t.Context(), "Implement the controller.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: phase != "completion_review"})
			wantCalls := 4
			if phase == "stop_continuation" {
				wantCalls = 3
			}
			if len(mock.Calls()) != wantCalls {
				t.Fatalf("fixture did not reach the internal guard: calls=%d result=%+v error=%v", len(mock.Calls()), result, err)
			}
			if err == nil || result.Status != "error" || result.Error == nil || result.Error.Kind != "degenerate_output" {
				t.Fatalf("internal guard swallowed or misclassified: status=%s error=%+v returned=%v", result.Status, result.Error, err)
			}
		})
	}
}

func TestHeadlessSchemaCorrectionSharesOutputRecoveryBudget(t *testing.T) {
	schema, err := CompileStructuredOutputSchema(structuredOutputTestSchema)
	if err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().EnqueueError(reviewGuardFailure()).
		EnqueueText("schema-invalid answer").EnqueueError(reviewGuardFailure()).
		EnqueueText(`{"answer":"must not get a second recovery allowance"}`)
	application, _ := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "unused"})
	result, err := application.RunHeadlessWithOptions(t.Context(), "Return structured data.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, JSONSchema: schema, NoSelfReview: true, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.Status != "error" || result.Error == nil || result.Error.Kind != "degenerate_output" || len(mock.Calls()) != 3 {
		t.Fatalf("schema correction reset recovery budget: status=%s error=%+v returned=%v calls=%d", result.Status, result.Error, err, len(mock.Calls()))
	}
}
