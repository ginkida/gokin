package app

import (
	"context"
	"testing"
	"time"

	"gokin/internal/chat"
	"gokin/internal/ui"
)

func TestSessionPickerScopesChoicesAndDoesNotSwitchBeforeSelection(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	dir := t.TempDir()
	a := newSessionLeaseTestApp(t, "current", "glm", dir, "current conversation")
	program, captured := newCapturingProgram(t)
	a.program, a.tui, a.running = program, ui.NewModel(), true
	sessions := []chat.SessionInfo{
		{ID: "other", WorkDir: t.TempDir()},
		{ID: "unknown"},
		{ID: "../invalid", WorkDir: dir},
		{ID: "current", WorkDir: dir, Summary: "current topic", MessageCount: 2, LastActive: time.Now()},
		{ID: "wanted", WorkDir: dir, Summary: "wanted topic", MessageCount: 4, LastActive: time.Now()},
	}
	if !a.ShowSessionPicker(t.Context(), sessions, "Some saved sessions could not be read") {
		t.Fatal("interactive picker unavailable")
	}
	program.Send("barrier")
	captured.mu.Lock()
	var menu *ui.OpenSessionPickerMsg
	for _, msg := range captured.msgs {
		if value, ok := msg.(ui.OpenSessionPickerMsg); ok {
			menu = &value
		}
	}
	captured.mu.Unlock()
	if menu == nil || len(menu.Choices) != 2 || !menu.Choices[0].Current || menu.Choices[1].ID != "wanted" || menu.Note == "" {
		t.Fatalf("wrong project choices: %+v", menu)
	}
	if a.session.GetID() != "current" {
		t.Fatal("opening a read-only picker switched the session")
	}
	a.headlessDirect = true
	if a.ShowSessionPicker(t.Context(), sessions, "") {
		t.Fatal("headless runtime opened an interactive picker")
	}
	a.headlessDirect = false
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if a.ShowSessionPicker(ctx, sessions, "") {
		t.Fatal("cancelled command opened a picker")
	}
}
