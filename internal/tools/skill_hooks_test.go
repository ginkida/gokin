package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func TestSkillHookStopsRemainingBatchCallsAndPairsEveryResult(t *testing.T) {
	executor, _, root := hookSkillFixture(t, "Write", `printf x >> hook-runs; printf '%s' '{"continue":false,"stopReason":"Stop this batch"}'`, "", "Review code.")
	loadGuardSkill(t, executor)
	read := &scriptedStaticTool{name: "read", content: "must not run"}
	if err := executor.registry.Register(read); err != nil {
		t.Fatal(err)
	}
	calls := []*genai.FunctionCall{
		{ID: "first", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "first.txt"), "content": "first"}},
		{ID: "second", Name: "write", Args: map[string]any{"file_path": filepath.Join(root, "second.txt"), "content": "second"}},
		{ID: "read", Name: "read", Args: map[string]any{}},
	}
	results, err := executor.executeTools(t.Context(), calls)
	if err != nil || len(results) != len(calls) || HookStopError(results) == nil || read.calls != 0 {
		t.Fatalf("results=%v err=%v read calls=%d", results, err, read.calls)
	}
	for i, result := range results {
		if result == nil || result.ID != calls[i].ID {
			t.Fatalf("unpaired result at %d", i)
		}
		if i > 0 && !strings.Contains(result.Response["error"].(string), "not executed") {
			t.Fatalf("skipped result=%v", result.Response)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "hook-runs"))
	if err != nil || string(data) != "x" {
		t.Fatalf("subsequent hook ran: %q err=%v", data, err)
	}
}

func TestForkSkillOwnHookGuardsRenderingAndRequiresChildOwner(t *testing.T) {
	fields := "background: false\nallowed-tools: Bash\nhooks:\n  PreToolUse:\n    - matcher: Bash\n      hooks:\n        - type: command\n          command: 'printf denied >&2; exit 2'"
	executor, skill, _, runner, root := forkSkillExecutor(t, fields, "!`printf x > forbidden`")
	executor.SetHooks(hooks.NewManager(true, root))
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
	if err != nil || result.Success || runner.foreground != 0 || len(skill.HookScope().Hooks()) != 0 {
		t.Fatalf("result=%+v err=%v child=%d", result, err, runner.foreground)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("fork rendering bypassed its own hook")
	}
	// A child tool ceiling must not silently discard executable controls.
	executor, _, _, runner, root = forkSkillExecutor(t, fields, "Review code.")
	executor.SetHooks(hooks.NewManager(true, root))
	result, err = executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "review"})
	if err != nil || !result.Success {
		t.Fatalf("launch=%+v err=%v", result, err)
	}
	ledger := skills.NewInvocationLedger()
	_, err = ConsumeForkedSkill(context.WithoutCancel(runner.ctx), NewRegistry(), ledger)
	if err == nil || !strings.Contains(err.Error(), "owning skill tool") || len(ledger.SnapshotNewestFirst()) != 0 {
		t.Fatalf("missing owner err=%v retained=%v", err, ledger.SnapshotNewestFirst())
	}
}

func TestSkillHooksMatchCanonicalMCPIdentityFromRegisteredTool(t *testing.T) {
	executor, _, root := hookSkillFixture(t, "^mcp__workspace__search$", "cat > mcp-input.json; printf remote-denied >&2; exit 2", "", "Review code.")
	remote := &skillForkMCPTool{scriptedStaticTool{name: "workspace_search", content: "result"}}
	if err := executor.registry.Register(remote); err != nil {
		t.Fatal(err)
	}
	loadGuardSkill(t, executor)
	result, err := executor.InvokeTool(t.Context(), "workspace_search", nil)
	if err != nil || result.Success || !strings.Contains(result.Error, "remote-denied") || remote.calls != 0 {
		t.Fatalf("result=%+v err=%v remote calls=%d", result, err, remote.calls)
	}
	data, err := os.ReadFile(filepath.Join(root, "mcp-input.json"))
	if err != nil || !strings.Contains(string(data), `"tool_name":"mcp__workspace__search"`) {
		t.Fatalf("input=%s err=%v", data, err)
	}
}

func hookSkillFixture(t *testing.T, matcher, command, extra, body string) (*Executor, *SkillTool, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	command = strings.ReplaceAll(command, "TEST_ROOT", root)
	writeToolSkill(t, root, "guard", fmt.Sprintf("---\ndescription: Guard workflow\n%s\nhooks:\n  PreToolUse:\n    - matcher: %q\n      hooks:\n        - type: command\n          command: %q\n          timeout: 2\n---\n%s", extra, matcher, command, body))
	skill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}}), root)
	registry := NewRegistry()
	registry.MustRegister(skill)
	registry.MustRegister(NewBashTool(root))
	registry.MustRegister(NewWriteTool(root))
	executor := NewExecutor(registry, testkit.NewMockClient(), 3*time.Second)
	executor.SetWorkDir(root)
	executor.SetHooks(hooks.NewManager(true, root))
	return executor, skill, root
}

