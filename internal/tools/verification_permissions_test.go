package tools

import (
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"gokin/internal/permission"
	"gokin/internal/skills"
	"google.golang.org/genai"
)

func TestHeadlessToolPolicyRetainsPotentialTrustedSkillGrants(t *testing.T) {
	for _, test := range []struct {
		name, source, grant string
		trusted, hardDeny   bool
		advertise           bool
	}{
		{name: "personal", source: "claude-global", grant: "Bash", advertise: true},
		{name: "trusted project", source: "project", trusted: true, grant: "Bash", advertise: true},
		{name: "untrusted project", source: "project", grant: "Bash"},
		{name: "scoped grant", source: "claude-global", grant: "Bash(go test:*)", advertise: true},
		{name: "unrelated grant", source: "claude-global", grant: "Read"},
		{name: "advertisement cannot override deny", source: "claude-global", grant: "Bash", hardDeny: true, advertise: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeToolSkill(t, root, "check", "---\nallowed-tools: "+test.grant+"\n---\nRun the check.")
			registry := NewRegistry()
			skill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: test.source}}), root)
			skill.SetWorkspaceTrusted(test.trusted)
			registry.MustRegister(skill)
			registry.MustRegister(&scriptedStaticTool{name: "bash", content: "must not execute"})
			rules := permission.DefaultRules()
			if test.hardDeny {
				rules.SetPolicy("bash", permission.LevelDeny)
			}
			executor := NewExecutor(registry, nil, time.Second)
			executor.SetPermissions(permission.NewManager(rules, true))
			ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
			ctx = executor.ContextWithHeadlessToolPolicy(ctx)
			if advertised := slices.Contains(toolDeclarationNames(FilterHeadlessTools(ctx, registry.GeminiTools())), "bash"); advertised != test.advertise {
				t.Fatalf("bash advertised=%v want=%v", advertised, test.advertise)
			}
			if executor.PreflightToolPermission(ctx, "bash", map[string]any{"command": "go test ./..."}) || len(skill.ActivePermissionGrants()) != 0 || len(state.Denials()) != 0 {
				t.Fatal("catalog discovery activated authority or executed a tool")
			}
		})
	}
}

func TestHeadlessToolPolicyFreezesAdvertisementWithoutChangingDispatch(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"read", "bash", "verify_code", "run_tests"} {
		registry.MustRegister(&scriptedStaticTool{name: name, content: "ok"})
	}
	manager := permission.NewManager(permission.DefaultRules(), true)
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetPermissions(manager)
	ctx, state := ContextWithHeadlessPermissions(t.Context(), true, "acceptEdits")
	ctx = ContextWithToolSchemaCeiling(ctx, executor, registry.Names())
	ctx = executor.ContextWithHeadlessToolPolicy(ctx)
	filtered := FilterHeadlessTools(ctx, registry.GeminiTools())
	if names := toolDeclarationNames(filtered); !slices.Equal(names, []string{"read"}) || !HeadlessVerificationUnavailable(ctx) {
		t.Fatalf("advertised=%v verification unavailable=%v", names, HeadlessVerificationUnavailable(ctx))
	}
	// A model that ignores the schema still receives an actual, strict denial.
	executor.executeTool(ctx, testFunctionCall("model-check", "bash", map[string]any{"command": "go test ./..."}))
	err := PermissionStopError(ctx)
	if !errors.Is(err, ErrStrictPermissionDenied) || len(state.Denials()) != 1 {
		t.Fatalf("strict semantics changed: err=%v denials=%v", err, state.Denials())
	}
	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelAllow)
	manager.SetRules(rules)
	if !slices.Equal(toolDeclarationNames(FilterHeadlessTools(ctx, registry.GeminiTools())), []string{"read"}) {
		t.Fatal("policy change rewrote the invocation's schema prefix")
	}
	if len(toolDeclarationNames(FilterHeadlessTools(t.Context(), registry.GeminiTools()))) != 4 {
		t.Fatal("headless filtering leaked into an interactive turn")
	}
}

func TestHeadlessToolPolicyDoesNotSuppressReviewWithoutAPermissionRestriction(t *testing.T) {
	registry := NewRegistry()
	registry.MustRegister(&scriptedStaticTool{name: "write", content: "ok"})
	executor := NewExecutor(registry, nil, time.Second)
	ctx, _ := ContextWithHeadlessPermissions(t.Context(), false, "default")
	ctx = executor.ContextWithHeadlessToolPolicy(ctx)
	if HeadlessVerificationUnavailable(ctx) {
		t.Fatal("absence of verification tools was mistaken for a permission denial")
	}
}

