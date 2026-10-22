package app

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/hooks"
	"gokin/internal/skills"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

// Capture the response before publishing the event barrier. The test can then
// change the next response without racing this invocation's file read.
const recordFileEvent = `gokin_hook_response=''; if [ -f file-response.json ]; then gokin_hook_response=$(cat file-response.json); fi; cat >> file-events.jsonl; printf '\n' >> file-events.jsonl; printf '%s' "$gokin_hook_response"`

func fileHookGroup(matcher, command string) map[string]any {
	return map[string]any{"matcher": matcher, "hooks": []any{map[string]any{"type": "command", "command": command}}}
}

func installFileHookSettings(t *testing.T, a *App, paths []string, groups ...map[string]any) string {
	t.Helper()
	a.hooksManager.ConfigureClaudeSettings(a.workDir, t.TempDir(), true)
	path := filepath.Join(a.workDir, ".claude/settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	events := map[string]any{}
	if len(groups) > 0 {
		events["FileChanged"] = groups
	}
	if paths != nil {
		output, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"watchPaths": paths}})
		command := "printf '%s' '" + strings.ReplaceAll(string(output), "'", "'\\''") + "'"
		events["SessionStart"] = []any{fileHookGroup("startup|resume|fork", command)}
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
	return path
}

func fileHookEvents(a *App) []map[string]any {
	data, _ := os.ReadFile(filepath.Join(a.workDir, "file-events.jsonl"))
	var result []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		var event map[string]any
		if json.Unmarshal([]byte(line), &event) == nil {
			result = append(result, event)
		}
	}
	return result
}

func waitFileHookEvents(t *testing.T, a *App, count int) []map[string]any {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if events := fileHookEvents(a); len(events) >= count {
			return events
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatalf("missing file hook events: have %d, want %d", len(fileHookEvents(a)), count)
		}
	}
}

func waitHookWatch(t *testing.T, owner *sessionStartOwner, path string, present bool) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		owner.fileHooksMu.Lock()
		files := owner.fileHooks
		owner.fileHooksMu.Unlock()
		if (files != nil && files.watcher.Contains(path)) == present {
			return
		}
		select {
		case <-tick.C:
		case <-timer.C:
			t.Fatalf("watch for %s did not become %t", path, present)
		}
	}
}

