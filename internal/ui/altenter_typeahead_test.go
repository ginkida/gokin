package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Alt+Enter is advertised as "insert newline". During type-ahead the same key
// must not submit a half-written queued message just because the model is
// currently Processing/Streaming.
func TestAltEnterDuringTypeAheadInsertsNewline(t *testing.T) {
	for _, state := range []State{StateProcessing, StateStreaming} {
		m := newTypeAheadModel(t)
		m.state = state

		var submitted []string
		m.SetCallbacks(func(s string) { submitted = append(submitted, s) }, nil)

		m = typeRunes(t, m, "refactor the parser but")
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
		mm := updated.(Model)
		m = &mm

		if len(submitted) != 0 {
			t.Fatalf("state %v: Alt+Enter submitted %q; want newline insertion only", state, submitted)
		}
		if got, want := m.input.textarea.Value(), "refactor the parser but\n"; got != want {
			t.Fatalf("state %v: raw input = %q, want %q", state, got, want)
		}
		if m.state != state {
			t.Fatalf("state %v: Alt+Enter changed state to %v", state, m.state)
		}
	}
}

func TestAltEnterInStateInputInsertsNewline(t *testing.T) {
	m := newTypeAheadModel(t)
	m.state = StateInput

	var submitted []string
	m.SetCallbacks(func(s string) { submitted = append(submitted, s) }, nil)

	m = typeRunes(t, m, "hello")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	mm := updated.(Model)
	m = &mm

	if len(submitted) != 0 {
		t.Fatalf("StateInput Alt+Enter must not submit, got %q", submitted)
	}
	if got, want := m.input.textarea.Value(), "hello\n"; got != want {
		t.Fatalf("StateInput Alt+Enter raw input = %q, want %q", got, want)
	}
}

func TestAltEnterDismissesAutocompleteWithoutAcceptingIt(t *testing.T) {
	tests := []struct {
		name       string
		typeID     SuggestionType
		configure  func(*InputModel)
		unexpected string
	}{
		{
			name:   "command",
			typeID: SuggestionCommand,
			configure: func(input *InputModel) {
				input.suggestions = []CommandInfo{{Name: "help"}}
			},
			unexpected: "/help ",
		},
		{
			name:   "argument",
			typeID: SuggestionArgument,
			configure: func(input *InputModel) {
				input.argSuggestions = []string{"--force"}
			},
			unexpected: "--force",
		},
		{
			name:   "file",
			typeID: SuggestionFile,
			configure: func(input *InputModel) {
				input.fileSuggestions = []string{"/workspace/main.go"}
			},
			unexpected: "main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTypeAheadModel(t)
			m.state = StateInput
			m.input.textarea.SetValue("draft")
			m.input.textarea.CursorEnd()
			m.input.showSuggestions = true
			m.input.suggestionType = tt.typeID
			tt.configure(&m.input)

			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
			got := updated.(Model)
			if value := got.input.textarea.Value(); value != "draft\n" {
				t.Fatalf("Alt+Enter changed draft to %q, want one newline", value)
			}
			if got.input.showSuggestions || len(got.input.suggestions) != 0 || len(got.input.argSuggestions) != 0 || len(got.input.fileSuggestions) != 0 {
				t.Fatalf("autocomplete survived newline: %+v", got.input)
			}
			if strings.Contains(got.input.textarea.Value(), tt.unexpected) {
				t.Fatalf("Alt+Enter accepted %q into draft: %q", tt.unexpected, got.input.textarea.Value())
			}
		})
	}
}
