package app

import (
	"testing"

	"gokin/internal/config"
	"gokin/internal/plan"
	"gokin/internal/tools"
	"gokin/internal/undo"
)

func TestInitIntegrationsWiresPlanUndoTools(t *testing.T) {
	dir := t.TempDir()
	cfg := config.DefaultConfig()
	builder := &Builder{
		cfg:      cfg,
		workDir:  dir,
		registry: tools.DefaultRegistry(dir),
	}

	// Exercise the same manager and tool wiring used by Build without starting
	// external clients or UI components.
	builder.planManager = plan.NewManager(cfg.Plan.Enabled, cfg.Plan.RequireApproval)
	builder.planManager.EnableUndo(10)
	builder.undoManager = undo.NewManager()
	builder.wirePlanTools()

	rawUndo, ok := builder.registry.Get("undo_plan")
	if !ok {
		t.Fatal("undo_plan missing from registry")
	}
	undoTool, ok := rawUndo.(*tools.UndoPlanTool)
	if !ok || undoTool == nil {
		t.Fatalf("undo_plan type=%T", rawUndo)
	}
	result, err := undoTool.Execute(t.Context(), map[string]any{"confirm": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "plan manager not configured" || result.Error == "file undo manager not configured" {
		t.Fatalf("undo_plan is registered but not wired: %+v", result)
	}
	rawRedo, ok := builder.registry.Get("redo_plan")
	if !ok {
		t.Fatal("redo_plan missing from registry")
	}
	redoTool, ok := rawRedo.(*tools.RedoPlanTool)
	if !ok || redoTool == nil {
		t.Fatalf("redo_plan type=%T", rawRedo)
	}
	result, err = redoTool.Execute(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error == "plan manager not configured" || result.Error == "file undo manager not configured" {
		t.Fatalf("redo_plan is registered but not wired: %+v", result)
	}
}