func TestSessionStartWatchPathsObserveExternalFilesWithoutModelContext(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("Done")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	outside := filepath.Join(t.TempDir(), "outside.env")
	command := recordFileEvent + `; printf 'export GOKIN_FILE_ENV=FILE_ENV_PROOF\n' >> "$CLAUDE_ENV_FILE"; touch file-env-written`
	installFileHookSettings(t, a, []string{outside}, fileHookGroup("", command))
	response := `{"continue":false,"decision":"block","systemMessage":"FILE_NOTIFICATION","hookSpecificOutput":{"additionalContext":"MUST_NOT_REACH_MODEL","sessionTitle":"must not rename"}}`
	if err := os.WriteFile(filepath.Join(a.workDir, "file-response.json"), []byte(response), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("external update"), 0600); err != nil {
		t.Fatal(err)
	}
	events := waitFileHookEvents(t, a, 1)
	waitSessionStartFile(t, filepath.Join(a.workDir, "file-env-written"))
	if events[0]["session_id"] != a.session.GetID() || events[0]["file_path"] != outside || events[0]["event"] != "add" || events[0]["tool_name"] != nil {
		t.Fatal("wrong external file identity", events)
	}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "inspect", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if len(worker.Calls()) != 1 || strings.Contains(worker.Calls()[0].Message, "FILE_NOTIFICATION") || strings.Contains(worker.Calls()[0].Message, "MUST_NOT_REACH_MODEL") || a.session.GetTitle() == "must not rename" {
		t.Fatal("file observation changed model context or session metadata", worker.Calls())
	}
	workDir, err := filepath.EvalSymlinks(a.workDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := a.withSessionStartHooks(t.Context())
	result, err := tools.NewBashTool(workDir).Execute(ctx, map[string]any{"command": `printf '%s' "$GOKIN_FILE_ENV"`})
	if err != nil || !result.Success || !strings.Contains(result.Content, "FILE_ENV_PROOF") || os.Getenv("GOKIN_FILE_ENV") != "" {
		t.Fatal("file hook environment was lost or escaped to host", result, err)
	}
}

func TestFileChangedDynamicReplacementKeepsConfiguredLiteralPaths(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	first, second, static := filepath.Join(a.workDir, "first"), filepath.Join(a.workDir, "second"), filepath.Join(a.workDir, ".env")
	installFileHookSettings(t, a, []string{first}, fileHookGroup(".env", "true"), fileHookGroup("", recordFileEvent))
	response, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"watchPaths": []string{second}}})
	if err := os.WriteFile(filepath.Join(a.workDir, "file-response.json"), response, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("first event"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFileHookEvents(t, a, 1)
	waitHookWatch(t, a.sessionStart, second, true)
	waitHookWatch(t, a.sessionStart, first, false)
	if err := os.WriteFile(second, []byte("second event"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFileHookEvents(t, a, 2)
	if err := os.WriteFile(first, []byte("no longer watched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(static, []byte("static survives"), 0600); err != nil {
		t.Fatal(err)
	}
	events := waitFileHookEvents(t, a, 3)
	if events[1]["file_path"] != second || events[2]["file_path"] != static {
		t.Fatal("dynamic replacement lost static watches", events)
	}
	if err := os.WriteFile(filepath.Join(a.workDir, "file-response.json"), []byte(`{"hookSpecificOutput":{"watchPaths":[]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("clear dynamic list"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFileHookEvents(t, a, 4)
	waitHookWatch(t, a.sessionStart, second, false)
	waitHookWatch(t, a.sessionStart, static, true)
}

func TestFileChangedClearCancelsWorkersAndDiscardsOldWatchControls(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	path := filepath.Join(a.workDir, "proof")
	installFileHookSettings(t, a, []string{path}, fileHookGroup("", `touch file-hook-started; sleep 30; touch leaked-file-hook`))
	old := a.sessionStart
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitSessionStartFile(t, filepath.Join(a.workDir, "file-hook-started"))
	if err := a.ClearConversationForRewind(t.Context()); err != nil {
		t.Fatal(err)
	}
	if old.ctx.Err() == nil {
		t.Fatal("clear left old file owner alive")
	}
	old.fileHooksMu.Lock()
	files := old.fileHooks
	old.fileHooksMu.Unlock()
	files.Close()
	if _, err := os.Stat(filepath.Join(a.workDir, "leaked-file-hook")); !os.IsNotExist(err) {
		t.Fatal("old file process outlived clear", err)
	}
	waitHookWatch(t, a.sessionStart, path, false)
}

func TestFileChangedFailedClearRetainsWorkingObserver(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	a.session.AddUserMessage("retain durable source")
	path := filepath.Join(a.workDir, "proof")
	installFileHookSettings(t, a, []string{path}, fileHookGroup("", recordFileEvent))
	old := a.sessionStart
	storage := filepath.Join(os.Getenv("XDG_DATA_HOME"), "gokin/sessions", a.session.GetID()+".json")
	if err := os.MkdirAll(storage, 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.ClearConversationForRewind(t.Context()); err == nil {
		t.Fatal("failed persistence published a new owner")
	}
	if old.ctx.Err() != nil || a.sessionStart != old {
		t.Fatal("failed clear canceled working file hooks")
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitFileHookEvents(t, a, 1)
}

func TestFileChangedSkillOnceDoesNotRemoveWatchSeedForOtherHandlers(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	skill := tools.NewSkillToolWithCatalog(skills.NewCatalog(nil))
	a.registry.MustRegister(skill)
	registration, err := skill.HookScope().Prepare("file-observer", []hooks.ClaudeCommand{
		{Event: "FileChanged", Matcher: "proof", Command: "printf x >> file-once", Once: true, Timeout: time.Second},
		{Event: "FileChanged", Command: recordFileEvent, Timeout: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	registration.Commit()
	installFileHookSettings(t, a, nil)
	path := filepath.Join(a.workDir, "proof")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFileHookEvents(t, a, 1)
	// The source policy refresh must retain the literal seed after once ran.
	a.sessionLeaseMu.Lock()
	a.refreshFileHookPathsLocked(a.sessionStart, a.sessionStart.fileHooks)
	a.sessionLeaseMu.Unlock()
	if err := os.WriteFile(path, []byte("second longer"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFileHookEvents(t, a, 2)
	data, err := os.ReadFile(filepath.Join(a.workDir, "file-once"))
	if err != nil || string(data) != "x" {
		t.Fatal("once claim rearmed", string(data), err)
	}
}

func TestFileChangedRewakeBetweenHeadlessRecordsWaitsForExplicitPrompt(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("First").EnqueueText("Second")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	path := filepath.Join(a.workDir, "proof")
	group := fileHookGroup("", `printf FILE_REWAKE_REASON >&2; exit 2`)
	group["hooks"].([]any)[0].(map[string]any)["asyncRewake"] = true
	installFileHookSettings(t, a, []string{path}, group)
	opts := HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "first record", opts); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitAppAsync(t, func() bool { return a.currentFileAsyncHooks(a.sessionStart).HasRewake() })
	if len(worker.Calls()) != 1 {
		t.Fatal("file hook launched an unbounded headless turn", worker.Calls())
	}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "explicit second record", opts); err != nil {
		t.Fatal(err)
	}
	if len(worker.Calls()) != 2 || !strings.Contains(worker.Calls()[1].Message, "FILE_REWAKE_REASON") {
		t.Fatal("explicit turn lost pending background result", worker.Calls())
	}
}

func TestEscapeCancelsFileAsyncWorkWithoutCancellingSessionStart(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	a.running = true
	path := filepath.Join(a.workDir, "proof")
	group := fileHookGroup("", `touch file-hook-started; sleep 30; touch file-hook-leaked`)
	group["hooks"].([]any)[0].(map[string]any)["async"] = true
	installFileHookSettings(t, a, []string{path}, group)
	owner := a.sessionStart
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitSessionStartFile(t, filepath.Join(a.workDir, "file-hook-started"))
	scope := a.currentFileAsyncHooks(owner)
	if !a.cancelIdleAsyncHooks() || owner.ctx.Err() != nil || a.pendingFileAsyncHooks() {
		t.Fatal("Esc failed to cancel file work or cancelled startup")
	}
	scope.Close()
	if _, err := os.Stat(filepath.Join(a.workDir, "file-hook-leaked")); !os.IsNotExist(err) {
		t.Fatal("cancelled file process escaped", err)
	}
}

func TestFileChangedRemovedAsyncSourceCannotRetargetWatches(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	path, late := filepath.Join(a.workDir, "proof"), filepath.Join(a.workDir, "late")
	output, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"watchPaths": []string{late}}})
	command := `touch file-hook-started; while [ ! -f release-file-hook ]; do sleep .01; done; printf '%s' '` + string(output) + `'`
	group := fileHookGroup("", command)
	group["hooks"].([]any)[0].(map[string]any)["async"] = true
	settings := installFileHookSettings(t, a, []string{path}, group)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitSessionStartFile(t, filepath.Join(a.workDir, "file-hook-started"))
	scope := a.currentFileAsyncHooks(a.sessionStart)
	if err := os.WriteFile(settings, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.workDir, "release-file-hook"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	awaitAppAsync(t, func() bool { return !scope.HasPendingWork() })
	waitHookWatch(t, a.sessionStart, late, false)
}

func TestFileChangedResumePublishesOnlyTargetOwner(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	a.running = true
	oldPath, newPath := filepath.Join(a.workDir, "source-proof"), filepath.Join(a.workDir, "target-proof")
	settings := installFileHookSettings(t, a, []string{oldPath}, fileHookGroup("", recordFileEvent))
	old := a.sessionStart
	target := chat.NewSession()
	target.SetWorkDir(a.workDir)
	target.SetProvider("glm")
	target.AddUserMessage("resumed target")
	storage, err := chat.NewHistoryManager()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveFull(target); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"hooks": map[string]any{"FileChanged": []any{fileHookGroup("", recordFileEvent)}, "SessionStart": []any{fileHookGroup("resume", `printf '%s' '{"hookSpecificOutput":{"watchPaths":["`+newPath+`"]}}'`)}}})
	if err := os.WriteFile(settings, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SwitchSession(t.Context(), target.GetState(), false); err != nil {
		t.Fatal(err)
	}
	if old.ctx.Err() == nil {
		t.Fatal("resume retained old file owner")
	}
	if err := os.WriteFile(oldPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, nil, 0600); err != nil {
		t.Fatal(err)
	}
	events := waitFileHookEvents(t, a, 1)
	if events[0]["session_id"] != target.GetID() || events[0]["file_path"] != newPath {
		t.Fatal("old source published after resume", events)
	}
}
