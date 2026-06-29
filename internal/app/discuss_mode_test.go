package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"gokin/internal/permission"
)

// discussMsg is classified as analysis/discussion by ClassifyTurnDiscuss
// (mirrors tools/intent_test.go's "how would we refactor" case).
const discussMsg = "how would we refactor this module?"

// TestBeginTurnIntent_PermissionsOnGatesDiscuss: with permission prompts ON, an
// analytical turn is classified as discuss so the first edit pauses for confirm.
func TestBeginTurnIntent_PermissionsOnGatesDiscuss(t *testing.T) {
	a := &App{
		program:     &tea.Program{},
		permManager: permission.NewManager(nil, true), // enabled
	}
	a.beginTurnIntent(discussMsg)
	if !a.turnDiscuss.Load() {
		t.Fatal("permissions ON: analytical turn must classify as discuss (gate active)")
	}
	if !a.discussGate() {
		t.Fatal("discussGate must be true when permissions are on and turn is analytical")
	}
}

// TestBeginTurnIntent_YOLODisablesDiscuss: with permission prompts OFF (YOLO),
// the discuss-mode gate is disabled — even an analytical message must NOT gate,
// so the first edit doesn't surprise a "just do it" user with a confirm prompt.
func TestBeginTurnIntent_YOLODisablesDiscuss(t *testing.T) {
	mgr := permission.NewManager(nil, true)
	mgr.SetEnabled(false) // YOLO: permissions off
	a := &App{
		program:     &tea.Program{},
		permManager: mgr,
	}
	a.beginTurnIntent(discussMsg)
	if a.turnDiscuss.Load() {
		t.Fatal("permissions OFF (YOLO): discuss-mode must be disabled (no first-edit prompt)")
	}
	if a.discussGate() {
		t.Fatal("discussGate must be false in YOLO")
	}
}

// TestBeginTurnIntent_NilPermManagerDisablesDiscuss: no enforcement at all → no gate.
func TestBeginTurnIntent_NilPermManagerDisablesDiscuss(t *testing.T) {
	a := &App{program: &tea.Program{}} // permManager nil
	a.beginTurnIntent(discussMsg)
	if a.turnDiscuss.Load() {
		t.Fatal("nil permManager (no enforcement) must not gate")
	}
}

// TestBeginTurnIntent_HeadlessNeverGates: no TUI → always acts (pre-existing).
func TestBeginTurnIntent_HeadlessNeverGates(t *testing.T) {
	a := &App{permManager: permission.NewManager(nil, true)} // program nil
	a.beginTurnIntent(discussMsg)
	if a.turnDiscuss.Load() {
		t.Fatal("headless (program nil) must never gate")
	}
}
