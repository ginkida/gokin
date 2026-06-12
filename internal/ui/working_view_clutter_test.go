package ui

import (
	"strings"
	"testing"
)

func TestWorkingViewDoesNotShowPersistentShortcutStrip(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.height = 30
	m.workDir = "/Users/example/github/gokin"
	m.runtimeStatus.Provider = "deepseek"
	m.currentModel = "deepseek-v4-pro"
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
	if strings.Contains(got, "● ● ●") {
		t.Fatalf("working view should not spend a row on decorative titlebar chrome:\n%s", got)
	}
}

func TestWorkingViewHidesEmptyTypeaheadWhileBusy(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.height = 30
	m.state = StateProcessing
	m.output.SetSize(100, 10)
	m.output.AppendLine("assistant response")

	if m.shouldRenderInputArea() {
		t.Fatal("busy view should hide the empty input row to preserve output space")
	}

	m.input.textarea.SetValue("next message")
	if !m.shouldRenderInputArea() {
		t.Fatal("busy view should show input once the user is composing type-ahead")
	}
}
