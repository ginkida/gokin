package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPendingModelSwitchRemainsVisibleAfterSelectorCloses(t *testing.T) {
	m := NewModel()
	m.SetAvailableModels([]ModelInfo{{ID: "fast", Name: "Fast"}, {ID: "reasoning", Name: "Reasoning"}})
	m.SetCurrentModel("fast")
	m.SetModelSelectCallback(func(string) {})
	m.state = StateModelSelector
	m.modelSelectedIndex = 1

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateInput || m.modelSwitchPending != "reasoning" || m.currentModel != "fast" {
		t.Fatalf("switch request state=%v pending=%q current=%q", m.state, m.modelSwitchPending, m.currentModel)
	}

	for _, tc := range []struct {
		width int
		want  string
	}{
		{140, "switching→reasoning"},
		{90, "switching→reasoning"},
		{70, "↻reasoning"},
		{40, "↻reasoning"},
	} {
		m.width = tc.width
		status := stripAnsi(m.renderStatusBar())
		if !strings.Contains(status, tc.want) {
			t.Fatalf("width=%d pending switch missing %q:\n%s", tc.width, tc.want, status)
		}
		if !strings.Contains(status, "fast") {
			t.Fatalf("width=%d replaced authoritative model before result:\n%s", tc.width, status)
		}
	}

	_ = m.handleMessageTypes(ModelSelectResultMsg{
		RequestedID: "reasoning",
		ModelID:     "reasoning",
		Success:     true,
	})
	if m.modelSwitchPending != "" || m.currentModel != "reasoning" {
		t.Fatalf("confirmed switch pending=%q current=%q", m.modelSwitchPending, m.currentModel)
	}
	m.width = 140
	status := stripAnsi(m.renderStatusBar())
	if strings.Contains(status, "switching→") || !strings.Contains(status, "reasoning") {
		t.Fatalf("confirmed status retained pending indicator or lost model:\n%s", status)
	}
}

func TestKeepingCurrentModelGivesContextualFeedback(t *testing.T) {
	for _, tc := range []struct {
		name        string
		returnState State
	}{
		{name: "standalone", returnState: StateInput},
		{name: "from settings", returnState: StateSettings},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewModel()
			m.SetAvailableModels([]ModelInfo{{ID: "fast", Name: "Fast"}})
			m.SetCurrentModel("fast")
			m.state = StateModelSelector
			m.modelSelectorReturnState = tc.returnState

			_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
			if m.state != tc.returnState {
				t.Fatalf("returned to %v, want %v", m.state, tc.returnState)
			}
			if tc.returnState == StateSettings {
				if !strings.Contains(m.settingsNotice, "Already using Fast") {
					t.Fatalf("nested keep-current feedback=%q", m.settingsNotice)
				}
			} else if !activeToastContains(m, "Already using Fast") {
				t.Fatal("standalone keep-current action closed without feedback")
			}
		})
	}
}
