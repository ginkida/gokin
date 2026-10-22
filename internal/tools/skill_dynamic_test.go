package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/skills"
)

func dynamicSkillExecutor(t *testing.T, body, grants, denies string, trusted bool) (*Executor, *SkillTool, *permission.Manager, string) {
	t.Helper()
	workDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	frontmatter := "---\ndescription: Dynamic context test\n"
	if grants != "" {
		frontmatter += "allowed-tools: " + grants + "\n"
	}
	if denies != "" {
		frontmatter += "disallowed-tools: " + denies + "\n"
	}
	writeToolSkill(t, root, "dynamic", frontmatter+"---\n"+body)
	skill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-project"}}), workDir)
	skill.SetWorkspaceTrusted(trusted)
	registry := NewRegistry()
	registry.MustRegister(skill)
	registry.MustRegister(NewBashTool(workDir))
	executor := NewExecutor(registry, nil, 3*time.Second)
	manager := permission.NewManager(permission.DefaultRules(), true)
	executor.SetPermissions(manager)
	return executor, skill, manager, workDir
}

func TestDynamicSkillUsesExecutorAndRendersCommandsExactlyOnce(t *testing.T) {
	executor, skill, _, workDir := dynamicSkillExecutor(t,
		"Argument: $0\nState: !`printf 'inline-result'`\n```!\nprintf x >> runs\nprintf '%s' '!`touch evil` $0 ${CLAUDE_PROJECT_DIR}'\n```", "Bash", "", true)
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "dynamic", "arguments": "\"!`touch argument-evil`\""})
	if err != nil || !result.Success {
		t.Fatalf("load=%+v err=%v", result, err)
	}
	for _, want := range []string{"inline-result", "!`touch evil`", "!`touch argument-evil`", workDir} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("missing %q: %s", want, result.Content)
		}
	}
	if data, err := os.ReadFile(filepath.Join(workDir, "runs")); err != nil || string(data) != "x" {
		t.Fatalf("shell executed more than once: %q err=%v", data, err)
	}
	for _, name := range []string{"evil", "argument-evil"} {
		if _, err := os.Stat(filepath.Join(workDir, name)); !os.IsNotExist(err) {
			t.Fatalf("literal output/argument executed: %s (%v)", name, err)
		}
	}
	if snapshots := skill.InvocationLedger().SnapshotNewestFirst(); len(snapshots) != 1 || snapshots[0].Rendered != result.Content {
		t.Fatalf("canonical render not retained: %+v", snapshots)
	}
}

func TestDynamicSkillDenialsDoNotPromptOrPublishAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, grants, denies               string
		trusted, configDeny, plan, ceiling bool
	}{
		{name: "approval required"},
		{name: "untrusted project", grants: "Bash"},
		{name: "scoped grant mismatch", grants: "Bash(git status *)", trusted: true},
		{name: "skill deny", grants: "Bash", denies: "Bash", trusted: true},
		{name: "config deny", grants: "Bash", trusted: true, configDeny: true},
		{name: "plan mode", grants: "Bash", trusted: true, plan: true},
		{name: "capability ceiling", grants: "Bash", trusted: true, ceiling: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor, skill, manager, workDir := dynamicSkillExecutor(t, "State: !`printf x > forbidden`", tc.grants, tc.denies, tc.trusted)
			prompts := 0
			manager.SetPromptHandler(func(context.Context, *permission.Request) (permission.Decision, error) {
				prompts++
				return permission.DecisionAllow, nil
			})
			if tc.configDeny {
				rules := permission.DefaultRules()
				rules.SetPolicy("bash", permission.LevelDeny)
				manager.SetRules(rules)
			}
			if tc.plan {
				executor.SetPlanModeCheck(func() bool { return true })
			}
			ctx := t.Context()
			if tc.ceiling {
				ctx = ContextWithToolCapabilityCeiling(ctx, []string{"skill"})
			}
			result, err := executor.InvokeTool(ctx, "skill", map[string]any{"name": "dynamic"})
			if err != nil || result.Success || prompts != 0 {
				t.Fatalf("load=%+v err=%v prompts=%d", result, err, prompts)
			}
			if result.PolicyBlock == nil {
				t.Fatalf("permission failure lost policy metadata: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(workDir, "forbidden")); !os.IsNotExist(err) {
				t.Fatalf("denied shell changed filesystem: %v", err)
			}
			if len(skill.ActivePermissionGrants()) != 0 || len(skill.ActivePermissionDenies()) != 0 || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
				t.Fatal("failed render published permission rules or instructions")
			}
		})
	}
}

