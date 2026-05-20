package ui

import (
	"strings"
	"testing"
)

func TestWorkingViewDoesNotShowPersistentShortcutStrip(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.height = 30
	m.output.SetSize(100, 10)
	m.output.AppendLine("assistant response")

	got := stripAnsi(m.View())
	unwanted := []string{
		"Ctrl+P commands",
		"Ctrl+K model",
		"Shift+Tab cycle mode",
		"? shortcuts",
	}
	for _, text := range unwanted {
		if strings.Contains(got, text) {
			t.Fatalf("working view should not show persistent shortcut strip %q:\n%s", text, got)
		}
	}
}
