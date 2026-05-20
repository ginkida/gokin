package ui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCtrlDScrollsHalfPageDownWhenInputEmpty(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(0)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.output.viewport.YOffset; got != 5 {
		t.Fatalf("YOffset after Ctrl+D = %d, want half page 5", got)
	}
	if !m.output.IsFrozen() {
		t.Fatalf("Ctrl+D away from bottom should keep output frozen")
	}
}

func TestCtrlDUnfreezesAtBottom(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 20 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(8)
	m.output.SetFrozen(true)

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if !m.output.IsAtBottom() {
		t.Fatalf("Ctrl+D near bottom should reach bottom")
	}
	if m.output.IsFrozen() {
		t.Fatalf("Ctrl+D at bottom should unfreeze output")
	}
}

func TestCtrlDDoesNotScrollWhileTyping(t *testing.T) {
	m := NewModel()
	m.output.SetSize(80, 10)
	for i := range 40 {
		m.output.AppendLine(fmt.Sprintf("line %02d", i))
	}
	m.output.viewport.SetYOffset(0)
	m.input.textarea.SetValue("draft")

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyCtrlD})
	if got := m.output.viewport.YOffset; got != 0 {
		t.Fatalf("Ctrl+D while typing should not scroll, YOffset=%d", got)
	}
}
