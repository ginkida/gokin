package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProsePunctuationNeverTurnsEnterIntoFileSelection(t *testing.T) {
	for _, prompt := range []string{"Ответь одним предложением. LIVE HELLO.", "Explain this in words e.g.", "Open https://example.com/file"} {
		work := t.TempDir()
		for _, file := range []string{"HELLO.txt", "e.g.txt"} {
			if err := os.WriteFile(filepath.Join(work, file), []byte("fixture"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		m := *NewModel()
		m.SetKeyboardProfile("claude")
		m.SetWorkDir(work)
		var sent string
		m.SetCallbacks(func(value string) { sent = value }, nil)
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(prompt)})
		m = updated.(Model)
		if m.input.showSuggestions || m.input.suggestionNotice != "" {
			t.Fatalf("normal sentence opened path completion: %q", prompt)
		}
		updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = updated.(Model)
		if sent != prompt || m.input.Value() != "" {
			t.Fatalf("Enter lost prose: sent=%q draft=%q", sent, m.input.Value())
		}
	}
}

func TestExplicitFileCompletionRetainsTrailingDotPrefixes(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	for _, input := range []string{"./HELLO.", "look ./HELLO.", "/open HELLO."} {
		if !m.shouldSuggestFiles(input) {
			t.Fatalf("explicit path lost suggestions: %q", input)
		}
	}
}
