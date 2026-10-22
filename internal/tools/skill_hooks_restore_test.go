package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
)

func TestSkillHookRestorePreservesOnceAndDoesNotRenderShellAgain(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Write", "printf x >> TEST_ROOT/hook-runs", "allowed-tools: Bash", "Review !`printf x >> TEST_ROOT/render-runs; printf rendered`.")
	path := filepath.Join(root, "guard", "SKILL.md")
	data, _ := os.ReadFile(path)
	data = []byte(strings.ReplaceAll(string(data), "TEST_ROOT", root))
	data = []byte(strings.Replace(string(data), "          timeout: 2", "          timeout: 2\n          once: true", 1))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	loadGuardSkill(t, executor)
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "first.txt"), "content": "first"})
	if err != nil || !result.Success {
		t.Fatalf("first write=%+v err=%v", result, err)
	}
	refs, _ := skill.InvocationLedger().HookActivations()
	if len(refs) != 1 || !refs[0].Consumed[0] {
		t.Fatalf("once not recorded: %+v", refs)
	}
	restored := skills.NewInvocationLedger()
	if err := restored.Restore(skill.InvocationLedger().SnapshotNewestFirst()); err != nil {
		t.Fatal(err)
	}
	if err := restored.RestoreHookActivations(refs); err != nil {
		t.Fatal(err)
	}
	newSkill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	newSkill.SetInvocationLedger(restored)
	registry := NewRegistry()
	registry.MustRegister(newSkill)
	registry.MustRegister(NewBashTool(root))
	registry.MustRegister(NewWriteTool(root))
	resumed := NewExecutor(registry, testkit.NewMockClient(), time.Second)
	resumed.SetWorkDir(root)
	resumed.SetHooks(hooks.NewManager(true, root))
	result, err = resumed.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "second.txt"), "content": "second"})
	if err != nil || !result.Success {
		t.Fatalf("restored write=%+v err=%v", result, err)
	}
	for _, file := range []string{"hook-runs", "render-runs"} {
		contents, err := os.ReadFile(filepath.Join(root, file))
		if err != nil || string(contents) != "x" {
			t.Fatalf("restore reran %s: %q (%v)", file, contents, err)
		}
	}
	// Explicitly reloading identical frontmatter also must not rearm once.
	loadGuardSkill(t, resumed)
	result, err = resumed.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "third.txt"), "content": "third"})
	contents, readErr := os.ReadFile(filepath.Join(root, "hook-runs"))
	if err != nil || !result.Success || readErr != nil || string(contents) != "x" {
		t.Fatalf("identical reload rearmed once: %+v %q %v", result, contents, err)
	}
}

func TestSkillHookRestoreBlocksBeforeProviderAndTrustRevocationReachesChild(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Write", "printf denied >&2; exit 2", "", "Review.")
	loadGuardSkill(t, executor)
	refs, _ := skill.InvocationLedger().HookActivations()
	refs[0].Consumed[0] = true // Invalid: this handler is not once.
	if err := skill.InvocationLedger().RestoreHookActivations(refs); err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().EnqueueText("must not request")
	executor.SetClient(mock)
	if _, _, err := executor.Execute(t.Context(), nil, "review"); !IsHookStopError(err) || len(mock.Calls()) != 0 {
		t.Fatalf("invalid restore reached model: %v calls=%d", err, len(mock.Calls()))
	}
	// Restore the exact valid reference and verify a fresh epoch recovers.
	refs[0].Consumed[0] = false
	if err := skill.InvocationLedger().RestoreHookActivations(refs); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSkillHooks(executor.registry, executor.hooks); err != nil {
		t.Fatal(err)
	}
	// Project hooks lose executable authority immediately when trust is revoked,
	// including a child that already inherited the parent's stable scope.
	project := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "project"}}), root)
	project.SetWorkspaceTrusted(true)
	parent := NewRegistry()
	parent.MustRegister(project)
	owned := NewExecutor(parent, testkit.NewMockClient(), time.Second)
	owned.SetWorkDir(root)
	owned.SetHooks(executor.hooks)
	loadGuardSkill(t, owned)
	child := NewRegistry() // No Skill tool; inherited checks still apply.
	ctx := ContextWithDelegatedSkillHooks(owned.SkillInvocationContext(t.Context()), child, parent, executor.hooks)
	if err := SkillHooksError(ctx); err != nil {
		t.Fatal(err)
	}
	project.SetWorkspaceTrusted(false)
	if err := SkillHooksError(ctx); !IsHookStopError(err) {
		t.Fatalf("child bypassed revoked trust: %v", err)
	}
}

func TestFailedOnceHookRemainsRetryableAfterRestore(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Write", "printf x >> TEST_ROOT/hook-runs; test -e TEST_ROOT/ready", "", "Review.")
	path := filepath.Join(root, "guard", "SKILL.md")
	body, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), "          timeout: 2", "          timeout: 2\n          once: true", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	loadGuardSkill(t, executor)
	_, _ = executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "first.txt"), "content": "first"})
	refs, _ := skill.InvocationLedger().HookActivations()
	if len(refs) != 1 || refs[0].Consumed[0] {
		t.Fatal("failed hook consumed once")
	}
	if err := skill.InvocationLedger().RestoreHookActivations(refs); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ready"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "second.txt"), "content": "second"})
	contents, readErr := os.ReadFile(filepath.Join(root, "hook-runs"))
	refs, _ = skill.InvocationLedger().HookActivations()
	if err != nil || !result.Success || readErr != nil || string(contents) != "xx" || !refs[0].Consumed[0] {
		t.Fatalf("failed once did not retry after restore: %+v %q %v", result, contents, err)
	}
}

func TestRemovingHookFrontmatterOnExplicitReloadRemovesPersistedRegistration(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Write", "exit 2", "", "Review.")
	loadGuardSkill(t, executor)
	if err := os.WriteFile(filepath.Join(root, "guard", "SKILL.md"), []byte("---\ndescription: Guard\n---\nReview."), 0o600); err != nil {
		t.Fatal(err)
	}
	loadGuardSkill(t, executor)
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "allowed.txt"), "content": "allowed"})
	refs, _ := skill.InvocationLedger().HookActivations()
	if err != nil || !result.Success || len(refs) != 0 || len(skill.HookScope().Hooks()) != 0 {
		t.Fatalf("removed hooks stayed active: %+v refs=%v err=%v", result, refs, err)
	}
}

func TestHookPolicyToggleDuringRenderCannotPublishUnguardedSkill(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Bash", "exit 0", "allowed-tools: Bash", "!`printf x > forbidden`")
	ctx := executor.SkillInvocationContext(t.Context())
	ctx = ContextWithSkillShellInvoker(ctx, func(ctx context.Context, command string) (ToolResult, error) {
		executor.hooks.SetEnabled(false)
		return executor.InvokeTool(ctx, "bash", map[string]any{"command": command})
	})
	loaded, err := skill.ExecuteForUserRawArgumentsContext(ctx, "guard", "")
	if err != nil || loaded.Success || loaded.HookStopReason == "" || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
		t.Fatalf("policy toggle dropped render guard: %+v err=%v", loaded, err)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("unguarded shell executed")
	}
}
