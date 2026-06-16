package app

import (
	"testing"

	"gokin/internal/config"
	"gokin/internal/permission"
	"gokin/internal/tools"
)

// TestUpdateUnrestrictedMode_SyncsDiffPreview pins the fix for the bug where
// toggling YOLO (permissions off) at runtime left diffEnabled=true on the
// edit/write tools forever — the "Apply" confirmation kept showing even though
// the user opted out of all confirmations. updateUnrestrictedModeLocked must
// sync diffEnabled to the live permission state on every toggle.
func TestUpdateUnrestrictedMode_SyncsDiffPreview(t *testing.T) {
	workDir := t.TempDir()

	registry := tools.NewRegistry()
	editTool := tools.NewEditTool(workDir)
	writeTool := tools.NewWriteTool(workDir)
	if err := registry.Register(editTool); err != nil {
		t.Fatalf("Register(edit): %v", err)
	}
	if err := registry.Register(writeTool); err != nil {
		t.Fatalf("Register(write): %v", err)
	}

	cfg := &config.Config{}
	cfg.DiffPreview.Enabled = true
	cfg.Permission.Enabled = true

	app := &App{
		config:      cfg,
		permManager: permission.NewManager(nil, true),
		registry:    registry,
		executor:    tools.NewExecutor(registry, nil, 0),
	}

	// Permissions ON + diff preview enabled in config → diff enabled.
	app.updateUnrestrictedModeLocked()
	if !editTool.DiffEnabled() {
		t.Fatal("diff should be enabled when permissions are ON")
	}
	if !writeTool.DiffEnabled() {
		t.Fatal("diff should be enabled when permissions are ON")
	}

	// Toggle permissions OFF (YOLO) → diff must turn OFF.
	app.permManager.SetEnabled(false)
	app.updateUnrestrictedModeLocked()
	if editTool.DiffEnabled() {
		t.Fatal("diff must be disabled after permissions toggled OFF (YOLO)")
	}
	if writeTool.DiffEnabled() {
		t.Fatal("diff must be disabled after permissions toggled OFF (YOLO)")
	}

	// Toggle permissions back ON → diff must turn ON again.
	app.permManager.SetEnabled(true)
	app.updateUnrestrictedModeLocked()
	if !editTool.DiffEnabled() {
		t.Fatal("diff should re-enable when permissions toggled back ON")
	}
	if !writeTool.DiffEnabled() {
		t.Fatal("diff should re-enable when permissions toggled back ON")
	}
}

// TestUpdateUnrestrictedMode_DiffDisabledInConfig stays off regardless of
// permissions — the config-file opt-out is authoritative.
func TestUpdateUnrestrictedMode_DiffDisabledInConfig(t *testing.T) {
	workDir := t.TempDir()

	registry := tools.NewRegistry()
	editTool := tools.NewEditTool(workDir)
	writeTool := tools.NewWriteTool(workDir)
	_ = registry.Register(editTool)
	_ = registry.Register(writeTool)

	cfg := &config.Config{}
	cfg.DiffPreview.Enabled = false
	cfg.Permission.Enabled = true

	app := &App{
		config:      cfg,
		permManager: permission.NewManager(nil, true),
		registry:    registry,
		executor:    tools.NewExecutor(registry, nil, 0),
	}

	app.updateUnrestrictedModeLocked()
	if editTool.DiffEnabled() {
		t.Fatal("diff must stay off when disabled in config")
	}
	if writeTool.DiffEnabled() {
		t.Fatal("diff must stay off when disabled in config")
	}
}
