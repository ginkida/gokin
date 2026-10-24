package donegate

import (
	"context"
	"testing"

	"gokin/internal/tools"
)

func TestDoneGateToolInvokerHonorsCeilingAndOwnsExecution(t *testing.T) {
	registry := tools.NewRegistry()
	registry.MustRegister(&doneGateStubTool{name: "bash"})
	registry.MustRegister(&doneGateStubTool{name: "verify_code"})
	ctx := tools.ContextWithToolCapabilityCeiling(t.Context(), []string{"bash"})
	calls := 0
	reg := WithToolInvoker(ctx, registry, func(ctx context.Context, name string, args map[string]any) (tools.ToolResult, error) {
		calls++
		if name != "bash" || args["command"] != "go test ./..." {
			t.Fatalf("unexpected invocation: %s %v", name, args)
		}
		return tools.NewPolicyBlockedResult(tools.PolicyBlockPermission, "denied"), nil
	})
	if _, ok := reg.Get("verify_code"); ok {
		t.Fatal("check discovery widened the caller's tool ceiling")
	}
	tool, ok := reg.Get("bash")
	if !ok {
		t.Fatal("allowed check disappeared")
	}
	result, err := tool.Execute(ctx, map[string]any{"command": "go test ./..."})
	if err != nil || result.Success || result.PolicyBlock == nil || calls != 1 {
		t.Fatalf("check bypassed owning dispatcher: %+v %v calls=%d", result, err, calls)
	}
}

func TestDoneGateVerificationReceiptDistinguishesSkipsAndDenials(t *testing.T) {
	for _, test := range []struct {
		name, status string
		result       tools.ToolResult
	}{
		{"passed", "passed", tools.NewSuccessResult("ok")},
		{"failed", "failed", tools.NewErrorResult("build failed")},
		{"denied", "denied", tools.NewPolicyBlockedResult(tools.PolicyBlockPermission, "denied")},
		{"skipped", "not_run", tools.ToolResult{Success: true, VerificationSkipped: true}},
		{"toolchain absent", "not_run", tools.NewSuccessResult("go not found — skipping")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "default")
			evidence := 0
			results := RunChecks(ctx, []Check{{Name: "go_vet@.", Run: func(context.Context) (tools.ToolResult, error) { return test.result, nil }}}, 0, nil, func(string) { evidence++ })
			if state.Verification().Status != test.status || (evidence > 0) != (test.status == "passed") {
				t.Fatalf("receipt=%+v results=%+v evidence=%d", state.Verification(), results, evidence)
			}
		})
	}
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "default")
	RunChecks(ctx, []Check{{Name: "git_diff_check", Run: func(context.Context) (tools.ToolResult, error) { return tools.NewSuccessResult("ok"), nil }}}, 0, nil, nil)
	if state.Verification().Status != "not_run" {
		t.Fatal("a git whitespace check claimed build/test verification")
	}
}
