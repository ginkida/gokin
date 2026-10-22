package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
)

type skillForkRunner struct {
	stubAgentRunner
	ctx                      context.Context
	prompt, agentType, model string
	foreground, background   int
}

type skillForkMCPTool struct{ scriptedStaticTool }

func (*skillForkMCPTool) GetServerName() string       { return "workspace" }
func (*skillForkMCPTool) GetOriginalToolName() string { return "search" }

func (r *skillForkRunner) ResolveSkillModelSettings(model, effort string) (string, string, error) {
	return client.ResolveSkillModelSettings(forkGLMClient{testkit.NewMockClient()}, model, effort)
}

type forkGLMClient struct{ *testkit.MockClient }

func (forkGLMClient) GetModel() string    { return "glm-5.3" }
func (forkGLMClient) GetProvider() string { return "glm" }

func (r *skillForkRunner) Spawn(ctx context.Context, agentType, prompt string, _ int, model string) (string, error) {
	r.ctx, r.prompt, r.agentType, r.model = ctx, prompt, agentType, model
	r.foreground++
	r.results = map[string]AgentResult{"child": {AgentID: "child", Type: agentType, Completed: true, Status: "completed", Output: "child summary"}}
	return "child", nil
}
func (r *skillForkRunner) SpawnAsync(ctx context.Context, agentType, prompt string, _ int, model string) string {
	r.ctx, r.prompt, r.agentType, r.model = ctx, prompt, agentType, model
	r.background++
	r.results = map[string]AgentResult{"child": {AgentID: "child", Type: agentType, Completed: false, Status: "running"}}
	return "child"
}
func (r *skillForkRunner) SpawnAsyncWithStreaming(ctx context.Context, agentType, prompt string, maxTurns int, model string, _ func(string), _ func(string, *AgentProgress)) string {
	return r.SpawnAsync(ctx, agentType, prompt, maxTurns, model)
}

func forkSkillExecutor(t *testing.T, fields, body string) (*Executor, *SkillTool, *TaskTool, *skillForkRunner, string) {
	t.Helper()
	root := t.TempDir()
	writeToolSkill(t, root, "review", "---\ndescription: Review code\ncontext: fork\n"+fields+"\n---\n"+body)
	skill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	runner := &skillForkRunner{}
	task := NewTaskTool()
	task.SetRunner(runner)
	output := NewTaskOutputTool()
	output.SetRunner(runner)
	registry := NewRegistry()
	registry.MustRegister(skill)
	registry.MustRegister(task)
	registry.MustRegister(output)
	registry.MustRegister(NewBashTool(root))
	registry.MustRegister(&scriptedStaticTool{name: "ask_user", content: "question"})
	registry.MustRegister(&skillForkMCPTool{scriptedStaticTool{name: "workspace_search", content: "result"}})
	executor := NewExecutor(registry, nil, time.Second)
	rules := permission.DefaultRules()
	rules.SetPolicy("task", permission.LevelAllow)
	executor.SetPermissions(permission.NewManager(rules, true))
	return executor, skill, task, runner, root
}

func TestForkSkillRunsInChildAndDoesNotActivateParentInstructionsOrGrants(t *testing.T) {
	executor, skill, _, runner, _ := forkSkillExecutor(t, "background: false\nagent: Explore\nallowed-tools: Read\ndisallowed-tools: Write", "Review $0 and summarize.")
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review", "arguments": "parser"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "child summary") || runner.foreground != 1 || runner.agentType != "explore" {
		t.Fatalf("result=%+v err=%v runner=%+v", result, err, runner)
	}
	if !strings.Contains(runner.prompt, "Review parser and summarize.") {
		t.Fatalf("unexpanded child task: %q", runner.prompt)
	}
	if len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 || len(skill.ActivePermissionGrants()) != 0 {
		t.Fatal("fork published instructions or grants into parent")
	}
	childRegistry := NewRegistry()
	childLedger := skills.NewInvocationLedger()
	childCtx, err := ConsumeForkedSkill(runner.ctx, childRegistry, childLedger)
	if err != nil || len(childLedger.SnapshotNewestFirst()) != 1 {
		t.Fatalf("child retention=%v err=%v", childLedger.SnapshotNewestFirst(), err)
	}
	grants, _ := SkillPermissionRules(childCtx, childRegistry)
	if len(grants) != 1 || grants[0] != "read" {
		t.Fatalf("child grants=%v", grants)
	}
	grandchildRegistry := NewRegistry()
	grandchildCtx, err := ConsumeForkedSkill(childCtx, grandchildRegistry, skills.NewInvocationLedger())
	if err != nil {
		t.Fatal(err)
	}
	grants, denies := SkillPermissionRules(grandchildCtx, grandchildRegistry)
	if len(grants) != 0 || len(denies) != 1 || denies[0] != "write" {
		t.Fatalf("grandchild rules grants=%v denies=%v", grants, denies)
	}
	resumed, err := ConsumeForkedSkill(childCtx, childRegistry, childLedger)
	if err != nil {
		t.Fatal(err)
	}
	grants, denies = SkillPermissionRules(resumed, childRegistry)
	if len(grants) != 0 || len(denies) != 0 {
		t.Fatalf("resumed run retained authority: grants=%v denies=%v", grants, denies)
	}
}

