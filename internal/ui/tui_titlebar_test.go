package ui

import (
	"strings"
	"testing"
)

// TestTitlebarOmitsModeBadge pins the inverse of the v0.84.0 behaviour:
// the titlebar must NOT include a plan/YOLO chip. Mode lives in the
// status bar only — having both surfaces show the same badge two lines
// apart was redundant chrome. If a future change re-introduces a
// titlebarModeSegment, this test trips and forces the discussion.
func TestTitlebarOmitsModeBadge(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.runtimeStatus.Provider = "deepseek"
	m.currentModel = "deepseek-v4-pro"

	t.Run("plan mode hidden from titlebar", func(t *testing.T) {
		m.planningModeEnabled = true
		defer func() { m.planningModeEnabled = false }()
		got := stripAnsi(m.titlebarRightSegment())
		if strings.Contains(got, "plan") {
			t.Fatalf("titlebar should not show plan mode badge (lives in status bar): %q", got)
		}
	})

	t.Run("YOLO mode hidden from titlebar", func(t *testing.T) {
		m.permissionsEnabled = false
		defer func() { m.permissionsEnabled = true }()
		got := stripAnsi(m.titlebarRightSegment())
		if strings.Contains(got, "YOLO") {
			t.Fatalf("titlebar should not show YOLO mode badge (lives in status bar): %q", got)
		}
	})

	t.Run("normal mode also hidden", func(t *testing.T) {
		got := stripAnsi(m.titlebarRightSegment())
		if strings.Contains(got, "normal") {
			t.Fatalf("normal mode should stay implicit: %q", got)
		}
	})
}
