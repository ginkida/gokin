package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRewindMenuSelectionUsesCheckpointIdentity(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	submitted := ""
	m.onSubmit = func(s string) { submitted = s }
	id := strings.Repeat("a", 32)
	m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{ID: id, Prompt: "change", HasCode: true}}})
	if m.state != StateRewind || m.typeAheadActive() {
		t.Fatal("menu does not own keyboard")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.rewindChoosingAction || submitted != "" {
		t.Fatal("selection restored without choosing an action")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != "/rewind "+id+" both" || m.state != StateProcessing {
		t.Fatal("wrong rewind dispatch", submitted, m.state)
	}
}
func TestRewindNoCodeAndCancel(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	submitted := false
	m.onSubmit = func(string) { submitted = true }
	m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{ID: strings.Repeat("b", 32), Prompt: "no edits"}}})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.rewindActions()) != 4 || strings.Contains(m.renderRewindMenu(), "Restore code") {
		t.Fatal("code restore offered without edits")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEscape})
	if m.rewindChoosingAction || m.state != StateRewind {
		t.Fatal("Esc should return to point list")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput || submitted {
		t.Fatal("cancel mutated session")
	}
}
func TestDoubleEscapeRewindAndDraftHistory(t *testing.T) {
	for _, draft := range []string{"", "unsent draft"} {
		t.Run(draft, func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 80, 24
			m.state = StateInput
			m.input.setRecalledValue(draft)
			submitted := ""
			m.onSubmit = func(s string) { submitted = s }
			m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEscape})
			if submitted != "" {
				t.Fatal("single Esc opens rewind")
			}
			m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEscape})
			if draft == "" {
				if submitted != "/rewind" {
					t.Fatal("double Esc did not open rewind")
				}
			} else {
				if submitted != "" || m.input.Value() != "" {
					t.Fatal("draft should clear before rewind")
				}
				history := m.input.GetHistory()
				if len(history) == 0 || history[len(history)-1] != draft {
					t.Fatal("cleared draft lost")
				}
			}
		})
	}
}
func TestRewindSmallTerminalCannotActivateHiddenAction(t *testing.T) {
	m := NewModel()
	m.width, m.height = 12, 4
	submitted := false
	m.onSubmit = func(string) { submitted = true }
	m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{ID: strings.Repeat("c", 32), Prompt: "edit", HasCode: true}}})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted {
		t.Fatal("invisible action activated")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput {
		t.Fatal("small menu could not close")
	}
}

func TestRewindSummaryGuidanceAndNumberShortcut(t *testing.T) {
	for _, shortcut := range []bool{false, true} {
		t.Run(map[bool]string{false: "guided", true: "number"}[shortcut], func(t *testing.T) {
			m := NewModel()
			m.width, m.height = 100, 30
			submitted := ""
			m.onSubmit = func(s string) { submitted = s }
			id := strings.Repeat("d", 32)
			m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{ID: id, Prompt: "chosen point", HasCode: true}}})
			m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
			for i := 0; i < 3; i++ {
				m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyDown})
			}
			if !strings.Contains(m.renderRewindMenu(), "add context (optional)") {
				t.Fatal("summary guidance input missing")
			}
			m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(`keep "migration" decisions`)})
			if !strings.Contains(m.renderRewindMenu(), "migration") {
				t.Fatal("guidance not rendered")
			}
			if shortcut {
				m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
				if submitted != "/rewind "+id+" summarize-from" {
					t.Fatal("number shortcut retained guidance", submitted)
				}
			} else {
				m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
				if submitted != `/rewind `+id+` summarize-from -- "keep \"migration\" decisions"` {
					t.Fatal("guided command changed text", submitted)
				}
			}
		})
	}
}
func TestRewindHiddenNumberActionDoesNotExecute(t *testing.T) {
	m := NewModel()
	m.width, m.height = 60, 9
	submitted := false
	m.onSubmit = func(string) { submitted = true }
	m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{ID: strings.Repeat("e", 32), Prompt: "point", HasCode: true}}})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("5")})
	if submitted || m.rewindAction != 4 {
		t.Fatal("hidden numeric action executed instead of scrolling into view")
	}
	if !strings.Contains(m.renderRewindMenu(), "Summarize up to here") {
		t.Fatal("hidden target was not brought into view")
	}
}

func TestRewindSummaryTypedSpacesArePreserved(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	submitted := ""
	m.onSubmit = func(value string) { submitted = value }
	id := strings.Repeat("f", 32)
	m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{ID: id, Prompt: "point"}}})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyDown})
	for _, r := range "Keep migration constraints" {
		if r == ' ' {
			m.handleKeyMsg(tea.KeyMsg{Type: tea.KeySpace})
		} else {
			m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != `/rewind `+id+` summarize-from -- "Keep migration constraints"` {
		t.Fatal("typed spaces lost", submitted)
	}
}

func TestRewindPreviousSessionEntryDispatchesDirectly(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 24
	submitted := ""
	m.onSubmit = func(value string) { submitted = value }
	previous := "20261016-previous"
	m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{Prompt: "/resume " + previous + " (previous session)", PreviousSessionID: previous}}})
	if !strings.Contains(m.renderRewindMenu(), "(previous session)") {
		t.Fatal("previous entry is not visible")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted != `/rewind previous "`+previous+`"` || m.rewindChoosingAction || m.state != StateProcessing {
		t.Fatal("previous entry opened file-restore actions", submitted, m.state)
	}
}
func TestRewindPreviousRowDoesNotRenumberPromptActions(t *testing.T) {
	m := NewModel()
	m.width, m.height = 100, 24
	m.handleMessageTypes(OpenRewindMenuMsg{Choices: []RewindChoice{{Prompt: "/resume old (previous session)", PreviousSessionID: "old"}, {ID: strings.Repeat("a", 32), Prompt: "first fresh prompt"}}})
	rendered := m.renderRewindMenu()
	if !strings.Contains(rendered, "1. first fresh prompt") || strings.Contains(rendered, "2. first fresh prompt") {
		t.Fatal("prompt numbers no longer match direct /rewind selection", rendered)
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyHome})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput {
		t.Fatal("previous entry cannot be cancelled")
	}
}
