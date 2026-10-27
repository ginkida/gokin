package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/commands"
	"gokin/internal/permission"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessModelSkillGrantKeepsToolsVisibleAcrossRounds(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "check"}).
		EnqueueToolCall("bash", map[string]any{"command": "printf checked"}).EnqueueText("Done.")
	bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("checked")}}
	a, executor := newHeadlessPolicyTestApp(t, worker, bash)
	sdkTestFile(t, filepath.Join(a.workDir, "check", "SKILL.md"), "---\ndescription: Check\nallowed-tools: Bash\n---\nRun the check.")
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: a.workDir, Source: "claude-global"}}), a.workDir)
	a.registry.MustRegister(skill)
	executor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
	var schemas []string
	worker.OnSend = func(ctx context.Context) {
		schema, err := json.Marshal(worker.GetTools())
		if err != nil {
			t.Error(err)
		}
		schemas = append(schemas, string(schema))
		if !strings.Contains(string(schema), `"name":"bash"`) {
			t.Error("potential trusted skill grant was omitted from the provider schema")
		}
		if allowed := executor.PreflightToolPermission(ctx, "bash", map[string]any{"command": "printf checked"}); allowed != (len(schemas) > 1) {
			t.Errorf("advertisement changed actual authority: bash allowed=%v round=%d", allowed, len(schemas))
		}
	}
	result, err := a.RunHeadlessWithOptions(t.Context(), "Load the check skill and follow its workflow", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || len(result.PermissionDenials) != 0 || bash.CallCount() != 1 || len(schemas) != 3 {
		t.Fatalf("skill run=%+v err=%v bash=%d schemas=%d", result, err, bash.CallCount(), len(schemas))
	}
	if schemas[0] != schemas[1] || schemas[0] != schemas[2] {
		t.Fatal("model-invoked grants changed the tool schema between rounds")
	}
}

func TestHeadlessSlashSkillGrantsBeforeFreezingTools(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "printf checked"}).EnqueueText("Done.")
	bash := &appHeadlessScriptedTool{name: "bash", results: []tools.ToolResult{tools.NewSuccessResult("checked")}}
	a, executor := newHeadlessPolicyTestApp(t, worker, bash)
	sdkTestFile(t, filepath.Join(a.workDir, "check", "SKILL.md"), "---\ndescription: Check\nallowed-tools: Bash\n---\nRun the check.")
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: a.workDir, Source: "claude-global"}}), a.workDir)
	a.registry.MustRegister(skill)
	a.commandHandler = commands.NewHandler()
	a.commandHandler.ConfigureSkillResolver(skill.HasUserSkill)
	executor.SetPermissions(permission.NewManager(permission.DefaultRules(), true))
	var schemas []string
	worker.OnSend = func(context.Context) {
		schema, err := json.Marshal(worker.GetTools())
		if err != nil {
			t.Fatal(err)
		}
		schemas = append(schemas, string(schema))
		if !strings.Contains(schemas[len(schemas)-1], `"name":"bash"`) {
			t.Error("schema omitted a potential trusted skill grant")
		}
	}
	result, err := a.RunHeadlessWithOptions(t.Context(), "/skill check", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || len(result.PermissionDenials) != 0 || bash.CallCount() != 1 || len(schemas) != 2 {
		t.Fatalf("skill run=%+v err=%v checks=%d schemas=%d", result, err, bash.CallCount(), len(schemas))
	}
	if schemas[0] != schemas[1] {
		t.Fatal("tool schema changed between provider rounds")
	}
	worker.EnqueueText("Next task finished.")
	result, err = a.RunHeadlessWithOptions(t.Context(), "A separate task", HeadlessOptions{PermissionMode: "acceptEdits", NoSelfReview: true, OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err != nil || result.Status != "success" || len(schemas) != 3 || len(skill.ActivePermissionGrants()) != 0 {
		t.Fatalf("new run retained previous authority: result=%+v err=%v schemas=%d grants=%v", result, err, len(schemas), skill.ActivePermissionGrants())
	}
	ctx, _ := tools.ContextWithHeadlessPermissions(t.Context(), false, "acceptEdits")
	if executor.PreflightToolPermission(ctx, "bash", map[string]any{"command": "printf checked"}) {
		t.Fatal("visible tool retained the previous turn's permission grant")
	}
}

func TestHeadlessExpandsLiveSkillAndRunsLocalClearWithoutModel(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog(nil), a.workDir)
	skill.SetWorkspaceTrusted(true)
	a.registry.MustRegister(skill)
	a.commandHandler = commands.NewHandler()
	a.commandHandler.ConfigureSkillResolver(skill.HasUserSkill)
	a.commandHandler.SealBootPhase()
	installCwdSettings(t, a, nil)
	repo := filepath.Join(a.workDir, "repository")
	sdkTestFile(t, filepath.Join(repo, ".claude/skills/new-check/SKILL.md"), "---\nname: new-check\ndescription: New check\n---\nEXPANDED_HEADLESS_WORKFLOW")
	if _, err := sdkControl(t, a, map[string]any{"subtype": "register_repo_root", "directory": repo, "reload_skills": true}); err != nil {
		t.Fatal(err)
	}
	opts := HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}
	if result, err := a.RunHeadlessWithOptions(t.Context(), "/new-check", opts); err != nil || result.Status != "success" {
		t.Fatal("headless workflow failed", result, err)
	}
	if len(worker.Calls()) != 1 || !strings.Contains(worker.Calls()[0].Message, "EXPANDED_HEADLESS_WORKFLOW") {
		t.Fatal("slash workflow reached model without expansion", worker.Calls())
	}
	id := a.session.GetID()
	result, err := a.RunHeadlessWithOptions(t.Context(), "/clear", opts)
	if err != nil || result.Status != "success" || !strings.Contains(result.Result, "fresh conversation") || a.session.GetID() == id || len(worker.Calls()) != 1 {
		t.Fatal("headless clear failed or invoked model", result, err, len(worker.Calls()))
	}
	if result.SessionID != a.session.GetID() {
		t.Fatal("clear result retained old session identity", result.SessionID, a.session.GetID())
	}
}

