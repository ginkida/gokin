package app

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gokin/internal/agent"
	"gokin/internal/memory"
	"gokin/internal/plan"
	"gokin/internal/testkit"
	"gokin/internal/tools"
	"gokin/internal/undo"
)

func headlessDeclaredWriteFixture(t *testing.T, work, name string) (tools.Tool, map[string]any, []string) {
	t.Helper()
	if name == "memorize" {
		learning, err := memory.NewProjectLearning(work)
		if err != nil {
			t.Fatal(err)
		}
		return tools.NewMemorizeTool(learning), map[string]any{"type": "fact", "key": "workflow", "content": "Run focused tests."}, []string{".gokin/learning.yaml", ".gokin/project-memory.md"}
	}
	path := filepath.Join(work, "planned.txt")
	manager := plan.NewManager(true, false)
	current := plan.NewPlan("fixture plan", "track exact file changes")
	current.AddStep("edit", "update planned.txt")
	manager.SetPlan(current)
	manager.EnableUndo(10)
	undoManager := undo.NewManager()
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := manager.BeginPlanUndoCapture(undoManager.Snapshot()); err != nil {
		t.Fatal(err)
	}
	change := undo.NewFileChange(path, "write", []byte("before"), []byte("after"), false)
	if err := os.WriteFile(path, change.NewContent, 0600); err != nil {
		t.Fatal(err)
	}
	undoManager.Record(*change)
	manager.CompleteStep(1, "done")
	manager.FinishPlanUndoCapture(undoManager.Snapshot())
	if name == "undo_plan" {
		tool := tools.NewUndoPlanTool()
		tool.SetManager(manager)
		tool.SetUndoManager(undoManager)
		return tool, map[string]any{"confirm": "yes"}, []string{"planned.txt"}
	}
	checkpoint := manager.GetUndoExtension().GetLastCheckpoint()
	if _, err := undoManager.UndoChanges(checkpoint.ChangeIDs); err != nil {
		t.Fatal(err)
	}
	if err := manager.GetUndoExtension().MarkUndone(checkpoint.PlanID); err != nil {
		t.Fatal(err)
	}
	tool := tools.NewRedoPlanTool()
	tool.SetManager(manager)
	tool.SetUndoManager(undoManager)
	return tool, map[string]any{}, []string{"planned.txt"}
}

func runHeadlessWriteFixture(t *testing.T, work string, tool tools.Tool, args map[string]any, delegated bool) (*App, HeadlessResult) {
	t.Helper()
	model := &sharedBudgetTestClient{MockClient: testkit.NewMockClient()}
	model.SetModel("glm-5.3-flash")
	a, e := newHeadlessPolicyTestApp(t, model.MockClient, tool)
	a.config.Plan.Enabled = true
	a.workDir = work
	a.client = model
	e.SetClient(model)
	e.SetWorkDir(work)
	e.SetDeltaCheckConfig(false, 0, false, 0)
	if delegated {
		task := tools.NewTaskTool()
		a.registry.MustRegister(task)
		runner := agent.NewRunner(a.ctx, model, a.registry, work)
		types := agent.NewAgentTypeRegistry()
		runner.SetTypeRegistry(types)
		runner.SetOnAgentUsage(func(_ string, result *agent.AgentResult) { a.accumulateScopedAgentResultUsage(result) })
		task.SetRunner(&agentRunnerAdapter{runner: runner})
		task.SetAgentTypeProvider(types)
		model.EnqueueToolCall("task", map[string]any{"prompt": "Perform the requested tool operation.", "subagent_type": "general", "max_turns": 4})
	}
	model.EnqueueToolCall(tool.Name(), args).EnqueueText("Finished.")
	if delegated {
		model.EnqueueText("Finished.")
	}
	result, err := a.RunHeadlessWithOptions(t.Context(), "Perform the requested tool operation.", HeadlessOptions{Stdout: io.Discard, Stderr: io.Discard, NoSelfReview: true})
	if err != nil || result.Status != "success" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	return a, result
}

