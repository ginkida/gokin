package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gokin/internal/commands"
	"gokin/internal/hooks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func addDirectoryCommand(t *testing.T, a *App, path string) {
	t.Helper()
	if _, err := (&commands.AddDirCommand{}).Execute(t.Context(), []string{path}, a); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryAddedCommandPublishesAfterToolsAndDeliversRootContext(t *testing.T) {
	worker := testkit.NewMockClient().EnqueueText("First").EnqueueText("Second")
	a := newGoalTestApp(t, worker, testkit.NewMockClient())
	_, ctx, root := installCwdSettings(t, a, map[string]any{"DirectoryAdded": []any{
		fileHookGroup("slash_command", `cat > added-input.json; printf x >> added-count; touch added-ready; printf '%s' '{"continue":false,"systemMessage":"DIRECTORY_CONTEXT_PROOF","hookSpecificOutput":{"additionalContext":"DISCARDED_ADDITIONAL_CONTEXT"}}'`),
		fileHookGroup("register_repo_root", "touch wrong-source"),
	}})
	external, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.registry.MustRegister(tools.NewReadTool(root))
	path := filepath.Join(external, "proof")
	if err := os.WriteFile(path, []byte("new scope"), 0600); err != nil {
		t.Fatal(err)
	}
	addDirectoryCommand(t, a, external)
	if !a.isDirAccessAllowed(path) {
		t.Fatal("command returned before the grant was published")
	}
	registered, _ := a.registry.Get("read")
	if result, err := registered.Execute(t.Context(), map[string]any{"file_path": path}); err != nil || !result.Success {
		t.Fatal("new tool scope unavailable", result, err)
	}
	waitSessionStartFile(t, filepath.Join(root, "added-ready"))
	a.sessionStart.directoryWorkers.Wait()
	if childNotes := hooks.TakeAsyncNotes(hooks.ContextWithAsyncScope(ctx, nil)); childNotes != "" {
		t.Fatal("child consumed directory result", childNotes)
	}
	data, _ := os.ReadFile(filepath.Join(root, "added-input.json"))
	var input map[string]any
	_ = json.Unmarshal(data, &input)
	if input["directory"] != external || input["cwd"] != root || input["source"] != "slash_command" || input["session_id"] != a.session.GetID() || input["tool_name"] != nil {
		t.Fatalf("wrong directory event: %s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "wrong-source")); !os.IsNotExist(err) {
		t.Fatal("wrong-source matcher ran", err)
	}
	opts := HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: io.Discard, Stderr: io.Discard}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "first", opts); err != nil {
		t.Fatal(err)
	}
	if _, err := a.RunHeadlessWithOptions(t.Context(), "second", opts); err != nil {
		t.Fatal(err)
	}
	if len(worker.Calls()) != 2 || !strings.Contains(worker.Calls()[0].Message, "DIRECTORY_CONTEXT_PROOF") || strings.Contains(worker.Calls()[0].Message, "DISCARDED_ADDITIONAL_CONTEXT") || strings.Contains(worker.Calls()[1].Message, "DIRECTORY_CONTEXT_PROOF") {
		t.Fatal("directory result was lost, over-delivered or unsanitized", worker.Calls())
	}
}

func TestDirectoryAddedSkipsCoveredPathsAndNonCommandGrants(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	_, _, root := installCwdSettings(t, a, map[string]any{"DirectoryAdded": []any{fileHookGroup("", `printf x >> added-count`)}})
	external, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(external, "nested")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	// Ordinary grants are also used by boot/permission UI paths and are silent.
	if _, err := a.GrantAllowedDir(external, false); err != nil {
		t.Fatal(err)
	}
	addDirectoryCommand(t, a, root)
	addDirectoryCommand(t, a, external)
	addDirectoryCommand(t, a, child)
	if _, err := os.Stat(filepath.Join(root, "added-count")); !os.IsNotExist(err) {
		t.Fatal("already-covered directory triggered setup", err)
	}
	fresh := t.TempDir()
	var workers sync.WaitGroup
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := a.GrantAllowedDirFromCommand(t.Context(), fresh, false); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	a.sessionStart.directoryWorkers.Wait()
	data, err := os.ReadFile(filepath.Join(root, "added-count"))
	if err != nil || string(data) != "x" {
		t.Fatal("concurrent duplicate commands reran setup", string(data), err)
	}
}

