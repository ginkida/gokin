package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/chat"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestSavedSkillHooksResumeWithOnceStateAndNoShellRerender(t *testing.T) {
	for _, mode := range []string{"resume", "switch"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeResumeHookSkill(t, root)
			firstMock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueToolCall("read", map[string]any{"file_path": "notes.txt"}).EnqueueText("Ready to continue.")
			first, _, _ := newResumeHookApp(t, root, firstMock, "claude-global", false)
			var stdout bytes.Buffer
			if result, err := first.RunHeadlessWithOptions(t.Context(), "Load guard and read notes.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard}); err != nil {
				t.Fatalf("initial run: %+v %v", result, err)
			}
			savedID := first.session.GetID()
			first.sessionManager.Stop()
			secondMock := testkit.NewMockClient().EnqueueToolCall("read", map[string]any{"file_path": "notes.txt"}).EnqueueToolCall("write", map[string]any{"file_path": "must-not-write.txt"}).EnqueueText("Write was blocked.")
			resumed, write, read := newResumeHookApp(t, root, secondMock, "claude-global", false)
			state, _, err := resumed.sessionManager.LoadSession(savedID)
			if err != nil {
				t.Fatal(err)
			}
			if len(state.SkillHooks) != 1 || len(state.SkillHooks[0].Consumed) != 2 || !state.SkillHooks[0].Consumed[0] || state.SkillHooks[0].Consumed[1] {
				t.Fatalf("disk lost once state: %+v", state.SkillHooks)
			}
			if mode == "switch" {
				if _, err := resumed.SwitchSession(t.Context(), state, false); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = resumed.ReleaseSessionWriterLease() })
			} else {
				if err := resumed.ResumeSession(savedID); err != nil {
					t.Fatal(err)
				}
			}
			stdout.Reset()
			result, err := resumed.RunHeadlessWithOptions(t.Context(), "Read again and try writing.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
			if err == nil || result.Error == nil || result.Error.Kind != "policy_blocked" || write.CallCount() != 0 || read.CallCount() != 1 || len(secondMock.Calls()) != 3 {
				t.Fatalf("resumed enforcement: %+v %v write=%d read=%d calls=%d", result, err, write.CallCount(), read.CallCount(), len(secondMock.Calls()))
			}
			for _, filename := range []string{"once-runs", "render-runs"} {
				data, readErr := os.ReadFile(filepath.Join(root, filename))
				if readErr != nil || string(data) != "x" {
					t.Fatalf("restore reran %s: %q %v", filename, data, readErr)
				}
			}
			if len(resumed.GetSkillHooks()) != 1 || result.Usage.ModelRounds != 3 || result.SessionID != savedID {
				t.Fatalf("restored owner state lost: %+v hooks=%d", result, len(resumed.GetSkillHooks()))
			}
			decodeSingleHeadlessResult(t, stdout.Bytes())
		})
	}
}

func TestSkillHookRestoreRejectsChangedSourceTrustAndCorruptStateBeforePublication(t *testing.T) {
	for _, scenario := range []string{"removed", "changed", "shadowed", "untrusted", "disabled", "consumed_non_once"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			writeResumeHookSkill(t, root)
			mock := testkit.NewMockClient().EnqueueToolCall("skill", map[string]any{"name": "guard"}).EnqueueText("Ready.")
			source := "claude-global"
			if scenario == "untrusted" {
				source = "project"
			}
			original, _, _ := newResumeHookApp(t, root, mock, source, true)
			if _, err := original.RunHeadlessWithOptions(t.Context(), "Load guard.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
				t.Fatal(err)
			}
			saved := original.session.GetState()
			if len(saved.SkillHooks) != 1 {
				t.Fatalf("missing saved reference: %+v", saved.SkillHooks)
			}
			path := filepath.Join(root, "guard", "SKILL.md")
			switch scenario {
			case "removed":
				if err := os.Rename(path, path+".disabled"); err != nil {
					t.Fatal(err)
				}
			case "changed":
				body, _ := os.ReadFile(path)
				if err := os.WriteFile(path, []byte(strings.Replace(string(body), "resume-denied", "changed-definition", 1)), 0o600); err != nil {
					t.Fatal(err)
				}
			case "shadowed":
				source = "global"
			case "consumed_non_once":
				saved.SkillHooks[0].Consumed[1] = true
			}
			freshMock := testkit.NewMockClient().EnqueueText("must not request")
			resumed, _, _ := newResumeHookApp(t, root, freshMock, source, false)
			if scenario == "disabled" {
				resumed.hooksManager.SetEnabled(false)
			}
			before := resumed.session.GetState()
			if err := resumed.restoreLoadedSession(saved, nil, "candidate"); err == nil {
				t.Fatalf("accepted %s hook restore", scenario)
			}
			after := resumed.session.GetState()
			if after.ID != before.ID || len(after.History) != len(before.History) || len(resumed.GetSkillHooks()) != 0 || len(freshMock.Calls()) != 0 {
				t.Fatal("rejected restore replaced the current owner or reached the model")
			}
		})
	}
}

