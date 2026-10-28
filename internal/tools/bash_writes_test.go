package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/gitexec"
	"gokin/internal/security"
	"gokin/internal/tasks"
	"gokin/internal/testkit"
	"google.golang.org/genai"
)

func TestExecutorBashWriteRefreshesReadAndDeclaresOnlyDelta(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	path := filepath.Join(root, "value.go")
	for _, name := range []string{"value.go", "untouched.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bash := NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := NewRegistry()
	registry.MustRegister(bash)
	registry.MustRegister(NewReadTool(root))
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	e.SetReadTracker(NewFileReadTracker())
	e.SetWriteTracker(NewFileWriteTracker())
	read := &genai.FunctionCall{ID: "read", Name: "read", Args: map[string]any{"file_path": path}}
	if r := e.doExecuteTool(t.Context(), read); !r.Success {
		t.Fatal(r)
	}
	// Keep a pre-write read in the result cache as well as the dedup tracker.
	e.SetToolCache(NewToolResultCache(DefaultCacheConfig()))
	if r := e.doExecuteTool(t.Context(), read); !r.Success {
		t.Fatal(r)
	}
	r := e.doExecuteTool(t.Context(), &genai.FunctionCall{ID: "bash", Name: "bash", Args: map[string]any{"command": "sed -i.bak 's/old/new/' value.go && touch -r value.go.bak value.go && rm value.go.bak"}})
	if !r.Success {
		t.Fatal(r)
	}
	fresh := e.doExecuteTool(t.Context(), read)
	if !fresh.Success || !strings.Contains(fresh.Content, "new") || strings.Contains(fresh.Content, "[Unchanged") {
		t.Fatalf("stale read after bash: %+v", fresh)
	}
	if got := WrittenPathsFromResult(r); !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("written_paths=%v want [%s]", got, path)
	}
}

func TestWorkspaceWriteObservationHandlesOverlappingOwners(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	var first, second WorkspaceWriteTracker
	started, release := make(chan struct{}), make(chan struct{})
	completed := make(chan ToolResult, 1)
	updates := make(chan ToolResult, 2)
	go func() {
		r, _ := first.Observe(t.Context(), root, "bash", nil, func(context.Context) (ToolResult, error) {
			close(started)
			<-release
			if err := os.WriteFile(filepath.Join(root, "first.go"), []byte("first"), 0600); err != nil {
				return NewErrorResult(err.Error()), nil
			}
			return NewSuccessResult("first"), nil
		}, nil)
		completed <- r
	}()
	<-started
	r, _ := second.Observe(t.Context(), root, "bash", nil, func(context.Context) (ToolResult, error) {
		if err := os.WriteFile(filepath.Join(root, "second.go"), []byte("second"), 0600); err != nil {
			return NewErrorResult(err.Error()), nil
		}
		return NewSuccessResult("second"), nil
	}, func(result ToolResult, _ bool) { updates <- result })
	close(release)
	for _, result := range []ToolResult{r, <-completed} {
		if !result.Success || !FilesChangedPartial(result) || len(WrittenPathsFromResult(result)) == 0 || !strings.Contains(result.Content, "overlapping") {
			t.Fatalf("overlapping owners guessed attribution: %+v", result)
		}
	}
	select {
	case updated := <-updates:
		want := []string{filepath.Join(root, "first.go"), filepath.Join(root, "second.go")}
		if !reflect.DeepEqual(WrittenPathsFromResult(updated), want) || !FilesChangedPartial(updated) {
			t.Fatalf("earlier owner did not receive the overlap union: %+v", updated)
		}
	default:
		t.Fatal("later writes were never published to the earlier owner")
	}
}

