package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func cwdShellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func installCwdSettings(t *testing.T, a *App, events map[string]any) (*tools.BashTool, context.Context, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	a.registry.MustRegister(bash)
	a.hooksManager.ConfigureClaudeSettings(a.workDir, t.TempDir(), true)
	path := filepath.Join(a.workDir, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"hooks": events})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.closeSessionStartHooks)
	ctx := a.withSessionStartHooks(t.Context())
	if err := hooks.AwaitSessionStart(ctx); err != nil {
		t.Fatal(err)
	}
	return bash, ctx, root
}

func executeCwdBash(t *testing.T, bash *tools.BashTool, ctx context.Context, command string) string {
	t.Helper()
	result, err := bash.Execute(ctx, map[string]any{"command": command})
	if err != nil || !result.Success {
		t.Fatalf("bash %q failed: %+v %v", command, result, err)
	}
	return result.Content
}

func TestCwdChangedRotatesTransientEnvironmentAndStaticPaths(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	root, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	fileBarrier := filepath.Join(root, "file-env-ready")
	bash, ctx, _ := installCwdSettings(t, a, map[string]any{
		"SessionStart": []any{fileHookGroup("startup", `printf 'export GOKIN_CWD_START=permanent\n' >> "$CLAUDE_ENV_FILE"`)},
		"FileChanged":  []any{fileHookGroup(".env", `printf 'export GOKIN_CWD_FILE=old\n' >> "$CLAUDE_ENV_FILE"; touch `+cwdShellQuote(fileBarrier))},
		"CwdChanged":   []any{fileHookGroup("[", `cat > cwd-input.json; printf 'export GOKIN_CWD_NEW=current\n' >> "$CLAUDE_ENV_FILE"; touch cwd-ready; printf '%s' '{"continue":false,"decision":"block","systemMessage":"CWD_NOTIFICATION_ONLY","hookSpecificOutput":{"additionalContext":"CWD_NO_MODEL_CONTEXT","watchPaths":[]}}'`)},
	})
	owner := a.sessionStart
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("change"), 0600); err != nil {
		t.Fatal(err)
	}
	waitSessionStartFile(t, fileBarrier)
	if content := executeCwdBash(t, bash, ctx, `printf '%s/%s' "$GOKIN_CWD_START" "$GOKIN_CWD_FILE"`); content != "permanent/old" {
		t.Fatal("missing initial environment", content)
	}
	executeCwdBash(t, bash, ctx, "cd "+cwdShellQuote(child))
	waitSessionStartFile(t, filepath.Join(child, "cwd-ready"))
	if content := executeCwdBash(t, bash, ctx, `printf '%s/%s/%s' "$GOKIN_CWD_START" "$GOKIN_CWD_FILE" "$GOKIN_CWD_NEW"`); content != "permanent//current" {
		t.Fatal("cwd did not clear transient environment or lost startup environment", content)
	}
	waitHookWatch(t, owner, filepath.Join(child, ".env"), true)
	waitHookWatch(t, owner, filepath.Join(root, ".env"), false)
	data, _ := os.ReadFile(filepath.Join(child, "cwd-input.json"))
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	if input["old_cwd"] != root || input["new_cwd"] != child || input["cwd"] != child || input["session_id"] != owner.id {
		t.Fatalf("wrong directory event: %s", data)
	}
	if a.workDir == child || a.session.GetWorkDir() == child {
		t.Fatal("shell directory replaced project/session identity")
	}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "inspect", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if len(worker.Calls()) != 1 || strings.Contains(worker.Calls()[0].Message, "CWD_NOTIFICATION_ONLY") || strings.Contains(worker.Calls()[0].Message, "CWD_NO_MODEL_CONTEXT") {
		t.Fatal("directory notification reached model context", worker.Calls())
	}
}

