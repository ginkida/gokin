package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gokin/internal/donegate"
	"gokin/internal/tasks"
	"gokin/internal/testkit"
	"gokin/internal/tools"
)

func TestHeadlessBashWritesReachFilesChangedAndDoneGate(t *testing.T) {
	for _, command := range []string{"echo x > new.go", "echo x > new.go; exit 1"} {
		t.Run(command, func(t *testing.T) {
			root := testkit.ResolvedTempDir(t)
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0600); err != nil {
				t.Fatal(err)
			}
			bash := tools.NewBashTool(root)
			bash.SetSandboxEnabled(false)
			model := testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": command}).EnqueueText("Created new.go.")
			a, e := newHeadlessPolicyTestApp(t, model, bash)
			a.workDir = root
			e.SetWorkDir(root)
			result, err := a.RunHeadlessWithOptions(t.Context(), "Create new.go", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.FilesChanged, []string{"new.go"}) || result.ClaimMismatch {
				t.Fatalf("files_changed=%v claim_mismatch=%v", result.FilesChanged, result.ClaimMismatch)
			}
			touched := a.snapshotResponseTouchedPaths()
			if !reflect.DeepEqual(touched, []string{"new.go"}) {
				t.Fatalf("done-gate targets=%v", touched)
			}
			profile := donegate.DetectProfileWithTouchedPaths(root, touched)
			checks := donegate.BuildChecks(a.registry, root, "Create new.go", []string{"bash"}, profile, donegate.Policy{Enabled: true, Mode: "strict"})
			if len(checks) == 0 {
				t.Fatal("bash-touched Go code did not produce done-gate checks")
			}
		})
	}
}

func TestHeadlessBashWorkspaceCapIsPublic(t *testing.T) {
	t.Setenv("GOKIN_WORKSPACE_SNAPSHOT_MAX_FILES", "1")
	root := testkit.ResolvedTempDir(t)
	for _, name := range []string{"first.go", "second.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("before\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	model := testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "echo after > first.go"}).EnqueueText("Updated first.go.")
	a, e := newHeadlessPolicyTestApp(t, model, bash)
	a.workDir = root
	e.SetWorkDir(root)
	var stdout bytes.Buffer
	result, err := a.RunHeadlessWithOptions(t.Context(), "Update first.go", HeadlessOptions{OutputFormat: HeadlessOutputJSON, Stdout: &stdout, Stderr: io.Discard, NoSelfReview: true})
	if err != nil || !result.FilesChangedPartial || len(result.FilesChanged) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded["files_changed_partial"] != true {
		t.Fatalf("JSON lost partial flag: %s %v", stdout.String(), err)
	}
	if frame := SDKResult(result, 0, 0); frame["files_changed_partial"] != true {
		t.Fatalf("SDK lost partial flag: %+v", frame)
	}
	// The next invocation starts with a fresh reporting flag.
	model.EnqueueText("No changes.")
	next, err := a.RunHeadlessWithOptions(t.Context(), "Say hello", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
	if err != nil || next.FilesChangedPartial {
		t.Fatalf("partial flag leaked: %+v %v", next, err)
	}
}