func TestHeadlessFilesChangedHonorsDeclaredWrites(t *testing.T) {
	for _, name := range []string{"memorize", "undo_plan", "redo_plan"} {
		for _, delegated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/delegated=%v", name, delegated), func(t *testing.T) {
				work := testkit.ResolvedTempDir(t)
				tool, args, want := headlessDeclaredWriteFixture(t, work, name)
				a, result := runHeadlessWriteFixture(t, work, tool, args, delegated)
				for _, path := range want {
					if _, err := os.Stat(filepath.Join(work, path)); err != nil {
						t.Fatalf("expected mutation missing: %s %v", path, err)
					}
				}
				if name != "memorize" {
					data, err := os.ReadFile(filepath.Join(work, "planned.txt"))
					expected := "before"
					if name == "redo_plan" {
						expected = "after"
					}
					if err != nil || string(data) != expected {
						t.Fatalf("plan operation did not mutate file: %q %v", data, err)
					}
				}
				if got := a.snapshotResponseTouchedPaths(); len(got) != 0 {
					t.Errorf("reporting broadened done-gate targets: %v", got)
				}
				if !reflect.DeepEqual(result.FilesChanged, want) {
					t.Fatalf("files_changed=%v want=%v", result.FilesChanged, want)
				}
			})
		}
	}
}

func TestHeadlessMkdirReportsOnlyCreation(t *testing.T) {
	for _, exists := range []bool{false, true} {
		for _, delegated := range []bool{false, true} {
			t.Run(fmt.Sprintf("exists=%v/delegated=%v", exists, delegated), func(t *testing.T) {
				work := testkit.ResolvedTempDir(t)
				path := filepath.Join(work, "assets")
				want := []string{"assets"}
				if exists {
					if err := os.Mkdir(path, 0755); err != nil {
						t.Fatal(err)
					}
					want = []string{}
				}
				a, result := runHeadlessWriteFixture(t, work, tools.NewMkdirTool(work), map[string]any{"path": path}, delegated)
				if info, err := os.Stat(path); err != nil || !info.IsDir() {
					t.Fatalf("directory missing: %v", err)
				}
				// The done-gate retains its existing conservative target even for a no-op.
				if !delegated && !reflect.DeepEqual(a.snapshotResponseTouchedPaths(), []string{"assets"}) {
					t.Errorf("done-gate targeting changed: %v", a.snapshotResponseTouchedPaths())
				}
				if !reflect.DeepEqual(result.FilesChanged, want) {
					t.Fatalf("files_changed=%v want=%v", result.FilesChanged, want)
				}
			})
		}
	}
}

func TestHeadlessFilesChangedDoesNotInferReadTargets(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		t.Run(fmt.Sprintf("delegated=%v", delegated), func(t *testing.T) {
			work := testkit.ResolvedTempDir(t)
			path := filepath.Join(work, "input.txt")
			if err := os.WriteFile(path, []byte("read-only"), 0600); err != nil {
				t.Fatal(err)
			}
			_, result := runHeadlessWriteFixture(t, work, tools.NewReadTool(work), map[string]any{"file_path": path}, delegated)
			if len(result.FilesChanged) != 0 {
				t.Fatalf("read target reported as changed: %v", result.FilesChanged)
			}
		})
	}
}

func TestDeclaredWriteRetainsSingleEditCompletionReview(t *testing.T) {
	model := testkit.NewMockClient().EnqueueText("Reviewed all writes.")
	a, _ := newHeadlessPolicyTestApp(t, model, &appHeadlessScriptedTool{name: "unused"})
	a.workDir = testkit.ResolvedTempDir(t)
	if err := a.beginHeadlessPolicyTracking(); err != nil {
		t.Fatal(err)
	}
	defer a.endHeadlessPolicyTracking()
	a.recordResponseTouchedPaths("edit", map[string]any{"file_path": "value.go"}, tools.NewSuccessResultWithData("edited", map[string]any{"diff_added": 1, "diff_removed": 1}))
	a.recordResponseTouchedPaths("memorize", nil, tools.NewSuccessResultWithData("saved", map[string]any{"written_paths": []string{".gokin/project-memory.md"}}))
	answer := "Updated value.go and project memory."
	if !a.runCompletionReviewIfNeeded(t.Context(), "Fix value.go and save the convention.", &answer, nil) || len(model.Calls()) != 1 {
		t.Fatalf("multi-file review skipped: calls=%d", len(model.Calls()))
	}
	if !reflect.DeepEqual(a.snapshotResponseTouchedPaths(), []string{"value.go"}) {
		t.Fatalf("reporting broadened done-gate targets: %v", a.snapshotResponseTouchedPaths())
	}
}