func TestDynamicSkillFailuresStopLaterCommandsAndRespectHooks(t *testing.T) {
	for _, useHook := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonzero exit", true: "hook block"}[useHook], func(t *testing.T) {
			executor, skill, _, workDir := dynamicSkillExecutor(t, "Fail: !`exit 2`\nLater: !`printf x > forbidden`", "Bash", "", true)
			if useHook {
				manager := hooks.NewManager(true, workDir)
				manager.AddHook(&hooks.Hook{Name: "block-shell", Type: hooks.PreTool, ToolName: "bash", Command: "exit 1", Enabled: true, FailOnError: true})
				executor.SetHooks(manager)
			}
			result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "dynamic"})
			if err != nil || result.Success {
				t.Fatalf("load=%+v err=%v", result, err)
			}
			if _, err := os.Stat(filepath.Join(workDir, "forbidden")); !os.IsNotExist(err) {
				t.Fatalf("later command ran after failure: %v", err)
			}
			if len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 || len(skill.ActivePermissionGrants()) != 0 {
				t.Fatal("failed render retained instructions or grants")
			}
		})
	}
}

func TestDynamicSkillValidatesAllExpansionBeforeExecuting(t *testing.T) {
	executor, _, _, workDir := dynamicSkillExecutor(t, "First: !`printf x > forbidden`\nSecond: !`$ARGUMENTS`", "Bash", "", true)
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "dynamic"})
	if err != nil || result.Success || !strings.Contains(result.Error, "empty command") {
		t.Fatalf("load=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("first shell ran before expansion failed: %v", err)
	}
}

func TestDynamicSkillCancellationStopsRemainingShells(t *testing.T) {
	executor, skill, _, workDir := dynamicSkillExecutor(t, "Wait: !`sleep 10`\nLater: !`printf x > forbidden`", "Bash", "", true)
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	start := time.Now()
	result, _ := executor.InvokeTool(ctx, "skill", map[string]any{"name": "dynamic"})
	if result.Success || time.Since(start) > 2*time.Second {
		t.Fatalf("cancel did not settle render promptly: %+v duration=%v", result, time.Since(start))
	}
	if _, err := os.Stat(filepath.Join(workDir, "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("command ran after cancellation: %v", err)
	}
	if len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
		t.Fatal("cancelled render recorded instructions")
	}
}

func TestClaudeSkillArgumentSemantics(t *testing.T) {
	for _, tc := range []struct{ body, arguments, want string }{
		{"$0 / $1 / $ARGUMENTS[0]", "one two", "one / two / one"},
		{"$2 / $ARGUMENTS[5]", "one", "$2 / $ARGUMENTS[5]\n\nARGUMENTS: one"},
		{"$name", "", ""},
		{"\\$0 / \\\\$0", "one", "$0 / \\\\one"},
		{"$ARGUMENTS", "\"$0\"  two", "\"$0\"  two"},
		{"$0", "'${CLAUDE_PROJECT_DIR}'", "/workspace"},
	} {
		skill := skills.Skill{Body: tc.body, Source: "claude-project", Arguments: []string{"name"}, Path: "/workspace/.claude/skills/example/SKILL.md"}
		got, err := expandSkillArguments(skill, tc.arguments, nil, "/workspace")
		if err != nil || got != tc.want {
			t.Errorf("%q args=%q: got=%q want=%q err=%v", tc.body, tc.arguments, got, tc.want, err)
		}
	}
}

func TestDynamicSkillShellDisablePolicySurvivesCloning(t *testing.T) {
	_, skill, _, workDir := dynamicSkillExecutor(t, "First: !`printf x > forbidden`\n```!\nprintf later\n```", "Bash", "", true)
	skill.SetShellExecutionDisabled(true)
	cloned := CloneToolForWorkDir(skill, "").(*SkillTool)
	result, err := cloned.Execute(t.Context(), map[string]any{"name": "dynamic"})
	if err != nil || !result.Success || strings.Count(result.Content, "[shell command execution disabled by policy]") != 2 {
		t.Fatalf("disabled render=%+v err=%v", result, err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("disabled shell executed: %v", err)
	}
	worktreeClone := CloneToolForWorkDir(skill, t.TempDir()).(*SkillTool)
	if !worktreeClone.ShellExecutionDisabled() {
		t.Fatal("isolated-workspace clone widened shell policy")
	}
}

func TestDynamicSkillDeniesRemainAuthoritativeWithoutPermissionManager(t *testing.T) {
	executor, _, _, workDir := dynamicSkillExecutor(t, "State: !`printf x > forbidden`", "Bash", "Bash", true)
	executor.SetPermissions(nil)
	result, _ := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "dynamic"})
	if result.Success || result.PolicyBlock == nil {
		t.Fatalf("embedder bypassed skill deny: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(workDir, "forbidden")); !os.IsNotExist(err) {
		t.Fatalf("denied shell executed: %v", err)
	}
}
