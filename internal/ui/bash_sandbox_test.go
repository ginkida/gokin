package ui

import (
	"strings"
	"testing"
)

func TestStatusBarReportsUnavailableSandboxAtEveryWidth(t *testing.T) {
	for _, width := range []int{50, 70, 100, 160} {
		model := NewModel()
		model.width = width
		model.permissionsEnabled = true
		model.sandboxEnabled = true // Requested configuration alone is insufficient.
		model.runtimeStatus.SandboxStatus = "unavailable"
		view := stripAnsi(model.renderStatusBar())
		if !strings.Contains(view, "!SBX") {
			t.Fatalf("width %d concealed unavailable enforcement: %q", width, view)
		}
		if strings.Count(view, "!SBX") != 1 {
			t.Fatalf("width %d duplicated sandbox warning: %q", width, view)
		}
	}
}
