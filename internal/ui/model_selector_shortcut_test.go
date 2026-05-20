package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCtrlKOpensModelSelector(t *testing.T) {
	m := NewModel()
	m.availableModels = []ModelInfo{
		{ID: "fast", Name: "Fast"},
		{ID: "reasoning", Name: "Reasoning"},
	}
	m.currentModel = "reasoning"

	cmd := m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}, Alt: false})
	if cmd != nil {
		t.Fatalf("ctrl+k handler should not return a command")
	}
	if m.state != StateInput {
		t.Fatalf("plain k must not open model selector, state=%v", m.state)
	}

	cmd = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlK})
	if cmd != nil {
		t.Fatalf("ctrl+k handler should not return a command")
	}
	if m.state != StateModelSelector {
		t.Fatalf("ctrl+k state=%v, want StateModelSelector", m.state)
	}
	if m.modelSelectedIndex != 1 {
		t.Fatalf("selected index=%d, want current model index 1", m.modelSelectedIndex)
	}
}

func TestCtrlKWithoutModelsStaysInInput(t *testing.T) {
	m := NewModel()

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.state != StateInput {
		t.Fatalf("ctrl+k without models should stay in input, state=%v", m.state)
	}
}

func TestModelSelectorHandlesInvalidSelectionIndex(t *testing.T) {
	m := NewModel()
	m.state = StateModelSelector
	m.availableModels = []ModelInfo{{ID: "fast", Name: "Fast"}}
	m.modelSelectedIndex = -1

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateModelSelector {
		t.Fatalf("invalid index should leave selector open, state=%v", m.state)
	}
}

func TestModelSelectorEmptyState(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.currentModel = "fast"

	got := stripAnsi(m.renderModelSelector())
	if !strings.Contains(got, "No model choices are loaded") {
		t.Fatalf("empty model selector should explain missing choices:\n%s", got)
	}
	if !strings.Contains(got, "/model") {
		t.Fatalf("empty model selector should point to /model:\n%s", got)
	}
}