func TestHeadlessBashTouchedCodeRunsDoneGate(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	model := testkit.NewMockClient().EnqueueToolCall("bash", map[string]any{"command": "echo x > new.go"}).EnqueueText("Created new.go.")
	a, e := newHeadlessPolicyTestApp(t, model, bash)
	a.workDir = root
	e.SetWorkDir(root)
	a.config.DoneGate.Enabled = true
	a.config.DoneGate.AutoFixAttempts = 0
	result, err := a.RunHeadlessWithOptions(t.Context(), "Do it", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
	if err == nil || result.Status == "success" || !reflect.DeepEqual(result.FilesChanged, []string{"new.go"}) {
		t.Fatalf("invalid generated Go code bypassed gate: %+v %v", result, err)
	}
	if result.Verification.Status != "failed" {
		t.Fatalf("no failing completion check observed: %+v", result.Verification)
	}
	for _, check := range result.Verification.Checks {
		if strings.HasPrefix(check.Name, "go ") && check.Status == "failed" {
			return
		}
	}
	t.Fatalf("done-gate did not run a Go check: %+v", result.Verification)
}

func TestAppPartialObservationArmsDoneGate(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	for name, content := range map[string]string{"go.mod": "module example\n\ngo 1.25\n", "bad.go": "invalid\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	a, e := newHeadlessPolicyTestApp(t, testkit.NewMockClient(), bash)
	a.workDir = root
	e.SetWorkDir(root)
	a.config.DoneGate.Enabled = true
	a.config.DoneGate.AutoFixAttempts = 0
	a.recordResponseTouchedPaths("bash", nil, tools.ToolResult{Success: true, Data: map[string]any{"workspace_observed": true, "files_changed_partial": true}})
	if a.enforceDoneGate(t.Context(), "Do it") {
		t.Fatal("partial observation with no known paths skipped checks")
	}
}

func TestObservedBashWritesCannotBeHiddenByDryRunArgument(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	a := &App{workDir: root, headlessRunActive: true}
	a.recordResponseTouchedPaths("bash", map[string]any{"dry_run": true}, tools.ToolResult{
		Success: false,
		Data:    map[string]any{"workspace_observed": true, "files_changed_partial": true, "written_paths": []string{filepath.Join(root, "actual.go")}},
	})
	if !reflect.DeepEqual(a.snapshotResponseTouchedPaths(), []string{"actual.go"}) ||
		!reflect.DeepEqual(a.headlessFilesChanged, []string{"actual.go"}) ||
		!a.headlessFilesChangedPartial || !a.responseEvidence.PartialWrites {
		t.Fatalf("unsupported dry_run argument erased observed writes: touched=%v changed=%v partial=%v evidence=%+v", a.snapshotResponseTouchedPaths(), a.headlessFilesChanged, a.headlessFilesChangedPartial, a.responseEvidence)
	}
}

func TestBashCheckoutClaimUsesObservedWriteEvenWhenGitClean(t *testing.T) {
	root := newClaimGitWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "m.go"), []byte("// preexisting dirty content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.go"), []byte("// unrelated dirty file\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	model := testkit.NewMockClient().EnqueueText("Restored m.go.")
	a, e := newHeadlessPolicyTestApp(t, model, bash)
	a.workDir = root
	e.SetWorkDir(root)
	r, err := e.InvokeTool(t.Context(), "bash", map[string]any{"command": "git checkout -- m.go; exit 1"})
	if err != nil || r.Success || len(tools.WrittenPathsFromResult(r)) != 1 {
		t.Fatalf("checkout=%+v %v", r, err)
	}
	answer := "Updated m.go."
	if !a.runCompletionReviewIfNeeded(t.Context(), "Update m.go", &answer, nil) || len(model.Calls()) != 1 {
		t.Fatal("completion review did not run")
	}
	prompt := model.Calls()[0].Message
	if strings.Contains(prompt, "[VERIFICATION FAILED]") {
		t.Fatalf("real shell restoration called a false claim: %s", prompt)
	}
}

func TestHeadlessTaskOutputDoesNotReattributeEarlierBashWrites(t *testing.T) {
	root := testkit.ResolvedTempDir(t)
	manager := tasks.NewManager(root)
	t.Cleanup(manager.CancelAll)
	bash := tools.NewBashTool(root)
	bash.SetSandboxEnabled(false)
	bash.SetTaskManager(manager)
	model := testkit.NewMockClient()
	a, e := newHeadlessPolicyTestApp(t, model, bash)
	a.workDir = root
	e.SetWorkDir(root)
	output := tools.NewTaskOutputTool()
	output.SetManager(manager)
	a.registry.MustRegister(output)
	r, err := e.InvokeTool(t.Context(), "bash", map[string]any{"command": "echo earlier > earlier.go", "run_in_background": true})
	if err != nil || !r.Success {
		t.Fatalf("start=%+v %v", r, err)
	}
	id, _ := r.Data.(map[string]any)["task_id"].(string)
	task, ok := manager.Get(id)
	if !ok {
		t.Fatal("missing task")
	}
	select {
	case <-task.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("background task did not finish")
	}
	model.EnqueueToolCall("task_output", map[string]any{"task_id": id}).EnqueueText("Read the earlier output.")
	result, err := a.RunHeadlessWithOptions(t.Context(), "Read the earlier task output", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
	if err != nil || len(result.FilesChanged) != 0 || result.FilesChangedPartial {
		t.Fatalf("reading prior task output invented current writes: %+v %v", result, err)
	}
}
