package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUnavailableComposerSubmitPreservesDraftAndIdleState(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateInput
	m.input.textarea.SetValue("keep this draft")

	updatedAny, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updatedAny.(Model)
	if got.state != StateInput || got.input.Value() != "keep this draft" {
		t.Fatalf("unavailable submit lost idle draft or faked processing: state=%v draft=%q", got.state, got.input.Value())
	}
	if len(got.input.history) != 0 || strings.Contains(stripAnsi(got.output.Content()), "keep this draft") {
		t.Fatalf("unavailable submit recorded an unsent message: history=%v output=%q", got.input.history, stripAnsi(got.output.Content()))
	}
	view := stripAnsi(got.input.View())
	for _, want := range []string{"Unavailable: message submission", "Draft preserved"} {
		if !strings.Contains(view, want) {
			t.Fatalf("composer lacks unavailable feedback %q:\n%s", want, view)
		}
	}
	if got.toastManager.Count() != 1 || got.toastManager.toasts[0].Type != ToastError {
		t.Fatalf("unavailable submit toast=%+v", got.toastManager.toasts)
	}
}

func TestUnavailableTypeAheadSubmitPreservesFollowUp(t *testing.T) {
	m := NewModel()
	m.width = 80
	m.state = StateProcessing
	m.input.textarea.SetValue("follow up later")

	updatedAny, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updatedAny.(Model)
	if got.state != StateProcessing || got.input.Value() != "follow up later" || len(got.input.history) != 0 {
		t.Fatalf("unavailable type-ahead changed active work or draft: state=%v draft=%q history=%v", got.state, got.input.Value(), got.input.history)
	}
	if status := stripAnsi(got.interruptHint()); !strings.Contains(status, "Send unavailable") || !strings.Contains(status, "Esc interrupt") {
		t.Fatalf("busy status advertises a false send action: %q", status)
	}
}

func TestUnavailablePaletteSlashSubmissionKeepsPaletteAndQuery(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StateCommandPalette
	m.commandPalette.visible = true
	m.commandPalette.commands = []EnhancedPaletteCommand{{Name: "open", Shortcut: "/open", Enabled: true, Type: CommandTypeSlash}}
	m.commandPalette.SetQuery("/open file.go")

	_ = m.handleCommandPaletteKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateCommandPalette || !m.commandPalette.IsVisible() || m.commandPalette.GetQuery() != "/open file.go" {
		t.Fatalf("unavailable direct command closed palette or lost query: state=%v visible=%v query=%q", m.state, m.commandPalette.IsVisible(), m.commandPalette.GetQuery())
	}
	view := stripAnsi(m.commandPalette.View(80, 24))
	for _, want := range []string{"Unavailable: command submission", "Submission unavailable", "/open file.go"} {
		if !strings.Contains(view, want) {
			t.Fatalf("palette lacks unavailable feedback %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Enter Run") {
		t.Fatalf("unavailable palette advertises execution:\n%s", view)
	}
}

func TestUnavailablePaletteArgSubmissionKeepsEnteredValue(t *testing.T) {
	m := NewModel()
	m.width, m.height = 80, 24
	m.state = StateCommandPalette
	m.commandPalette.visible = true
	m.commandPalette.BeginArgEntry(EnhancedPaletteCommand{Name: "open", Shortcut: "/open", Enabled: true, Type: CommandTypeSlash, ArgHint: "<file>"})
	m.commandPalette.AppendArg("folder/file.go")

	_ = m.handlePaletteArgKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if m.state != StateCommandPalette || !m.commandPalette.InArgEntry() || m.commandPalette.argValue != "folder/file.go" {
		t.Fatalf("unavailable arg submit closed entry or lost value: state=%v argEntry=%v value=%q", m.state, m.commandPalette.InArgEntry(), m.commandPalette.argValue)
	}
	view := stripAnsi(m.commandPalette.View(80, 24))
	for _, want := range []string{"Unavailable: command submission", "folder/file.go", "Submission unavailable"} {
		if !strings.Contains(view, want) {
			t.Fatalf("arg entry lacks unavailable feedback %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Enter Run") {
		t.Fatalf("unavailable arg entry advertises execution:\n%s", view)
	}
}