func loadGuardSkill(t *testing.T, executor *Executor) {
	t.Helper()
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil || !loaded.Success {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
}

func TestSkillHooksPersistAcrossTurnsAndStayOutOfClonedRegistry(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Write", "printf denied >&2; exit 2", "", "Review code.")
	loadGuardSkill(t, executor)
	for range 2 {
		result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": "blocked.txt", "content": "forbidden"})
		if err != nil || result.Success || result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockHook {
			t.Fatalf("write=%+v err=%v", result, err)
		}
		BeginSkillPermissionTurn(executor.registry)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("hook-blocked write executed")
	}
	child := CloneRegistryForWorkDir(executor.registry, "")
	if len(SkillHooks(child)) != 0 || len(SkillHooks(executor.registry)) != 1 {
		t.Fatal("registration leaked across owners")
	}
	executor.ResetSession()
	if len(skill.HookScope().Hooks()) != 0 {
		t.Fatal("hooks survived new conversation")
	}
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "allowed.txt"), "content": "allowed"})
	if err != nil || !result.Success {
		t.Fatalf("after reset=%+v err=%v", result, err)
	}
}

func TestSkillOwnHooksGuardDynamicContextWithoutPublishingFailedLoad(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Bash", "printf stop >&2; exit 2", "allowed-tools: Bash", "!`printf x > forbidden`")
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil || loaded.Success {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("dynamic context escaped its own hook")
	}
	if len(skill.HookScope().Hooks()) != 0 || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 || len(skill.ActivePermissionGrants()) != 0 {
		t.Fatal("failed rendering published state")
	}
}

func TestSkillHookStopInsideRenderingRemainsTerminalWithoutPublishingLoad(t *testing.T) {
	executor, skill, root := hookSkillFixture(t, "Bash", `printf '%s' '{"continue":false,"stopReason":"Stop rendering"}'`, "allowed-tools: Bash", "!`printf x > forbidden`\nReview code.")
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil || result.Success || !strings.Contains(result.HookStopReason, "Stop rendering") {
		t.Fatalf("load=%+v err=%v", result, err)
	}
	if len(skill.HookScope().Hooks()) != 0 || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 || len(skill.ActivePermissionGrants()) != 0 {
		t.Fatal("stopped rendering published state")
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
		t.Fatal("stopped shell command executed")
	}
}

func TestSkillHookUpdatedInputIsAuthorizedAndOriginalInputStaysImmutable(t *testing.T) {
	command := `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"file_path":"TEST_ROOT/rewritten.txt","content":"rewritten"},"additionalContext":"Use the rewritten target"}}'`
	executor, _, root := hookSkillFixture(t, "Write", command, "", "Review code.")
	loadGuardSkill(t, executor)
	args := map[string]any{"file_path": "original.txt", "content": "original"}
	result, err := executor.InvokeTool(t.Context(), "write", args)
	if err != nil || !result.Success || !strings.Contains(result.Content, "Use the rewritten target") {
		t.Fatalf("write=%+v err=%v", result, err)
	}
	if args["file_path"] != "original.txt" || args["content"] != "original" {
		t.Fatal("hook rewrote original model input")
	}
	if data, err := os.ReadFile(filepath.Join(root, "rewritten.txt")); err != nil || string(data) != "rewritten" {
		t.Fatalf("effective write=%q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "original.txt")); !os.IsNotExist(err) {
		t.Fatal("original target was written")
	}
	manager := permission.NewManager(permission.DefaultRules(), false)
	manager.SetRunToolRules(nil, []string{"write(rewritten.txt)"})
	executor.SetPermissions(manager)
	result, err = executor.InvokeTool(t.Context(), "write", args)
	if err != nil || result.Success || result.PolicyBlock == nil || result.PolicyBlock.Kind != PolicyBlockPermission {
		t.Fatalf("rewritten denial=%+v err=%v", result, err)
	}
}

func TestSkillHooksRequireEnabledRuntimeAndTrustBeforeDynamicSideEffects(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		executor, skill, root := hookSkillFixture(t, "Bash", "exit 0", "allowed-tools: Bash", "!`printf x > forbidden`")
		if disabled {
			executor.hooks.SetEnabled(false)
		} else {
			skill.catalog = skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-project"}})
		}
		loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
		if err != nil || loaded.Success {
			t.Fatalf("load=%+v err=%v", loaded, err)
		}
		if _, err := os.Stat(filepath.Join(root, "forbidden")); !os.IsNotExist(err) {
			t.Fatal("hook preflight ran dynamic context")
		}
		if len(skill.HookScope().Hooks()) != 0 || len(skill.InvocationLedger().SnapshotNewestFirst()) != 0 {
			t.Fatal("failed preflight published state")
		}
	}
}