func TestHeadlessToolPolicyKeepsPatternAllowsAndVerificationPoliciesIndependent(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{"bash", "verify_code", "run_tests"} {
		registry.MustRegister(&scriptedStaticTool{name: name, content: "ok"})
	}
	rules := permission.DefaultRules()
	rules.SetPolicy("bash", permission.LevelDeny)
	rules.SetPolicy("verify_code", permission.LevelAllow)
	rules.SetPolicy("run_tests", permission.LevelAllow)
	manager := permission.NewManager(rules, true)
	executor := NewExecutor(registry, nil, time.Second)
	executor.SetPermissions(manager)
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	for _, name := range []string{"verify_code", "run_tests"} {
		if !executor.PreflightToolPermission(ctx, name, nil) {
			t.Fatalf("bash policy incorrectly suppressed %s", name)
		}
		result, err := executor.InvokeTool(ctx, name, nil)
		if err != nil || !result.Success {
			t.Fatalf("dispatch disagreed for %s: %+v %v", name, result, err)
		}
	}
	if executor.PreflightToolPermission(ctx, "bash", map[string]any{"command": "go test ./..."}) || len(state.Denials()) != 0 {
		t.Fatal("preflight attempted a forbidden check")
	}
	rules.SetPolicy("bash", permission.LevelAsk)
	manager.SetRules(rules)
	manager.SetRunToolRules([]string{"bash(go test:*)"}, nil)
	ctx = executor.ContextWithHeadlessToolPolicy(ctx)
	if !slices.Contains(toolDeclarationNames(FilterHeadlessTools(ctx, registry.GeminiTools())), "bash") {
		t.Fatal("pattern-scoped allow was classified as an unconditional deny")
	}
	if !executor.PreflightToolPermission(ctx, "bash", map[string]any{"command": "go test ./..."}) || executor.PreflightToolPermission(ctx, "bash", map[string]any{"command": "go build ./..."}) {
		t.Fatal("preflight ignored the command-specific permission decision")
	}
}

func TestVerificationPreflightLeavesHooksToDispatcher(t *testing.T) {
	executor, manager, root, _ := settingsExecutorFixture(t, `printf '%s' '{"continue":false,"stopReason":"Stop verification"}'`)
	executor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
	if err := manager.RefreshClaudeSettings(); err != nil {
		t.Fatal(err)
	}
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	ctx = executor.ContextWithHeadlessToolPolicy(ctx)
	if !slices.Contains(toolDeclarationNames(FilterHeadlessTools(ctx, executor.registry.GeminiTools())), "write") {
		t.Fatal("pre-tool hook authorization was hidden")
	}
	args := map[string]any{"file_path": root + "/out.txt", "content": "text"}
	if !executor.PreflightToolPermission(ctx, "write", args) {
		t.Fatal("preflight suppressed the terminal hook")
	}
	result, err := executor.InvokeTool(ctx, "write", args)
	if err != nil || result.HookStopReason != "Stop verification" || len(state.Denials()) != 0 {
		t.Fatalf("hook stop changed: result=%+v err=%v denials=%v", result, err, state.Denials())
	}
}

func TestVerificationPreflightRefreshesHookErrorsBeforeSkippingDispatch(t *testing.T) {
	executor, manager, root, settings := settingsExecutorFixture(t, "exit 0")
	if err := os.WriteFile(settings, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.RefreshClaudeSettings(); err != nil {
		t.Fatal(err)
	}
	rules := permission.DefaultRules()
	rules.SetPolicy("write", permission.LevelDeny)
	executor.SetPermissions(permission.NewManager(rules, true))
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	if err := os.WriteFile(settings, []byte(`{"hooks":`), 0600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"file_path": root + "/out.txt", "content": "must not write"}
	if !executor.PreflightToolPermission(ctx, "write", args) {
		t.Fatal("preflight bypassed the dispatcher's hook settings failure")
	}
	result, err := executor.InvokeTool(ctx, "write", args)
	if err != nil || result.HookStopReason == "" || len(state.Denials()) != 0 {
		t.Fatalf("hook failure was replaced by permission denial: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(root + "/out.txt"); !os.IsNotExist(err) {
		t.Fatal("tool executed despite invalid hook settings", err)
	}
}

func toolDeclarationNames(declarations []*genai.Tool) []string {
	var names []string
	for _, declaration := range declarations {
		for _, function := range declaration.FunctionDeclarations {
			names = append(names, function.Name)
		}
	}
	slices.Sort(names)
	return names
}