func TestExecutorBackgroundNativeWritesAreNotShellWrites(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	manager := tasks.NewManager(root)
	t.Cleanup(manager.CancelAll)
	bash := NewBashTool(root)
	bash.SetSandboxEnabled(false)
	bash.SetTaskManager(manager)
	registry := NewRegistry()
	registry.MustRegister(bash)
	registry.MustRegister(NewWriteTool(root))
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	completed := make(chan ToolResult, 1)
	e.SetHandler(&ExecutionHandler{OnToolEnd: func(name string, _ map[string]any, r ToolResult) {
		if name == "bash" && HasWorkspaceWriteObservation(r) {
			completed <- r
		}
	}})
	r, err := e.InvokeTool(t.Context(), "bash", map[string]any{"command": "while [ ! -f .gokin-tmp/release ]; do sleep 0.01; done", "run_in_background": true})
	if err != nil || !r.Success {
		t.Fatalf("background start: %+v %v", r, err)
	}
	r, err = e.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "other.go"), "content": "package example\n"})
	if err != nil || !r.Success {
		t.Fatalf("native write: %+v %v", r, err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".gokin-tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gokin-tmp", "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completed:
		if !result.Success || len(WrittenPathsFromResult(result)) != 0 || !FilesChangedPartial(result) {
			t.Fatalf("background claimed the native write: %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("background completion did not publish")
	}
}

func TestExecutorGitBackgroundPrintfReportsNoWrites(t *testing.T) {
	for _, command := range []string{"printf hello", "sh -c 'printf hello'"} {
		t.Run(command, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			cmd := gitexec.CommandContext(t.Context(), "init", "-q")
			cmd.Dir = root
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git init: %s %v", out, err)
			}
			manager := tasks.NewManager(root)
			t.Cleanup(manager.CancelAll)
			bash := NewBashTool(root)
			bash.SetSandboxEnabled(false)
			bash.SetTaskManager(manager)
			registry := NewRegistry()
			registry.MustRegister(bash)
			e := NewExecutor(registry, nil, time.Second)
			e.SetWorkDir(root)
			r, err := e.InvokeTool(t.Context(), "bash", map[string]any{"command": command, "run_in_background": true})
			if err != nil || !r.Success {
				t.Fatalf("start: %+v %v", r, err)
			}
			id, _ := r.Data.(map[string]any)["task_id"].(string)
			task, ok := manager.Get(id)
			if !ok {
				t.Fatal("missing task")
			}
			select {
			case <-task.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("background did not complete")
			}
			if info := task.GetInfo(); info.WriteDelta != nil && (len(info.WriteDelta.Paths) != 0 || info.WriteDelta.Partial) {
				t.Fatalf("transcript was counted: %+v", info.WriteDelta)
			}
			if e.WorkspaceWritesPartial() || len(WrittenPathsFromResult(r)) != 0 {
				t.Fatal("read-only background command armed observation")
			}

		})
	}
}

func TestWorkspaceObservationSubtractsNativeSiblingWrites(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	registry := NewRegistry()
	registry.MustRegister(NewWriteTool(root))
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	var tracker WorkspaceWriteTracker
	r, err := tracker.Observe(t.Context(), root, "bash", map[string]any{"command": "sleep 1"}, func(ctx context.Context) (ToolResult, error) {
		written, err := e.InvokeTool(ctx, "write", map[string]any{"file_path": filepath.Join(root, "other.go"), "content": "package example\n"})
		if err != nil || !written.Success {
			return written, err
		}
		return NewSuccessResult("slept"), nil
	}, nil)
	if err != nil || !r.Success || len(WrittenPathsFromResult(r)) != 0 || !FilesChangedPartial(r) {
		t.Fatalf("native sibling write claimed by shell: %+v %v", r, err)
	}
}

func TestWorkspaceObservationSkipsReadOnlyBash(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	t.Setenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_FILES", "1")
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"git status --short", "git log -1", "git diff", "ls", "cat a", "cat f | wc -l", "grep -rn x .", "rg x", "find . -name '*.go'"} {
		t.Run(command, func(t *testing.T) {
			var tracker WorkspaceWriteTracker
			r, err := tracker.Observe(t.Context(), root, "bash", map[string]any{"command": command}, func(context.Context) (ToolResult, error) { return NewSuccessResult("read"), nil }, nil)
			if err != nil || HasWorkspaceWriteObservation(r) || tracker.previous != nil || tracker.Partial() {
				t.Fatalf("read-only command paid snapshot cost: %+v %v", r, err)
			}
		})
	}
}

