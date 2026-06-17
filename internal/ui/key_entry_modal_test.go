package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestKeyEntryModal_SubmitAndCancel: open enters the modal, typed runes land in
// the (masked) field, Enter submits the real key via the callback, Esc cancels
// without submitting. The key value is never the masked form — masking is
// display-only.
func TestKeyEntryModal_SubmitAndCancel(t *testing.T) {
	m := NewModel()

	var gotProvider, gotKey string
	var calls int
	m.SetKeyEntrySubmitCallback(func(provider, key string) { gotProvider = provider; gotKey = key; calls++ })

	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm", DisplayName: "GLM", SetupURL: "https://z.ai"})
	if m.state != StateAPIKeyEntry {
		t.Fatal("openKeyEntry should enter StateAPIKeyEntry")
	}

	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sk-secret-123456")})
	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEnter})

	if calls != 1 || gotProvider != "glm" || gotKey != "sk-secret-123456" {
		t.Errorf("submit: calls=%d provider=%q key=%q, want 1/glm/sk-secret-123456", calls, gotProvider, gotKey)
	}
	if m.state != StateInput {
		t.Errorf("submit should return to StateInput, got %v", m.state)
	}

	// Re-open, type, then Esc — must NOT submit.
	m.openKeyEntry(OpenKeyEntryMsg{Provider: "glm"})
	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("partial")})
	m.handleKeyEntryKeys(tea.KeyMsg{Type: tea.KeyEsc})
	if calls != 1 {
		t.Errorf("esc must not submit a key; callback calls=%d", calls)
	}
	if m.state != StateInput {
		t.Errorf("esc should return to StateInput, got %v", m.state)
	}
}
