package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/commands"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

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
