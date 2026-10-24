package tools

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gokin/internal/config"
	"gokin/internal/permission"

	"google.golang.org/genai"
)

func TestScratchpadInternalStatePolicyAndProvenance(t *testing.T) {
	for _, mode := range []string{"acceptEdits", "dontAsk", "plan"} {
		t.Run(mode, func(t *testing.T) {
			var stored string
			registry := NewRegistry()
			registry.MustRegister(NewUpdateScratchpadTool(func(content string) { stored = content }))
			executor := NewExecutor(registry, nil, 0)
			cfg := config.DefaultConfig().Permission
			manager := permission.NewManager(permission.NewRulesFromConfig(cfg.DefaultPolicy, cfg.Rules), true)
			manager.SetDontAsk(mode == "dontAsk")
			executor.SetPermissions(manager)
			executor.SetPlanModeCheck(func() bool { return mode == "plan" })
			ctx, state := ContextWithHeadlessPermissions(t.Context(), false, mode)
			RecordHeadlessVerification(ctx, "earlier tests", "passed")
			result, err := executor.InvokeTool(ctx, "update_scratchpad", map[string]any{"content": "working notes"})
			if err != nil || !result.Success || stored != "working notes" || len(state.Denials()) != 0 {
				t.Fatalf("internal state update refused: %+v %v stored=%q", result, err, stored)
			}
			if IsImplementationTool("update_scratchpad") || isFileModifyingTool("update_scratchpad") || len(WrittenPathsFromResult(result)) > 0 || state.Verification().Status != "passed" {
				t.Fatal("scratchpad treated as project mutation")
			}
			manager.SetRules(permission.NewRulesFromConfig("ask", map[string]string{"update_scratchpad": "deny"}))
			denied, _ := executor.InvokeTool(ctx, "update_scratchpad", map[string]any{"content": "forbidden"})
			if denied.Success || denied.PolicyBlock == nil || stored != "working notes" {
				t.Fatalf("explicit deny bypassed: %+v", denied)
			}
		})
	}
}

func TestScratchpadPolicyClassificationMatchesRealRegistry(t *testing.T) {
	if _, ok := DefaultRegistry(t.TempDir()).Get("update_scratchpad"); !ok {
		t.Fatal("scratchpad is absent from eager registry")
	}
	if _, ok := DefaultLazyRegistry(t.TempDir()).Get("update_scratchpad"); !ok {
		t.Fatal("scratchpad is absent from lazy registry")
	}
	meta, ok := NewDefaultSafetyValidator().GetMetadata("update_scratchpad")
	if !ok || meta.SafetyLevel != SafetyLevelSafe || permission.GetToolRiskLevel("update_scratchpad") != permission.RiskLow || permission.DefaultRules().GetPolicy("update_scratchpad") != permission.LevelAllow || config.DefaultConfig().Permission.Rules["update_scratchpad"] != "allow" || !idempotentStateTools["update_scratchpad"] || !IsReadOnlyForPlanMode("update_scratchpad") {
		t.Fatalf("internal-state policy drift: metadata=%+v", meta)
	}
}

func TestVerificationTracksBuildWithSuccessMarker(t *testing.T) {
	for _, status := range []string{"passed", "failed", "denied"} {
		t.Run(status, func(t *testing.T) {
			ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
			result := NewSuccessResult("BUILD_OK")
			if status == "failed" {
				result = NewErrorResult("build failed")
			}
			if status == "denied" {
				result = NewPolicyBlockedResult(PolicyBlockPermission, "denied")
			}
			ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "go build ./... && echo BUILD_OK"}}, result)
			ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "verify_code", Args: map[string]any{"path": "/project"}}, result)
			receipt := state.Verification()
			if receipt.Status != status || len(receipt.Checks) != 2 || !strings.HasPrefix(receipt.Checks[0].Name, "bash: go build ./...") || receipt.Checks[0].Status != status {
				t.Fatalf("validation attempt disappeared: %+v", receipt)
			}
		})
	}
}

func TestDeniedShellCannotInvalidateEarlierVerification(t *testing.T) {
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "go test ./..."}}, NewSuccessResult("passed"))
	ObserveHeadlessToolResult(ctx, &genai.FunctionCall{Name: "bash", Args: map[string]any{"command": "echo changed > m.go"}}, NewPolicyBlockedResult(PolicyBlockPermission, "denied"))
	if state.Verification().Status != "passed" {
		t.Fatalf("denied shell erased evidence: %+v", state.Verification())
	}
}

func TestMutationDenialExplainsOnlyExecutedTargetArguments(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    map[string]any
		updated map[string]any
		want    []string
	}{
		{"edit", map[string]any{"file_path": "m.go", "path": "other.go", "new_path": "fake.go"}, nil, []string{"m.go"}},
		{"edit", map[string]any{"file_path": "original.go"}, map[string]any{"file_path": "actual.go"}, []string{"actual.go"}},
		{"copy", map[string]any{"source": "read.go", "destination": "written.go"}, nil, []string{"written.go"}},
		{"bash", map[string]any{"command": "go build ./...", "file_path": "fake.go"}, nil, nil},
		{"ssh", map[string]any{"action": "download", "local_path": "m.go"}, nil, []string{"m.go"}},
		{"ssh", map[string]any{"action": "upload", "local_path": "read.go"}, nil, nil},
		{"batch", map[string]any{"files": []any{"m.go"}, "dry_run": true}, nil, nil},
	} {
		ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
		denied := NewPolicyBlockedResult(PolicyBlockPermission, "denied")
		denied.HookUpdatedInput = tc.updated
		ApplyHeadlessPermissionResult(ctx, &genai.FunctionCall{Name: tc.name, Args: tc.args}, denied)
		if got := HeadlessDeniedMutationPaths(ctx); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s args=%v denied targets=%v want=%v", tc.name, tc.args, got, tc.want)
		}
		data, _ := json.Marshal(state.Denials())
		if strings.Contains(string(data), ".go") {
			t.Fatalf("private path leaked into denial receipt: %s", data)
		}
	}
}

func TestVerificationSuccessMarkerCannotMaskSkippedOrUnsafeChecks(t *testing.T) {
	for _, command := range []string{"echo 'go build ./... && echo BUILD_OK'", "go build ./... || echo BUILD_OK", "false && go build ./... && echo BUILD_OK", "go build ./... | echo BUILD_OK", "go build ./... && echo BUILD_OK > changed.go"} {
		if IsVerificationCommand(command) {
			t.Errorf("unproven/mutating chain accepted: %s", command)
		}
	}
}
