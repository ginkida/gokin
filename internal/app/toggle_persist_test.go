package app

import (
	"testing"

	"gokin/internal/config"
	"gokin/internal/permission"
)

// TestTogglePermissions_SyncsConfigField pins the mid-session-revert fix: the
// keyboard permission toggle must keep a.config.Permission.Enabled in sync, or
// the next ApplyConfig (step 8: permManager.SetEnabled(a.config.Permission.Enabled))
// silently reverts the toggle — e.g. flip YOLO off, run /model, prompts return.
func TestTogglePermissions_SyncsConfigField(t *testing.T) {
	cfg := &config.Config{}
	cfg.Permission.Enabled = true
	app := &App{
		config:      cfg,
		permManager: permission.NewManager(nil, true), // enabled
	}

	// Toggle OFF.
	if got := app.TogglePermissions(); got {
		t.Fatalf("toggle returned %v, want false (disabled)", got)
	}
	if app.permManager.IsEnabled() {
		t.Fatal("permManager should be disabled after toggle")
	}
	if app.config.Permission.Enabled {
		t.Fatal("config.Permission.Enabled must sync to false — else next ApplyConfig reverts it mid-session")
	}

	// Toggle back ON.
	if got := app.TogglePermissions(); !got {
		t.Fatalf("toggle returned %v, want true (enabled)", got)
	}
	if !app.config.Permission.Enabled {
		t.Fatal("config.Permission.Enabled must sync back to true")
	}
}
