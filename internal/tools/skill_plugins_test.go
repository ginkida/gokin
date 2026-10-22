package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
)

func writeWorkflowPluginFile(t *testing.T, root, path, body string) {
	t.Helper()
	path = filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pluginSkillExecutor(t *testing.T, body string) (*Executor, *SkillTool, *hooks.Manager, string, string, string) {
	t.Helper()
	canonical := func() string {
		path, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	work, plugin, user, managed := canonical(), canonical(), canonical(), canonical()
	writeWorkflowPluginFile(t, plugin, ".claude-plugin/plugin.json", `{"name":"suite","userConfig":{"text":{"type":"string","default":"literal"}}}`)
	writeWorkflowPluginFile(t, plugin, "skills/guard/SKILL.md", body)
	manager := hooks.NewManager(false, work)
	manager.ConfigureClaudeManagedSettings(managed)
	manager.ConfigureClaudeSettings(work, user, false)
	manager.ConfigureClaudePlugins([]string{plugin})
	skill := NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog(nil), work)
	skill.ConfigureClaudePlugins(manager)
	registry := NewRegistry()
	registry.MustRegister(skill)
	registry.MustRegister(NewWriteTool(work))
	registry.MustRegister(NewBashTool(work))
	executor := NewExecutor(registry, testkit.NewMockClient(), 3*time.Second)
	executor.SetWorkDir(work)
	executor.SetHooks(manager)
	return executor, skill, manager, work, plugin, managed
}

func TestPluginSkillLiteralRenderingAndDynamicContextUsesPermissionChecks(t *testing.T) {
	executor, skill, _, work, plugin, _ := pluginSkillExecutor(t, "---\ndescription: Plugin context\nallowed-tools: Bash\n---\nRoot=${CLAUDE_PLUGIN_ROOT}\nOption=${user_config.text}\nArguments=$ARGUMENTS\nState: !`printf ready`")
	writeWorkflowPluginFile(t, plugin, ".claude-plugin/plugin.json", `{"name":"suite","userConfig":{"text":{"type":"string","default":"$ARGUMENTS ${CLAUDE_PLUGIN_ROOT} !`+"`touch injected`"+`"}}}`)
	result, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard", "arguments": "hello"})
	if err != nil || !result.Success {
		t.Fatalf("plugin render=%+v %v", result, err)
	}
	for _, want := range []string{"Root=" + plugin, "Option=$ARGUMENTS ${CLAUDE_PLUGIN_ROOT} !`touch injected`", "Arguments=hello", "State: ready"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("missing %q: %q", want, result.Content)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "injected")); !os.IsNotExist(err) {
		t.Fatal("option became dynamic command syntax")
	}
	entries := skill.InvocationLedger().SnapshotNewestFirst()
	if len(entries) != 1 || entries[0].Name != "suite:guard" {
		t.Fatalf("alias lost durable namespace: %+v", entries)
	}
	// A plugin's Bash allowance cannot widen a caller's capability ceiling.
	result, err = executor.InvokeTool(ContextWithToolCapabilityCeiling(t.Context(), []string{"skill"}), "skill", map[string]any{"name": "suite:guard"})
	if err != nil || result.Success || result.PolicyBlock == nil {
		t.Fatalf("plugin bypassed capability ceiling: %+v %v", result, err)
	}
}

