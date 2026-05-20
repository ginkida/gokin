package ui

import (
	"strings"
	"testing"
)

func TestTitlebarShowsNonDefaultSessionMode(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.runtimeStatus.Provider = "deepseek"
	m.currentModel = "deepseek-v4-pro"
	m.planningModeEnabled = true

	got := stripAnsi(m.titlebarRightSegment())
	if !strings.Contains(got, "plan") {
		t.Fatalf("plan mode missing from titlebar: %q", got)
	}

	m.planningModeEnabled = false
	m.permissionsEnabled = false
	got = stripAnsi(m.titlebarRightSegment())
	if !strings.Contains(got, "YOLO") {
		t.Fatalf("YOLO mode missing from titlebar: %q", got)
	}
}

func TestTitlebarHidesNormalMode(t *testing.T) {
	m := NewModel()
	m.width = 120
	m.runtimeStatus.Provider = "deepseek"
	m.currentModel = "deepseek-v4-pro"

	got := stripAnsi(m.titlebarRightSegment())
	if strings.Contains(got, "normal") {
		t.Fatalf("normal mode should stay implicit in titlebar: %q", got)
	}
}
