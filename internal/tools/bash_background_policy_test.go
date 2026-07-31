package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestBashBackgroundPolicyDisablesSchemaValidationAndExecution(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if _, ok := tool.Declaration().Parameters.Properties["run_in_background"]; !ok {
		t.Fatal("interactive/default bash declaration lost background option")
	}

	tool.SetBackgroundAllowed(false)
	if _, ok := tool.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("foreground-only bash declaration still advertises background execution")
	}
	if strings.Contains(tool.Description(), "Use run_in_background=true") {
		t.Fatalf("foreground-only description still recommends background mode: %q", tool.Description())
	}
	args := map[string]any{"command": "echo safe", "run_in_background": true}
	if err := tool.Validate(args); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("Validate error = %v", err)
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Success || result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockSafety {
		t.Fatalf("background execution was not fail-closed: %+v", result)
	}

	tool.SetBackgroundAllowed(true)
	if _, ok := tool.Declaration().Parameters.Properties["run_in_background"]; !ok {
		t.Fatal("re-enabled bash declaration did not restore background option")
	}
}

func TestManagedApplyBackDisablesBackgroundAtPolicySource(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.EnableManagedWorkspaceApplyBackMode(t.TempDir())

	if _, ok := tool.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("managed apply-back declaration advertises an execution mode the runtime rejects")
	}
	if !strings.Contains(tool.Description(), "prefer run_tests") ||
		strings.Contains(tool.Description(), "Use run_in_background=true") {
		t.Fatalf("managed description gives contradictory long-command guidance:\n%s", tool.Description())
	}
	err := tool.Validate(map[string]any{"command": "cargo test --workspace", "run_in_background": true})
	if err == nil || !strings.Contains(err.Error(), "run_tests") {
		t.Fatalf("managed validation should provide the safe fallback, got %v", err)
	}

	cloned, ok := CloneToolForWorkDir(tool, t.TempDir()).(*BashTool)
	if !ok {
		t.Fatal("bash clone has unexpected type")
	}
	if _, ok := cloned.Declaration().Parameters.Properties["run_in_background"]; ok {
		t.Fatal("bash clone widened the source background policy")
	}
}

func TestBashTimeoutSecondsContract(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	if schema := tool.Declaration().Parameters.Properties["timeout_seconds"]; schema == nil {
		t.Fatal("bash declaration missing foreground timeout control")
	}
	for _, args := range []map[string]any{
		{"command": "true", "timeout_seconds": "600"},
		{"command": "true", "timeout_seconds": 0},
		{"command": "true", "timeout_seconds": int(MaxBashTimeout/time.Second) + 1},
	} {
		if err := tool.Validate(args); err == nil {
			t.Fatalf("invalid timeout passed validation: %#v", args)
		}
	}
	if err := tool.Validate(map[string]any{"command": "true", "timeout_seconds": 600}); err != nil {
		t.Fatalf("valid long foreground timeout rejected: %v", err)
	}
}

func TestManagedBashDirectVerificationGetsLongForegroundBudget(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.SetTimeout(30 * time.Second)
	tool.EnableManagedWorkspaceApplyBackMode(t.TempDir())

	view, _ := tool.executionViewLocked()
	timeout := view.foregroundCommandTimeout("cargo test --workspace", nil)
	if timeout != DefaultRunTestsTimeout {
		t.Fatalf("managed direct verification timeout = %v, want %v", timeout, DefaultRunTestsTimeout)
	}
}

func TestBashDirectVerificationGetsLongForegroundBudgetWithoutIsolation(t *testing.T) {
	tool := NewBashTool(t.TempDir())
	tool.SetTimeout(30 * time.Second)

	view, _ := tool.executionViewLocked()
	timeout := view.foregroundCommandTimeout(
		"cargo test --workspace 2>&1 | tail -20", nil,
	)
	if timeout != DefaultRunTestsTimeout {
		t.Fatalf("direct verification timeout = %v, want %v", timeout, DefaultRunTestsTimeout)
	}
}

func TestTimeoutSuggestionDoesNotAdvertiseUnavailableBackgroundExecution(t *testing.T) {
	suggestion := getErrorSuggestion("command timed out after 30s")
	if strings.Contains(suggestion, "run_in_background") {
		t.Fatalf("generic timeout suggestion advertised a mode that may be unavailable: %q", suggestion)
	}
	for _, want := range []string{"timeout", "run_tests", "available tool schema"} {
		if !strings.Contains(suggestion, want) {
			t.Fatalf("timeout suggestion %q does not contain %q", suggestion, want)
		}
	}
}
