package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestInvalidSelectedModelDoesNotAdvertiseOrEmitConfirmation(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StateModelSelector
	m.availableModels = []ModelInfo{{Name: "Broken"}, {ID: "fast", Name: "Fast"}}
	calls := 0
	m.onModelSelect = func(string) { calls++ }

	view := stripAnsi(m.renderModelSelector())
	for _, want := range []string{"Broken (unavailable)", "Selected unavailable", "↑/↓ Navigate", "Esc Close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("invalid selection missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Enter Confirm") || strings.Contains(view, "1-2 Select") {
		t.Fatalf("invalid selection advertised Enter confirmation:\n%s", view)
	}
	assertShortcutHints(t, m,
		[]string{"↑↓ Navigate", "esc Close"},
		[]string{"Enter Select", "Enter Keep current"},
	)

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if calls != 0 || m.state != StateModelSelector || !strings.Contains(m.modelSelectorNotice, "no selectable ID") {
		t.Fatalf("invalid Enter escaped selector: calls=%d state=%v notice=%q", calls, m.state, m.modelSelectorNotice)
	}
	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if calls != 0 || m.state != StateModelSelector {
		t.Fatalf("invalid quick key emitted selection: calls=%d state=%v", calls, m.state)
	}
}

func TestCurrentModelCanBeKeptWithoutSwitchCallback(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StateModelSelector
	m.currentModel = "fast"
	m.availableModels = []ModelInfo{{ID: "fast", Name: "Fast"}}

	view := stripAnsi(m.renderModelSelector())
	if !strings.Contains(view, "Enter Keep current") || strings.Contains(view, "Read-only") {
		t.Fatalf("current model did not expose its safe close action:\n%s", view)
	}
	assertShortcutHints(t, m,
		[]string{"Enter Keep current", "esc Close"},
		[]string{"Enter Select", "↑↓ Navigate", "↑↓ Inspect"},
	)

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateInput {
		t.Fatalf("keeping current model did not close selector: state=%v", m.state)
	}
}

func TestDifferentModelRemainsReadOnlyWithoutSwitchCallback(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StateModelSelector
	m.currentModel = "fast"
	m.availableModels = []ModelInfo{{ID: "deep", Name: "Deep"}}

	view := stripAnsi(m.renderModelSelector())
	if !strings.Contains(view, "Read-only") || strings.Contains(view, "Enter Confirm") || strings.Contains(view, "Enter Keep current") {
		t.Fatalf("unlinked different model has dishonest actions:\n%s", view)
	}
	assertShortcutHints(t, m,
		[]string{"esc Close"},
		[]string{"Enter Select", "Enter Keep current", "↑↓ Inspect"},
	)

	_ = m.handleModelSelectorKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateModelSelector || !strings.Contains(m.modelSelectorNotice, "unavailable in this session") {
		t.Fatalf("read-only Enter lacked feedback: state=%v notice=%q", m.state, m.modelSelectorNotice)
	}
}
