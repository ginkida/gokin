package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestQuestionMarkOpensFilterableShortcutsOverlay(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.height = 30

	_ = m.handleGlobalKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.state != StateShortcutsOverlay {
		t.Fatalf("? should open shortcuts overlay, state=%v", m.state)
	}
	if m.shortcutsOverlay == nil || !m.shortcutsOverlay.IsVisible() {
		t.Fatalf("shortcuts overlay should be visible")
	}

	_ = m.handleShortcutsOverlayKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("model")})
	if got := m.shortcutsOverlay.GetSearch(); got != "model" {
		t.Fatalf("search query = %q, want model", got)
	}
	if m.state != StateShortcutsOverlay {
		t.Fatalf("typing in shortcuts overlay should not close it")
	}

	got := stripAnsi(m.renderShortcutsOverlay())
	if !strings.Contains(got, "Filter: model") {
		t.Fatalf("overlay should render active filter:\n%s", got)
	}
	if !strings.Contains(got, "Open model selector") {
		t.Fatalf("filtered overlay should include model shortcut:\n%s", got)
	}
}

func TestShortcutsOverlayEscClearsThenCloses(t *testing.T) {
	m := NewModel()
	m.shortcutsOverlay.Show()
	m.shortcutsOverlay.SetSearch("model")
	m.state = StateShortcutsOverlay

	_ = m.handleShortcutsOverlayKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if got := m.shortcutsOverlay.GetSearch(); got != "" {
		t.Fatalf("first Esc should clear search, got %q", got)
	}
	if m.state != StateShortcutsOverlay {
		t.Fatalf("first Esc should keep overlay open")
	}

	_ = m.handleShortcutsOverlayKeys(tea.KeyMsg{Type: tea.KeyEscape})
	if m.state != StateInput {
		t.Fatalf("second Esc should close overlay, state=%v", m.state)
	}
}

func TestStaticShortcutsFallbackMatchesCurrentBindings(t *testing.T) {
	m := NewModel()
	m.shortcutsOverlay = nil

	got := stripAnsi(m.renderShortcutsOverlay())
	for _, want := range []string{"Keyboard Shortcuts", "Type to filter", "Esc close", "Ctrl + d", "half-page down"} {
		if !strings.Contains(got, want) {
			t.Fatalf("static shortcuts fallback missing %q:\n%s", want, got)
		}
	}
	for _, stale := range []string{"Quit (alternative)", "Apply code block", "Copy selected block", "Press any key to close"} {
		if strings.Contains(got, stale) {
			t.Fatalf("static shortcuts fallback advertises stale action %q:\n%s", stale, got)
		}
	}
}

func TestShortcutsFallbackRemainsInteractiveOnFirstKey(t *testing.T) {
	m := NewModel()
	m.shortcutsOverlay = nil
	m.state = StateShortcutsOverlay

	_ = m.handleShortcutsOverlayKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})

	if m.shortcutsOverlay == nil || !m.shortcutsOverlay.IsVisible() {
		t.Fatal("first key should materialize the fallback overlay and keep it visible")
	}
	if got := m.shortcutsOverlay.GetSearch(); got != "m" {
		t.Fatalf("first filter key was discarded: query=%q, want m", got)
	}
	if m.state != StateShortcutsOverlay {
		t.Fatalf("first filter key closed fallback overlay: state=%v", m.state)
	}
}