func TestDirectoryAddedFailureCountAndSDKOutputRouting(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	_, _, root := installCwdSettings(t, a, map[string]any{"DirectoryAdded": []any{
		fileHookGroup("slash_command", `printf PRIVATE_FAILURE_DETAILS >&2; exit 2`),
		fileHookGroup("register_repo_root", `printf '%s' '{"continue":false,"systemMessage":"SDK_DEBUG_ONLY"}'`),
	}})
	addDirectoryCommand(t, a, t.TempDir())
	owner := a.sessionStart
	owner.directoryWorkers.Wait()
	notes := a.takeSessionStartNotes(owner)
	if !strings.Contains(notes, "1 hook failed") || strings.Contains(notes, "PRIVATE_FAILURE_DETAILS") {
		t.Fatal("directory failure leaked details or was lost", notes)
	}
	a.sessionLeaseMu.Lock()
	a.startDirectoryAddedLocked(owner, root, "register_repo_root")
	a.sessionLeaseMu.Unlock()
	owner.directoryWorkers.Wait()
	if notes := a.takeSessionStartNotes(owner); notes != "" {
		t.Fatal("SDK event entered model context", notes)
	}
}

func TestDirectoryAddedClearCancelsWorkAndRemovedSourceDropsNotes(t *testing.T) {
	for _, mode := range []string{"clear", "removed", "async"} {
		t.Run(mode, func(t *testing.T) {
			a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
			group := fileHookGroup("slash_command", `touch added-started; while [ ! -f added-release ]; do sleep .02; done; touch added-finished; printf '%s' '{"systemMessage":"LATE_DIRECTORY_CONTEXT"}'`)
			if mode == "async" {
				group["hooks"].([]any)[0].(map[string]any)["async"] = true
			}
			_, _, root := installCwdSettings(t, a, map[string]any{"DirectoryAdded": []any{group}})
			owner := a.sessionStart
			addDirectoryCommand(t, a, t.TempDir())
			waitSessionStartFile(t, filepath.Join(root, "added-started"))
			if mode == "clear" {
				if err := a.ClearConversationForRewind(t.Context()); err != nil {
					t.Fatal(err)
				}
				closeSessionStartOwner(owner)
				if owner.ctx.Err() == nil || a.takeSessionStartNotes(owner) != "" {
					t.Fatal("clear retained old directory owner")
				}
				if _, err := os.Stat(filepath.Join(root, "added-finished")); !os.IsNotExist(err) {
					t.Fatal("directory worker outlived clear", err)
				}
				return
			}
			if mode == "removed" {
				if err := os.WriteFile(filepath.Join(root, ".claude/settings.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "added-release"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			owner.directoryWorkers.Wait()
			awaitAppAsync(t, func() bool { return !owner.async.HasPendingWork() })
			notes := a.takeSessionStartNotes(owner)
			if strings.Contains(notes, "LATE_DIRECTORY_CONTEXT") != (mode == "async") {
				t.Fatal("late result routing failed", notes)
			}
		})
	}
}

func TestDirectoryAddedCanceledCommandCannotGrant(t *testing.T) {
	a := newGoalTestApp(t, testkit.NewMockClient(), testkit.NewMockClient())
	installCwdSettings(t, a, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	external := t.TempDir()
	if _, err := a.GrantAllowedDirFromCommand(ctx, external, false); err == nil || a.isDirAccessAllowed(external) {
		t.Fatal("cancelled command published directory access", err)
	}
}
