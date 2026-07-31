package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"gokin/internal/plan"
)

func TestRunTestsRejectsUnknownFrameworkInsteadOfFalsePass(t *testing.T) {
	workspace, _ := setupExecutionScopeProjects(t)
	tool := NewRunTestsTool(workspace)
	args := map[string]any{"framework": "definitely-not-a-runner"}

	if err := tool.Validate(args); err == nil {
		t.Fatal("unknown framework passed validation")
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "framework") {
		t.Fatalf("unknown framework produced a false success: success=%v error=%q content=%q", result.Success, result.Error, result.Content)
	}
}

func TestRunTestsValidatesOptionalArgumentTypes(t *testing.T) {
	tool := NewRunTestsTool(t.TempDir())
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{name: "path", args: map[string]any{"path": 42}},
		{name: "filter", args: map[string]any{"filter": true}},
		{name: "framework", args: map[string]any{"framework": false}},
		{name: "verbose", args: map[string]any{"verbose": "yes"}},
		{name: "coverage", args: map[string]any{"coverage": 1}},
		{name: "timeout_seconds", args: map[string]any{"timeout_seconds": "600"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tool.Validate(tc.args); err == nil {
				t.Fatalf("invalid %s type passed validation", tc.name)
			}
		})
	}
}

func TestRunTestsLongTimeoutAndCargoWorkspaceContract(t *testing.T) {
	tool := NewRunTestsTool(t.TempDir())
	if schema := tool.Declaration().Parameters.Properties["timeout_seconds"]; schema == nil {
		t.Fatal("run_tests declaration missing timeout_seconds")
	}
	for _, value := range []any{0, int(MaxRunTestsTimeout/time.Second) + 1} {
		if err := tool.Validate(map[string]any{"timeout_seconds": value}); err == nil {
			t.Fatalf("invalid timeout %v passed validation", value)
		}
	}
	if err := tool.Validate(map[string]any{"timeout_seconds": 900}); err != nil {
		t.Fatalf("valid workspace-test timeout rejected: %v", err)
	}

	name, args := buildTestCommand("cargo", "", "", false, false)
	joined := name + " " + strings.Join(args, " ")
	if !strings.Contains(joined, "cargo test --workspace") {
		t.Fatalf("cargo runner does not cover the full workspace: %q", joined)
	}
	if strings.Contains(joined, "--format json") {
		t.Fatalf("cargo runner still forces unstable libtest JSON: %q", joined)
	}
}

func TestCargoParserAggregatesEveryHarnessInsteadOfFinalDocTail(t *testing.T) {
	output := `running 1700 tests
test result: ok. 1700 passed; 0 failed; 2 ignored; 0 measured; 0 filtered out; finished in 11.00s
running 10 tests
test result: ok. 10 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 1.00s
Doc-tests demo
running 0 tests
test result: ok. 0 passed; 0 failed; 0 ignored; 0 measured; 0 filtered out; finished in 0.00s
`
	got := parseCargoTestResults(output, nil, 12*time.Second)
	for _, want := range []string{"PASS - 1710 tests passed", "2 ignored", "across 3 test harnesses"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cargo aggregate missing %q:\n%s", want, got)
		}
	}
}

func TestRunTestsClassifiesCancellationHonestly(t *testing.T) {
	workspace, _ := setupExecutionScopeProjects(t)
	tool := NewRunTestsTool(workspace)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, err := tool.Execute(cancelledCtx, map[string]any{"framework": "go"})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Success || !strings.Contains(cancelled.Error, "cancelled before completion") ||
		strings.Contains(cancelled.Error, "tests failed") {
		t.Fatalf("cancellation was misclassified: success=%v error=%q", cancelled.Success, cancelled.Error)
	}

	expiredCtx, expiredCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer expiredCancel()
	expired, err := tool.Execute(expiredCtx, map[string]any{"framework": "go"})
	if err != nil {
		t.Fatal(err)
	}
	if expired.Success || !strings.Contains(expired.Error, "timed out before completion") ||
		strings.Contains(expired.Error, "tests failed") {
		t.Fatalf("deadline was misclassified: success=%v error=%q", expired.Success, expired.Error)
	}
}

func TestVerifyCodeRejectsMalformedPathInsteadOfUsingProjectRoot(t *testing.T) {
	workspace, _ := setupExecutionScopeProjects(t)
	tool := NewVerifyCodeTool(workspace)
	args := map[string]any{"path": 42}

	if err := tool.Validate(args); err == nil {
		t.Fatal("non-string path passed validation")
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "path: must be a string") {
		t.Fatalf("malformed path silently fell back to project root: success=%v error=%q", result.Success, result.Error)
	}
}

func TestExitPlanModeRejectsUnknownReasonWithoutClearingPlan(t *testing.T) {
	manager := plan.NewManager(true, false)
	active := manager.CreatePlan("Keep me", "A plan that must survive bad input", "request")
	tool := NewExitPlanModeTool()
	tool.SetManager(manager)
	args := map[string]any{"reason": "complete"}

	if err := tool.Validate(args); err == nil {
		t.Fatal("unknown exit reason passed validation")
	}
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || !strings.Contains(result.Error, "reason") {
		t.Fatalf("unknown reason was not rejected: success=%v error=%q", result.Success, result.Error)
	}
	if got := manager.GetCurrentPlan(); got != active {
		t.Fatal("invalid exit reason cleared or replaced the active plan")
	}
}

func TestExitPlanModeTreatsBlankReasonAsDocumentedDefault(t *testing.T) {
	manager := plan.NewManager(true, false)
	tool := NewExitPlanModeTool()
	tool.SetManager(manager)

	result, err := tool.Execute(context.Background(), map[string]any{"reason": "   "})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || !strings.Contains(result.Content, "reason: completed") {
		t.Fatalf("blank reason did not use completed default: success=%v error=%q content=%q", result.Success, result.Error, result.Content)
	}
}
