package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/skills"
)

func TestObserveHookConsumptionReplaysCompletionBeforeObserver(t *testing.T) {
	root := t.TempDir()
	definitions := []hooks.ClaudeCommand{{Event: "PreToolUse", Command: "exit 0", Once: true, Async: true, Timeout: time.Second}}
	scope := &hooks.Scope{}
	registration, err := scope.Prepare("guard", definitions)
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Cancel()
	ledger := skills.NewInvocationLedger()
	activation, err := skills.NewHookActivation("guard", "claude-global", filepath.Join(root, "SKILL.md"), definitions)
	if err != nil {
		t.Fatal(err)
	}
	_, epoch := ledger.HookActivations()
	activation.Consumed = registration.Consumed() // Snapshot taken before async completion.
	manager := hooks.NewManager(true, root)
	owner := hooks.NewAsyncScope(t.Context(), nil)
	defer owner.Close()
	ctx := hooks.ContextWithAsyncScope(hooks.ContextWithScope(t.Context(), registration.RenderScope()), owner)
	results := manager.RunScoped(ctx, hooks.PreTool, hooks.NewContext("write", nil, root))
	if len(results) != 1 || results[0].Error != nil {
		t.Fatalf("render hook=%+v", results)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !registration.Consumed()[0] && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !registration.Consumed()[0] {
		t.Fatal("async hook did not finish")
	}
	_, _, registered, err := ledger.RecordSkillActivation("guard", "body", "claude-global", activation.Path, &activation, epoch)
	if err != nil {
		t.Fatal(err)
	}
	observeHookConsumption(registration, ledger, registered, epoch)
	refs, _ := ledger.HookActivations()
	if len(refs) != 1 || !refs[0].Consumed[0] {
		t.Fatalf("completion between snapshot and observer was lost: %+v", refs)
	}
}

func TestPluginSkillRestoreSurvivesManagedForceToggle(t *testing.T) {
	body := "---\ndescription: Plugin guard\nhooks:\n  PreToolUse:\n    - matcher: Write\n      hooks:\n        - type: command\n          command: 'printf x >> \"$CLAUDE_PLUGIN_DATA/runs\"'\n          once: true\n---\nReview writes."
	executor, skill, manager, work, _, managed := pluginSkillExecutor(t, body)
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "suite:guard"})
	if err != nil || !loaded.Success {
		t.Fatalf("activation=%+v %v", loaded, err)
	}
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, "first.txt"), "content": "first"})
	if err != nil || !result.Success {
		t.Fatalf("write=%+v %v", result, err)
	}
	refs, _ := skill.InvocationLedger().HookActivations()
	if len(refs) != 1 || !refs[0].Consumed[0] {
		t.Fatalf("once state=%+v", refs)
	}
	for _, policy := range []string{`{"enabledPlugins":{"suite@inline":true}}`, `{}`} {
		writeWorkflowPluginFile(t, managed, "managed-settings.json", policy)
		// A fresh owner ensures this exercises durable restore, not only live scope reuse.
		ledger := skills.NewInvocationLedger()
		if err := ledger.RestoreHookActivations(refs); err != nil {
			t.Fatal(err)
		}
		skill.SetInvocationLedger(ledger)
		if err := RestoreSkillHooks(executor.registry, manager); err != nil {
			t.Fatalf("authority-only change broke restore: %v", err)
		}
		if len(skill.HookScope().Hooks()) != 0 {
			t.Fatal("authority change rearmed once hook")
		}
	}
}

func TestPluginSkillSameOwnerRefreshesLiveAuthority(t *testing.T) {
	body := "---\ndescription: Plugin guard\nhooks:\n  PreToolUse:\n    - matcher: Write\n      hooks:\n        - type: command\n          command: 'printf x >> \"$CLAUDE_PLUGIN_DATA/runs\"'\n          once: true\n---\nReview writes."
	executor, skill, manager, work, _, managed := pluginSkillExecutor(t, body)
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "suite:guard"})
	if err != nil || !loaded.Success {
		t.Fatalf("activation=%+v %v", loaded, err)
	}
	ledger, scope := skill.InvocationLedger(), skill.HookScope()
	for i, policy := range []string{`{"enabledPlugins":{"suite@inline":true}}`, `{}`} {
		writeWorkflowPluginFile(t, managed, "managed-settings.json", policy)
		if err := RestoreSkillHooks(executor.registry, manager); err != nil {
			t.Fatal(err)
		}
		if skill.InvocationLedger() != ledger || skill.HookScope() != scope {
			t.Fatal("authority refresh replaced owner")
		}
		result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, fmt.Sprintf("allowed-%d.txt", i)), "content": "allowed"})
		if err != nil || !result.Success {
			t.Fatalf("authorized plugin stopped after toggle: %+v %v", result, err)
		}
	}
	selected, err := manager.ClaudePlugins()
	if err != nil || len(selected) != 1 {
		t.Fatalf("selected=%v %v", selected, err)
	}
	root, err := selected[0].Expand("${CLAUDE_PLUGIN_DATA}")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "runs"))
	refs, _ := ledger.HookActivations()
	if err != nil || string(data) != "x" || len(refs) != 1 || !refs[0].Consumed[0] {
		t.Fatalf("once state lost during authority refresh: %q %+v %v", data, refs, err)
	}
	// A revoked plugin must still stop this same owner before the tool runs.
	writeWorkflowPluginFile(t, managed, "managed-settings.json", `{"enabledPlugins":{"suite@inline":false}}`)
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, "forbidden.txt"), "content": "forbidden"})
	if !IsHookStopError(err) && result.HookStopReason == "" {
		t.Fatalf("revoked plugin ran: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(work, "forbidden.txt")); !os.IsNotExist(err) {
		t.Fatal("revoked plugin wrote file")
	}
}