func TestPluginSkillOnceRestoresAndManagedRevocationStopsBeforeProviderAndTool(t *testing.T) {
	body := "---\ndescription: Plugin guard\nhooks:\n  PreToolUse:\n    - matcher: Write\n      hooks:\n        - type: command\n          command: 'printf x >> \"$CLAUDE_PLUGIN_DATA/runs\"'\n          once: true\n---\nReview writes."
	executor, skill, manager, work, _, managed := pluginSkillExecutor(t, body)
	policy := `{"allowManagedHooksOnly":true,"enabledPlugins":{"suite@inline":true}}`
	writeWorkflowPluginFile(t, managed, "managed-settings.json", policy)
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "suite:guard"})
	if err != nil || !loaded.Success {
		t.Fatalf("forced plugin activation=%+v %v", loaded, err)
	}
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, "first.txt"), "content": "first"})
	if err != nil || !result.Success {
		t.Fatalf("first write=%+v %v", result, err)
	}
	refs, _ := skill.InvocationLedger().HookActivations()
	if len(refs) != 1 || !refs[0].Consumed[0] || refs[0].Name != "suite:guard" {
		t.Fatalf("once reference=%+v", refs)
	}
	encoded, _ := json.Marshal(refs)
	if strings.Contains(string(encoded), "printf") || strings.Contains(string(encoded), "Forced") {
		t.Fatal("persisted executable commands or authority")
	}
	resumedSkill := NewSkillToolWithCatalogAndWorkDir(skill.catalog, work)
	ledger := skills.NewInvocationLedger()
	if err := ledger.RestoreHookActivations(refs); err != nil {
		t.Fatal(err)
	}
	resumedSkill.SetInvocationLedger(ledger)
	registry := NewRegistry()
	registry.MustRegister(resumedSkill)
	registry.MustRegister(NewWriteTool(work))
	mock := testkit.NewMockClient().EnqueueText("must not call")
	resumed := NewExecutor(registry, mock, 3*time.Second)
	resumed.SetWorkDir(work)
	resumed.SetHooks(manager)
	result, err = resumed.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, "second.txt"), "content": "second"})
	if err != nil || !result.Success {
		t.Fatalf("restored write=%+v %v", result, err)
	}
	plugins, err := manager.ClaudePlugins()
	if err != nil || len(plugins) != 1 {
		t.Fatalf("selected plugins=%v %v", plugins, err)
	}
	dataRoot, err := plugins[0].Expand("${CLAUDE_PLUGIN_DATA}")
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(dataRoot, "runs"))
	if err != nil || string(marker) != "x" {
		t.Fatalf("once reran across restore: %q %v", marker, err)
	}
	for _, revoked := range []string{`{"allowManagedHooksOnly":true}`, `{"allowManagedHooksOnly":true,"enabledPlugins":{"suite@inline":false}}`} {
		writeWorkflowPluginFile(t, managed, "managed-settings.json", revoked)
		if _, _, err := resumed.Execute(t.Context(), nil, "Continue."); !IsHookStopError(err) || len(mock.Calls()) != 0 {
			t.Fatalf("revoked plugin reached provider: %v calls=%d", err, len(mock.Calls()))
		}
		result, err := resumed.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, "forbidden.txt"), "content": "forbidden"})
		if !IsHookStopError(err) && result.HookStopReason == "" {
			t.Fatalf("revoked plugin reached tool: %+v %v", result, err)
		}
		if _, err := os.Stat(filepath.Join(work, "forbidden.txt")); !os.IsNotExist(err) {
			t.Fatal("revoked skill wrote file")
		}
		writeWorkflowPluginFile(t, managed, "managed-settings.json", policy)
		if err := RestoreSkillHooks(registry, manager); err != nil {
			t.Fatalf("policy recovery=%v", err)
		}
	}
	result, err = resumed.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, "third.txt"), "content": "third"})
	marker, readErr := os.ReadFile(filepath.Join(dataRoot, "runs"))
	if err != nil || !result.Success || readErr != nil || string(marker) != "x" {
		t.Fatalf("policy repair rearmed once: %+v %q %v", result, marker, err)
	}
	// Saved definitions bind the non-sensitive option values too.
	writeWorkflowPluginFile(t, managed, "managed-settings.json", `{"allowManagedHooksOnly":true,"enabledPlugins":{"suite@inline":true},"pluginConfigs":{"suite@inline":{"options":{"text":"changed"}}}}`)
	if err := ValidateSkillHookRestore(registry, manager, refs); err == nil || !strings.Contains(err.Error(), "definitions changed") {
		t.Fatalf("changed option runtime restored old hook: %v", err)
	}
}

func TestPluginSkillFrontmatterExecHooksReceiveOwningRootAndOptions(t *testing.T) {
	body := "---\ndescription: Plugin exec guard\nhooks:\n  PreToolUse:\n    - matcher: Write\n      hooks:\n        - type: command\n          command: /bin/sh\n          args: ['${CLAUDE_PLUGIN_ROOT}/hook.sh', '${user_config.text}']\n---\nGuard writes."
	executor, _, _, work, plugin, _ := pluginSkillExecutor(t, body)
	writeWorkflowPluginFile(t, plugin, "hook.sh", "printf '%s' \"$1\" > \"$CLAUDE_PLUGIN_DATA/option\"\nprintf '%s' '{\"continue\":false,\"stopReason\":\"plugin guard\"}'")
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "suite:guard"})
	if err != nil || !loaded.Success {
		t.Fatalf("activation=%+v %v", loaded, err)
	}
	result, err := executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(work, "blocked.txt"), "content": "blocked"})
	if err != nil || result.Success || !strings.Contains(result.HookStopReason, "plugin guard") {
		t.Fatalf("plugin hook did not stop write: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(work, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatal("frontmatter hook bypassed")
	}
}

func TestManagedDisableAllHooksKeepsPluginWorkflowDiscoveryAndSkipsHookParsing(t *testing.T) {
	executor, _, _, _, plugin, managed := pluginSkillExecutor(t, "Review without frontmatter hooks.")
	writeWorkflowPluginFile(t, plugin, "hooks/hooks.json", "invalid disabled hook file")
	writeWorkflowPluginFile(t, managed, "managed-settings.json", `{"disableAllHooks":true,"enabledPlugins":{"suite@inline":true}}`)
	loaded, err := executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "suite:guard"})
	if err != nil || !loaded.Success {
		t.Fatalf("disabled plugin hooks broke workflow discovery: %+v %v", loaded, err)
	}
}