func TestForkSkillModelSettingsReachOnlyChildAndExpireOnResume(t *testing.T) {
	executor, skill, _, runner, _ := forkSkillExecutor(t, "background: false\nmodel: haiku\neffort: xhigh", "Review code.")
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
	if err != nil || !result.Success || runner.model != "glm-5.3-flash" {
		t.Fatalf("result=%+v model=%q err=%v", result, runner.model, err)
	}
	parentRegistry := executor.registry
	if ForkedSkillEffort(runner.ctx, parentRegistry) != "" || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
		t.Fatal("fork settings reached parent")
	}
	childRegistry := NewRegistry()
	ledger := skills.NewInvocationLedger()
	childCtx, err := ConsumeForkedSkill(context.WithoutCancel(runner.ctx), childRegistry, ledger)
	if err != nil || ForkedSkillEffort(childCtx, childRegistry) != "max" {
		t.Fatalf("effort=%q err=%v", ForkedSkillEffort(childCtx, childRegistry), err)
	}
	grandchildRegistry := NewRegistry()
	if ForkedSkillEffort(childCtx, grandchildRegistry) != "" {
		t.Fatal("skill effort leaked to grandchild")
	}
	resumed, err := ConsumeForkedSkill(childCtx, childRegistry, ledger)
	if err != nil || ForkedSkillEffort(resumed, childRegistry) != "" {
		t.Fatalf("resumed effort=%q err=%v", ForkedSkillEffort(resumed, childRegistry), err)
	}
	data := result.Data.(map[string]any)
	if data["requested_model"] != "glm-5.3-flash" || data["effort"] != "max" {
		t.Fatalf("settings not reported: %v", data)
	}
}

type forkParentGLMClient struct{ forkGLMClient }

func (forkParentGLMClient) GetModel() string { return "glm-5.3-flash" }

func TestForkSkillInheritUsesInvokingClientInsteadOfRunnerDefault(t *testing.T) {
	executor, _, _, runner, _ := forkSkillExecutor(t, "background: false\nmodel: inherit", "Review code.")
	executor.SetClient(forkParentGLMClient{forkGLMClient{testkit.NewMockClient()}})
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
	if err != nil || !result.Success || runner.model != "glm-5.3-flash" {
		t.Fatalf("result=%+v model=%q err=%v", result, runner.model, err)
	}
}

func TestForkSkillUnsupportedModelSettingsNeverExecuteDynamicContext(t *testing.T) {
	for _, fields := range []string{"model: nonexistent", "model: MiniMax-M2.7", "model: glm-4.7\neffort: high"} {
		t.Run(strings.ReplaceAll(fields, "\n", " "), func(t *testing.T) {
			executor, skill, _, runner, root := forkSkillExecutor(t, "background: false\n"+fields+"\nallowed-tools: Bash", "!`printf x > should-not-exist`\nReview code.")
			result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
			if err != nil || result.Success || runner.foreground != 0 || runner.background != 0 {
				t.Fatalf("result=%+v err=%v runner=%+v", result, err, runner)
			}
			if _, err := os.Stat(filepath.Join(root, "should-not-exist")); !os.IsNotExist(err) {
				t.Fatal("invalid model settings executed shell injection")
			}
			if len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 || len(skill.ActivePermissionGrants()) != 0 {
				t.Fatal("failed model preflight published skill state")
			}
		})
	}
}

