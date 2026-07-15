package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestOneRowSuggestionSurfaceKeepsTargetAndActionsTogether(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(40)
	m.SetViewportHeight(5) // input frame (3) + one suggestion + status (1)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/m")})
	if rows := m.auxiliaryRowBudget(); rows != 1 {
		t.Fatalf("test setup needs one auxiliary row, got %d", rows)
	}
	plain := stripAnsi(m.View())
	for _, want := range []string{"> /", "Enter/Tab", "Esc"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("one-row suggestion surface lost %q:\n%s", want, plain)
		}
	}
	if !m.SuggestionsBlockSubmit() {
		t.Fatal("a readable one-row command completion should still own Enter")
	}
}

func TestSuggestionSelectionSurvivesCursorOnlyNavigation(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(80)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/m")})
	if len(m.suggestions) < 2 {
		t.Fatalf("test setup needs multiple command suggestions, got %v", m.suggestions)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	selected := m.suggestions[m.suggestionIndex].Name
	if m.suggestionIndex == 0 {
		t.Fatal("Down did not move the suggestion selection")
	}

	// Moving the text cursor does not change the completion query and must not
	// silently retarget the next Enter/Tab to the first ranked item.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if got := m.suggestions[m.suggestionIndex].Name; got != selected {
		t.Fatalf("Left retargeted selection from %q to %q", selected, got)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if got := m.suggestions[m.suggestionIndex].Name; got != selected {
		t.Fatalf("Right retargeted selection from %q to %q", selected, got)
	}
}

func TestInvisibleSuggestionSurfaceDoesNotOwnCompletionKeys(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(40)
	m.SetViewportHeight(1)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/m")})
	if !m.showSuggestions || m.auxiliaryRowBudget() != 0 {
		t.Fatalf("test setup needs a hidden active dropdown: show=%v rows=%d", m.showSuggestions, m.auxiliaryRowBudget())
	}
	if m.SuggestionsBlockSubmit() {
		t.Fatal("a dropdown with no renderable target/actions must not block submit")
	}

	before := m.textarea.Value()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.textarea.Value() != before || m.showSuggestions {
		t.Fatalf("Down operated on an invisible dropdown: value=%q show=%v", m.textarea.Value(), m.showSuggestions)
	}

	// Reopen the hidden state and verify Tab cannot accept an unseen target.
	m.refreshCompletionStateForValue()
	if !m.showSuggestions {
		t.Fatal("test setup did not reopen hidden suggestions")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.textarea.Value(); got != before {
		t.Fatalf("Tab accepted an invisible suggestion: got %q, want %q", got, before)
	}
	if m.showSuggestions {
		t.Fatal("Tab did not dismiss the unusable hidden dropdown")
	}

	// Direct InputModel use must also consume Enter without inserting a
	// newline or accepting an unseen target. The parent model submits the raw
	// draft because SuggestionsBlockSubmit reports false.
	m.refreshCompletionStateForValue()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.textarea.Value(); got != before {
		t.Fatalf("Enter changed draft through an invisible dropdown: got %q, want %q", got, before)
	}
	if m.showSuggestions {
		t.Fatal("Enter did not dismiss the unusable hidden dropdown")
	}
}

func TestCroppedSuggestionActionsRenderPausedAndFailClosed(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(20)
	m.SetViewportHeight(6)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/m")})
	if rows := m.auxiliaryRowBudget(); rows != 2 {
		t.Fatalf("test setup needs two auxiliary rows, got %d", rows)
	}
	plain := stripAnsi(m.View())
	if strings.Contains(plain, "> ") || !strings.Contains(plain, "Resize") || !strings.Contains(plain, "Esc") {
		t.Fatalf("cropped actions should render fail-closed recovery, got:\n%s", plain)
	}
	if m.SuggestionsBlockSubmit() {
		t.Fatal("a cropped action footer must not leave an unseen Enter target active")
	}
}

func TestHistorySearchCannotBecomeAnInvisibleFocusTrap(t *testing.T) {
	m := NewInputModel(DefaultStyles(), t.TempDir())
	m.SetWidth(40)
	m.SetViewportHeight(1)
	m.SetHistory([]string{"older request"})
	m.textarea.SetValue("current draft")
	m.textarea.CursorEnd()

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if m.historySearchMode {
		t.Fatal("Ctrl+R entered history search with no renderable search surface")
	}
	if got := m.textarea.Value(); got != "current draft" {
		t.Fatalf("refusing hidden history search changed draft: %q", got)
	}

	// A resize can remove the surface after search starts. Exit immediately so
	// typed keys return to the visible composer and the draft remains intact.
	m.SetViewportHeight(8)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if !m.historySearchMode {
		t.Fatal("roomy viewport did not enter history search")
	}
	m.SetViewportHeight(1)
	if m.historySearchMode {
		t.Fatal("history search remained active after its surface became invisible")
	}
	if got := m.textarea.Value(); got != "current draft" {
		t.Fatalf("resize cancellation changed draft: %q", got)
	}
}