func TestWorkspaceObservationIncludesPreviouslyReadOnlyEscapes(t *testing.T) {
	for _, command := range []string{"go test ./...", "go build ./...", "go vet ./...", "git diff --output=state.go", "git -c core.fsmonitor=./mutate status", "printf ok\ntouch changed", "find . '-exec' touch changed +"} {
		t.Run(command, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			var tracker WorkspaceWriteTracker
			path := filepath.Join(root, "changed")
			r, err := tracker.Observe(t.Context(), root, "bash", map[string]any{"command": command}, func(context.Context) (ToolResult, error) {
				return NewSuccessResult("completed"), os.WriteFile(path, []byte("changed"), 0600)
			}, nil)
			if err != nil || !HasWorkspaceWriteObservation(r) || !reflect.DeepEqual(WrittenPathsFromResult(r), []string{path}) {
				t.Fatalf("workspace write escaped observation: %+v err=%v", r, err)
			}
		})
	}
}

func TestWorkspaceNativeWriteRefreshesCachedIgnoredBaseline(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	for name, content := range map[string]string{".gitignore": "draft.go\n", "draft.go": "old\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := gitexec.CommandContext(t.Context(), "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	var tracker WorkspaceWriteTracker
	args := map[string]any{"command": ":"}
	noop := func(context.Context) (ToolResult, error) { return NewSuccessResult("noop"), nil }
	if r, _ := tracker.Observe(t.Context(), root, "bash", args, noop, nil); !r.Success || FilesChangedPartial(r) {
		t.Fatal(r)
	}
	registry := NewRegistry()
	registry.MustRegister(NewWriteTool(root))
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	r, err := e.InvokeTool(t.Context(), "write", map[string]any{"file_path": filepath.Join(root, "draft.go"), "content": "native\n"})
	if err != nil || !r.Success {
		t.Fatalf("native write: %+v %v", r, err)
	}
	_, _ = tracker.Observe(t.Context(), root, "bash", args, noop, nil)
	r, err = tracker.Observe(t.Context(), root, "bash", args, func(context.Context) (ToolResult, error) {
		return NewSuccessResult("unignore"), os.WriteFile(filepath.Join(root, ".gitignore"), nil, 0600)
	}, nil)
	if err != nil || FilesChangedPartial(r) || !reflect.DeepEqual(WrittenPathsFromResult(r), []string{filepath.Join(root, ".gitignore")}) {
		t.Fatalf("cached native write attributed to later shell: %+v %v", r, err)
	}
}

func TestWorkspaceWriteObservationPreservesWritesOnExecutionError(t *testing.T) {
	for _, panicAfterWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panicAfterWrite], func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			var tracker WorkspaceWriteTracker
			r, err := tracker.Observe(t.Context(), root, "bash", nil, func(context.Context) (ToolResult, error) {
				if err := os.WriteFile(filepath.Join(root, "written.go"), []byte("written"), 0600); err != nil {
					return ToolResult{}, err
				}
				if panicAfterWrite {
					panic("after write")
				}
				return ToolResult{}, errors.New("after write")
			}, nil)
			if err != nil || r.Success || !strings.Contains(r.Error, "after write") || !reflect.DeepEqual(WrittenPathsFromResult(r), []string{filepath.Join(root, "written.go")}) {
				t.Fatalf("execution failure erased mutation: %+v %v", r, err)
			}
		})
	}
}