func TestForkSkillBackgroundModeHonorsExecutionPolicyAndHeadless(t *testing.T) {
	for _, tc := range []struct {
		name, fields       string
		headless, disabled bool
		background         bool
	}{
		{"default", "", false, false, true},
		{"explicit foreground", "background: false", false, false, false},
		{"headless", "background: true", true, false, false},
		{"background disabled", "", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.disabled {
				t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "1")
			} else {
				t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "0")
			}
			executor, _, task, runner, _ := forkSkillExecutor(t, tc.fields, "Review code.")
			if tc.headless {
				task.SetBackgroundAllowed(false)
			}
			result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
			if err != nil || !result.Success || (runner.background == 1) != tc.background {
				t.Fatalf("result=%+v err=%v foreground=%d background=%d", result, err, runner.foreground, runner.background)
			}
			if tc.background {
				ceiling, restricted := ToolCapabilityCeilingFromContext(runner.ctx)
				if !restricted || toolCapabilityAllows(ceiling, "ask_user") || !toolCapabilityAllows(ceiling, "bash") || !toolCapabilityAllows(ceiling, "workspace_search") {
					t.Fatalf("background capability=%v restricted=%v", ceiling, restricted)
				}
			}
		})
	}
}

func TestForkSkillDenialsAndUnknownAgentNeverStartChild(t *testing.T) {
	for _, tc := range []struct {
		name, fields        string
		ceiling, deny, plan bool
	}{
		{"unknown agent", "agent: unavailable\nallowed-tools: Bash", false, false, false},
		{"capability", "", true, false, false},
		{"permission", "", false, true, false},
		{"plan", "", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "Review code."
			if tc.name == "unknown agent" || tc.ceiling {
				body += "\nState: !`printf x > marker`"
			}
			executor, skill, _, runner, root := forkSkillExecutor(t, tc.fields, body)
			ctx := t.Context()
			if tc.ceiling {
				ctx = ContextWithToolCapabilityCeiling(ctx, []string{"skill"})
			}
			if tc.deny {
				rules := permission.DefaultRules()
				rules.SetPolicy("task", permission.LevelDeny)
				executor.SetPermissions(permission.NewManager(rules, true))
			}
			if tc.plan {
				executor.SetPlanModeCheck(func() bool { return true })
			}
			result, err := executor.InvokeTool(ctx, "skill", map[string]any{"name": "review"})
			if err != nil || result.Success || runner.background+runner.foreground != 0 || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
				t.Fatalf("denied fork=%+v err=%v runner=%+v", result, err, runner)
			}
			if tc.name == "unknown agent" || tc.ceiling {
				if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
					t.Fatalf("shell ran before fork preflight: %v", err)
				}
			}
		})
	}
}

func TestForkSkillRepeatedInvocationRunsNewArgumentsInForeground(t *testing.T) {
	t.Setenv("CLAUDE_CODE_DISABLE_BACKGROUND_TASKS", "0")
	executor, _, _, runner, root := forkSkillExecutor(t, "allowed-tools: Bash", "Context: !`printf x >> runs`\nReview $0.")
	first, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review", "arguments": "first"})
	if err != nil || !first.Success {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review", "arguments": "second"})
	if err != nil || !second.Success || runner.background != 1 || runner.foreground != 1 || !strings.Contains(runner.prompt, "Review second.") {
		t.Fatalf("repeat=%+v err=%v runner=%+v", second, err, runner)
	}
	if data, err := os.ReadFile(filepath.Join(root, "runs")); err != nil || string(data) != "xx" {
		t.Fatalf("each invocation should render once: %q err=%v", data, err)
	}
}

func TestForkSkillOwnerGrantsRemainAvailableDuringNestedSkillRendering(t *testing.T) {
	executor, _, _, runner, root := forkSkillExecutor(t, "background: false\nallowed-tools: Bash", "Review.")
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
	if err != nil || !loaded.Success {
		t.Fatalf("fork=%+v err=%v", loaded, err)
	}
	writeToolSkill(t, root, "context", "---\ndescription: Nested dynamic context\n---\nState: !`printf nested-context`")
	childRegistry := NewRegistry()
	childSkill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	childRegistry.MustRegister(childSkill)
	childRegistry.MustRegister(NewBashTool(root))
	childExecutor := NewExecutor(childRegistry, nil, time.Second)
	childExecutor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
	// The fake synchronous runner returned without consuming its seed. Model
	// the live child's execution context, rather than the parent's closed tool.
	ctx, err := ConsumeForkedSkill(context.WithoutCancel(runner.ctx), childRegistry, childSkill.InvocationLedger())
	if err != nil {
		t.Fatal(err)
	}
	result, err := childExecutor.InvokeTool(ctx, "skill", map[string]any{"name": "context"})
	if err != nil || !result.Success || !strings.Contains(result.Content, "State: nested-context") {
		t.Fatalf("nested=%+v err=%v", result, err)
	}
}
