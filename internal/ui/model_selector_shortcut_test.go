package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	// round 8: ctrl+k now returns keyConsumed (a non-nil no-op tea.Cmd) so
	// Update() doesn't ALSO forward the keystroke to the compose textarea's
	// own ctrl+k binding (DeleteAfterCursor) — see the handler's comment.
	cmd = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlK})
	if cmd == nil {
		t.Fatalf("ctrl+k handler should return keyConsumed to suppress textarea forwarding")
	}
	if m.state != StateModelSelector {
		t.Fatalf("ctrl+k state=%v, want StateModelSelector", m.state)
	}
	if m.modelSelectedIndex != 1 {
		t.Fatalf("selected index=%d, want current model index 1", m.modelSelectedIndex)
	}
}

func TestCtrlKWithoutModelsOpensRecoverableEmptySelector(t *testing.T) {
	m := NewModel()
	m.width = 80

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.state != StateModelSelector {
		t.Fatalf("ctrl+k without models should open the empty selector, state=%v", m.state)
	}
	view := stripAnsi(m.renderModelSelector())
	for _, want := range []string{"No model choices are registered", "/provider", "/doctor", "Esc Close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("empty selector missing recovery %q:\n%s", want, view)
		}
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
	if !strings.Contains(got, "No model choices are registered") {
		t.Fatalf("empty model selector should explain missing choices:\n%s", got)
	}
	for _, recovery := range []string{"/provider", "/doctor"} {
		if !strings.Contains(got, recovery) {
			t.Fatalf("empty model selector should point to %s:\n%s", recovery, got)
		}
	}
	if strings.Contains(got, "Use /model") {
		t.Fatalf("empty model selector should not suggest reopening itself:\n%s", got)
	}
}

func TestModelSelectorLongListKeepsSelectionVisible(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.height = 17
	for i := 1; i <= 12; i++ {
		m.availableModels = append(m.availableModels, ModelInfo{
			ID:          fmt.Sprintf("model-%d", i),
			Name:        fmt.Sprintf("Model %d", i),
			Description: fmt.Sprintf("Description %d", i),
		})
	}
	m.currentModel = "model-1"
	m.modelSelectedIndex = len(m.availableModels) - 1

	got := stripAnsi(m.renderModelSelector())
	if !strings.Contains(got, "> 12. Model 12") {
		t.Fatalf("selected last model was clipped:\n%s", got)
	}
	if !strings.Contains(got, "↑") || !strings.Contains(got, "more") {
		t.Fatalf("clipped list lacks an upper scroll indicator:\n%s", got)
	}
	if strings.Contains(got, "2. Model 2") {
		t.Fatalf("long selector rendered the whole list instead of a window:\n%s", got)
	}
}

func TestModelSelectorFitsHeightWithMiddleSelectionAndNotice(t *testing.T) {
	for _, height := range []int{8, 10, 12, 18, 24} {
		m := NewModel()
		m.width, m.height = 72, height
		m.state = StateModelSelector
		m.onModelSelect = func(string) {}
		for i := 1; i <= 16; i++ {
			m.availableModels = append(m.availableModels, ModelInfo{
				ID:          fmt.Sprintf("model-%d", i),
				Name:        fmt.Sprintf("Model %d", i),
				Description: strings.Repeat("Long capability description ", 6),
			})
		}
		m.modelSelectedIndex = 6
		m.modelSelectorNotice = "Wait for the current model switch to finish"

		view := m.renderModelSelector()
		if got := lipgloss.Height(view); got > height {
			t.Fatalf("height=%d model selector rendered %d rows:\n%s", height, got, stripAnsi(view))
		}
		plain := stripAnsi(view)
		for _, want := range []string{"Select Model", "> 7. Model 7", "Wait for", "Esc"} {
			if !strings.Contains(plain, want) {
				t.Fatalf("height=%d selector missing %q:\n%s", height, want, plain)
			}
		}
		if height < 12 && strings.Contains(plain, "Long capability description") {
			t.Fatalf("height=%d ultra-compact selector kept secondary description:\n%s", height, plain)
		}
		if height >= 12 && !strings.Contains(plain, "Long capability description") {
			t.Fatalf("height=%d selector dropped available description:\n%s", height, plain)
		}
	}
}

func TestModelSelectorUsesFriendlyCurrentNameAndContextualFooter(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.availableModels = []ModelInfo{{ID: "provider/model-v2", Name: "Model V2", Description: "Current model"}}
	m.currentModel = "provider/model-v2"
	m.modelSelectorReturnState = StateSettings

	got := stripAnsi(m.renderModelSelector())
	if !strings.Contains(got, "Current: Model V2") {
		t.Fatalf("selector should prefer friendly current name:\n%s", got)
	}
	if !strings.Contains(got, "Esc Back to settings") {
		t.Fatalf("nested selector footer should explain its return target:\n%s", got)
	}
}

func TestModelSelectorPageNavigation(t *testing.T) {
	m := NewModel()
	m.height = 17
	m.state = StateModelSelector
	for i := range 12 {
		m.availableModels = append(m.availableModels, ModelInfo{ID: fmt.Sprintf("m%d", i), Name: fmt.Sprintf("M%d", i)})
	}

	page := m.modelSelectorPageSize()
	m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyPgDown})
	if m.modelSelectedIndex != page {
		t.Fatalf("PgDn index=%d, want %d", m.modelSelectedIndex, page)
	}
	m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnd})
	if m.modelSelectedIndex != len(m.availableModels)-1 {
		t.Fatalf("End index=%d, want last", m.modelSelectedIndex)
	}
	m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyHome})
	if m.modelSelectedIndex != 0 {
		t.Fatalf("Home index=%d, want 0", m.modelSelectedIndex)
	}
}
