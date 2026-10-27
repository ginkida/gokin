package donegate

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"gokin/internal/permission"
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
	}, nil)
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

func TestDoneGatePreflightsEachCheckWithoutDispatchingDenials(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "strict"}[strict], func(t *testing.T) {
			registry := tools.NewRegistry()
			for _, name := range []string{"bash", "verify_code", "run_tests"} {
				registry.MustRegister(&doneGateStubTool{name: name})
			}
			executor := tools.NewExecutor(registry, nil, time.Second)
			executor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
			ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), strict, "acceptEdits")
			preflights, dispatches := 0, 0
			reg := WithToolInvoker(ctx, registry, func(ctx context.Context, name string, args map[string]any) (tools.ToolResult, error) {
				dispatches++
				return executor.InvokeTool(ctx, name, args)
			}, func(ctx context.Context, name string, args map[string]any) bool {
				preflights++
				return executor.PreflightToolPermission(ctx, name, args)
			})
			work := t.TempDir()
			profile := Profile{WorkDir: work, GoModules: []string{work}, TouchedPaths: []string{"m.go"}}
			checks := BuildChecks(reg, work, "Update m.go", []string{"edit"}, profile, Policy{Mode: "strict"})
			results := RunChecks(ctx, checks, time.Second, nil, nil)
			if len(results) != 5 || preflights != 5 || dispatches != 0 || !AllFailuresDenied(results) || len(state.Denials()) != 0 || tools.PermissionStopError(ctx) != nil {
				t.Fatalf("results=%+v preflights=%d dispatches=%d denials=%v", results, preflights, dispatches, state.Denials())
			}
			var names []string
			for _, check := range state.Verification().Checks {
				if check.Status != "denied" {
					t.Fatalf("check=%+v", check)
				}
				names = append(names, check.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, []string{"git diff --check", "git unmerged paths check", "go vet", "run tests go .", "verify code"}) {
				t.Fatalf("unreadable or missing check names: %v", names)
			}
		})
	}
}

func TestDoneGateVerificationReceiptRedactsLabelsAndIDs(t *testing.T) {
	ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "default")
	RunChecks(ctx, []Check{{Name: "go_vet@--token=private-value", Evidence: "go vet --token=private-value", Run: func(context.Context) (tools.ToolResult, error) {
		return tools.NewPolicyBlockedResult(tools.PolicyBlockPermission, "denied"), nil
	}}}, 0, nil, nil)
	check := state.Verification().Checks[0]
	if check.Status != "denied" || strings.Contains(check.ID+check.Name, "private-value") || !strings.Contains(check.Name, "go vet") || !strings.Contains(check.Name, "[REDACTED]") {
		t.Fatalf("unsafe receipt: %+v", check)
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

func TestDoneGateGitRechecksReplaceDeniedReceipts(t *testing.T) {
	for _, name := range []string{"git_diff_check", "git_unmerged_paths"} {
		t.Run(name, func(t *testing.T) {
			ctx, state := tools.ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
			result := tools.NewPolicyBlockedResult(tools.PolicyBlockPermission, "denied")
			check := Check{Name: name, Run: func(context.Context) (tools.ToolResult, error) { return result, nil }}
			RunChecks(ctx, []Check{check}, 0, nil, nil)
			if state.Verification().Status != "denied" {
				t.Fatal("missing initial denial", state.Verification())
			}
			result = tools.NewSuccessResult("clean")
			RunChecks(ctx, []Check{check}, 0, nil, nil)
			summary := state.Verification()
			if summary.Status != "not_run" || len(summary.Checks) != 1 || summary.Checks[0].Status != "passed" {
				t.Fatalf("stale refusal or inflated verification: %+v", summary)
			}
			tools.RecordHeadlessVerification(ctx, "verify_code", "passed")
			if state.Verification().Status != "passed" {
				t.Fatal("successful verification hidden by stale Git denial", state.Verification())
			}
		})
	}
}