func TestHeadlessWorkflowSubmissionHookRunsBeforeDynamicRendering(t *testing.T) {
	worker := testkit.NewMockClient()
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog(nil), a.workDir)
	skill.SetWorkspaceTrusted(true)
	a.registry.MustRegister(skill)
	a.commandHandler = commands.NewHandler()
	a.commandHandler.ConfigureSkillResolver(skill.HasUserSkill)
	a.commandHandler.SealBootPhase()
	_, _, root := installCwdSettings(t, a, map[string]any{"UserPromptSubmit": []any{fileHookGroup("", `printf '%s' '{"decision":"block","reason":"HEADLESS_RAW_WORKFLOW_DENIED"}'`)}})
	repo := filepath.Join(root, "repository")
	sdkTestFile(t, filepath.Join(repo, ".claude/skills/dynamic-check/SKILL.md"), "---\nname: dynamic-check\ndescription: Dynamic check\n---\n!`touch dynamic-render-leaked`\n")
	skill.SetAdditionalProjectRoots([]string{repo})
	result, err := a.RunHeadlessWithOptions(t.Context(), "/dynamic-check", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard})
	if err == nil || len(worker.Calls()) != 0 || !strings.Contains(result.Error.Message, "HEADLESS_RAW_WORKFLOW_DENIED") {
		t.Fatal("blocked workflow executed", result, err)
	}
	if _, err := os.Stat(filepath.Join(root, "dynamic-render-leaked")); !os.IsNotExist(err) {
		t.Fatal("blocked workflow rendered dynamic shell context", err)
	}
}

func TestHeadlessLocalCommandTextIsPrintedOnce(t *testing.T) {
	for _, prompt := range []string{"/help", "/clear", "/goal"} {
		t.Run(prompt, func(t *testing.T) {
			worker := testkit.NewMockClient()
			a := newGoalTestApp(t, worker, testkit.NewMockClient())
			a.commandHandler = commands.NewHandler()
			var stdout bytes.Buffer
			result, err := a.RunHeadlessWithOptions(t.Context(), prompt, HeadlessOptions{OutputFormat: HeadlessOutputText, Stdout: &stdout, Stderr: io.Discard})
			if err != nil || result.Status != "success" || result.Result == "" {
				t.Fatalf("command failed: %+v %v", result, err)
			}
			want := result.Result
			if !strings.HasSuffix(want, "\n") {
				want += "\n"
			}
			if got := stdout.String(); got != want {
				t.Fatalf("stdout differs from command result: got %d bytes, want %d", len(got), len(want))
			}
			if len(worker.Calls()) != 0 {
				t.Fatal("local command invoked model")
			}
		})
	}
}
