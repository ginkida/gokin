package tools

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/client"
	"gokin/internal/skills"
	"gokin/internal/testkit"
)

func inlineSkillFixture(t *testing.T) (*Executor, *SkillTool, string) {
	t.Helper()
	root := t.TempDir()
	writeToolSkill(t, root, "fast", "---\nmodel: haiku\neffort: xhigh\n---\nReview code.")
	writeToolSkill(t, root, "inherited", "---\nmodel: inherit\neffort: low\n---\nReview code.")
	writeToolSkill(t, root, "effort-only", "---\neffort: medium\n---\nReview code.")
	skill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	registry := NewRegistry()
	registry.MustRegister(skill)
	registry.MustRegister(NewBashTool(root))
	executor := NewExecutor(registry, forkGLMClient{testkit.NewMockClient()}, time.Second)
	return executor, skill, root
}

func TestInlineSkillModelSettingsAreTurnScopedAndIndependentOfLedger(t *testing.T) {
	executor, skill, _ := inlineSkillFixture(t)
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "fast"})
	if err != nil || !loaded.Success {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
	if settings := SkillModelSettings(executor.registry); settings.Model != "glm-5.3-flash" || settings.Effort != "max" {
		t.Fatalf("settings=%+v", settings)
	}
	loaded, err = executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "inherited"})
	if err != nil || !loaded.Success {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
	if settings := SkillModelSettings(executor.registry); settings.Model != "glm-5.3-flash" || settings.Effort != "low" {
		t.Fatalf("inherit=%+v", settings)
	}
	BeginSkillPermissionTurn(executor.registry)
	if settings := SkillModelSettings(executor.registry); settings != (client.RequestSettings{}) {
		t.Fatalf("settings leaked to next turn: %+v", settings)
	}
	if len(skill.InvocationLedger().SnapshotNewestFirst()) != 2 {
		t.Fatal("turn reset lost durable instructions")
	}
	if _, _, err := skill.InvocationLedger().Record("other", "context", "project", "source"); err != nil {
		t.Fatal(err)
	}
	if settings := SkillModelSettings(executor.registry); settings != (client.RequestSettings{}) {
		t.Fatal("retaining instructions restored model overrides")
	}
}

func TestInlineSkillUserHandoffIgnoresPreviousTurnAndIsOneShot(t *testing.T) {
	executor, skill, _ := inlineSkillFixture(t)
	if loaded, _ := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "fast"}); !loaded.Success {
		t.Fatal(loaded)
	}
	ctx := executor.SkillInvocationContext(t.Context())
	loaded, err := skill.ExecuteForUserArgumentsContext(ctx, "inherited", nil)
	if err != nil || !loaded.Success {
		t.Fatalf("handoff=%+v err=%v", loaded, err)
	}
	// The next user turn starts from the session model, not the preceding
	// turn's Flash override. Preparing it does not change the live scope.
	if SkillModelSettings(executor.registry).Model != "glm-5.3-flash" {
		t.Fatal("handoff changed the previous active turn")
	}
	BeginSkillPermissionTurn(executor.registry)
	if got := SkillModelSettings(executor.registry); got.Model != "glm-5.3" || got.Effort != "low" {
		t.Fatalf("handoff=%+v", got)
	}
	BeginSkillPermissionTurn(executor.registry)
	if got := SkillModelSettings(executor.registry); got != (client.RequestSettings{}) {
		t.Fatalf("pending settings reused: %+v", got)
	}
	loaded, err = skill.ExecuteForUserArgumentsContext(ctx, "fast", nil)
	if err != nil || !loaded.Success {
		t.Fatal(loaded, err)
	}
	ResetSkillPermissionGrants(executor.registry)
	BeginSkillPermissionTurn(executor.registry)
	if got := SkillModelSettings(executor.registry); got != (client.RequestSettings{}) {
		t.Fatalf("cancelled handoff restored: %+v", got)
	}
}