func TestWorkspaceBackgroundRemainsPendingUntilLedgerPublish(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	manager := tasks.NewManager(root)
	t.Cleanup(manager.CancelAll)
	var tracker WorkspaceWriteTracker
	entered, release := make(chan struct{}), make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	var id string
	r, err := tracker.Observe(t.Context(), root, "bash", map[string]any{"run_in_background": true}, func(ctx context.Context) (ToolResult, error) {
		var err error
		id, err = manager.StartInDir(ctx, root, "echo child > child.go")
		return NewSuccessResult("started"), err
	}, func(ToolResult, bool) {
		close(entered)
		<-release
	})
	if err != nil || !r.Success {
		t.Fatalf("start=%+v %v", r, err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("completion callback did not start")
	}
	pending := tracker.Partial()
	close(release)
	released = true
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("missing task")
	}
	select {
	case <-task.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("completion callback did not finish")
	}
	if !pending || tracker.Partial() {
		t.Fatal("completion was declared before ledger publication, or remained pending afterward")
	}
}

func TestExecutorBackgroundBashWritesCompleteBeforeDone(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	path := filepath.Join(root, "value.go")
	if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	manager := tasks.NewManager(root)
	t.Cleanup(manager.CancelAll)
	bash := NewBashTool(root)
	bash.SetSandboxEnabled(false)
	bash.SetTaskManager(manager)
	output := NewTaskOutputTool()
	output.SetManager(manager)
	registry := NewRegistry()
	registry.MustRegister(bash)
	registry.MustRegister(output)
	registry.MustRegister(NewReadTool(root))
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	e.SetWriteTracker(NewFileWriteTracker())
	e.SetReadTracker(NewFileReadTracker())
	e.SetToolCache(NewToolResultCache(DefaultCacheConfig()))
	completed := make(chan ToolResult, 1)
	e.SetHandler(&ExecutionHandler{OnToolEnd: func(name string, _ map[string]any, r ToolResult) {
		if name == "bash" && len(WrittenPathsFromResult(r)) > 0 {
			completed <- r
		}
	}})
	read := &genai.FunctionCall{ID: "r", Name: "read", Args: map[string]any{"file_path": filepath.Join(root, "value.go")}}
	if r := e.doExecuteTool(t.Context(), read); !r.Success {
		t.Fatal(r)
	}
	started := e.doExecuteTool(t.Context(), &genai.FunctionCall{ID: "b", Name: "bash", Args: map[string]any{
		"command": "echo new > value.go; exit 1", "run_in_background": true,
	}})
	if !started.Success {
		t.Fatal(started)
	}
	id, _ := started.Data.(map[string]any)["task_id"].(string)
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("background task missing")
	}
	select {
	case <-task.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("background task did not finish")
	}
	select {
	case result := <-completed:
		if !reflect.DeepEqual(WrittenPathsFromResult(result), []string{path}) || FilesChangedPartial(result) || !strings.Contains(result.Content, "not undoable") {
			t.Fatalf("completion evidence=%+v", result)
		}
	default:
		t.Fatal("Done published before executor received write evidence")
	}
	if got := e.writeTracker.RecentlyModifiedFiles(0); !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("background writes=%v", got)
	}
	fresh := e.doExecuteTool(t.Context(), read)
	if !strings.Contains(fresh.Content, "new") || strings.Contains(fresh.Content, "[Unchanged") {
		t.Fatalf("stale background read: %+v", fresh)
	}
	// Even an empty incremental page retains the completed command's delta.
	r := e.doExecuteTool(t.Context(), &genai.FunctionCall{ID: "out", Name: "task_output", Args: map[string]any{"task_id": id, "offset": 999999}})
	var historical struct {
		Paths []string `json:"background_written_paths"`
	}
	encoded, _ := json.Marshal(r.Data)
	if err := json.Unmarshal(encoded, &historical); err != nil || !reflect.DeepEqual(historical.Paths, []string{path}) || len(WrittenPathsFromResult(r)) != 0 || HasWorkspaceWriteObservation(r) {
		t.Fatalf("task_output lost history or invented a new mutation: %+v", r)
	}
}