func TestCwdChangedCancelsOldDirectoryWorkersAndLateControls(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	root, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
	for _, dir := range []string{first, second} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := `touch cwd-started; if [ "$PWD" = ` + cwdShellQuote(first) + ` ]; then sleep 30; printf 'export GOKIN_CWD_STALE=leaked\n' >> "$CLAUDE_ENV_FILE"; touch leaked-worker; else printf 'export GOKIN_CWD_CURRENT=second\n' >> "$CLAUDE_ENV_FILE"; touch cwd-ready; fi`
	bash, ctx, _ := installCwdSettings(t, a, map[string]any{"CwdChanged": []any{fileHookGroup("", command)}})
	owner := a.sessionStart
	executeCwdBash(t, bash, ctx, "cd "+cwdShellQuote(first))
	waitSessionStartFile(t, filepath.Join(first, "cwd-started"))
	files := owner.fileHooks
	files.mu.Lock()
	oldCtx, oldScope := files.dirCtx, files.async
	files.mu.Unlock()
	owner.fileHooksMu.Lock()
	oldEnv := owner.fileEnvFile
	owner.fileHooksMu.Unlock()
	executeCwdBash(t, bash, ctx, "cd "+cwdShellQuote(second))
	waitSessionStartFile(t, filepath.Join(second, "cwd-ready"))
	if oldCtx.Err() == nil {
		t.Fatal("directory transition did not cancel previous workers")
	}
	oldScope.Close()
	files.cwdWorkers.Wait()
	owner.fileHooksMu.Lock()
	newEnv := owner.fileEnvFile
	owner.fileHooksMu.Unlock()
	if oldEnv == newEnv || newEnv == "" {
		t.Fatal("directory transition reused the environment file")
	}
	if _, err := os.Stat(filepath.Join(first, "leaked-worker")); !os.IsNotExist(err) {
		t.Fatal("old directory worker outlived transition", err)
	}
	// Even a delayed handler completion cannot publish into the new generation.
	late := filepath.Join(root, "late-watch")
	defs := a.hooksManager.GetHooks()
	for _, hook := range defs {
		if hook.Type == hooks.CwdChanged {
			a.applyFileChangeResults(owner, files, oldCtx, []hooks.Result{{Hook: hook, WatchPaths: []string{late}}})
		}
	}
	waitHookWatch(t, owner, late, false)
	// An escaped process writing its old private file cannot refill the new env.
	if err := os.WriteFile(oldEnv, []byte("export GOKIN_CWD_STALE=leaked\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if content := executeCwdBash(t, bash, ctx, `printf '%s/%s' "$GOKIN_CWD_STALE" "$GOKIN_CWD_CURRENT"`); content != "/second" {
		t.Fatal("old directory environment leaked", content)
	}
}

func TestCwdChangedChildAndStaleContextCannotChangeRootOwner(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	bash, ctx, root := installCwdSettings(t, a, map[string]any{"CwdChanged": []any{fileHookGroup("", "touch cwd-reached")}})
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	owner := a.sessionStart
	private := tools.NewBashTool(root)
	private.SetSandboxEnabled(false)
	executeCwdBash(t, private, hooks.ContextWithAsyncScope(ctx, nil), "cd "+cwdShellQuote(child))
	if owner.cwd != root || bash.WorkingDirectory() != root || private.WorkingDirectory() != child {
		t.Fatal("private shell changed main directory")
	}
	stale := withConversationLineage(ctx, owner.id, a.recoveryEpoch.Load())
	a.advanceConversationEpoch()
	executeCwdBash(t, bash, stale, "cd "+cwdShellQuote(child))
	if bash.WorkingDirectory() != root || owner.cwd != root {
		t.Fatal("stale turn published shell state")
	}
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	executeCwdBash(t, bash, ctx, "cd "+cwdShellQuote(child))
	if bash.WorkingDirectory() != root {
		t.Fatal("replaced conversation published shell state")
	}
	fresh := a.withSessionStartHooks(t.Context())
	executeCwdBash(t, bash, fresh, "cd "+cwdShellQuote(child))
	waitSessionStartFile(t, filepath.Join(child, "cwd-reached"))
	if bash.WorkingDirectory() != child || a.sessionStart.cwd != child {
		t.Fatal("current owner failed to publish shell state")
	}
}