type inlineOtherProviderClient struct{ *testkit.MockClient }

func (inlineOtherProviderClient) GetProvider() string { return "kimi" }
func (inlineOtherProviderClient) GetModel() string    { return "k3" }

func TestInlineSkillInvokingContextRetainsClientSnapshotAfterReplacement(t *testing.T) {
	executor, _, _ := inlineSkillFixture(t)
	ctx := executor.SkillInvocationContext(t.Context())
	executor.SetClient(inlineOtherProviderClient{testkit.NewMockClient()})
	loaded, err := executor.InvokeTool(ctx, "skill", map[string]any{"name": "fast"})
	if err != nil || !loaded.Success {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
	if got := SkillModelSettings(executor.registry); got.Model != "glm-5.3-flash" || got.Effort != "max" {
		t.Fatalf("old request changed provider: %+v", got)
	}
}

func TestInlineSkillEffortOnlyKeepsPreviousModelAndCloneDoesNotInherit(t *testing.T) {
	executor, skill, _ := inlineSkillFixture(t)
	if loaded, _ := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "fast"}); !loaded.Success {
		t.Fatal(loaded)
	}
	if loaded, _ := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "effort-only"}); !loaded.Success {
		t.Fatal(loaded)
	}
	if got := SkillModelSettings(executor.registry); got.Model != "glm-5.3-flash" || got.Effort != "high" {
		t.Fatalf("settings=%+v", got)
	}
	cloned := CloneRegistryForWorkDir(executor.registry, "")
	if got := SkillModelSettings(cloned); got != (client.RequestSettings{}) {
		t.Fatalf("clone inherited settings: %+v", got)
	}
	if clonedTool, _ := cloned.Get("skill"); clonedTool == skill {
		t.Fatal("skill tool was shared")
	}
}

func TestInlineSkillDelegationKeepsSelectedModelWithIndependentChildScope(t *testing.T) {
	executor, _, _ := inlineSkillFixture(t)
	runner := &skillForkRunner{}
	task := NewTaskTool()
	task.SetRunner(runner)
	executor.registry.(*Registry).MustRegister(task)
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "fast"})
	if err != nil || !loaded.Success {
		t.Fatal(loaded, err)
	}
	result, err := executor.InvokeTool(t.Context(), "task", map[string]any{"subagent_type": "general", "prompt": "Review separately."})
	if err != nil || !result.Success || runner.model != "glm-5.3-flash" {
		t.Fatalf("result=%+v model=%q err=%v", result, runner.model, err)
	}
}

func TestInlineSkillModelPreflightAndFailedRenderDoNotPublishOverrides(t *testing.T) {
	for _, tc := range []struct{ name, fields, body string }{
		{"unknown", "model: nonexistent", "!`touch should-not-exist`"},
		{"wrong provider", "model: MiniMax-M2.7", "!`touch should-not-exist`"},
		{"unsupported effort", "model: glm-4.7\neffort: max", "!`touch should-not-exist`"},
		{"failed rendering", "model: haiku\neffort: high", "${CLAUDE_SESSION_ID}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, skill, root := inlineSkillFixture(t)
			writeToolSkill(t, root, "broken", "---\n"+tc.fields+"\nallowed-tools: Bash\n---\n"+tc.body)
			loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "broken"})
			if err != nil || loaded.Success {
				t.Fatalf("load=%+v err=%v", loaded, err)
			}
			if got := SkillModelSettings(executor.registry); got != (client.RequestSettings{}) {
				t.Fatalf("failed load published settings: %+v", got)
			}
			if _, err := os.Stat(filepath.Join(root, "should-not-exist")); !os.IsNotExist(err) {
				t.Fatalf("model validation ran shell: %v", err)
			}
			if len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 || len(skill.ActivePermissionGrants()) != 0 {
				t.Fatal("failed load published instructions or grants")
			}
		})
	}
}