func writeResumeHookSkill(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, "guard", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\ndescription: Guard\nallowed-tools: Bash\nhooks:\n  PreToolUse:\n    - matcher: Read\n      hooks:\n        - type: command\n          command: 'printf x >> once-runs'\n          once: true\n    - matcher: Write\n      hooks:\n        - type: command\n          command: 'printf resume-denied >&2; exit 2'\n---\nReview !`printf x >> render-runs; printf rendered` ."
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHeadlessHookRestorationFailureStopsBeforeAnyModelRequest(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	writeResumeHookSkill(t, root)
	mock := testkit.NewMockClient().EnqueueText("must not request")
	application, _, _ := newResumeHookApp(t, root, mock, "claude-global", false)
	definition := skills.NewCatalog([]skills.Root{{Path: root, Source: "claude-global"}})
	skill, _ := definition.Get("guard")
	activation, err := skills.NewHookActivation("guard", skill.Source, skill.Path, skill.Hooks)
	if err != nil {
		t.Fatal(err)
	}
	activation.Consumed[1] = true // The write guard is not once.
	if err := application.session.InvocationLedger().RestoreHookActivations([]skills.HookActivation{activation}); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	result, err := application.RunHeadlessWithOptions(t.Context(), "Try writing.", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard})
	if err == nil || result.Error == nil || result.Error.Kind != "hook_stopped" || len(mock.Calls()) != 0 || result.Usage.TotalTokens != 0 || result.Usage.ModelRounds != 0 {
		t.Fatalf("restore preflight reached provider or invented usage: %+v %v calls=%d", result, err, len(mock.Calls()))
	}
	if application.GetSkillHookRestoreError() == nil {
		t.Fatal("/hooks diagnostics lost restore failure")
	}
	decodeSingleHeadlessResult(t, stdout.Bytes())
}

func TestCheckpointRestoreReconcilesExecutableSkillHooks(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	root := t.TempDir()
	writeResumeHookSkill(t, root)
	application, write, _ := newResumeHookApp(t, root, testkit.NewMockClient(), "claude-global", false)
	tools.BindSkillInvocationLedger(application.registry, application.session.InvocationLedger())
	application.session.SaveCheckpoint("before")
	loaded, err := application.executor.InvokeTool(t.Context(), "skill", map[string]any{"name": "guard"})
	if err != nil || !loaded.Success {
		t.Fatalf("load=%+v err=%v", loaded, err)
	}
	application.session.SaveCheckpoint("loaded")
	_, _ = application.executor.InvokeTool(t.Context(), "read", map[string]any{"file_path": "notes.txt"})
	refs, _ := application.session.InvocationLedger().HookActivations()
	if !refs[0].Consumed[0] {
		t.Fatal("once hook did not run")
	}
	if !application.session.RestoreCheckpoint("before") {
		t.Fatal("restore before skill")
	}
	result, err := application.executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": "allowed.txt"})
	if err != nil || !result.Success || write.CallCount() != 1 || len(application.GetSkillHooks()) != 0 {
		t.Fatalf("rewind retained later guard: %+v err=%v", result, err)
	}
	if !application.session.RestoreCheckpoint("loaded") {
		t.Fatal("restore loaded skill")
	}
	result, err = application.executor.InvokeTool(t.Context(), "write", map[string]any{"file_path": "denied.txt"})
	if err != nil || result.Success || result.PolicyBlock == nil || write.CallCount() != 1 {
		t.Fatalf("rewind lost required guard: %+v err=%v", result, err)
	}
	refs, _ = application.session.InvocationLedger().HookActivations()
	if len(refs) != 1 || refs[0].Consumed[0] {
		t.Fatal("rollback did not restore its once state")
	}
}

func newResumeHookApp(t *testing.T, root string, mock *testkit.MockClient, source string, trusted bool) (*App, *appHeadlessScriptedTool, *appHeadlessScriptedTool) {
	t.Helper()
	write := &appHeadlessScriptedTool{name: "write", results: []tools.ToolResult{tools.NewSuccessResult("must not execute")}}
	read := &appHeadlessScriptedTool{name: "read", results: []tools.ToolResult{tools.NewSuccessResult("notes")}}
	application, executor := newHeadlessPolicyTestApp(t, mock, write)
	application.workDir = root
	application.session.SetWorkDir(root)
	application.session.SetProvider("mock")
	application.registry.MustRegister(read)
	application.registry.MustRegister(tools.NewBashTool(root))
	skill := tools.NewSkillToolWithCatalogAndWorkDir(skills.NewCatalog([]skills.Root{{Path: root, Source: source}}), root)
	skill.SetWorkspaceTrusted(trusted)
	application.registry.MustRegister(skill)
	application.hooksManager = hooks.NewManager(true, root)
	executor.SetHooks(application.hooksManager)
	executor.SetWorkDir(root)
	manager, err := chat.NewSessionManager(application.session, chat.DefaultSessionManagerConfig())
	if err != nil {
		t.Fatal(err)
	}
	application.sessionManager = manager
	return application, write, read
}
