package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appcontext "gokin/internal/context"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func sdkTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func sdkControl(t *testing.T, a *App, body map[string]any) (map[string]any, error) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return a.HandleSDKControl(t.Context(), data)
}

func TestSDKRepositoryRegistrationConstraintsAndSource(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	_, _, root := installCwdSettings(t, a, map[string]any{"DirectoryAdded": []any{fileHookGroup("register_repo_root", `cat > sdk-event.json; printf '%s' '{"systemMessage":"SDK_PRIVATE_SETUP"}'`)}})
	first := filepath.Join(root, "repository")
	if err := os.Mkdir(first, 0700); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", root, t.TempDir(), filepath.Join(root, "missing")} {
		if _, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": invalid}); err == nil {
			t.Fatal("invalid registration accepted", invalid)
		}
	}
	result, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": "repository"})
	if err != nil || result["directory"] != first {
		t.Fatal("registration failed", result, err)
	}
	a.sessionStart.directoryWorkers.Wait()
	data, _ := os.ReadFile(filepath.Join(root, "sdk-event.json"))
	var event map[string]any
	_ = json.Unmarshal(data, &event)
	if event["source"] != "register_repo_root" || event["directory"] != first || event["cwd"] != root {
		t.Fatalf("wrong SDK event: %s", data)
	}
	if notes := a.takeSessionStartNotes(a.sessionStart); notes != "" {
		t.Fatal("SDK setup entered model context", notes)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(first, alias); err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []string{first, alias} {
		if _, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": duplicate}); err == nil || !strings.Contains(err.Error(), "already a registered") {
			t.Fatal("duplicate root accepted", duplicate, err)
		}
	}
	// A runtime /add-dir must not expand the immutable SDK registration boundary.
	external := t.TempDir()
	nested := filepath.Join(external, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := a.GrantAllowedDirFromCommand(t.Context(), external, false); err != nil {
		t.Fatal(err)
	}
	if _, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": nested}); err == nil {
		t.Fatal("runtime grant widened the SDK boundary")
	}
	outsideAlias := filepath.Join(root, "escape")
	if err := os.Symlink(external, outsideAlias); err != nil {
		t.Fatal(err)
	}
	if _, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": filepath.Join(outsideAlias, "nested")}); err == nil {
		t.Fatal("symlink escaped the launch root")
	}
}

func TestSDKRepositoryReloadsComponentsAndClearDropsThem(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	root, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	a.promptBuilder = appcontext.NewPromptBuilder(root, nil)
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog(nil), root)
	skill.SetWorkspaceTrusted(true)
	a.registry.MustRegister(skill)
	installCwdSettings(t, a, nil)
	repo := filepath.Join(root, "registered")
	sdkTestFile(t, filepath.Join(repo, "CLAUDE.md"), "SDK_INSTRUCTION_PROOF\n@local.md\n@../outside.md\n")
	sdkTestFile(t, filepath.Join(repo, "local.md"), "CONTAINED_INCLUDE_PROOF")
	sdkTestFile(t, filepath.Join(root, "outside.md"), "MUST_NOT_IMPORT_FROM_PARENT")
	sdkTestFile(t, filepath.Join(repo, ".claude/skills/registered-check/SKILL.md"), "---\nname: registered-check\ndescription: Registered repository check\n---\nSDK_SKILL_PROOF")
	sdkTestFile(t, filepath.Join(repo, ".claude/settings.json"), `{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"printf '%s' '{\"hookSpecificOutput\":{\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"REGISTERED_POLICY_PROOF\"}}'"}]}]}}`)
	result, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": repo, "reload_claude_md": true, "reload_skills": true, "reload_plugins": true})
	if err != nil || len(result["warnings"].([]string)) != 0 {
		t.Fatal("component reload failed", result, err)
	}
	instruction := a.session.GetSystemInstruction()
	if !strings.Contains(instruction, "SDK_INSTRUCTION_PROOF") || !strings.Contains(instruction, "CONTAINED_INCLUDE_PROOF") || strings.Contains(instruction, "MUST_NOT_IMPORT_FROM_PARENT") {
		t.Fatal("registered instruction boundary failed", instruction)
	}
	if !skill.HasUserSkill("registered-check") {
		t.Fatal("registered skill is absent")
	}
	results := a.hooksManager.RunPreTool(t.Context(), "write", nil)
	if len(results) != 1 || results[0].BlockReason != "REGISTERED_POLICY_PROOF" {
		t.Fatal("registered plugin/settings policy is absent", results)
	}
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if skill.HasUserSkill("registered-check") || strings.Contains(a.session.GetSystemInstruction(), "SDK_INSTRUCTION_PROOF") {
		t.Fatal("registered components escaped conversation replacement")
	}
	if got := a.hooksManager.RunPreTool(t.Context(), "write", nil); len(got) != 0 {
		t.Fatal("old repository policy escaped clear", got)
	}
	if result, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": repo}); err != nil || result["directory"] != repo {
		t.Fatal("new conversation retained old registration", result, err)
	}
}

func TestSDKLaunchDirectoryAndCanceledRegistration(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	external, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.config.Tools.AllowedDirs = []string{external}
	installCwdSettings(t, a, nil)
	nested := filepath.Join(external, "repository")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	data, _ := json.Marshal(map[string]any{"subtype": "register_repo_root", "directory": nested})
	if _, err := a.HandleSDKControl(ctx, data); err == nil {
		t.Fatal("canceled registration succeeded")
	}
	if len(a.sessionStart.sdkRepositories) != 0 {
		t.Fatal("canceled registration published a root")
	}
	if _, err := a.HandleSDKControl(t.Context(), data); err != nil {
		t.Fatal("launch-time parent did not permit child registration", err)
	}
}

func TestSDKInitializationDoesNotAcknowledgeUnavailableCallbacks(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	result, err := a.HandleSDKControl(t.Context(), json.RawMessage(`{"subtype":"initialize","hooks":{ },"sdkMcpServers":[ ]}`))
	if err != nil || result["commands"] == nil || result["models"] == nil || result["hooks_applied"] != true {
		t.Fatal("initialization failed", result, err)
	}
	for _, request := range []string{
		`{"subtype":"initialize","hooks":{"PreToolUse":[{"hookCallbackIds":["guard"]}]}}`,
		`{"subtype":"initialize","sdkMcpServers":["hosted"]}`,
		`{"subtype":"register_repo_root","directory":"one","reload_skills":"true"}`,
		`{"subtype":"register_repo_root","directory":"one","unknown":true}`,
		`{"subtype":"initialize","subtype":"register_repo_root"}`,
	} {
		if _, err := a.HandleSDKControl(t.Context(), json.RawMessage(request)); err == nil {
			t.Fatal("unsupported control was acknowledged", request)
		}
	}
}

func TestSDKRegistrationCanCancelWhileConversationLeaseIsHeld(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installCwdSettings(t, a, nil)
	repository := filepath.Join(a.workDir, "repository")
	if err := os.Mkdir(repository, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"subtype": "register_repo_root", "directory": repository})
	a.sessionLeaseMu.Lock()
	defer a.sessionLeaseMu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.HandleSDKControl(ctx, data); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("lost cancellation while waiting for conversation lease", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SDK cancellation blocked behind conversation replacement")
	}
	if len(a.sessionStart.sdkRepositories) != 0 {
		t.Fatal("canceled waiter registered a repository")
	}
}
