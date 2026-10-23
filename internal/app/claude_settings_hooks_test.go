package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/commands"
	"gokin/internal/config"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func writeAppClaudeSettings(t *testing.T, root, command string) string {
	t.Helper()
	path := filepath.Join(root, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := map[string]any{"hooks": map[string]any{"PreToolUse": []any{map[string]any{
		"matcher": "Write", "hooks": []any{map[string]any{"type": "command", "command": command}},
	}}}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuilderImportsPluginWorkflowsAndParsesSlashWithAutomaticHooksDisabled(t *testing.T) {
	for _, mode := range []string{"installed", "explicit"} {
		t.Run(mode, func(t *testing.T) {
			root, user, plugin := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", user)
			if err := os.MkdirAll(filepath.Join(plugin, "commands"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(plugin, "commands", "deploy.md"), []byte("Deploy target $0."), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(plugin, "agents"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(plugin, "agents", "reviewer.md"), []byte("---\ndescription: Reviews plugin changes\ntools: Read\n---\nPLUGIN_REVIEWER_PROMPT"), 0o600); err != nil {
				t.Fatal(err)
			}
			options := BuildOptions{NonInteractive: true}
			name := filepath.Base(plugin)
			if mode == "installed" {
				if err := os.MkdirAll(filepath.Join(user, "plugins"), 0o700); err != nil {
					t.Fatal(err)
				}
				data, _ := json.Marshal(map[string]any{"version": 2, "plugins": map[string]any{"deployment@org": []any{map[string]any{"scope": "user", "installPath": plugin}}}})
				if err := os.WriteFile(filepath.Join(user, "plugins", "installed_plugins.json"), data, 0o600); err != nil {
					t.Fatal(err)
				}
				name = "deployment" // Registry identity wins without a manifest.
			} else {
				options.PluginDirs = []string{plugin}
			}
			cfg := config.DefaultConfig()
			cfg.Hooks.Enabled = false
			cfg.Session.Enabled = false
			builder := NewBuilderWithOptions(cfg, root, options)
			t.Cleanup(builder.cancel)
			builder.mainClient = testkit.NewMockClient()
			builder.registry = tools.NewRegistry()
			skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog(nil), root)
			builder.registry.MustRegister(skill)
			builder.executor = tools.NewExecutor(builder.registry, builder.mainClient, time.Second)
			builder.configDirErr = errors.New("disable persistence in unit test")
			if err := builder.initManagers(); err != nil {
				t.Fatal(err)
			}
			if !builder.agentTypeRegistry.Exists(name+":reviewer") || !strings.Contains(builder.agentTypeRegistry.GetPromptForType(name+":reviewer"), "PLUGIN_REVIEWER_PROMPT") {
				t.Fatalf("builder omitted plugin agent: %v", builder.agentTypeRegistry.PluginWarnings())
			}
			command, args, ok := builder.commandHandler.Parse("/" + name + ":deploy staging")
			if !ok || command != name+":deploy" || len(args) != 1 {
				t.Fatalf("builder omitted plugin slash: %q %v %v", command, args, ok)
			}
			loaded, err := skill.ExecuteForUserArgumentsContext(builder.executor.SkillInvocationContext(t.Context()), command, args)
			if err != nil || !loaded.Success || !strings.Contains(loaded.Content, "Deploy target staging.") {
				t.Fatalf("builder workflow=%+v %v", loaded, err)
			}
			if builder.hooksManager.IsEnabled() {
				t.Fatal("workflow discovery enabled automatic command hooks")
			}
		})
	}
}

func TestBuilderAutomaticallyLoadsClaudeSettingsAndKeepsBareBoundary(t *testing.T) {
	for _, mode := range []string{"trusted", "untrusted", "bare"} {
		t.Run(mode, func(t *testing.T) {
			root, user := t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", user)
			projectPath := writeAppClaudeSettings(t, root, "printf project")
			data, err := os.ReadFile(projectPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(user, "settings.json"), bytes.ReplaceAll(data, []byte("printf project"), []byte("printf user")), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.DefaultConfig()
			cfg.Hooks.Enabled = true
			cfg.Session.Enabled = false
			if mode == "trusted" {
				cfg.Hooks.TrustedWorkspaces = []string{root}
			}
			cfg.Bare = mode == "bare"
			builder := NewBuilderWithOptions(cfg, root, BuildOptions{NonInteractive: true, Bare: cfg.Bare})
			t.Cleanup(builder.cancel)
			builder.mainClient = testkit.NewMockClient()
			builder.registry = tools.NewRegistry()
			builder.executor = tools.NewExecutor(builder.registry, builder.mainClient, time.Second)
			builder.configDirErr = errors.New("disable persistence in unit test")
			if err := builder.initManagers(); err != nil {
				t.Fatal(err)
			}
			results := builder.hooksManager.RunPreTool(t.Context(), "write", nil)
			want := 1
			if mode == "trusted" {
				want = 2
			} else if mode == "bare" {
				want = 0
			}
			if len(results) != want {
				t.Fatalf("%s builder hooks=%+v want=%d", mode, results, want)
			}
		})
	}
}

func TestBuilderDisabledHooksDoNotRequireClaudeHomeDirectory(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	cfg := config.DefaultConfig()
	cfg.Session.Enabled = false
	cfg.Hooks.Enabled = false
	builder := NewBuilderWithOptions(cfg, t.TempDir(), BuildOptions{NonInteractive: true})
	t.Cleanup(builder.cancel)
	builder.mainClient = testkit.NewMockClient()
	builder.registry = tools.NewRegistry()
	builder.executor = tools.NewExecutor(builder.registry, builder.mainClient, time.Second)
	builder.configDirErr = errors.New("disable persistence in unit test")
	if err := builder.initManagers(); err != nil {
		t.Fatalf("disabled optional hooks prevented startup: %v", err)
	}
}

func TestHeadlessClaudeSettingsHooksApplyWithoutInvokingSkill(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, user := t.TempDir(), t.TempDir()
	writeAppClaudeSettings(t, root, "cat > settings-input.json; printf settings-denied >&2; exit 2")
	mock := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": "forbidden.txt"}).EnqueueText("Write blocked.")
	application, write, _ := newResumeHookApp(t, root, mock, "global", true)
	application.executor.SetSessionID(application.session.GetID())
	application.hooksManager.ConfigureClaudeSettings(root, user, true)
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(t.Context(), "Try writing.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
	if err == nil || result.Error == nil || result.Error.Kind != "policy_blocked" || write.CallCount() != 0 || len(mock.Calls()) != 2 {
		t.Fatalf("settings not enforced: %+v %v writes=%d calls=%d", result, err, write.CallCount(), len(mock.Calls()))
	}
	data, err := os.ReadFile(filepath.Join(root, "settings-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(data, &input); err != nil || input["hook_event_name"] != "PreToolUse" || input["tool_name"] != "Write" || input["session_id"] != application.session.GetID() {
		t.Fatalf("input=%v error=%v", input, err)
	}
	if hooks := application.GetSkillHooks(); len(hooks) != 0 {
		t.Fatalf("configured hooks became skill registrations: %+v", hooks)
	}
	listing, err := (&commands.HooksCommand{}).Execute(t.Context(), nil, application)
	if err != nil || !strings.Contains(listing, "claude:project:") || !strings.Contains(listing, "PreToolUse") {
		t.Fatalf("source invisible: %q %v", listing, err)
	}
	decodeSingleHeadlessResult(t, stdout.Bytes())
}

func TestHeadlessInvalidClaudeSettingsStopBeforeProviderAndRecoverAfterRepair(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, user := t.TempDir(), t.TempDir()
	path := writeAppClaudeSettings(t, root, "exit 0")
	mock := testkit.NewMockClient().EnqueueText("Repaired settings work.")
	application, write, _ := newResumeHookApp(t, root, mock, "global", true)
	application.hooksManager.ConfigureClaudeSettings(root, user, true)
	if err := application.hooksManager.CheckReady(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"PreToolUse":[{"hooks":[{"type":"unsupported-handler","prompt":"check"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(t.Context(), "Try writing.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
	if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" || len(mock.Calls()) != 0 || write.CallCount() != 0 || result.Usage.TotalTokens != 0 || result.Usage.ModelRounds != 0 {
		t.Fatalf("invalid settings reached provider: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
	listing, err := (&commands.HooksCommand{}).Execute(t.Context(), nil, application)
	if err != nil || !strings.Contains(listing, "settings hooks could not be loaded") || !strings.Contains(listing, "unsupported") {
		t.Fatalf("diagnostics=%q %v", listing, err)
	}
	writeAppClaudeSettings(t, root, "exit 0")
	result, err = application.RunHeadlessWithOptions(t.Context(), "Continue after repair.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Error != nil || len(mock.Calls()) != 1 {
		t.Fatalf("repair did not recover: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestHeadlessClaudeSettingsStopRequestsOneOwnedContinuation(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	mock := testkit.NewMockClient().EnqueueText("Initial answer.").EnqueueText("Reviewed answer.")
	application, executor := newHeadlessPolicyTestApp(t, mock, &appHeadlessScriptedTool{name: "write"})
	user := t.TempDir()
	command := `payload=$(cat); printf '%s\n' "$payload" >> stop-settings.jsonl; case "$payload" in *'"stop_hook_active":true'*) exit 0;; *) printf '%s' '{"decision":"block","reason":"Review required checks"}';; esac`
	settings := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(user, "settings.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	application.hooksManager = hooks.NewManager(true, application.workDir)
	application.hooksManager.ConfigureClaudeSettings(application.workDir, user, false)
	executor.SetHooks(application.hooksManager)
	result, err := application.RunHeadlessWithOptions(t.Context(), "Review this task.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Error != nil || result.Result != "Reviewed answer." || len(mock.Calls()) != 2 || result.Usage.ModelRounds != 2 {
		t.Fatalf("configured Stop did not continue: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
	data, err = os.ReadFile(filepath.Join(application.workDir, "stop-settings.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("Stop runs=%d: %s", len(lines), data)
	}
	for i, line := range lines {
		var input map[string]any
		if err := json.Unmarshal([]byte(line), &input); err != nil || input["stop_hook_active"] != (i == 1) || input["hook_event_name"] != "Stop" {
			t.Fatalf("Stop payload=%v %v", input, err)
		}
	}
}

func TestHeadlessManagedHooksSurviveNativeDisableAndFailBeforeProviderOnCorruption(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, managed := t.TempDir(), t.TempDir()
	project := writeAppClaudeSettings(t, root, "cat > managed-input.json; printf managed-denied >&2; exit 2")
	path := filepath.Join(managed, "managed-settings.json")
	if err := os.Rename(project, path); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mock := testkit.NewMockClient().EnqueueToolCall("write", map[string]any{"file_path": "forbidden.txt"}).EnqueueText("Write blocked.")
	application, write, _ := newResumeHookApp(t, root, mock, "global", true)
	application.hooksManager.ConfigureClaudeManagedSettings(managed)
	application.hooksManager.SetEnabled(false)
	result, err := application.RunHeadlessWithOptions(t.Context(), "Try writing.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.Error == nil || result.Error.Kind != "policy_blocked" || write.CallCount() != 0 || len(mock.Calls()) != 2 {
		t.Fatalf("managed check bypassed: %+v %v writes=%d calls=%d", result, err, write.CallCount(), len(mock.Calls()))
	}
	listing, err := (&commands.HooksCommand{}).Execute(t.Context(), nil, application)
	if err != nil || !strings.Contains(listing, "claude:managed:") || !strings.Contains(listing, "managed hooks active; ordinary hooks disabled") {
		t.Fatalf("managed diagnostics=%q %v", listing, err)
	}
	if err := os.WriteFile(path, []byte("malformed organizational policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = application.RunHeadlessWithOptions(t.Context(), "Continue.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" || result.Usage.ModelRounds != 0 || result.Usage.TotalTokens != 0 || len(mock.Calls()) != 2 {
		t.Fatalf("corrupted managed policy reached provider: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
	if err := os.WriteFile(path, good, 0o600); err != nil {
		t.Fatal(err)
	}
	mock.EnqueueText("Recovered.")
	result, err = application.RunHeadlessWithOptions(t.Context(), "Continue after repair.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Error != nil || len(mock.Calls()) != 3 {
		t.Fatalf("managed repair failed: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestHeadlessManagedPolicyRevocationDuringModelCannotSkipRequiredStop(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root, managed := t.TempDir(), t.TempDir()
	writeResumeHookSkill(t, root)
	mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("Answer before policy update.")
	application, _, _ := newResumeHookApp(t, root, mock, "claude-global", true)
	application.hooksManager.ConfigureClaudeManagedSettings(managed)
	mock.OnSend = func(context.Context) {
		if len(mock.Calls()) != 2 {
			return
		}
		if err := os.WriteFile(filepath.Join(managed, "managed-settings.json"), []byte(`{"allowManagedHooksOnly":true}`), 0o600); err != nil {
			t.Error(err)
		}
	}
	result, err := application.RunHeadlessWithOptions(t.Context(), "Finish.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" || result.Status != "policy_blocked" || len(mock.Calls()) != 2 || !strings.Contains(result.Error.Message, "allowManagedHooksOnly") {
		t.Fatalf("response silently suppressed required hooks: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
}

func TestBuilderLoadsExplicitPluginHooksAndRejectsUnavailableOrBarePlugins(t *testing.T) {
	for _, mode := range []string{"normal", "invalid", "bare"} {
		t.Run(mode, func(t *testing.T) {
			root, user, plugin := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", user)
			source := writeAppClaudeSettings(t, plugin, "printf plugin-loaded")
			target := filepath.Join(plugin, "hooks", "hooks.json")
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(source, target); err != nil {
				t.Fatal(err)
			}
			if mode == "invalid" {
				if err := os.WriteFile(target, []byte(`{"hooks":{"UnsupportedEvent":[]}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cfg := config.DefaultConfig()
			cfg.Hooks.Enabled = false
			cfg.Session.Enabled = false
			builder := NewBuilderWithOptions(cfg, root, BuildOptions{NonInteractive: true, Bare: mode == "bare", PluginDirs: []string{plugin}})
			t.Cleanup(builder.cancel)
			builder.mainClient = testkit.NewMockClient()
			builder.registry = tools.NewRegistry()
			builder.executor = tools.NewExecutor(builder.registry, builder.mainClient, time.Second)
			builder.configDirErr = errors.New("disable persistence in unit test")
			err := builder.initManagers()
			if mode != "normal" {
				if err == nil {
					t.Fatal("unavailable/bare plugin accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			results := builder.hooksManager.RunPreTool(t.Context(), "write", nil)
			if len(results) != 1 || results[0].Error != nil || results[0].Output != "plugin-loaded" || !strings.Contains(results[0].Hook.Source, "claude:plugin:") {
				t.Fatalf("builder plugin=%+v", results)
			}
		})
	}
}
