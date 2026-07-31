package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gokin/internal/plan"
	"gokin/internal/undo"
)

func TestPlanUndoRedoToolsUseCapturedChangesOnly(t *testing.T) {
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.txt")
	foreignPath := filepath.Join(dir, "foreign.txt")
	if err := os.WriteFile(planPath, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreignPath, []byte("foreign-before"), 0o644); err != nil {
		t.Fatal(err)
	}

	planManager := plan.NewManager(true, false)
	currentPlan := plan.NewPlan("captured plan", "test exact plan undo")
	currentPlan.AddStep("edit", "edit plan file")
	planManager.SetPlan(currentPlan)
	planManager.EnableUndo(10)
	undoManager := undo.NewManager()

	if err := planManager.BeginPlanUndoCapture(undoManager.Snapshot()); err != nil {
		t.Fatal(err)
	}
	planChange := undo.NewFileChange(
		planPath, "write", []byte("before"), []byte("after"), false)
	if err := os.WriteFile(planPath, planChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	undoManager.Record(*planChange)
	planManager.CompleteStep(1, "done")
	planManager.FinishPlanUndoCapture(undoManager.Snapshot())

	foreignChange := undo.NewFileChange(
		foreignPath, "write", []byte("foreign-before"), []byte("foreign-after"), false)
	if err := os.WriteFile(foreignPath, foreignChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	undoManager.Record(*foreignChange)

	undoTool := NewUndoPlanTool()
	undoTool.SetManager(planManager)
	undoTool.SetUndoManager(undoManager)
	if err := undoTool.Validate(map[string]any{}); err == nil {
		t.Fatal("undo_plan must require explicit confirmation")
	}
	result, err := undoTool.Execute(context.Background(), map[string]any{"confirm": "yes"})
	if err != nil || !result.Success {
		t.Fatalf("undo_plan result=%+v err=%v", result, err)
	}
	assertPlanToolFile(t, planPath, "before")
	assertPlanToolFile(t, foreignPath, "foreign-after")
	if planManager.GetCurrentPlan() == nil {
		t.Fatal("undo_plan discarded plan metadata")
	}

	redoTool := NewRedoPlanTool()
	redoTool.SetManager(planManager)
	redoTool.SetUndoManager(undoManager)
	result, err = redoTool.Execute(context.Background(), map[string]any{
		"plan_id": currentPlan.ID,
	})
	if err != nil || !result.Success {
		t.Fatalf("redo_plan result=%+v err=%v", result, err)
	}
	assertPlanToolFile(t, planPath, "after")
	assertPlanToolFile(t, foreignPath, "foreign-after")
}

func TestPlanUndoToolRefusesOverlappingPostPlanEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	planManager := plan.NewManager(true, false)
	planManager.SetPlan(plan.NewPlan("plan", ""))
	planManager.EnableUndo(10)
	undoManager := undo.NewManager()

	if err := planManager.BeginPlanUndoCapture(undoManager.Snapshot()); err != nil {
		t.Fatal(err)
	}
	planChange := undo.NewFileChange(path, "write", []byte("before"), []byte("plan"), false)
	if err := os.WriteFile(path, planChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	undoManager.Record(*planChange)
	planManager.FinishPlanUndoCapture(undoManager.Snapshot())

	laterChange := undo.NewFileChange(path, "write", []byte("plan"), []byte("later"), false)
	if err := os.WriteFile(path, laterChange.NewContent, 0o644); err != nil {
		t.Fatal(err)
	}
	undoManager.Record(*laterChange)

	tool := NewUndoPlanTool()
	tool.SetManager(planManager)
	tool.SetUndoManager(undoManager)
	result, err := tool.Execute(context.Background(), map[string]any{"confirm": "true"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("undo_plan overwrote a post-plan edit: %+v", result)
	}
	assertPlanToolFile(t, path, "later")
}

func TestPlanUndoToolRefusesTruncatedCaptureInsteadOfPartialUndo(t *testing.T) {
	dir := t.TempDir()
	planManager := plan.NewManager(true, false)
	planManager.SetPlan(plan.NewPlan("large plan", ""))
	planManager.EnableUndo(10)
	undoManager := undo.NewManager()

	if err := planManager.BeginPlanUndoCapture(undoManager.Snapshot()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < undo.DefaultMaxChanges+1; i++ {
		path := filepath.Join(dir, fmt.Sprintf("file-%03d.txt", i))
		change := undo.NewFileChange(path, "write", nil, []byte("created"), true)
		if err := os.WriteFile(path, change.NewContent, 0o644); err != nil {
			t.Fatal(err)
		}
		undoManager.Record(*change)
	}
	planManager.FinishPlanUndoCapture(undoManager.Snapshot())

	checkpoint := planManager.GetUndoExtension().GetLastCheckpoint()
	if checkpoint == nil || checkpoint.CaptureOK {
		t.Fatalf("checkpoint=%+v, want incomplete capture", checkpoint)
	}

	tool := NewUndoPlanTool()
	tool.SetManager(planManager)
	tool.SetUndoManager(undoManager)
	result, err := tool.Execute(context.Background(), map[string]any{"confirm": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("undo_plan performed a partial undo: %+v", result)
	}
	assertPlanToolFile(t, filepath.Join(dir, "file-000.txt"), "created")
	assertPlanToolFile(
		t,
		filepath.Join(dir, fmt.Sprintf("file-%03d.txt", undo.DefaultMaxChanges)),
		"created",
	)
}

func assertPlanToolFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s content=%q, want %q", path, got, want)
	}
}
