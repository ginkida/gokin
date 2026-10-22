package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func pickerModel(t *testing.T) *Model {
	t.Helper()
	m := claudeComposer(t)
	m.width, m.height = 80, 24
	m.input.setRecalledValue("unsent composer draft")
	var choices []SessionChoice
	for i := 0; i < 30; i++ {
		choices = append(choices, SessionChoice{ID: fmt.Sprintf("checkpoint-%02d", i), Summary: fmt.Sprintf("topic %02d", i), Detail: "4 messages · yesterday"})
	}
	m.openSessionPicker(OpenSessionPickerMsg{Choices: choices})
	return &m
}

func TestSessionPickerSearchResumesStableIdentityAndKeepsDraft(t *testing.T) {
	m := pickerModel(t)
	var submitted []string
	m.onSubmit = func(s string) { submitted = append(submitted, s) }
	// The foreground /resume command finishes after delivering its list.
	m.handleMessageTypes(ResponseDoneMsg{})
	if m.state != StateSessionPicker || m.typeAheadActive() {
		t.Fatal("picker lost ownership when the command completed")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("TOPIC 19")})
	if !strings.Contains(stripAnsi(m.View()), "1 of 30 sessions") || m.input.Value() != "unsent composer draft" {
		t.Fatal("search leaked to the composer or failed to filter")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if len(submitted) != 1 || submitted[0] != `/resume "checkpoint-19"` || m.state != StateProcessing || m.input.Value() != "unsent composer draft" {
		t.Fatalf("bad resume dispatch/draft: %v %v %q", submitted, m.state, m.input.Value())
	}
}

func TestSessionPickerNoMatchesCancelAndUnicodeEditing(t *testing.T) {
	m := pickerModel(t)
	submitted := false
	m.onSubmit = func(string) { submitted = true }
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("missing👩🏽‍💻")})
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.sessionPicker.query != "missing" {
		t.Fatal("backspace split a grapheme")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if submitted || m.state != StateSessionPicker || !strings.Contains(stripAnsi(m.View()), "No matching sessions") {
		t.Fatal("empty search selected a hidden session")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlU})
	if len(m.sessionPicker.filtered) != 30 {
		t.Fatal("clear search lost choices")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyCtrlC})
	if submitted || m.state != StateInput || m.input.Value() != "unsent composer draft" {
		t.Fatal("cancel changed the session or composer")
	}
}

func TestSessionPickerGeometryPagingAndTerminalSafety(t *testing.T) {
	m := pickerModel(t)
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnd})
	if !strings.Contains(stripAnsi(m.View()), "topic 29") {
		t.Fatal("last choice is outside the visible page")
	}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.sessionPicker.selected >= 29 || !strings.Contains(stripAnsi(m.View()), fmt.Sprintf("topic %02d", m.sessionPicker.selected)) {
		t.Fatal("paging failed to keep selection visible")
	}
	for _, size := range [][2]int{{1, 1}, {20, 4}, {32, 8}, {60, 9}, {100, 40}} {
		m.width, m.height = size[0], size[1]
		view := m.View()
		if lipgloss.Height(view) > m.height || lipgloss.Width(view) > m.width {
			t.Fatalf("picker exceeds %dx%d: %dx%d", m.width, m.height, lipgloss.Width(view), lipgloss.Height(view))
		}
	}
	m.width, m.height = 80, 24
	m.state = StateInput
	m.openSessionPicker(OpenSessionPickerMsg{Choices: []SessionChoice{
		{ID: "--force", Summary: "invalid option"},
		{ID: "valid", Summary: "topic\nforged\x1b]0;title\a", Detail: "row\rforged"},
	}})
	view := m.View()
	if len(m.sessionPicker.choices) != 1 || strings.Contains(view, "\x1b]0;") || strings.Contains(view, "topic\nforged") {
		t.Fatal("saved metadata forged a control or a new row")
	}
	called := false
	m.onSubmit = func(string) { called = true }
	m.pendingResize = &tea.WindowSizeMsg{Width: 20, Height: 4}
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEnter})
	if called {
		t.Fatal("Enter activated an option during a pending shrink")
	}
}

func TestSessionPickerYieldsToPermissionAndRestoresRequestState(t *testing.T) {
	m := pickerModel(t)
	m.transientOverlayReturnState = StateProcessing
	m.handleKeyMsg(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateProcessing || m.input.Value() != "unsent composer draft" {
		t.Fatal("cancel lost the live request underneath")
	}
	m.openSessionPicker(OpenSessionPickerMsg{Choices: []SessionChoice{{ID: "valid"}}})
	m.handleMessageTypes(PermissionRequestMsg{ID: "permission-1", ToolName: "edit"})
	if m.state != StatePermissionPrompt || m.sessionPicker != nil || m.input.Value() != "unsent composer draft" {
		t.Fatal("read-only picker swallowed a blocking tool request")
	}
}