func TestExecutorWorkspaceCapWarnsAndInvalidatesReads(t *testing.T) {
	t.Setenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_FILES", "1")
	root := testkit.ResolvedTempDir(t)
	for _, name := range []string{"value.go", "other.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bash := NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := NewRegistry()
	registry.MustRegister(bash)
	registry.MustRegister(NewReadTool(root))
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	e.SetReadTracker(NewFileReadTracker())
	e.SetToolCache(NewToolResultCache(DefaultCacheConfig()))
	read := &genai.FunctionCall{ID: "r", Name: "read", Args: map[string]any{"file_path": filepath.Join(root, "value.go")}}
	e.doExecuteTool(t.Context(), read)
	r := e.doExecuteTool(t.Context(), &genai.FunctionCall{ID: "b", Name: "bash", Args: map[string]any{"command": "echo new > value.go"}})
	if !r.Success || !FilesChangedPartial(r) || !strings.Contains(r.Content, "files_changed is partial") || len(WrittenPathsFromResult(r)) != 0 {
		t.Fatalf("cap result=%+v", r)
	}
	if fresh := e.doExecuteTool(t.Context(), read); !strings.Contains(fresh.Content, "new") || strings.Contains(fresh.Content, "[Unchanged") {
		t.Fatalf("partial scan served stale read: %+v", fresh)
	}
}

func TestExecutorShellUndoNoticeOncePerTurn(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	bash := NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := NewRegistry()
	registry.MustRegister(bash)
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	for i, command := range []string{"echo one > value.go", "echo two > value.go", "echo three > value.go"} {
		if i == 2 {
			e.ResetSideEffectLedger()
		}
		r := e.doExecuteTool(t.Context(), &genai.FunctionCall{ID: command, Name: "bash", Args: map[string]any{"command": command}})
		if !r.Success || strings.Contains(r.Content, "not undoable") != (i != 1) {
			t.Fatalf("notice at call %d: %+v", i, r)
		}
	}
}

func TestExecutorVerificationToolsObserveGeneratedFiles(t *testing.T) {
	// Fake local runner binaries exercise the real tools without dependencies,
	// network access, or recursively launching this repository's own tests.
	for _, name := range []string{"run_tests", "verify_code"} {
		t.Run(name, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			bin := testkit.ResolvedTempDir(t)
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\necho generated > generated.go\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			policy := security.NewSandboxPolicy(root, security.SandboxConfig{Enabled: false})
			registry := NewRegistry()
			args := map[string]any{}
			if name == "run_tests" {
				tool := NewRunTestsTool(root)
				tool.SetSandboxPolicy(policy)
				registry.MustRegister(tool)
				args["framework"] = "go"
			} else {
				tool := NewVerifyCodeTool(root)
				tool.SetSandboxPolicy(policy)
				registry.MustRegister(tool)
			}
			e := NewExecutor(registry, nil, time.Second)
			e.SetWorkDir(root)
			r := e.doExecuteTool(t.Context(), &genai.FunctionCall{ID: name, Name: name, Args: args})
			if !r.Success || !reflect.DeepEqual(WrittenPathsFromResult(r), []string{filepath.Join(root, "generated.go")}) {
				t.Fatalf("%s failed to observe generated code: %+v", name, r)
			}
		})
	}
}

func TestFailedVerificationShellWriteInvalidatesEarlierProof(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	bin := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\necho generated > generated.go\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	bash := NewBashTool(root)
	bash.SetSandboxEnabled(false)
	registry := NewRegistry()
	registry.MustRegister(bash)
	e := NewExecutor(registry, nil, time.Second)
	e.SetWorkDir(root)
	ctx, state := ContextWithHeadlessPermissions(t.Context(), false, "default")
	RecordHeadlessVerification(ctx, "earlier-check", "passed")
	r, err := e.InvokeTool(ctx, "bash", map[string]any{"command": "go test ./... > generated.go"})
	checks := state.Verification()
	if err != nil || r.Success || len(WrittenPathsFromResult(r)) != 1 || len(checks.Checks) != 1 || checks.Status != "failed" || checks.Checks[0].Name == "earlier-check" {
		t.Fatalf("failed generating check retained pre-write proof: result=%+v receipt=%+v err=%v", r, checks, err)
	}
}
