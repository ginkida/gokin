package ui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func armedModelSelectorEnterGuard(t *testing.T, submissions *int) Model {
	t.Helper()
	m := *NewModel()
	m.width, m.height = 80, 24
	m.input.textarea.SetValue("intentional draft")
	m.SetAvailableModels([]ModelInfo{{ID: "current"}, {ID: "target"}})
	m.SetCurrentModel("current")
	m.SetModelSelectCallback(func(string) {})
	m.SetCallbacks(func(string) { *submissions = *submissions + 1 }, nil)
	m.openModelSelector()
	m.modelSelectedIndex = 1

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	selected := updated.(Model)
	if selected.state != StateInput || selected.modalEnterGuardUntil.IsZero() {
		t.Fatalf("model confirmation did not arm bounded Enter guard: state=%v until=%v", selected.state, selected.modalEnterGuardUntil)
	}
	return selected
}

func TestModalEnterGuardExpiresForLaterIntentionalSubmit(t *testing.T) {
	submissions := 0
	m := armedModelSelectorEnterGuard(t, &submissions)
	m.modalEnterGuardUntil = time.Now().Add(-time.Millisecond)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if submissions != 1 || got.input.Value() != "" {
		t.Fatalf("expired guard blocked intentional submit: submissions=%d draft=%q", submissions, got.input.Value())
	}
}

func TestModalEnterGuardClearsOnDistinctUserInput(t *testing.T) {
	submissions := 0
	m := armedModelSelectorEnterGuard(t, &submissions)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	if !m.modalEnterGuardUntil.IsZero() {
		t.Fatal("distinct navigation key did not release modal Enter guard")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Model)
	if submissions != 1 || got.input.Value() != "" {
		t.Fatalf("new intent after navigation was blocked: submissions=%d draft=%q", submissions, got.input.Value())
	}
}
